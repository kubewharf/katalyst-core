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
	"sort"
	"sync"
	"time"

	"golang.org/x/time/rate"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
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
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	kccTargetControllerName = "kcct"
)

const (
	kcctWorkerCount = 1
	cncWorkerCount  = 16
)

const (
	defaultCNCEnqueueDelay  = 20 * time.Second
	defaultKCCTEnqueueDelay = 10 * time.Second
)

const (
	defaultCNCUpdateQPS   = 10
	defaultCNCUpdateBurst = 100
)

const (
	kccTargetConditionReasonNormal                      = "Normal"
	kccTargetConditionReasonHashFailed                  = "HashFailed"
	kccTargetConditionReasonMatchMoreOrLessThanOneKCC   = "MatchMoreOrLessThanOneKCC"
	kccTargetConditionReasonValidateFailed              = "ValidateFailed"
	kccTargetConditionReasonCalculateCanaryCutoffFailed = "CalculateCanaryCutoffFailed"
)

type KatalystCustomConfigTargetController struct {
	ctx       context.Context
	dryRun    bool
	kccConfig *controller.KCCConfig

	client              *kcclient.GenericClientSet
	kccControl          control.KCCControl
	unstructuredControl control.UnstructuredControl
	cncControl          control.CNCControl

	// listers from the shared informer's stores
	katalystCustomConfigLister v1alpha1.KatalystCustomConfigLister
	customNodeConfigLister     v1alpha1.CustomNodeConfigLister

	syncedFunc []cache.InformerSynced

	queue workqueue.RateLimitingInterface

	rateLimiters sync.Map

	// targetHandler store gvr kcc and gvr
	targetHandler *kcctarget.KatalystCustomConfigTargetHandler

	// metricsEmitter for emit metrics
	metricsEmitter metrics.MetricEmitter

	cncEnqueueDelay  time.Duration
	kcctEnqueueDelay time.Duration
	cncUpdateQPS     int
	cncUpdateBurst   int
}

func NewKatalystCustomConfigTargetController(
	ctx context.Context,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	kccConfig *controller.KCCConfig,
	client *kcclient.GenericClientSet,
	katalystCustomConfigInformer configinformers.KatalystCustomConfigInformer,
	customNodeConfigInformer configinformers.CustomNodeConfigInformer,
	metricsEmitter metrics.MetricEmitter,
	targetHandler *kcctarget.KatalystCustomConfigTargetHandler,
) (*KatalystCustomConfigTargetController, error) {
	k := &KatalystCustomConfigTargetController{
		ctx:                        ctx,
		client:                     client,
		dryRun:                     genericConf.DryRun,
		kccConfig:                  kccConfig,
		katalystCustomConfigLister: katalystCustomConfigInformer.Lister(),
		customNodeConfigLister:     customNodeConfigInformer.Lister(),
		targetHandler:              targetHandler,
		syncedFunc: []cache.InformerSynced{
			katalystCustomConfigInformer.Informer().HasSynced,
			customNodeConfigInformer.Informer().HasSynced,
			targetHandler.HasSynced,
		},
		queue:            workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), kccTargetControllerName),
		rateLimiters:     sync.Map{},
		cncEnqueueDelay:  defaultCNCEnqueueDelay,
		kcctEnqueueDelay: defaultKCCTEnqueueDelay,
		cncUpdateQPS:     defaultCNCUpdateQPS,
		cncUpdateBurst:   defaultCNCUpdateBurst,
	}

	if metricsEmitter == nil {
		k.metricsEmitter = metrics.DummyMetrics{}
	} else {
		k.metricsEmitter = metricsEmitter.WithTags(kccTargetControllerName)
	}

	k.kccControl = control.DummyKCCControl{}
	k.unstructuredControl = control.DummyUnstructuredControl{}
	k.cncControl = control.DummyCNCControl{}
	if !k.dryRun {
		k.kccControl = control.NewRealKCCControl(client.InternalClient)
		k.unstructuredControl = control.NewRealUnstructuredControl(client.DynamicClient)
		k.cncControl = control.NewRealCNCControl(client.InternalClient)
	}

	customNodeConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    k.handleCNCAdd,
		UpdateFunc: k.handleCNCUpdate,
		DeleteFunc: k.handleCNCDelete,
	})

	// register kcc-target informer handler
	targetHandler.RegisterTargetHandler(kccTargetControllerName, k.handleTargetEvent)
	return k, nil
}

