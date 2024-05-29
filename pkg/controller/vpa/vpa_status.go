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
	"time"

	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	metricNameVAPControlVPAUpdateStatusCosts = "vpa_vpa_update_resource_costs"
)

type vpaStatusController struct {
	ctx  context.Context
	conf *controller.VPAConfig

	vpaIndexer cache.Indexer
	podIndexer cache.Indexer

	podLister corelisters.PodLister
	vpaLister autoscalelister.KatalystVerticalPodAutoscalerLister

	syncedFunc           []cache.InformerSynced
	vpaSyncQueue         workqueue.RateLimitingInterface
	vpaStatusSyncWorkers int

	workloadLister map[schema.GroupVersionKind]cache.GenericLister

	vpaUpdater control.VPAUpdater

	metricsEmitter metrics.MetricEmitter
}

func newVPAStatusController(ctx context.Context, controlCtx *katalyst_base.GenericContext,
	conf *controller.VPAConfig, workloadLister map[schema.GroupVersionKind]cache.GenericLister,
	vpaUpdater control.VPAUpdater,
) *vpaStatusController {
	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()
	vpaInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()

	c := &vpaStatusController{
		ctx:        ctx,
		conf:       conf,
		vpaIndexer: vpaInformer.Informer().GetIndexer(),
		vpaLister:  vpaInformer.Lister(),
		podIndexer: podInformer.Informer().GetIndexer(),
		podLister:  podInformer.Lister(),
		syncedFunc: []cache.InformerSynced{
			podInformer.Informer().HasSynced,
			vpaInformer.Informer().HasSynced,
		},
		vpaSyncQueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "vpa-status"),
		vpaStatusSyncWorkers: conf.VPASyncWorkers,
		vpaUpdater:           vpaUpdater,
		workloadLister:       workloadLister,
		metricsEmitter:       controlCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags("vpa-status"),
	}

	// we need update current container resource to vpa status,
	// so we need watch pod update event (if the in-place updating succeeded)
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addPod,
		UpdateFunc: c.updatePod,
	})

	vpaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addVPA,
		UpdateFunc: c.updateVPA,
	})

	return c
}

func (vs *vpaStatusController) run() {
	defer utilruntime.HandleCrash()
	defer vs.vpaSyncQueue.ShutDown()

	defer klog.Infof("[vpa-status] shutting down vpa status collector")

	if !cache.WaitForCacheSync(vs.ctx.Done(), vs.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for vpa status collector"))
		return
	}

	klog.Infof("[vpa-status] caches are synced for vpa status collector")

	for i := 0; i < vs.vpaStatusSyncWorkers; i++ {
		go wait.Until(vs.vpaWorker, time.Second, vs.ctx.Done())
	}

	<-vs.ctx.Done()
}

func (vs *vpaStatusController) addVPA(obj interface{}) {
	v, ok := obj.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("[vpa-status] cannot convert obj to *apis.VerticalPodAutoscaler: %v", obj)
		return
	}

	klog.V(6).Infof("[vpa-status] notice addition of VerticalPodAutoscaler %s", v.Name)
	vs.enqueueVPA(v)
}

func (vs *vpaStatusController) updateVPA(old, cur interface{}) {
	oldVPA, ok := old.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("[vpa-status] cannot convert oldObj to *apis.VerticalPodAutoscaler: %v", old)
		return
	}

	curVPA, ok := cur.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("[vpa-status] cannot convert curObj to *apis.VerticalPodAutoscaler: %v", cur)
		return
	}

	if apiequality.Semantic.DeepEqual(oldVPA.Status, curVPA.Status) {
		return
	}

	klog.V(6).Infof("[vpa-status] notice update of vpa %s", native.GenerateUniqObjectNameKey(curVPA))
	vs.enqueueVPA(curVPA)
}

func (vs *vpaStatusController) addPod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.Errorf("[vpa-status] cannot convert obj to *core.Pod: %v", obj)
		return
	}

	vpa, err := katalystutil.GetVPAForPod(pod, vs.vpaIndexer, vs.workloadLister, vs.vpaLister)
	if err != nil {
		klog.V(6).Infof("[vpa-status] didn't to find vpa of pod %s, err: %v", native.GenerateUniqObjectNameKey(pod), err)
		return
	}

	klog.V(6).Infof("[vpa-status] notice addition of pod %s", native.GenerateUniqObjectNameKey(pod))
	vs.enqueueVPA(vpa)
}

func (vs *vpaStatusController) updatePod(old interface{}, cur interface{}) {
	oldPod, ok := old.(*v1.Pod)
	if !ok {
		klog.Errorf("[vpa-status] cannot convert obj to *core.Pod: %v", cur)
		return
	}

	curPod, ok := cur.(*v1.Pod)
	if !ok {
		klog.Errorf("[vpa-status] cannot convert obj to *core.Pod: %v", cur)
		return
	}

	// only when pod status or spec.containers has been changed, in-place update resource may be completed,
	// so it only enqueue vpa to collect vpa status when they are different with old one
	if apiequality.Semantic.DeepEqual(oldPod.Status, curPod.Status) &&
		apiequality.Semantic.DeepEqual(oldPod.Spec.Containers, curPod.Spec.Containers) {
		return
	}

	vpa, err := katalystutil.GetVPAForPod(curPod, vs.vpaIndexer, vs.workloadLister, vs.vpaLister)
	if err != nil {
		klog.V(6).Infof("[vpa-status] didn't to find vpa of pod %s, err: %v",
			native.GenerateUniqObjectNameKey(curPod), err)
		return
	}

	klog.V(6).Infof("[vpa-status] notice update of pod %s", native.GenerateUniqObjectNameKey(curPod))
	vs.enqueueVPA(vpa)
}

