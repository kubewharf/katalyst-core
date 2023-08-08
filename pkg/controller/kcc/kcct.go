/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kcc

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	configapis "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	configinformers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/listers/config/v1alpha1"
	kcclient "github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	kcctarget "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
	kccutil "github.com/kubewharf/katalyst-core/pkg/controller/kcc/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	kccTargetControllerName = "kcct"
)

const (
	kccTargetConditionReasonNormal                    = "Normal"
	kccTargetConditionReasonHashFailed                = "HashFailed"
	kccTargetConditionReasonMatchMoreOrLessThanOneKCC = "MatchMoreOrLessThanOneKCC"
	kccTargetConditionReasonValidateFailed            = "ValidateFailed"
)

type KatalystCustomConfigTargetController struct {
	ctx       context.Context
	dryRun    bool
	kccConfig *controller.KCCConfig

	client              *kcclient.GenericClientSet
	kccControl          control.KCCControl
	unstructuredControl control.UnstructuredControl

	// katalystCustomConfigLister can list/get KatalystCustomConfig from the shared informer's store
	katalystCustomConfigLister v1alpha1.KatalystCustomConfigLister

	syncedFunc []cache.InformerSynced

	// targetHandler store gvr kcc and gvr
	targetHandler *kcctarget.KatalystCustomConfigTargetHandler

	// metricsEmitter for emit metrics
	metricsEmitter metrics.MetricEmitter
}

func NewKatalystCustomConfigTargetController(
	ctx context.Context,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	kccConfig *controller.KCCConfig,
	client *kcclient.GenericClientSet,
	katalystCustomConfigInformer configinformers.KatalystCustomConfigInformer,
	metricsEmitter metrics.MetricEmitter,
	targetHandler *kcctarget.KatalystCustomConfigTargetHandler,
) (*KatalystCustomConfigTargetController, error) {
	k := &KatalystCustomConfigTargetController{
		ctx:                        ctx,
		client:                     client,
		dryRun:                     genericConf.DryRun,
		kccConfig:                  kccConfig,
		katalystCustomConfigLister: katalystCustomConfigInformer.Lister(),
		targetHandler:              targetHandler,
		syncedFunc: []cache.InformerSynced{
			katalystCustomConfigInformer.Informer().HasSynced,
		},
	}

	if metricsEmitter == nil {
		k.metricsEmitter = metrics.DummyMetrics{}
	} else {
		k.metricsEmitter = metricsEmitter.WithTags(kccTargetControllerName)
	}

	k.kccControl = control.DummyKCCControl{}
	k.unstructuredControl = control.DummyUnstructuredControl{}
	if !k.dryRun {
		k.kccControl = control.NewRealKCCControl(client.InternalClient)
		k.unstructuredControl = control.NewRealUnstructuredControl(client.DynamicClient)
	}

	// register kcc-target informer handler
	targetHandler.RegisterTargetHandler(kccTargetControllerName, k.katalystCustomConfigTargetHandler)
	return k, nil
}

// Run don't need to trigger reconcile logic.
func (k *KatalystCustomConfigTargetController) Run() {
	defer utilruntime.HandleCrash()

	defer klog.Infof("shutting down %s controller", kccTargetControllerName)

	if !cache.WaitForCacheSync(k.ctx.Done(), k.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", kccTargetControllerName))
		return
	}
	klog.Infof("caches are synced for %s controller", kccTargetControllerName)

	go wait.Until(k.clearExpiredKCCTarget, 30*time.Second, k.ctx.Done())

	<-k.ctx.Done()
}