// Run don't need to trigger reconcile logic.
func (k *KatalystCustomConfigTargetController) Run() {
	defer utilruntime.HandleCrash()
	defer k.queue.ShutDown()

	defer klog.Infof("shutting down %s controller", kccTargetControllerName)

	if !cache.WaitForCacheSync(k.ctx.Done(), k.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", kccTargetControllerName))
		return
	}
	klog.Infof("caches are synced for %s controller", kccTargetControllerName)
	klog.Infof("start %d workers for %s controller", kcctWorkerCount, kccTargetControllerName)

	for i := 0; i < kcctWorkerCount; i++ {
		go wait.Until(k.worker, time.Second, k.ctx.Done())
	}
	go wait.Until(k.clearUnusedConfig, 5*time.Minute, k.ctx.Done())

	<-k.ctx.Done()
}

func (k *KatalystCustomConfigTargetController) handleCNCAdd(obj interface{}) {
	k.handleCNCStatusUpdate()
}

func (k *KatalystCustomConfigTargetController) handleCNCUpdate(old, new interface{}) {
	oldCNC, ok := old.(*configapis.CustomNodeConfig)
	if !ok {
		general.Errorf("received invalid old CNC type: %T", old)
		return
	}
	newCNC, ok := new.(*configapis.CustomNodeConfig)
	if !ok {
		general.Errorf("received invalid new CNC type: %T", new)
		return
	}

	if !apiequality.Semantic.DeepEqual(oldCNC, newCNC) {
		k.handleCNCStatusUpdate()
	}
}

func (k *KatalystCustomConfigTargetController) handleCNCDelete(obj interface{}) {
	k.handleCNCStatusUpdate()
}

// Enqueue all kcc target gvr to correct any accidentally changed CNC status and update the KCCT status.
func (k *KatalystCustomConfigTargetController) handleCNCStatusUpdate() {
	// enqueue all kcct gvr
	k.targetHandler.RangeGVRTargetAccessor(func(gvr metav1.GroupVersionResource, accessor kcctarget.KatalystCustomConfigTargetAccessor) bool {
		// enqueue gvr with delay to frequent reconciles caused by many CNC events
		k.queue.AddAfter(gvr, k.cncEnqueueDelay)
		return true
	})
}

func (k *KatalystCustomConfigTargetController) handleTargetEvent(gvr metav1.GroupVersionResource, _ *unstructured.Unstructured) error {
	k.queue.AddAfter(gvr, k.kcctEnqueueDelay)
	return nil
}

func (k *KatalystCustomConfigTargetController) worker() {
	for k.processNextWorkItem() {
	}
}

func (k *KatalystCustomConfigTargetController) processNextWorkItem() bool {
	key, quit := k.queue.Get()
	if quit {
		return false
	}
	defer k.queue.Done(key)

	gvr, ok := key.(metav1.GroupVersionResource)
	if !ok {
		k.queue.Forget(key)
		general.Errorf("received invalid key type: %T", key)
		return true
	}

	err := k.syncKCCTs(gvr)
	if err == nil {
		k.queue.Forget(key)
		return true
	}

	general.Errorf("sync kcct gvr %s failed with %v", gvr, err)
	k.queue.AddRateLimited(key)

	return true
}

