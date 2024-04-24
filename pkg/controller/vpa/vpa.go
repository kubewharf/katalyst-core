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

package vpa

import (
	"context"
	"fmt"
	"sync"
	"time"

	core "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/json"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const vpaControllerName = "vpa"

const (
	metricNameVAPControlVPASync          = "vpa_vpa_sync"
	metricNameVAPControlVPASyncCosts     = "vpa_vpa_sync_costs"
	metricNameVAPControlGetWorkloadCosts = "vpa_vpa_get_workload_costs"
	metricNameVAPControlVPAPatchCosts    = "vpa_vpa_patch_costs"
	metricNameVAPControlSyncPodCosts     = "vpa_vpa_sync_pod_costs"
	metricNameVAPControlVPAUpdateCosts   = "vpa_vpa_update_costs"

	metricNameVAPControlVPAPodCount = "vpa_pod_count"
)

// VPAController is responsible to update pod resources according to
// recommended results in vpa status.
//
// although we use informer index mechanism to speed up the looking
// efficiency, we can't assume that all function callers MUST use an
// indexed informer to look up objects.
type VPAController struct {
	ctx  context.Context
	conf *controller.VPAConfig

	vpaUpdater      control.VPAUpdater
	podUpdater      control.PodUpdater
	workloadControl control.UnstructuredControl

	vpaIndexer cache.Indexer
	podIndexer cache.Indexer

	// workloadLister stores all the dynamic informers the controller needs,
	// while vpaEnabledWorkload stores all the workload that be enabled with vpa
	podLister          corelisters.PodLister
	vpaLister          autoscalelister.KatalystVerticalPodAutoscalerLister
	vpaRecLister       autoscalelister.VerticalPodAutoscalerRecommendationLister
	workloadLister     map[schema.GroupVersionKind]cache.GenericLister
	vpaEnabledWorkload map[schema.GroupVersionKind]interface{}

	syncedFunc []cache.InformerSynced

	vpaSyncQueue   workqueue.RateLimitingInterface
	vpaSyncWorkers int

	vpaStatusController *vpaStatusController

	metricsEmitter metrics.MetricEmitter
}

func NewVPAController(ctx context.Context, controlCtx *katalyst_base.GenericContext,
	genericConf *generic.GenericConfiguration, _ *controller.GenericControllerConfiguration,
	vpaConf *controller.VPAConfig,
) (*VPAController, error) {
	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()
	vpaInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()
	vpaRecInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().VerticalPodAutoscalerRecommendations()

	genericClient := controlCtx.Client
	vpaController := &VPAController{
		ctx:                ctx,
		conf:               vpaConf,
		vpaIndexer:         vpaInformer.Informer().GetIndexer(),
		podIndexer:         podInformer.Informer().GetIndexer(),
		podLister:          podInformer.Lister(),
		vpaLister:          vpaInformer.Lister(),
		vpaRecLister:       vpaRecInformer.Lister(),
		workloadLister:     make(map[schema.GroupVersionKind]cache.GenericLister),
		vpaEnabledWorkload: make(map[schema.GroupVersionKind]interface{}),
		vpaUpdater:         &control.DummyVPAUpdater{},
		podUpdater:         &control.DummyPodUpdater{},
		workloadControl:    &control.DummyUnstructuredControl{},
		vpaSyncQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "vpa"),
		vpaSyncWorkers:     vpaConf.VPASyncWorkers,
		syncedFunc: []cache.InformerSynced{
			podInformer.Informer().HasSynced,
			vpaInformer.Informer().HasSynced,
			vpaRecInformer.Informer().HasSynced,
		},
	}

	workloadInformers := controlCtx.DynamicResourcesManager.GetDynamicInformers()
	for _, wf := range workloadInformers {
		vpaController.workloadLister[wf.GVK] = wf.Informer.Lister()
		vpaController.syncedFunc = append(vpaController.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	for _, workload := range vpaConf.VPAWorkloadGVResources {
		wf, ok := workloadInformers[workload]
		if !ok {
			klog.Errorf("vpa concerned workload %s not found in dynamic GVR resources", workload)
			continue
		}

		vpaController.vpaEnabledWorkload[wf.GVK] = struct{}{}
		wf.Informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    vpaController.addWorkload,
			UpdateFunc: vpaController.updateWorkload,
		})
	}

	vpaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    vpaController.addVPA,
		UpdateFunc: vpaController.updateVPA,
	})

	// vpa controller need update pod resource when pod was recreated
	// because vpa pod webhook may not always work
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: vpaController.addPod,
	})

	// build indexer: workload --> vpa
	if _, ok := vpaInformer.Informer().GetIndexer().GetIndexers()[consts.TargetReferenceIndex]; !ok {
		err := vpaInformer.Informer().GetIndexer().AddIndexers(cache.Indexers{
			consts.TargetReferenceIndex: katalystutil.VPATargetReferenceIndex,
		})
		if err != nil {
			klog.Errorf("failed to add vpa target reference index: %v", err)
			return nil, err
		}
	}

	// build index: workload ---> pod
	for _, key := range vpaConf.VPAPodLabelIndexerKeys {
		indexer := native.PodLabelIndexer(key)
		if _, ok := vpaController.podIndexer.GetIndexers()[key]; !ok {
			err := vpaController.podIndexer.AddIndexers(cache.Indexers{
				key: indexer.IndexFunc,
			})
			if err != nil {
				klog.Errorf("[vpa] failed to add label index for pod: %v", err)
				return nil, err
			}
		}
	}

	vpaController.metricsEmitter = controlCtx.EmitterPool.GetDefaultMetricsEmitter()
	if vpaController.metricsEmitter == nil {
		vpaController.metricsEmitter = metrics.DummyMetrics{}
	}

	if !genericConf.DryRun {
		vpaController.vpaUpdater = control.NewRealVPAUpdater(genericClient.InternalClient)
		vpaController.podUpdater = control.NewRealPodUpdater(genericClient.KubeClient)
		vpaController.workloadControl = control.NewRealUnstructuredControl(genericClient.DynamicClient)
	}

	vpaController.vpaStatusController = newVPAStatusController(
		ctx,
		controlCtx,
		vpaConf,
		vpaController.workloadLister,
		vpaController.vpaUpdater,
	)

	return vpaController, nil
}