func (vs *vpaStatusController) enqueueVPA(vpa *apis.KatalystVerticalPodAutoscaler) {
	if vpa == nil {
		klog.Warning("[vpa-status] trying to enqueueVPA a nil VPA")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(vpa)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", vpa, err))
		return
	}

	vs.vpaSyncQueue.Add(key)
}

func (vs *vpaStatusController) vpaWorker() {
	for vs.processNextVPA() {
	}
}

func (vs *vpaStatusController) processNextVPA() bool {
	key, quit := vs.vpaSyncQueue.Get()
	if quit {
		return false
	}
	defer vs.vpaSyncQueue.Done(key)

	err := vs.syncVPA(key.(string))
	if err == nil {
		vs.vpaSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	vs.vpaSyncQueue.AddRateLimited(key)

	return true
}

func (vs *vpaStatusController) syncVPA(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[vpa-status] failed to split namespace and name from key %s", key)
		return err
	}

	begin := time.Now()
	defer func() {
		now := time.Now()
		costs := now.Sub(begin).Microseconds()
		klog.V(3).Infof("[vpa-status] [%v/%v] %v costs %v us", namespace, name, metricNameVAPControlVPAUpdateStatusCosts, costs)
		_ = vs.metricsEmitter.StoreInt64(metricNameVAPControlVPAUpdateStatusCosts, costs, metrics.MetricTypeNameRaw, []metrics.MetricTag{
			{Key: "vpa_namespace", Val: namespace},
			{Key: "vpa_name", Val: name},
		}...)
	}()

	vpa, err := vs.vpaLister.KatalystVerticalPodAutoscalers(namespace).Get(name)
	if err != nil {
		klog.Errorf("[vpa-status] vpa %s/%s get error: %v", namespace, name, err)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	gvk := schema.FromAPIVersionAndKind(vpa.Spec.TargetRef.APIVersion, vpa.Spec.TargetRef.Kind)
	workloadLister, ok := vs.workloadLister[gvk]
	if !ok {
		klog.Errorf("[vpa-status] vpa %s/%s without workload lister %v", namespace, name, gvk)
		return nil
	}

	pods, err := katalystutil.GetPodListForVPA(vpa, vs.podIndexer, vs.conf.VPAPodLabelIndexerKeys, workloadLister, vs.podLister)
	if err != nil {
		klog.Errorf("[vpa-status] failed to get pods by vpa %s, err %v", vpa.Name, err)
		return err
	}

	// get pod resources and container resources according to current pods
	vpaPodResources, vpaContainerResources, err := util.GetVPAResourceStatusWithCurrent(vpa, pods)
	if err != nil {
		klog.Errorf("[vpa-status] get vpa status with current pods err: %v", err)
		return err
	}

	vpaNew := vpa.DeepCopy()
	vpaNew.Status.PodResources = vpaPodResources
	vpaNew.Status.ContainerResources = vpaContainerResources

	// set RecommendationApplied condition, based on whether all pods for this vpa
	// are updated to the expected resources in their annotations
	err = vs.setRecommendationAppliedCondition(vpaNew, pods)
	if err != nil {
		klog.Errorf("[vpa-status] set recommendation applied condition failed: %v", err)
		return err
	}

	// skip to update status if no change happened
	if apiequality.Semantic.DeepEqual(vpa.Status, vpaNew.Status) {
		return nil
	}

	_, err = vs.vpaUpdater.UpdateVPAStatus(vs.ctx, vpaNew, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("[vpa-status] update vpa status err: %v", err)
		return err
	}

	return nil
}

// setRecommendationAppliedCondition set vpa recommendation applied condition by checking all pods whether
// are updated to the expected resources in their annotations
func (vs *vpaStatusController) setRecommendationAppliedCondition(vpa *apis.KatalystVerticalPodAutoscaler, pods []*v1.Pod) error {
	allPodSpecUpdated := func() bool {
		for _, pod := range pods {
			if !katalystutil.CheckPodSpecUpdated(pod) {
				return false
			}
		}
		return true
	}()

	if allPodSpecUpdated {
		err := util.SetVPAConditions(vpa, apis.RecommendationApplied, v1.ConditionTrue, util.VPAConditionReasonPodSpecUpdated, "")
		if err != nil {
			return err
		}
	} else {
		err := util.SetVPAConditions(vpa, apis.RecommendationApplied, v1.ConditionFalse, util.VPAConditionReasonPodSpecNoUpdate, "")
		if err != nil {
			return err
		}
	}
	return nil
}