/*
syncKCCTs is the main reconciliation logic for Katalyst Custom Config Target Controller.
It roughly performs the following steps:

1. handle terminating kcc target(s)

2. clear expired kcc target(s)

3. check the validity of each kcc target

4. calculate the scope of each kcc target

5. update the CNCs in each kcc target's scope

6. update the status of each kcc target
*/
func (k *KatalystCustomConfigTargetController) syncKCCTs(gvr metav1.GroupVersionResource) error {
	reconcileStartTime := time.Now()
	general.InfofV(4, "reconcile KCCT GVR %s", gvr.String())
	defer func() {
		general.InfofV(4, "reconcile KCCT GVR %s finished in %v", gvr.String(), time.Since(reconcileStartTime))
	}()

	// Since each KCC target can affect the validity and scope of other KCC targets,
	// we reconcile all KCC targets of the same GVR at once.
	accessor, ok := k.targetHandler.GetTargetAccessorByGVR(gvr)
	if !ok {
		return fmt.Errorf("target accessor %s not found", gvr)
	}
	list, err := accessor.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("list kcc targets failed: %w", err)
	}

	var errors []error

	targetResources := make([]util.KCCTargetResource, 0, len(list))
	for _, obj := range list {
		if obj.GetDeletionTimestamp() != nil {
			// handle kcc target finalizer
			if err := k.handleKCCTargetFinalizers(gvr, obj); err != nil {
				errors = append(errors, fmt.Errorf("handle kcc target finalizer failed: %w", err))
			}
			continue
		}

		targetResource := util.ToKCCTargetResource(obj.DeepCopy())
		if validityPeriod := targetResource.GetLastDuration(); validityPeriod != nil {
			expiry := targetResource.GetCreationTimestamp().Add(*validityPeriod)
			untilExpiry := time.Until(expiry)
			if untilExpiry <= 0 {
				// delete expired kcc target
				general.Infof("delete expired kcc target %s %s", gvr.String(), native.GenerateUniqObjectNameKey(targetResource))
				err := k.unstructuredControl.DeleteUnstructured(k.ctx, gvr, obj, metav1.DeleteOptions{})
				if err != nil && !apierrors.IsNotFound(err) {
					errors = append(errors, fmt.Errorf("delete expired kcc target failed: %w", err))
				}
				continue
			} else {
				// enqueue the yet-to-expire kcc target for cleanup at its expiry
				k.queue.AddAfter(gvr, untilExpiry)
			}
		}

		targetResources = append(targetResources, targetResource)
	}

	if len(targetResources) == 0 {
		return utilerrors.NewAggregate(errors)
	}

	// Check if the corresponding KCC is valid
	kccKeys := k.targetHandler.GetKCCKeyListByGVR(gvr)
	if len(kccKeys) != 1 {
		message := fmt.Sprintf("more or less than one kcc %v match same gvr %s", kccKeys, gvr.String())
		for _, targetResource := range targetResources {
			newTargetResource := targetResource.DeepCopy()
			updateInvalidTargetResourceStatus(newTargetResource, message, kccTargetConditionReasonMatchMoreOrLessThanOneKCC)
			if !apiequality.Semantic.DeepEqual(newTargetResource, targetResource) {
				general.Infof("gvr: %s, target: %s need update status due to more or less than one kcc keys %v", gvr.String(), native.GenerateUniqObjectNameKey(targetResource), kccKeys)
				_, err = k.unstructuredControl.UpdateUnstructuredStatus(k.ctx, gvr, newTargetResource.Unstructured, metav1.UpdateOptions{})
				if err != nil {
					errors = append(errors, fmt.Errorf("update kcc target status failed: %w", err))
				}
			}
		}

		return utilerrors.NewAggregate(errors)
	}

	// Check if each KCCT is valid and update its validity status
	kccKey := kccKeys[0]
	namespace, name, err := cache.SplitMetaNamespaceKey(kccKey)
	if err != nil {
		return utilerrors.NewAggregate(append(errors, fmt.Errorf("failed to split namespace and name from kcc key %s: %w", kccKey, err)))
	}

	kcc, err := k.katalystCustomConfigLister.KatalystCustomConfigs(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		return utilerrors.NewAggregate(append(errors, fmt.Errorf("kcc %s is not found", kccKey)))
	} else if err != nil {
		return utilerrors.NewAggregate(append(errors, fmt.Errorf("get kcc %s failed: %w", kccKey, err)))
	}

	invalidKCCTs := []string{}
	for _, targetResource := range targetResources {
		isValid, message, err := k.validateTargetResourceGenericSpec(kcc, targetResource, targetResources)
		if err != nil {
			errors = append(errors, fmt.Errorf("validate kcc target resource failed: %w", err))
			invalidKCCTs = append(invalidKCCTs, native.GenerateUniqObjectNameKey(targetResource))
		}

		if isValid {
			continue
		}

		invalidKCCTs = append(invalidKCCTs, native.GenerateUniqObjectNameKey(targetResource))
		newTargetResource := targetResource.DeepCopy()
		updateInvalidTargetResourceStatus(newTargetResource, message, kccTargetConditionReasonValidateFailed)
		if !apiequality.Semantic.DeepEqual(newTargetResource, targetResource) {
			general.Infof("gvr: %s, target: %s need update status due to failed validation: %s", gvr.String(), native.GenerateUniqObjectNameKey(targetResource), message)
			_, err := k.unstructuredControl.UpdateUnstructuredStatus(k.ctx, gvr, newTargetResource.Unstructured, metav1.UpdateOptions{})
			if err != nil {
				errors = append(errors, fmt.Errorf("update kcc target %s %s status failed: %w", gvr.String(), native.GenerateUniqObjectNameKey(targetResource), err))
			}
		}
	}

	if len(errors) > 0 {
		return utilerrors.NewAggregate(errors)
	}

	// The scope of each KCC target depends on other KCC targets.
	// If any KCC target is invalid, we need to wait until it is corrected to be able to compute the scope of each KCC target.
	if len(invalidKCCTs) > 0 {
		general.Infof("skip manage CNCs for KCCT GVR %s due to presence of invalid KCCTs %v", gvr.String(), invalidKCCTs)
		return nil
	}

	return k.manageCNCs(gvr, targetResources)
}