func (vc *VPAController) Run() {
	defer utilruntime.HandleCrash()
	defer vc.vpaSyncQueue.ShutDown()

	defer klog.Infof("[vpa] shutting down %s controller", vpaControllerName)

	if !cache.WaitForCacheSync(vc.ctx.Done(), vc.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", vpaControllerName))
		return
	}

	klog.Infof("[vpa] caches are synced for %s controller", vpaControllerName)
	klog.Infof("[vpa] start %d workers for %s controller", vc.vpaSyncWorkers, vpaControllerName)

	for i := 0; i < vc.vpaSyncWorkers; i++ {
		go wait.Until(vc.vpaWorker, time.Second, vc.ctx.Done())
	}
	go wait.Until(vc.maintainVPAName, time.Second*10, vc.ctx.Done())

	// run update vpa status manager.
	go vc.vpaStatusController.run()

	<-vc.ctx.Done()
}

func (vc *VPAController) addWorkload(obj interface{}) {
	workload, ok := obj.(*unstructured.Unstructured)
	if !ok {
		klog.Errorf("[vpa] cannot convert obj to *unstructured.Unstructured)")
		return
	}

	if !katalystutil.CheckWorkloadEnableVPA(workload) {
		return
	}

	vpa, err := katalystutil.GetVPAForWorkload(workload, vc.vpaIndexer, vc.vpaLister)
	if err != nil {
		klog.Errorf("[vpa] get vpa for workload %v err: %v", workload.GetName(), err)
		return
	}
	vc.enqueueVPA(vpa)
}

func (vc *VPAController) updateWorkload(_, cur interface{}) {
	vc.addWorkload(cur)
}

func (vc *VPAController) addVPA(obj interface{}) {
	v, ok := obj.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("cannot convert obj to *apis.VerticalPodAutoscaler: %v", obj)
		return
	}

	klog.V(4).Infof("notice addition of VerticalPodAutoscaler %s", v.Name)
	vc.enqueueVPA(v)
}

