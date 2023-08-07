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

package util

import (
	"context"
	"fmt"
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apisv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/listers/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	kcctarget "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// GetRelatedCNCForTargetConfig get current related cnc for the config obey follow rule:
// 1. only updated and deleting config will be concerned, which means that ObservedGeneration and Generation is equal, to make sure the config has been checked by kcc controller
// 2. only valid config will be concerned, because invalid config cover cnc should keep current state
// 3. select related cnc by the config's labelSelector or nodeNames
func GetRelatedCNCForTargetConfig(customNodeConfigLister v1alpha1.CustomNodeConfigLister, unstructured *unstructured.Unstructured) ([]*apisv1alpha1.CustomNodeConfig, error) {
	targetResource := util.ToKCCTargetResource(unstructured)
	if !targetResource.IsUpdated() && targetResource.GetDeletionTimestamp() == nil {
		return nil, nil
	}

	if targetResource.CheckExpired(time.Now()) {
		return nil, nil
	}

	labelSelector := targetResource.GetLabelSelector()
	if labelSelector != "" {
		selector, err := labels.Parse(labelSelector)
		if err != nil {
			return nil, fmt.Errorf("obj %s is valid but parse labelSelector failed",
				native.GenerateUniqObjectNameKey(targetResource))
		}

		customNodeConfigList, err := customNodeConfigLister.List(selector)
		if err != nil {
			return nil, err
		}

		return customNodeConfigList, nil
	}

	nodeNames := targetResource.GetNodeNames()
	if len(nodeNames) > 0 {
		var relatedCustomNodeConfig []*apisv1alpha1.CustomNodeConfig
		for _, nodeName := range nodeNames {
			cnc, err := customNodeConfigLister.Get(nodeName)
			if err != nil {
				return nil, err
			}

			relatedCustomNodeConfig = append(relatedCustomNodeConfig, cnc)
		}
		return relatedCustomNodeConfig, nil
	}

	return customNodeConfigLister.List(labels.Everything())
}

// ApplyKCCTargetConfigToCNC sets the hash value for the given configurations in CNC
func ApplyKCCTargetConfigToCNC(cnc *apisv1alpha1.CustomNodeConfig,
	gvr metav1.GroupVersionResource, kccTarget *unstructured.Unstructured) {
	// only allow one kccTarget for same gvr of a cnc
	if cnc == nil || kccTarget == nil {
		return
	}

	targetResource := util.ToKCCTargetResource(kccTarget)
	if targetResource.CheckExpired(time.Now()) {
		return
	}

	idx := 0
	katalystCustomConfigList := cnc.Status.KatalystCustomConfigList
	// find target config
	for ; idx < len(katalystCustomConfigList); idx++ {
		if katalystCustomConfigList[idx].ConfigType == gvr {
			break
		}
	}

	// update target config if the gvr is already existed, otherwise append it and sort
	if idx < len(katalystCustomConfigList) {
		katalystCustomConfigList[idx] = apisv1alpha1.TargetConfig{
			ConfigType:      gvr,
			ConfigNamespace: targetResource.GetNamespace(),
			ConfigName:      targetResource.GetName(),
			Hash:            targetResource.GetHash(),
		}
	} else {
		katalystCustomConfigList = append(katalystCustomConfigList, apisv1alpha1.TargetConfig{
			ConfigType:      gvr,
			ConfigNamespace: targetResource.GetNamespace(),
			ConfigName:      targetResource.GetName(),
			Hash:            targetResource.GetHash(),
		})

		cnc.Status.KatalystCustomConfigList = katalystCustomConfigList
		sort.SliceStable(katalystCustomConfigList, func(i, j int) bool {
			return katalystCustomConfigList[i].ConfigType.String() < katalystCustomConfigList[j].ConfigType.String()
		})
	}
}

// FindMatchedKCCTargetConfigForNode is to find this cnc needed config, if there are some configs are still not updated, we skip it.
// The rule of cnc match to config is:
// 1. if there is only one matched nodeNames config, and it is valid, return it
// 2. if there is only one matched labelSelector config with the highest priority, and it is valid, return it
// 3. if there is only one global config (either nodeNames or labelSelector is not existed), and it is valid, return it
// 4. otherwise, return nil to keep current state no changed
func FindMatchedKCCTargetConfigForNode(cnc *apisv1alpha1.CustomNodeConfig, targetAccessor kcctarget.KatalystCustomConfigTargetAccessor) (*unstructured.Unstructured, error) {
	kccTargetList, err := targetAccessor.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	kccTargetList, err = filterAvailableKCCTargetConfigs(kccTargetList)
	if err != nil {
		return nil, err
	}

	kccTargetList, err = findMatchedKCCTargetConfigForNode(cnc, kccTargetList)
	if err != nil {
		return nil, err
	}

	if len(kccTargetList) == 0 {
		return nil, fmt.Errorf("matched kcc target config not found")
	} else if len(kccTargetList) > 1 {
		sort.SliceStable(kccTargetList, func(i, j int) bool {
			return util.ToKCCTargetResource(kccTargetList[i]).GetPriority() >
				util.ToKCCTargetResource(kccTargetList[j]).GetPriority()
		})

		if util.ToKCCTargetResource(kccTargetList[0]).GetPriority() ==
			util.ToKCCTargetResource(kccTargetList[1]).GetPriority() {
			return nil, fmt.Errorf("more than one kcc target config found with same priority")
		}
	}

	if !util.ToKCCTargetResource(kccTargetList[0]).CheckValid() {
		return nil, fmt.Errorf("one kcc target config found but invalid")
	}

	return kccTargetList[0], nil
}

// ClearUnNeededConfigForNode delete those un-needed configurations from CNC status
func ClearUnNeededConfigForNode(cnc *apisv1alpha1.CustomNodeConfig, needToDelete func(metav1.GroupVersionResource) bool) {
	if cnc == nil {
		return
	}

	katalystCustomConfigList := make([]apisv1alpha1.TargetConfig, 0, len(cnc.Status.KatalystCustomConfigList))
	for _, config := range cnc.Status.KatalystCustomConfigList {
		if needToDelete(config.ConfigType) {
			continue
		}
		katalystCustomConfigList = append(katalystCustomConfigList, config)
	}

	cnc.Status.KatalystCustomConfigList = katalystCustomConfigList
}

// filterAvailableKCCTargetConfigs returns those available configurations from kcc target list
func filterAvailableKCCTargetConfigs(kccTargetList []*unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	availableKCCTargetList := make([]*unstructured.Unstructured, 0, len(kccTargetList))
	// check whether all kccTarget has been updated
	for _, kccTarget := range kccTargetList {
		if kccTarget.GetDeletionTimestamp() != nil {
			continue
		}

		if !util.ToKCCTargetResource(kccTarget).IsUpdated() {
			return nil, fmt.Errorf("kccTarget %s is updating", native.GenerateUniqObjectNameKey(kccTarget))
		}

		availableKCCTargetList = append(availableKCCTargetList, kccTarget)
	}

	return availableKCCTargetList, nil
}

// findMatchedKCCTargetConfigForNode gets the matched configurations for CNC CR
// if multiple configurations can match up with the given node, return nil to ignore all of them
func findMatchedKCCTargetConfigForNode(cnc *apisv1alpha1.CustomNodeConfig, kccTargetList []*unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	var matchedNodeNameConfigs, matchedLabelSelectorConfigs, matchedGlobalConfigs []*unstructured.Unstructured
	for _, obj := range kccTargetList {
		targetResource := util.ToKCCTargetResource(obj)
		nodeNames := targetResource.GetNodeNames()
		labelSelector := targetResource.GetLabelSelector()
		if len(nodeNames) > 0 {
			if sets.NewString(nodeNames...).Has(cnc.GetName()) {
				matchedNodeNameConfigs = append(matchedNodeNameConfigs, obj)
			}
			continue
		}

		if labelSelector != "" {
			selector, err := labels.Parse(labelSelector)
			if err != nil {
				// if some label selector config parse failed, we make all label selector config invalid
				matchedLabelSelectorConfigs = append(matchedLabelSelectorConfigs, obj)
				continue
			}

			cncLabels := cnc.GetLabels()
			if selector.Matches(labels.Set(cncLabels)) {
				matchedLabelSelectorConfigs = append(matchedLabelSelectorConfigs, obj)
			}
			continue
		}

		matchedGlobalConfigs = append(matchedGlobalConfigs, obj)
	}

	if len(matchedNodeNameConfigs) > 0 {
		return matchedNodeNameConfigs, nil
	} else if len(matchedLabelSelectorConfigs) > 0 {
		return matchedLabelSelectorConfigs, nil
	} else if len(matchedGlobalConfigs) > 0 {
		return matchedGlobalConfigs, nil
	}

	return nil, nil
}

// UpdateKCCTGenericConditions is used to update conditions for kcc
func UpdateKCCTGenericConditions(status *apisv1alpha1.GenericConfigStatus, conditionType apisv1alpha1.ConfigConditionType,
	conditionStatus v1.ConditionStatus, reason, message string) bool {
	var (
		updated bool
		found   bool
	)

	conditions := status.Conditions
	for idx := range conditions {
		if conditions[idx].Type == conditionType {
			if conditions[idx].Status != conditionStatus {
				conditions[idx].Status = conditionStatus
				conditions[idx].LastTransitionTime = metav1.NewTime(time.Now())
				updated = true
			}
			if conditions[idx].Reason != reason {
				conditions[idx].Reason = reason
				updated = true
			}
			if conditions[idx].Message != message {
				conditions[idx].Message = message
				updated = true
			}
			found = true
			break
		}
	}

	if !found {
		conditions = append(conditions, apisv1alpha1.GenericConfigCondition{
			Type:               conditionType,
			Status:             conditionStatus,
			LastTransitionTime: metav1.NewTime(time.Now()),
			Reason:             reason,
			Message:            message,
		})
		status.Conditions = conditions
		updated = true
	}

	return updated
}

// EnsureKCCTargetFinalizer is used to add finalizer in kcc target
// any component (that depends on kcc target) should add a specific finalizer in the target CR
func EnsureKCCTargetFinalizer(ctx context.Context, unstructuredControl control.UnstructuredControl, finalizerName string,
	gvr metav1.GroupVersionResource, target *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var (
			err, getErr error
		)
		if controllerutil.ContainsFinalizer(target, finalizerName) {
			return nil
		}

		controllerutil.AddFinalizer(target, finalizerName)
		newTarget, err := unstructuredControl.UpdateUnstructured(ctx, gvr, target, metav1.UpdateOptions{})
		if errors.IsConflict(err) {
			newTarget, getErr = unstructuredControl.GetUnstructured(ctx, gvr, target.GetNamespace(), target.GetName(), metav1.GetOptions{ResourceVersion: "0"})
			if getErr != nil {
				return getErr
			}
		}

		target = newTarget
		return err
	})
	if err != nil {
		return nil, err
	}

	return target, nil
}

// RemoveKCCTargetFinalizer is used to remove finalizer in kcc target
// any component (that depends on kcc target) should make sure its dependency has been relieved before remove
func RemoveKCCTargetFinalizer(ctx context.Context, unstructuredControl control.UnstructuredControl, finalizerName string,
	gvr metav1.GroupVersionResource, target *unstructured.Unstructured) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var (
			err, getErr error
		)

		if !controllerutil.ContainsFinalizer(target, finalizerName) {
			return nil
		}

		controllerutil.RemoveFinalizer(target, finalizerName)
		newTarget, err := unstructuredControl.UpdateUnstructured(ctx, gvr, target, metav1.UpdateOptions{})
		if errors.IsConflict(err) {
			newTarget, getErr = unstructuredControl.GetUnstructured(ctx, gvr, target.GetNamespace(), target.GetName(), metav1.GetOptions{ResourceVersion: "0"})
			if getErr != nil {
				return getErr
			}
		}

		target = newTarget
		return err
	})

	return err
}