func (k *KatalystCustomConfigTargetController) manageCNCs(
	gvr metav1.GroupVersionResource,
	targetResources []util.KCCTargetResource,
) error {
	allCNCs, err := k.customNodeConfigLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("list CNCs failed: %w", err)
	}
	// sort the cncs by name for deterministic order
	sort.Slice(allCNCs, func(i, j int) bool {
		return allCNCs[i].GetName() < allCNCs[j].GetName()
	})

	// any errors encountered from this point onwards should not result in immediate return
	var errors []error

	// group the CNCs by KCCT
	targetCNCIndexes, errs := k.groupCNCsByKCCT(allCNCs, targetResources)
	if len(errs) > 0 {
		errors = append(errors, errs...)
	}

	// calculate the hash and canary cutoff point for each KCCT
	targetResources, hashes, errs := k.generateConfigHashesAndMaybeUpdateStatus(gvr, targetResources)
	if len(errs) > 0 {
		errors = append(errors, errs...)
	}
	targetResources, canaryCutoffPoints, errs := k.computeCanaryCutoffPointsAndMaybeUpdateStatus(gvr, targetResources, targetCNCIndexes)
	if len(errs) > 0 {
		errors = append(errors, errs...)
	}

	rateLimited, errs := k.updateCNCs(gvr, targetResources, hashes, canaryCutoffPoints, targetCNCIndexes, allCNCs)
	if len(errs) > 0 {
		errors = append(errors, errs...)
	}
	// If cnc updates are rate limited, requeue the GVR for later reconciliation
	if rateLimited {
		k.queue.AddAfter(gvr, time.Duration(k.cncUpdateBurst/k.cncUpdateQPS/2)*time.Second)
	}

	errs = k.updateTargetStatuses(gvr, targetResources, hashes, canaryCutoffPoints, targetCNCIndexes, allCNCs)
	if len(errs) > 0 {
		errors = append(errors, errs...)
	}

	return utilerrors.NewAggregate(errors)
}

func (k *KatalystCustomConfigTargetController) groupCNCsByKCCT(
	allCNCs []*configapis.CustomNodeConfig,
	targetResources []util.KCCTargetResource,
) (map[string][]int, []error) {
	var errors []error
	targetCNCIndexes := make(map[string][]int)

	for i, cnc := range allCNCs {
		if cnc.GetDeletionTimestamp() != nil {
			continue
		}

		matchedTarget, err := kccutil.FindMatchedKCCTargetConfigForNode(cnc, targetResources)
		if err != nil {
			errors = append(errors, fmt.Errorf("find matched target for CNC %s failed: %w", cnc.GetName(), err))
			continue
		}

		kcctName := native.GenerateUniqObjectNameKey(matchedTarget)
		targetCNCIndexes[kcctName] = append(targetCNCIndexes[kcctName], i)
	}

	return targetCNCIndexes, errors
}

func (k *KatalystCustomConfigTargetController) generateConfigHashesAndMaybeUpdateStatus(
	gvr metav1.GroupVersionResource,
	targetResources []util.KCCTargetResource,
) ([]util.KCCTargetResource, map[string]string, []error) {
	validTargetResources := make([]util.KCCTargetResource, 0, len(targetResources))
	hashes := make(map[string]string, len(targetResources))
	var errors []error

	for _, targetResource := range targetResources {
		kcctName := native.GenerateUniqObjectNameKey(targetResource)

		// generate the hash for the target resource. If the hash generation fails, update the status of the KCCT and skip reconciling its target CNCs
		hash, err := targetResource.GenerateConfigHash()
		if err != nil {
			message := fmt.Sprintf("failed to generate hash: %v", err)
			newTargetResource := targetResource.DeepCopy()
			updateInvalidTargetResourceStatus(newTargetResource, message, kccTargetConditionReasonHashFailed)
			if !apiequality.Semantic.DeepEqual(newTargetResource, targetResource) {
				general.Infof("gvr: %s, target: %s need update status due to hash generation: %v", gvr.String(), kcctName, err)
				_, err := k.unstructuredControl.UpdateUnstructuredStatus(k.ctx, gvr, newTargetResource.Unstructured, metav1.UpdateOptions{})
				if err != nil {
					errors = append(errors, fmt.Errorf("update kcc target %s %s status failed: %w", gvr.String(), kcctName, err))
				}
			}
			continue
		}

		hashes[kcctName] = hash
		validTargetResources = append(validTargetResources, targetResource)
	}

	return validTargetResources, hashes, errors
}