// katalystCustomConfigTargetHandler process object of kcc target type from targetAccessor, and
// KatalystCustomConfigTargetAccessor will call this handler when some update event on target is added.
func (k *KatalystCustomConfigTargetController) katalystCustomConfigTargetHandler(gvr metav1.GroupVersionResource, target *unstructured.Unstructured) error {
	for _, syncFunc := range k.syncedFunc {
		if !syncFunc() {
			return fmt.Errorf("[kcct] informer has not synced")
		}
	}

	klog.V(4).Infof("gvr: %s, target: %s updated", gvr.String(), native.GenerateUniqObjectNameKey(target))

	if target.GetDeletionTimestamp() != nil {
		err := k.handleKCCTargetFinalizer(gvr, target)
		if err != nil {
			return err
		}
		return nil
	}

	target, err := kccutil.EnsureKCCTargetFinalizer(k.ctx, k.unstructuredControl,
		consts.KatalystCustomConfigTargetFinalizerKCCT, gvr, target)
	if err != nil {
		return err
	}

	// add kcc target config process logic:
	//	1. clear expired valid target config
	//	2. generate hash of current target config and update it to annotation
	//	3. todo: control revision history if needed
	//	4. update target config annotation and status if needed

	targetResource := util.ToKCCTargetResource(target)
	if isExpired := targetResource.CheckExpired(time.Now()); isExpired {
		// delete expired kcc target
		err := k.unstructuredControl.DeleteUnstructured(k.ctx, gvr, target, metav1.DeleteOptions{})
		if err != nil {
			return err
		}
		return nil
	}

	var (
		isValid               bool
		message, hash, reason string
		overlapTargets        []util.KCCTargetResource
	)

	reason = kccTargetConditionReasonNormal
	kccKeys := k.targetHandler.GetKCCKeyListByGVR(gvr)
	if len(kccKeys) != 1 {
		isValid = false
		reason = kccTargetConditionReasonMatchMoreOrLessThanOneKCC
		message = fmt.Sprintf("more or less than one kcc %v match same gvr %s", kccKeys, gvr.String())
	} else {
		key := kccKeys[0]
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			klog.Errorf("failed to split namespace and name from key %s", key)
			return err
		}

		kcc, err := k.katalystCustomConfigLister.KatalystCustomConfigs(namespace).Get(name)
		if apierrors.IsNotFound(err) {
			klog.Warningf("kcc %s is not found", key)
			return nil
		} else if err != nil {
			klog.Errorf("kcc %s get error: %v", key, err)
			return err
		}

		isValid, message, overlapTargets, err = k.validateTargetResourceGenericSpec(kcc, targetResource)
		if err != nil {
			return err
		}

		if !isValid {
			reason = kccTargetConditionReasonValidateFailed
		}
	}

	if isValid {
		// update target resource hash only when config is valid
		hash, err = targetResource.GenerateConfigHash()
		if err != nil {
			// if generate config hash failed set target resource invalid
			isValid = false
			message = fmt.Sprintf("generate config hash failed: %s", err)
			reason = kccTargetConditionReasonHashFailed
		}

		// set kcc target hash
		if targetResource.GetHash() != hash {
			targetResource.SetHash(hash)
			target, err = k.unstructuredControl.UpdateUnstructured(k.ctx, gvr, target, metav1.UpdateOptions{})
			if err != nil {
				return err
			}

			targetResource = util.ToKCCTargetResource(target)
		}
	}

	// update kcc target status
	oldKCCTargetResource := targetResource.DeepCopy()
	updateTargetResourceStatus(targetResource, isValid, message, reason)
	if !apiequality.Semantic.DeepEqual(oldKCCTargetResource, targetResource) {
		klog.V(4).Infof("gvr: %s, target: %s need update status", gvr.String(), native.GenerateUniqObjectNameKey(target))
		_, err = k.unstructuredControl.UpdateUnstructuredStatus(k.ctx, gvr, target, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		if len(overlapTargets) > 0 {
			// if kcc target status changed, it needs trigger overlap kcc targets to reconcile
			err := k.enqueueTargets(gvr, overlapTargets)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (k *KatalystCustomConfigTargetController) clearExpiredKCCTarget() {
	k.targetHandler.RangeGVRTargetAccessor(func(gvr metav1.GroupVersionResource, accessor kcctarget.KatalystCustomConfigTargetAccessor) bool {
		configTargets, err := accessor.List(labels.Everything())
		if err != nil {
			return false
		}

		for _, target := range configTargets {
			// expired target config will be re-enqueue periodically to make sure it is cleared
			if util.ToKCCTargetResource(target).CheckExpired(time.Now()) {
				accessor.Enqueue(kccTargetControllerName, target)
			}
		}
		return true
	})
}

func (k *KatalystCustomConfigTargetController) enqueueTargets(gvr metav1.GroupVersionResource, targets []util.KCCTargetResource) error {
	accessor, ok := k.targetHandler.GetTargetAccessorByGVR(gvr)
	if !ok {
		return fmt.Errorf("target accessor %s not found", gvr)
	}

	for _, t := range targets {
		if t.GetDeletionTimestamp() != nil {
			continue
		}

		accessor.Enqueue(kccTargetControllerName, t.Unstructured)
	}

	return nil
}

// handleKCCTargetFinalizer enqueue all kcc target to reconcile when a target was deleted
func (k *KatalystCustomConfigTargetController) handleKCCTargetFinalizer(gvr metav1.GroupVersionResource, target *unstructured.Unstructured) error {
	if !controllerutil.ContainsFinalizer(target, consts.KatalystCustomConfigTargetFinalizerKCCT) {
		return nil
	}

	klog.Infof("handling gvr: %s kcc target %s finalizer", gvr.String(), native.GenerateUniqObjectNameKey(target))
	targets, err := k.listAllKCCTargetResource(gvr)
	if err != nil {
		return err
	}

	// if kcc target deleted, it needs trigger other target to reconcile
	err = k.enqueueTargets(gvr, targets)
	if err != nil {
		return err
	}

	err = kccutil.RemoveKCCTargetFinalizer(k.ctx, k.unstructuredControl, consts.KatalystCustomConfigTargetFinalizerKCCT, gvr, target)
	if err != nil {
		return err
	}

	klog.Infof("success remove gvr: %s kcc target %s finalizer", gvr.String(), native.GenerateUniqObjectNameKey(target))
	return nil
}

// validateTargetResourceGenericSpec validate target resource generic spec as follows rule:
// 1. can not set both labelSelector and nodeNames config at the same time
// 2. if nodeNames is not set, lastDuration must not be set either
// 3. labelSelector config must only contain kcc' labelSelectorKey in priority allowed key list
// 4. labelSelector config cannot overlap with other labelSelector config in same priority
// 5. nodeNames config must set lastDuration to make sure it will be auto cleared
// 6. nodeNames config cannot overlap with other nodeNames config
// 7. it is not allowed two global config (without either labelSelector or nodeNames) overlap
func (k *KatalystCustomConfigTargetController) validateTargetResourceGenericSpec(kcc *configapis.KatalystCustomConfig, targetResource util.KCCTargetResource) (bool, string, []util.KCCTargetResource, error) {
	labelSelector := targetResource.GetLabelSelector()
	nodeNames := targetResource.GetNodeNames()
	if len(labelSelector) != 0 && len(nodeNames) != 0 {
		return false, "both labelSelector and nodeNames has been set", nil, nil
	} else if len(labelSelector) != 0 {
		return k.validateTargetResourceLabelSelector(kcc, targetResource)
	} else if len(nodeNames) != 0 {
		return k.validateTargetResourceNodeNames(kcc, targetResource)
	} else {
		return k.validateTargetResourceGlobal(kcc, targetResource)
	}
}

func (k *KatalystCustomConfigTargetController) validateTargetResourceLabelSelector(kcc *configapis.KatalystCustomConfig, targetResource util.KCCTargetResource) (bool, string, []util.KCCTargetResource, error) {
	priorityAllowedKeyListMap := getPriorityAllowedKeyListMap(kcc)
	if len(priorityAllowedKeyListMap) == 0 {
		return false, fmt.Sprintf("kcc %s no support label selector", native.GenerateUniqObjectNameKey(kcc)), nil, nil
	}

	valid, msg, err := validateLabelSelectorMatchWithKCCDefinition(priorityAllowedKeyListMap, targetResource)
	if err != nil {
		return false, "", nil, nil
	} else if !valid {
		return false, msg, nil, nil
	}

	kccTargetResources, err := k.listAllKCCTargetResource(kcc.Spec.TargetType)
	if err != nil {
		return false, "", nil, err
	}

	return validateLabelSelectorOverlapped(priorityAllowedKeyListMap, targetResource, kccTargetResources)
}

func getPriorityAllowedKeyListMap(kcc *configapis.KatalystCustomConfig) map[int32]sets.String {
	priorityAllowedKeyListMap := make(map[int32]sets.String)
	for _, allowedKey := range kcc.Spec.NodeLabelSelectorAllowedKeyList {
		priorityAllowedKeyListMap[allowedKey.Priority] = sets.NewString(allowedKey.KeyList...)
	}
	return priorityAllowedKeyListMap
}

// validateLabelSelectorMatchWithKCCDefinition make sures that labelSelector config must only contain key in kcc' allowed key list
func validateLabelSelectorMatchWithKCCDefinition(priorityAllowedKeyListMap map[int32]sets.String, targetResource util.KCCTargetResource) (bool, string, error) {
	if targetResource.GetLastDuration() != nil {
		return false, "both labelSelector and lastDuration has been set", nil
	}

	labelSelector := targetResource.GetLabelSelector()
	selector, err := labels.Parse(labelSelector)
	if err != nil {
		return false, fmt.Sprintf("labelSelector parse failed: %s", err), nil
	}

	priority := targetResource.GetPriority()
	allowedKeyList, ok := priorityAllowedKeyListMap[priority]
	if !ok {
		return false, fmt.Sprintf("priority %d not supported", priority), nil
	}

	reqs, selectable := selector.Requirements()
	if !selectable {
		return false, fmt.Sprintf("labelSelector cannot selectable"), nil
	}

	inValidLabelKeys := sets.String{}
	for _, r := range reqs {
		key := r.Key()
		if !allowedKeyList.Has(key) {
			inValidLabelKeys.Insert(key)
		}
	}

	if len(inValidLabelKeys) > 0 {
		return false, fmt.Sprintf("labelSelector with invalid key %v (%s)", inValidLabelKeys.List(), allowedKeyList.List()), nil
	}

	return true, "", nil
}

// validateLabelSelectorOverlapped make sures that labelSelector config cannot overlap with other labelSelector config
func validateLabelSelectorOverlapped(priorityAllowedKeyListMap map[int32]sets.String, targetResource util.KCCTargetResource,
	otherResources []util.KCCTargetResource) (bool, string, []util.KCCTargetResource, error) {
	labelSelector := targetResource.GetLabelSelector()
	selector, err := labels.Parse(labelSelector)
	if err != nil {
		return false, fmt.Sprintf("labelSelector parse failed: %s", err), nil, nil
	}

	priority := targetResource.GetPriority()
	allowedKeyList, ok := priorityAllowedKeyListMap[priority]
	if !ok {
		return false, fmt.Sprintf("priority %d not supported", priority), nil, nil
	}

	var overlapTargets []util.KCCTargetResource
	overlapResources := sets.String{}
	for _, res := range otherResources {
		if (res.GetNamespace() == targetResource.GetNamespace() && res.GetName() == targetResource.GetName()) ||
			len(res.GetLabelSelector()) == 0 {
			continue
		}

		otherSelector, err := labels.Parse(res.GetLabelSelector())
		if err != nil {
			continue
		}

		otherPriority := res.GetPriority()
		if otherPriority != priority {
			continue
		}

		overlap := checkLabelSelectorOverlap(selector, otherSelector, allowedKeyList.List())
		if overlap {
			overlapTargets = append(overlapTargets, res)
			overlapResources.Insert(native.GenerateUniqObjectNameKey(res))
		}
	}

	if len(overlapResources) > 0 {
		return false, fmt.Sprintf("labelSelector overlay with others: %v", overlapResources.List()), overlapTargets, nil
	}

	return true, "", nil, nil
}

func (k *KatalystCustomConfigTargetController) validateTargetResourceNodeNames(kcc *configapis.KatalystCustomConfig, targetResource util.KCCTargetResource) (bool, string, []util.KCCTargetResource, error) {
	if targetResource.GetLastDuration() == nil {
		return false, "nodeNames has been set but lastDuration no set", nil, nil
	}

	kccTargetResources, err := k.listAllKCCTargetResource(kcc.Spec.TargetType)
	if err != nil {
		return false, "", nil, err
	}

	return validateTargetResourceNodeNamesOverlapped(targetResource, kccTargetResources)
}

// validateLabelSelectorOverlapped make sures that nodeNames config cannot overlap with other labelSelector config
func validateTargetResourceNodeNamesOverlapped(targetResource util.KCCTargetResource, otherResources []util.KCCTargetResource) (bool, string, []util.KCCTargetResource, error) {
	nodeNames := sets.NewString(targetResource.GetNodeNames()...)

	var overlapTargets []util.KCCTargetResource
	overlapResources := sets.String{}
	for _, res := range otherResources {
		if (res.GetNamespace() == targetResource.GetNamespace() && res.GetName() == targetResource.GetName()) ||
			len(res.GetNodeNames()) == 0 {
			continue
		}

		otherNodeNames := sets.NewString(res.GetNodeNames()...)
		if nodeNames.Intersection(otherNodeNames).Len() > 0 {
			overlapResources.Insert(native.GenerateUniqObjectNameKey(res))
			overlapTargets = append(overlapTargets, res)
		}
	}

	if len(overlapResources) > 0 {
		return false, fmt.Sprintf("nodeNames overlay with others: %v", overlapResources.List()), overlapTargets, nil
	}

	return true, "", nil, nil
}

func (k *KatalystCustomConfigTargetController) validateTargetResourceGlobal(kcc *configapis.KatalystCustomConfig, targetResource util.KCCTargetResource) (bool, string, []util.KCCTargetResource, error) {
	if targetResource.GetLastDuration() != nil {
		return false, "lastDuration has been set for global config", nil, nil
	}

	kccTargetResources, err := k.listAllKCCTargetResource(kcc.Spec.TargetType)
	if err != nil {
		return false, "", nil, err
	}

	return validateTargetResourceGlobalOverlapped(targetResource, kccTargetResources)
}

// validateLabelSelectorOverlapped make sures that only one global configurations is created.
func validateTargetResourceGlobalOverlapped(targetResource util.KCCTargetResource, otherResources []util.KCCTargetResource) (bool, string, []util.KCCTargetResource, error) {
	var overlapTargets []util.KCCTargetResource
	overlapTargetNames := sets.String{}
	for _, res := range otherResources {
		if (res.GetNamespace() == targetResource.GetNamespace() && res.GetName() == targetResource.GetName()) ||
			(len(res.GetNodeNames()) > 0 || len(res.GetLabelSelector()) > 0) {
			continue
		}

		overlapTargetNames.Insert(native.GenerateUniqObjectNameKey(res))
		overlapTargets = append(overlapTargets, res)
	}

	if len(overlapTargetNames) > 0 {
		return false, fmt.Sprintf("global config %s overlay with others: %v",
			native.GenerateUniqObjectNameKey(targetResource), overlapTargetNames.List()), overlapTargets, nil
	}

	return true, "", nil, nil
}

func (k *KatalystCustomConfigTargetController) listAllKCCTargetResource(gvr metav1.GroupVersionResource) ([]util.KCCTargetResource, error) {
	accessor, ok := k.targetHandler.GetTargetAccessorByGVR(gvr)
	if !ok {
		return nil, fmt.Errorf("target accessor %s not found", gvr)
	}

	list, err := accessor.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	resources := make([]util.KCCTargetResource, 0, len(list))
	for _, obj := range list {
		if obj.GetDeletionTimestamp() != nil {
			continue
		}
		resources = append(resources, util.ToKCCTargetResource(obj))
	}

	return resources, nil
}

func updateTargetResourceStatus(targetResource util.KCCTargetResource, isValid bool, msg, reason string) {
	status := targetResource.GetGenericStatus()
	status.ObservedGeneration = targetResource.GetGeneration()
	if !isValid {
		kccutil.UpdateKCCTGenericConditions(&status, configapis.ConfigConditionTypeValid, v1.ConditionFalse, reason, msg)
	} else {
		kccutil.UpdateKCCTGenericConditions(&status, configapis.ConfigConditionTypeValid, v1.ConditionTrue, reason, "")
	}

	targetResource.SetGenericStatus(status)
}

// checkLabelSelectorOverlap checks whether the labelSelector overlap with other labelSelector by the keyList
func checkLabelSelectorOverlap(selector labels.Selector, otherSelector labels.Selector,
	keyList []string) bool {
	for _, key := range keyList {
		equalLabelSet, inEqualLabelSet, _ := getMatchLabelSet(selector, key)
		otherEqualLabelSet, otherInEqualLabelSet, _ := getMatchLabelSet(otherSelector, key)
		if (equalLabelSet.Len() > 0 && otherEqualLabelSet.Len() > 0 && equalLabelSet.Intersection(otherEqualLabelSet).Len() > 0) ||
			(equalLabelSet.Len() == 0 && otherEqualLabelSet.Len() == 0 && (inEqualLabelSet.Len() > 0 || otherInEqualLabelSet.Len() > 0)) ||
			(inEqualLabelSet.Len() > 0 && !inEqualLabelSet.Intersection(otherEqualLabelSet).Equal(otherEqualLabelSet)) ||
			(otherInEqualLabelSet.Len() > 0 && !otherInEqualLabelSet.Intersection(equalLabelSet).Equal(equalLabelSet)) ||
			(equalLabelSet.Len() > 0 && otherEqualLabelSet.Len() == 0 && otherInEqualLabelSet.Len() == 0) ||
			(otherEqualLabelSet.Len() > 0 && equalLabelSet.Len() == 0 && inEqualLabelSet.Len() == 0) {
			continue
		} else {
			return false
		}
	}

	return true
}

func getMatchLabelSet(selector labels.Selector, key string) (sets.String, sets.String, error) {
	reqs, selectable := selector.Requirements()
	if !selectable {
		return nil, nil, fmt.Errorf("labelSelector cannot selectable")
	}

	equalLabelSet := sets.String{}
	inEqualLabelSet := sets.String{}
	for _, r := range reqs {
		if r.Key() != key {
			continue
		}
		switch r.Operator() {
		case selection.Equals, selection.DoubleEquals, selection.In:
			equalLabelSet = equalLabelSet.Union(r.Values())
		case selection.NotEquals, selection.NotIn:
			inEqualLabelSet = inEqualLabelSet.Union(r.Values())
		default:
			return nil, nil, fmt.Errorf("labelSelector operator %s not supported", r.Operator())
		}
	}
	return equalLabelSet, inEqualLabelSet, nil
}