func (vc *VPAController) updateVPA(_, cur interface{}) {
	v, ok := cur.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("cannot convert curObj to *apis.VerticalPodAutoscaler: %v", cur)
		return
	}

	klog.V(4).Infof("notice update of VerticalPodAutoscaler %s", v.Name)
	vc.enqueueVPA(v)
}

func (vc *VPAController) addPod(obj interface{}) {
	pod, ok := obj.(*core.Pod)
	if !ok {
		klog.Errorf("cannot convert obj to *core.Pod: %v", obj)
		return
	}

	vpa, err := katalystutil.GetVPAForPod(pod, vc.vpaIndexer, vc.workloadLister, vc.vpaLister)
	if err != nil {
		klog.V(6).Infof("didn't to find vpa of pod %v/%v, err: %v", pod.Namespace, pod.Name, err)
		return
	}

	klog.V(6).Infof("notice addition of pod %s", pod.Name)
	vc.enqueueVPA(vpa)
}

func (vc *VPAController) enqueueVPA(vpa *apis.KatalystVerticalPodAutoscaler) {
	if vpa == nil {
		klog.Warning("trying to enqueueVPA a nil VPA")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(vpa)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", vpa, err))
		return
	}

	vc.vpaSyncQueue.Add(key)
}

func (vc *VPAController) vpaWorker() {
	for vc.processNextVPA() {
	}
}