func (k *KatalystCustomConfigTargetController) computeCanaryCutoffPointsAndMaybeUpdateStatus(
	gvr metav1.GroupVersionResource,
	targetResources []util.KCCTargetResource,
	targetCNCIndexes map[string][]int,
) ([]util.KCCTargetResource, map[string]int, []error) {
	validTargetResources := make([]util.KCCTargetResource, 0, len(targetResources))
	canaryCutoffPoints := make(map[string]int, len(targetResources))
	var errors []error

	for _, targetResource := range targetResources {
		kcctName := native.GenerateUniqObjectNameKey(targetResource)

		// calculate the canary cutoff point for the KCCT. If the calculation fails, update the status of the KCCT and skip reconciling its target CNCs
		numTargetCNCs := len(targetCNCIndexes[kcctName])
		canaryConfig := targetResource.GetCanary()
		// if canaryConfig is nil, all nodes are canary nodes
		if canaryConfig == nil {
			canaryCutoffPoints[kcctName] = numTargetCNCs
		} else {
			// if canaryConfig is not nil, we need to calculate the cutoff point
			cutoffPoint, err := intstr.GetScaledValueFromIntOrPercent(canaryConfig, numTargetCNCs, false)
			if err != nil {
				newTargetResource := targetResource.DeepCopy()
				updateInvalidTargetResourceStatus(newTargetResource, fmt.Sprintf("failed to get canary cutoff point: %s", err), kccTargetConditionReasonCalculateCanaryCutoffFailed)
				if !apiequality.Semantic.DeepEqual(newTargetResource, targetResource) {
					general.Infof("gvr: %s, target: %s need update status due to canary cutoff calculation: %v", gvr.String(), kcctName, err)
					_, err := k.unstructuredControl.UpdateUnstructuredStatus(k.ctx, gvr, newTargetResource.Unstructured, metav1.UpdateOptions{})
					if err != nil {
						errors = append(errors, fmt.Errorf("update kcc target %s %s status failed: %w", gvr.String(), kcctName, err))
					}
				}
				continue
			}

			if cutoffPoint < 0 {
				cutoffPoint = 0
			} else if cutoffPoint > numTargetCNCs {
				cutoffPoint = numTargetCNCs
			}

			canaryCutoffPoints[kcctName] = cutoffPoint
		}

		general.Infof("kcct %s %s targetCNCs=%d canaryCutoff=%d", gvr.String(), kcctName, numTargetCNCs, canaryCutoffPoints[kcctName])
		validTargetResources = append(validTargetResources, targetResource)
	}

	return validTargetResources, canaryCutoffPoints, errors
}

func (k *KatalystCustomConfigTargetController) updateCNCs(
	gvr metav1.GroupVersionResource,
	targetResources []util.KCCTargetResource,
	hashes map[string]string,
	canaryCutoffPoints map[string]int,
	targetCNCIndexes map[string][]int,
	allCNCs []*configapis.CustomNodeConfig,
) (bool, []error) {
	var errors []error

	rateLimiterRaw, _ := k.rateLimiters.LoadOrStore(gvr, rate.NewLimiter(rate.Limit(k.cncUpdateQPS), k.cncUpdateBurst))
	rateLimiter := rateLimiterRaw.(*rate.Limiter)
	rateLimited := false

	// find the CNCs that need to be updated
	type updateTask struct {
		targetResource util.KCCTargetResource
		cncIndex       int
	}
	updateTasks := []updateTask{}

kcctLoop:
	for _, targetResource := range targetResources {
		if targetResource.GetPaused() {
			continue
		}

		kcctName := native.GenerateUniqObjectNameKey(targetResource)
		cutoffPoint := canaryCutoffPoints[kcctName]
		for _, cncIndex := range targetCNCIndexes[kcctName][:cutoffPoint] {
			if !kccutil.IsCNCUpdated(allCNCs[cncIndex], gvr, targetResource, hashes[kcctName]) {
				if !rateLimiter.Allow() {
					rateLimited = true
					break kcctLoop
				}

				updateTasks = append(updateTasks, updateTask{
					targetResource: targetResource,
					cncIndex:       cncIndex,
				})
			}
		}
	}

	general.Infof("updating %d CNCs for GVR %s", len(updateTasks), gvr.String())

	// update the CNCs
	mu := sync.Mutex{}
	failedCount := 0
	workqueue.ParallelizeUntil(k.ctx, cncWorkerCount, len(updateTasks), func(i int) {
		task := updateTasks[i]
		oldCNC := allCNCs[task.cncIndex]
		newCNC := oldCNC.DeepCopy()
		kcctName := native.GenerateUniqObjectNameKey(task.targetResource)
		kccutil.ApplyKCCTargetConfigToCNC(newCNC, gvr, task.targetResource, hashes[kcctName])
		newCNC, err := k.cncControl.PatchCNCStatus(k.ctx, oldCNC.GetName(), oldCNC, newCNC)

		if err != nil {
			mu.Lock()
			defer mu.Unlock()
			errors = append(errors, fmt.Errorf("update CNC %s status failed: %w", oldCNC.GetName(), err))
			failedCount++
		} else {
			// also update the CNC in the cache for kcct status calculation
			allCNCs[task.cncIndex] = newCNC
		}
	})

	general.Infof("updated %d CNCs for GVR %s, %d failed", len(updateTasks)-failedCount, gvr.String(), failedCount)
	return rateLimited, errors
}

func (k *KatalystCustomConfigTargetController) updateTargetStatuses(
	gvr metav1.GroupVersionResource,
	targetResources []util.KCCTargetResource,
	hashes map[string]string,
	canaryCutoffPoints map[string]int,
	targetCNCIndexes map[string][]int,
	allCNCs []*configapis.CustomNodeConfig,
) []error {
	var errors []error

	for _, targetResource := range targetResources {
		kcctName := native.GenerateUniqObjectNameKey(targetResource)
		cutoffPoint := canaryCutoffPoints[kcctName]
		targets := targetCNCIndexes[kcctName]
		targetIndexSet := sets.NewInt(targets...)

		var (
			targetNodes        = int32(len(targets))
			canaryNodes        = int32(cutoffPoint)
			updatedTargetNodes int32
			updatedNodes       int32
			hash               = hashes[kcctName]
		)
		for i, cnc := range allCNCs {
			if kccutil.IsCNCUpdated(cnc, gvr, targetResource, hashes[kcctName]) {
				updatedNodes++
				if targetIndexSet.Has(i) {
					updatedTargetNodes++
				}
			}
		}

		newTargetResource := targetResource.DeepCopy()
		updateValidTargetResourceStatus(newTargetResource, targetNodes, canaryNodes, updatedTargetNodes, updatedNodes, hash)
		if !apiequality.Semantic.DeepEqual(newTargetResource, targetResource) {
			general.Infof(
				"kcct %s %s update status targetNodes=%d canaryNodes=%d updatedTargetNodes=%d updatedNodes=%d hash=%s",
				gvr.String(), kcctName, targetNodes, canaryNodes, updatedTargetNodes, updatedNodes, hash)
			_, err := k.unstructuredControl.UpdateUnstructuredStatus(k.ctx, gvr, newTargetResource.Unstructured, metav1.UpdateOptions{})
			if err != nil {
				errors = append(errors, fmt.Errorf("update kcc target %s %s status failed: %w", gvr.String(), kcctName, err))
			}
		}
	}

	return errors
}

// NOTE: we no longer require these finalizers and will not add them to new KCCTs,
// but we keep the finalizer removal code for backward compatibility.
func (k *KatalystCustomConfigTargetController) handleKCCTargetFinalizers(
	gvr metav1.GroupVersionResource,
	target *unstructured.Unstructured,
) error {
	if !controllerutil.ContainsFinalizer(target, consts.KatalystCustomConfigTargetFinalizerKCCT) &&
		!controllerutil.ContainsFinalizer(target, consts.KatalystCustomConfigTargetFinalizerCNC) {
		return nil
	}

	general.Infof("removing gvr %s kcc target %s finalizer", gvr.String(), native.GenerateUniqObjectNameKey(target))
	err := kccutil.RemoveKCCTargetFinalizers(
		k.ctx, k.unstructuredControl, gvr, target,
		consts.KatalystCustomConfigTargetFinalizerKCCT,
		consts.KatalystCustomConfigTargetFinalizerCNC,
	)
	if err != nil {
		return err
	}

	general.Infof("successfully removed gvr %s kcc target %s finalizer", gvr.String(), native.GenerateUniqObjectNameKey(target))
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
func (k *KatalystCustomConfigTargetController) validateTargetResourceGenericSpec(
	kcc *configapis.KatalystCustomConfig,
	targetResource util.KCCTargetResource,
	allTargetResoures []util.KCCTargetResource,
) (bool, string, error) {
	labelSelector := targetResource.GetLabelSelector()
	nodeNames := targetResource.GetNodeNames()
	if len(labelSelector) != 0 && len(nodeNames) != 0 {
		return false, "both labelSelector and nodeNames has been set", nil
	} else if len(labelSelector) != 0 {
		return k.validateTargetResourceLabelSelector(kcc, targetResource, allTargetResoures)
	} else if len(nodeNames) != 0 {
		return k.validateTargetResourceNodeNames(kcc, targetResource, allTargetResoures)
	} else {
		return k.validateTargetResourceGlobal(kcc, targetResource, allTargetResoures)
	}
}

func (k *KatalystCustomConfigTargetController) validateTargetResourceLabelSelector(
	kcc *configapis.KatalystCustomConfig,
	targetResource util.KCCTargetResource,
	allTargetResources []util.KCCTargetResource,
) (bool, string, error) {
	priorityAllowedKeyListMap := getPriorityAllowedKeyListMap(kcc)
	if len(priorityAllowedKeyListMap) == 0 {
		return false, fmt.Sprintf("kcc %s no support label selector", native.GenerateUniqObjectNameKey(kcc)), nil
	}

	valid, msg, err := validateLabelSelectorMatchWithKCCDefinition(priorityAllowedKeyListMap, targetResource)
	if err != nil {
		return false, "", nil
	} else if !valid {
		return false, msg, nil
	}

	return validateLabelSelectorOverlapped(priorityAllowedKeyListMap, targetResource, allTargetResources)
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
	otherResources []util.KCCTargetResource,
) (bool, string, error) {
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
			overlapResources.Insert(native.GenerateUniqObjectNameKey(res))
		}
	}

	if len(overlapResources) > 0 {
		return false, fmt.Sprintf("labelSelector overlay with others: %v", overlapResources.List()), nil
	}

	return true, "", nil
}