func (vc *VPAController) processNextVPA() bool {
	key, quit := vc.vpaSyncQueue.Get()
	if quit {
		return false
	}
	defer vc.vpaSyncQueue.Done(key)

	err := vc.syncVPA(key.(string))
	if err == nil {
		vc.vpaSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	vc.vpaSyncQueue.AddRateLimited(key)

	return true
}

func (vc *VPAController) syncVPA(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[vpa] failed to split namespace and name from key %s", key)
		return err
	}

	timeSets := make(map[string]time.Time)
	tags := []metrics.MetricTag{
		{Key: "vpa_namespace", Val: namespace},
		{Key: "vpa_name", Val: name},
	}
	_ = vc.metricsEmitter.StoreInt64(metricNameVAPControlVPASync, 1, metrics.MetricTypeNameCount)

	timeSets[metricNameVAPControlVPASyncCosts] = time.Now()
	defer func() {
		vc.syncPerformance(namespace, name, timeSets, tags)
	}()

	vpa, err := vc.vpaLister.KatalystVerticalPodAutoscalers(namespace).Get(name)
	if err != nil {
		klog.Errorf("[vpa] vpa %s/%s get error: %v", namespace, name, err)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	klog.V(4).Infof("[vpa] syncing vpa %s", vpa.Name)

	timeSets[metricNameVAPControlGetWorkloadCosts] = time.Now()
	gvk := schema.FromAPIVersionAndKind(vpa.Spec.TargetRef.APIVersion, vpa.Spec.TargetRef.Kind)
	workloadLister, ok := vc.workloadLister[gvk]
	if !ok {
		klog.Errorf("[vpa] vpa %s/%s without workload lister %v", namespace, name, gvk)
		return nil
	}

	workloadObj, err := katalystutil.GetWorkloadForVPA(vpa, workloadLister)
	if err != nil {
		klog.Errorf("[vpa] vpa %s/%s get workload error: %v", namespace, name, err)
		return err
	}

	workload := workloadObj.(*unstructured.Unstructured)
	if err := vc.setVPAAnnotations(workload, gvk, vpa.Name); err != nil {
		klog.Errorf("[vpa] set workload %s annotation %v error: %v", workload.GetName(), vpa.Name, err)
		return err
	}

	timeSets[metricNameVAPControlVPAPatchCosts] = time.Now()
	vpaNew := vpa.DeepCopy()
	if vpa.Annotations[apiconsts.VPAAnnotationWorkloadRetentionPolicyKey] == apiconsts.VPAAnnotationWorkloadRetentionPolicyRetain {
		if err := util.SetOwnerReferencesForVPA(vpaNew, workloadObj); err != nil {
			klog.Errorf("[vpa] vpa %s/%s get workload error: %v", namespace, name, err)
			return err
		}
	} else {
		if err := util.DeleteOwnerReferencesForVPA(vpaNew, workloadObj); err != nil {
			klog.Errorf("[vpa] vpa %s/%s get workload error: %v", namespace, name, err)
			return err
		}
	}
	if _, err := vc.vpaUpdater.PatchVPA(context.TODO(), vpa, vpaNew); err != nil {
		return err
	}

	timeSets[metricNameVAPControlSyncPodCosts] = time.Now()
	pods, err := katalystutil.GetPodListForVPA(vpa, vc.podIndexer, vc.conf.VPAPodLabelIndexerKeys, workloadLister, vc.podLister)
	if err != nil {
		klog.Errorf("[vpa] failed to get pods by vpa %s, err %v", vpa.Name, err)
		_ = util.UpdateVPAConditions(vc.ctx, vc.vpaUpdater, vpa, apis.RecommendationUpdated, core.ConditionFalse, util.VPAConditionReasonCalculatedIllegal, "failed to find pods")
		return err
	}
	klog.V(4).Infof("[vpa] syncing vpa %s with %d pods", name, len(pods))
	_ = vc.metricsEmitter.StoreInt64(metricNameVAPControlVPAPodCount, int64(len(pods)), metrics.MetricTypeNameRaw, tags...)

	pods, err = vc.filterPodsByUpdatePolicy(vpa, pods)
	if err != nil {
		klog.Errorf("[vpa] failed to filter pods by vpa %s update policy", vpa.Name)
		_ = util.UpdateVPAConditions(vc.ctx, vc.vpaUpdater, vpa, apis.RecommendationUpdated, core.ConditionFalse, util.VPAConditionReasonCalculatedIllegal, "failed to filter pod")
		return nil
	}
	klog.V(4).Infof("[vpa] syncing vpa %s with filtered %d pods", name, len(pods))

	timeSets[metricNameVAPControlVPAUpdateCosts] = time.Now()
	if err := vc.updatePodResources(vpa, pods); err != nil {
		return err
	}

	return nil
}

func (vc *VPAController) syncPerformance(namespace, name string, times map[string]time.Time, tags []metrics.MetricTag) {
	now := time.Now()
	timeSets := []string{
		metricNameVAPControlVPASyncCosts,
		metricNameVAPControlGetWorkloadCosts,
		metricNameVAPControlVPAPatchCosts,
		metricNameVAPControlSyncPodCosts,
		metricNameVAPControlVPAUpdateCosts,
	}
	for _, timeSet := range timeSets {
		if begin, ok := times[timeSet]; ok {
			costs := now.Sub(begin).Microseconds()
			klog.V(3).Infof("[vpa] [%v/%v] %v costs %v us", namespace, name, timeSet, costs)
			_ = vc.metricsEmitter.StoreInt64(timeSet, costs, metrics.MetricTypeNameRaw, tags...)
		}
	}
}

// maintainVPAName is mainly responsible to main vpa annotation in workload
func (vc *VPAController) maintainVPAName() {
	for gvk, workloadLister := range vc.workloadLister {
		if _, ok := vc.vpaEnabledWorkload[gvk]; !ok {
			continue
		}

		workloadList, err := workloadLister.List(labels.Everything())
		if err != nil {
			klog.Errorf("[vpa] list workloads failed: %v", err)
			continue
		}

		for _, workloadObj := range workloadList {
			needDelete := false

			workload := workloadObj.(*unstructured.Unstructured)
			vpa, err := katalystutil.GetVPAForWorkload(workload, vc.vpaIndexer, vc.vpaLister)
			if err != nil {
				if errors.IsNotFound(err) {
					needDelete = true
				} else {
					klog.Errorf("[vpa] get vpa for workload %s error: %v", workload.GetName(), err)
				}
			} else if err := vc.setVPAAnnotations(workload, gvk, vpa.Name); err != nil {
				klog.Errorf("[vpa] set vpa name for workload %s error: %v", workload.GetName(), err)
			}

			if needDelete {
				klog.V(5).Infof("[vpa] delete un-wanted annotation for workload %v", workload.GetName())
				if err := vc.cleanVPAAnnotations(workload, gvk); err != nil {
					klog.Errorf("[vpa] clear vpa name for workload %s error: %v", workload.GetName(), err)
				}
			}
		}
	}
}

// filterPodsByUpdatePolicy filter out pods which didn't obey vpa update policy
func (vc *VPAController) filterPodsByUpdatePolicy(vpa *apis.KatalystVerticalPodAutoscaler, pods []*core.Pod) ([]*core.Pod, error) {
	if vpa.Spec.UpdatePolicy.PodUpdatingStrategy == apis.PodUpdatingStrategyRecreate {
		return nil, fmt.Errorf("PodUpdatingStrategy mustn't be PodUpdatingStrategyRecreate")
	}

	remainPods := make([]*core.Pod, 0)
	switch vpa.Spec.UpdatePolicy.PodMatchingStrategy {
	case apis.PodMatchingStrategyAll:
		remainPods = pods
	case apis.PodMatchingStrategyForFreshPod:
		for _, pod := range pods {
			if pod == nil {
				return nil, fmt.Errorf("pod can't be nil")
			}
			if vpa.CreationTimestamp.Before(&pod.CreationTimestamp) {
				remainPods = append(remainPods, pod)
			}
		}
	case apis.PodMatchingStrategyForHistoricalPod:
		for _, pod := range pods {
			if pod == nil {
				return nil, fmt.Errorf("pod can't be nil")
			}
			if pod.CreationTimestamp.Before(&vpa.CreationTimestamp) {
				remainPods = append(remainPods, pod)
			}
		}
	}
	return remainPods, nil
}

// setVPAAnnotations add vpa name in workload annotations
func (vc *VPAController) setVPAAnnotations(workload *unstructured.Unstructured, gvk schema.GroupVersionKind, vpaName string) error {
	if workload.GetAnnotations()[apiconsts.WorkloadAnnotationVPANameKey] == vpaName {
		return nil
	}

	workloadCopy := workload.DeepCopy()
	annotations := workloadCopy.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[apiconsts.WorkloadAnnotationVPANameKey] = vpaName
	workloadCopy.SetAnnotations(annotations)

	gvr, _ := meta.UnsafeGuessKindToResource(gvk)
	workloadGVR := metav1.GroupVersionResource{Version: gvr.Version, Group: gvr.Group, Resource: gvr.Resource}
	_, err := vc.workloadControl.PatchUnstructured(vc.ctx, workloadGVR, workload, workloadCopy)
	if err != nil {
		return err
	}

	klog.Infof("[vpa] successfully clear annotations for workload %v to %v", workload.GetName(), vpaName)
	return nil
}

// cleanVPAAnnotations removes vpa name in workload annotations
func (vc *VPAController) cleanVPAAnnotations(workload *unstructured.Unstructured, gvk schema.GroupVersionKind) error {
	if _, ok := workload.GetAnnotations()[apiconsts.WorkloadAnnotationVPANameKey]; !ok {
		return nil
	}

	workloadCopy := workload.DeepCopy()
	annotations := workloadCopy.GetAnnotations()
	delete(annotations, apiconsts.WorkloadAnnotationVPANameKey)
	workloadCopy.SetAnnotations(annotations)

	gvr, _ := meta.UnsafeGuessKindToResource(gvk)
	workloadGVR := metav1.GroupVersionResource{Version: gvr.Version, Group: gvr.Group, Resource: gvr.Resource}
	_, err := vc.workloadControl.PatchUnstructured(vc.ctx, workloadGVR, workload, workloadCopy)
	if err != nil {
		return err
	}

	klog.Infof("[vpa] successfully clear annotations for workload %v", workload.GetName())
	return nil
}

// updatePodResources updates resource recommendation for each individual pod
func (vc *VPAController) updatePodResources(vpa *apis.KatalystVerticalPodAutoscaler, pods []*core.Pod) error {
	podResources, containerResources, err := katalystutil.GenerateVPAResourceMap(vpa)
	if err != nil {
		return fmt.Errorf("[vpa] failed to get resource from VPA %s", vpa.Name)
	}

	containerPolicies, err := katalystutil.GenerateVPAPolicyMap(vpa)
	if err != nil {
		return fmt.Errorf("[vpa] get container policy for vpa %s error: %v", vpa.Name, err)
	}

	// If the PodApplyStrategy is set to 'Pod', the update policy controller will only apply changes to pod-level resources.
	// This approach ensures that when pod-level resources are not specified, they are not unintentionally overridden by the
	// container-level resource definitions.Therefore, if the strategy is 'Pod', we clear the containerResources to prevent
	// any container-specific resource updates.
	if vpa.Spec.UpdatePolicy.PodApplyStrategy == apis.PodApplyStrategyStrategyPod {
		containerResources = nil
	}

	var mtx sync.Mutex
	var errList []error
	updatePodAnnotations := func(i int) {
		pod := pods[i].DeepCopy()
		err := vc.patchPodResources(vpa, pod, podResources, containerResources, containerPolicies)
		if err != nil {
			mtx.Lock()
			errList = append(errList, err)
			mtx.Unlock()
			return
		}
	}
	workqueue.ParallelizeUntil(vc.ctx, 16, len(pods), updatePodAnnotations)
	if len(errList) > 0 {
		_ = util.UpdateVPAConditions(vc.ctx, vc.vpaUpdater, vpa, apis.RecommendationUpdated, core.ConditionFalse,
			util.VPAConditionReasonCalculatedIllegal, "failed to update pod annotations")
		return utilerrors.NewAggregate(errList)
	}

	return util.UpdateVPAConditions(vc.ctx, vc.vpaUpdater, vpa, apis.RecommendationUpdated, core.ConditionTrue, util.VPAConditionReasonUpdated, "")
}

// patchPodResources updates resource recommendation for each individual pod
func (vc *VPAController) patchPodResources(vpa *apis.KatalystVerticalPodAutoscaler, pod *core.Pod,
	podResources map[consts.PodContainerName]apis.ContainerResources, containerResources map[consts.ContainerName]apis.ContainerResources,
	containerPolicies map[string]apis.ContainerResourcePolicy,
) error {
	annotationResource, err := katalystutil.GenerateVPAPodResizeResourceAnnotations(pod, podResources, containerResources)
	if err != nil {
		return fmt.Errorf("failed to exact pod %v resize resource annotation from container resource: %v", pod.Name, err)
	}

	marshalledResourceAnnotation, err := json.Marshal(annotationResource)
	if err != nil {
		return err
	}

	annotationPolicy, err := katalystutil.GenerateVPAPodResizePolicyAnnotations(pod, containerPolicies)
	if err != nil {
		return err
	}

	marshalledPolicyAnnotation, err := json.Marshal(annotationPolicy)
	if err != nil {
		return err
	}

	podCopy := pod.DeepCopy()
	if native.PodResourceDiff(pod, annotationResource) {
		if len(annotationResource) > 0 {
			podCopy.Annotations[apiconsts.PodAnnotationInplaceUpdateResourcesKey] = string(marshalledResourceAnnotation)
		} else {
			delete(podCopy.Annotations, apiconsts.PodAnnotationInplaceUpdateResourcesKey)
		}
		if len(annotationPolicy) > 0 {
			podCopy.Annotations[apiconsts.PodAnnotationInplaceUpdateResizePolicyKey] = string(marshalledPolicyAnnotation)
		} else {
			delete(podCopy.Annotations, apiconsts.PodAnnotationInplaceUpdateResizePolicyKey)
		}
	}

	if !apiequality.Semantic.DeepEqual(pod.Annotations, podCopy.Annotations) {
		podUpdater := vc.podUpdater
		if vpa == nil || vpa.Spec.UpdatePolicy.PodUpdatingStrategy == apis.PodUpdatingStrategyOff {
			podUpdater = &control.DummyPodUpdater{}
			klog.Warning("will not update pod %s/%s due to PodUpdatingStrategy", pod.Namespace, pod.Name)
		}

		if err := podUpdater.PatchPod(vc.ctx, pod, podCopy); err != nil {
			return err
		}
	} else {
		klog.V(5).Infof("pod %s/%s has no need to update resources", pod.Namespace, pod.Name) //nolint:gomnd
	}

	return nil
}