func (k *KatalystCustomConfigTargetController) validateTargetResourceNodeNames(
	kcc *configapis.KatalystCustomConfig,
	targetResource util.KCCTargetResource,
	allTargetResources []util.KCCTargetResource,
) (bool, string, error) {
	if targetResource.GetLastDuration() == nil {
		return false, "nodeNames has been set but lastDuration no set", nil
	}

	return validateTargetResourceNodeNamesOverlapped(targetResource, allTargetResources)
}

// validateLabelSelectorOverlapped make sures that nodeNames config cannot overlap with other labelSelector config
func validateTargetResourceNodeNamesOverlapped(targetResource util.KCCTargetResource, otherResources []util.KCCTargetResource) (bool, string, error) {
	nodeNames := sets.NewString(targetResource.GetNodeNames()...)

	overlapResources := sets.String{}
	for _, res := range otherResources {
		if (res.GetNamespace() == targetResource.GetNamespace() && res.GetName() == targetResource.GetName()) ||
			len(res.GetNodeNames()) == 0 {
			continue
		}

		otherNodeNames := sets.NewString(res.GetNodeNames()...)
		if nodeNames.Intersection(otherNodeNames).Len() > 0 {
			overlapResources.Insert(native.GenerateUniqObjectNameKey(res))
		}
	}

	if len(overlapResources) > 0 {
		return false, fmt.Sprintf("nodeNames overlay with others: %v", overlapResources.List()), nil
	}

	return true, "", nil
}

func (k *KatalystCustomConfigTargetController) validateTargetResourceGlobal(
	kcc *configapis.KatalystCustomConfig,
	targetResource util.KCCTargetResource,
	allTargetResources []util.KCCTargetResource,
) (bool, string, error) {
	if targetResource.GetLastDuration() != nil {
		return false, "lastDuration has been set for global config", nil
	}

	return validateTargetResourceGlobalOverlapped(targetResource, allTargetResources)
}

// validateLabelSelectorOverlapped make sures that only one global configurations is created.
func validateTargetResourceGlobalOverlapped(targetResource util.KCCTargetResource, otherResources []util.KCCTargetResource) (bool, string, error) {
	overlapTargetNames := sets.String{}
	for _, res := range otherResources {
		if (res.GetNamespace() == targetResource.GetNamespace() && res.GetName() == targetResource.GetName()) ||
			(len(res.GetNodeNames()) > 0 || len(res.GetLabelSelector()) > 0) {
			continue
		}

		overlapTargetNames.Insert(native.GenerateUniqObjectNameKey(res))
	}

	if len(overlapTargetNames) > 0 {
		return false, fmt.Sprintf("global config %s overlay with others: %v",
			native.GenerateUniqObjectNameKey(targetResource), overlapTargetNames.List()), nil
	}

	return true, "", nil
}

func updateInvalidTargetResourceStatus(targetResource util.KCCTargetResource, msg, reason string) {
	status := targetResource.GetGenericStatus()
	status.ObservedGeneration = targetResource.GetGeneration()
	kccutil.UpdateKCCTGenericConditions(&status, configapis.ConfigConditionTypeValid, v1.ConditionFalse, reason, msg)

	targetResource.SetGenericStatus(status)
}

func updateValidTargetResourceStatus(
	targetResource util.KCCTargetResource,
	targetNodes, canaryNodes, updatedTargetNodes, updatedNodes int32,
	currentHash string,
) {
	status := targetResource.GetGenericStatus()
	status.TargetNodes = targetNodes
	status.CanaryNodes = canaryNodes
	status.UpdatedTargetNodes = updatedTargetNodes
	status.UpdatedNodes = updatedNodes
	status.CurrentHash = currentHash
	status.ObservedGeneration = targetResource.GetGeneration()
	kccutil.UpdateKCCTGenericConditions(&status, configapis.ConfigConditionTypeValid, v1.ConditionTrue, kccTargetConditionReasonNormal, "")

	targetResource.SetGenericStatus(status)
}

// checkLabelSelectorOverlap checks whether the labelSelector overlap with other labelSelector by the keyList
func checkLabelSelectorOverlap(selector labels.Selector, otherSelector labels.Selector,
	keyList []string,
) bool {
	for _, key := range keyList {
		equalValueSet, inEqualValueSet, _ := getMatchValueSet(selector, key)
		otherEqualValueSet, otherInEqualValueSet, _ := getMatchValueSet(otherSelector, key)
		if (equalValueSet.Len() > 0 && otherEqualValueSet.Len() > 0 && equalValueSet.Intersection(otherEqualValueSet).Len() > 0) ||
			(equalValueSet.Len() == 0 && otherEqualValueSet.Len() == 0) ||
			(inEqualValueSet.Len() > 0 && !inEqualValueSet.Intersection(otherEqualValueSet).Equal(otherEqualValueSet)) ||
			(otherInEqualValueSet.Len() > 0 && !otherInEqualValueSet.Intersection(equalValueSet).Equal(equalValueSet)) ||
			(equalValueSet.Len() > 0 && otherEqualValueSet.Len() == 0 && otherInEqualValueSet.Len() == 0) ||
			(otherEqualValueSet.Len() > 0 && equalValueSet.Len() == 0 && inEqualValueSet.Len() == 0) {
			continue
		} else {
			return false
		}
	}

	return true
}

func getMatchValueSet(selector labels.Selector, key string) (sets.String, sets.String, error) {
	reqs, selectable := selector.Requirements()
	if !selectable {
		return nil, nil, fmt.Errorf("labelSelector cannot selectable")
	}

	equalValueSet := sets.String{}
	inEqualValueSet := sets.String{}
	for _, r := range reqs {
		if r.Key() != key {
			continue
		}
		switch r.Operator() {
		case selection.Equals, selection.DoubleEquals, selection.In:
			equalValueSet = equalValueSet.Union(r.Values())
		case selection.NotEquals, selection.NotIn:
			inEqualValueSet = inEqualValueSet.Union(r.Values())
		default:
			return nil, nil, fmt.Errorf("labelSelector operator %s not supported", r.Operator())
		}
	}
	return equalValueSet, inEqualValueSet, nil
}

func (k *KatalystCustomConfigTargetController) clearUnusedConfig() {
	general.InfofV(4, "clearUnusedConfig start")
	defer general.InfofV(4, "clearUnusedConfig end")

	cncList, err := k.customNodeConfigLister.List(labels.Everything())
	if err != nil {
		general.Errorf("list all custom node config failed: %v", err)
		return
	}

	// save all gvr to map
	configGVRSet := make(map[metav1.GroupVersionResource]struct{})
	k.targetHandler.RangeGVRTargetAccessor(func(gvr metav1.GroupVersionResource, _ kcctarget.KatalystCustomConfigTargetAccessor) bool {
		configGVRSet[gvr] = struct{}{}
		return true
	})

	needToDeleteFunc := func(config configapis.TargetConfig) bool {
		if _, ok := configGVRSet[config.ConfigType]; !ok {
			return true
		}
		return false
	}

	clearCNCConfigs := func(i int) {
		oldCNC := cncList[i]
		newCNC := oldCNC.DeepCopy()
		newCNC.Status.KatalystCustomConfigList = util.RemoveUnusedTargetConfig(newCNC.Status.KatalystCustomConfigList, needToDeleteFunc)

		if apiequality.Semantic.DeepEqual(oldCNC, newCNC) {
			return
		}
		general.Infof("clearUnusedConfig patch cnc %s", oldCNC.GetName())
		_, err := k.cncControl.PatchCNCStatus(k.ctx, oldCNC.GetName(), oldCNC, newCNC)
		if err != nil {
			general.Errorf("clearUnusedConfig patch cnc %s failed: %v", oldCNC.GetName(), err)
		}
	}

	// parallelize to clear cnc configs
	workqueue.ParallelizeUntil(k.ctx, cncWorkerCount, len(cncList), clearCNCConfigs)
}
