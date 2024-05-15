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

	core "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const vpaRecControllerName = "vpaRec"

const metricNameVPARecControlVPASyncCosts = "vpa_rec_vpa_sync_costs"

// VerticalPodAutoScaleRecommendationController is responsible to sync
// the recommendation results in vpa-rec CR to vpa CR.
//
// although we use informer index mechanism to speed up the looking
// efficiency, we can't assume that all function callers MUST use an
// indexed informer to look up objects.
type VerticalPodAutoScaleRecommendationController struct {
	ctx context.Context

	vpaUpdater    control.VPAUpdater
	vpaRecUpdater control.VPARecommendationUpdater

	vpaRecIndexer cache.Indexer

	vpaLister    autoscalelister.KatalystVerticalPodAutoscalerLister
	vpaRecLister autoscalelister.VerticalPodAutoscalerRecommendationLister

	syncedFunc []cache.InformerSynced

	vpaQueue    workqueue.RateLimitingInterface
	vpaRecQueue workqueue.RateLimitingInterface

	metricsEmitter metrics.MetricEmitter

	vpaSyncWorkers    int
	vparecSyncWorkers int
}

func NewVPARecommendationController(ctx context.Context,
	controlCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	conf *controller.VPAConfig,
) (*VerticalPodAutoScaleRecommendationController, error) {
	if controlCtx == nil {
		return nil, fmt.Errorf("controlCtx is invalid")
	}

	vpaInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()
	vpaRecInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().VerticalPodAutoscalerRecommendations()

	genericClient := controlCtx.Client
	vpaRecController := &VerticalPodAutoScaleRecommendationController{
		ctx: ctx,

		vpaUpdater:    &control.DummyVPAUpdater{},
		vpaRecUpdater: &control.DummyVPARecommendationUpdater{},

		vpaRecIndexer: vpaRecInformer.Informer().GetIndexer(),

		vpaLister:    vpaInformer.Lister(),
		vpaRecLister: vpaRecInformer.Lister(),

		vpaQueue:    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "vpa"),
		vpaRecQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "vpaRec"),

		syncedFunc: []cache.InformerSynced{
			vpaInformer.Informer().HasSynced,
			vpaRecInformer.Informer().HasSynced,
		},
		vpaSyncWorkers:    conf.VPASyncWorkers,
		vparecSyncWorkers: conf.VPARecSyncWorkers,
	}

	vpaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    vpaRecController.addVPA,
		UpdateFunc: vpaRecController.updateVPA,
	})

	vpaRecInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    vpaRecController.addVPARec,
		UpdateFunc: vpaRecController.updateVPARec,
	})

	// build index: vpa ---> vpaRec
	if _, ok := vpaRecController.vpaRecIndexer.GetIndexers()[consts.OwnerReferenceIndex]; !ok {
		err := vpaRecController.vpaRecIndexer.AddIndexers(cache.Indexers{
			consts.OwnerReferenceIndex: native.ObjectOwnerReferenceIndex,
		})
		if err != nil {
			klog.Errorf("failed to add owner vpa index")
			return nil, err
		}
	}

	if !genericConf.DryRun {
		vpaRecController.vpaUpdater = control.NewRealVPAUpdater(genericClient.InternalClient)
		vpaRecController.vpaRecUpdater = control.NewRealVPARecommendationUpdater(genericClient.InternalClient)
	}

	vpaRecController.metricsEmitter = controlCtx.EmitterPool.GetDefaultMetricsEmitter()
	if vpaRecController.metricsEmitter == nil {
		vpaRecController.metricsEmitter = metrics.DummyMetrics{}
	}

	return vpaRecController, nil
}

func (rec *VerticalPodAutoScaleRecommendationController) Run() {
	defer utilruntime.HandleCrash()
	defer rec.vpaRecQueue.ShutDown()

	defer klog.Infof("[vpa-rec] shutting down %s controller", vpaRecControllerName)

	if !cache.WaitForCacheSync(rec.ctx.Done(), rec.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", vpaRecControllerName))
		return
	}
	klog.Infof("[vpa-rec] caches are synced for %s controller", vpaRecControllerName)

	klog.Infof("[vpa-rec] start %v vpaSyncWorkers and %v vparecSyncWorkers", rec.vpaSyncWorkers, rec.vparecSyncWorkers)

	for i := 0; i < rec.vparecSyncWorkers; i++ {
		go wait.Until(rec.vpaRecWorker, time.Second, rec.ctx.Done())
	}

	for i := 0; i < rec.vpaSyncWorkers; i++ {
		go wait.Until(rec.vpaWorker, time.Second, rec.ctx.Done())
	}

	<-rec.ctx.Done()
}

func (rec *VerticalPodAutoScaleRecommendationController) addVPA(obj interface{}) {
	v, ok := obj.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("[vpa-rec] cannot convert obj to *apis.KatalystVerticalPodAutoscaler: %v", obj)
		return
	}

	klog.V(4).Infof("[vpa-rec] notice addition of KatalystVerticalPodAutoscaler %s", v.Name)
	rec.enqueueVPA(v)
}

func (rec *VerticalPodAutoScaleRecommendationController) updateVPA(old, cur interface{}) {
	oldVPA, ok := old.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("[vpa-rec] cannot convert oldObj to *apis.KatalystVerticalPodAutoscaler: %v", old)
		return
	}

	curVPA, ok := cur.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("[vpa-rec] cannot convert curObj to *apis.KatalystVerticalPodAutoscaler: %v", cur)
		return
	}

	klog.V(4).Infof("[vpa-rec] notice update of KatalystVerticalPodAutoscaler %s", curVPA.Name)
	rec.enqueueVPA(curVPA)

	if !apiequality.Semantic.DeepEqual(oldVPA.Spec, curVPA.Spec) ||
		!apiequality.Semantic.DeepEqual(oldVPA.Status, curVPA.Status) {
		vpaRec, err := katalystutil.GetVPARecForVPA(curVPA, rec.vpaRecIndexer, rec.vpaRecLister)
		if err != nil {
			return
		}
		// if vpa spec changed, we also trigger vpa rec to update vpa status
		rec.enqueueVPARec(vpaRec)
	}
}

func (rec *VerticalPodAutoScaleRecommendationController) enqueueVPA(vpa *apis.KatalystVerticalPodAutoscaler) {
	if vpa == nil {
		klog.Warning("[spd] trying to enqueue a nil vpa")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(vpa)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	rec.vpaQueue.Add(key)
}

func (rec *VerticalPodAutoScaleRecommendationController) vpaWorker() {
	for rec.processNextVpa() {
	}
}

func (rec *VerticalPodAutoScaleRecommendationController) processNextVpa() bool {
	key, quit := rec.vpaQueue.Get()
	if quit {
		return false
	}
	defer rec.vpaQueue.Done(key)

	err := rec.syncVPA(key.(string))
	if err == nil {
		rec.vpaQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	rec.vpaQueue.AddRateLimited(key)

	return true
}

// syncVPA is mainly responsible to maintain vpaRec name in vpa annotations and
// update vpa status into vpaRec status
func (rec *VerticalPodAutoScaleRecommendationController) syncVPA(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[vpa-rec] failed to split namespace and name from key %s", key)
		return err
	}

	vpa, err := rec.vpaLister.KatalystVerticalPodAutoscalers(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Warningf("[vpa-rec] vpa %s/%s is not found", namespace, name)
			return nil
		}
		klog.Errorf("[vpa-rec] vpa %s/%s get error: %v", namespace, name, err)
		return err
	}

	vpaRec, err := katalystutil.GetVPARecForVPA(vpa, rec.vpaRecIndexer, rec.vpaRecLister)
	if err != nil {
		klog.Errorf("[vpa-rec] vpa %s no longer matches with vpaRec %s: %v, clear", name, vpa.Name, err)
		return rec.clearVPAAnnotations(vpa)
	}

	recPodResources, recContainerResources, err := util.GetVPARecResourceStatus(vpa.Status.PodResources, vpa.Status.ContainerResources)
	if err != nil {
		klog.Errorf("[vpa-rec] generate vpaRec resource for vpa %s error: %v", vpa.Name, err)
		return err
	}

	if err := rec.updateVPARecStatus(vpaRec, recPodResources, recContainerResources); err != nil {
		klog.Errorf("[vpa-rec] update vpaRec resource for vpa %s error: %v", vpa.Name, err)
		return err
	}

	return rec.setVPAAnnotations(vpa, vpaRec.Name)
}

func (rec *VerticalPodAutoScaleRecommendationController) addVPARec(obj interface{}) {
	v, ok := obj.(*apis.VerticalPodAutoscalerRecommendation)
	if !ok {
		klog.Errorf("[vpa-rec] cannot convert obj to *apis.VerticalPodAutoscalerRecommendation: %v", obj)
		return
	}
	klog.V(4).Infof("[vpa-rec] notice addition of VerticalPodAutoscalerRecommendation %s", v.Name)
	rec.enqueueVPARec(v)
}

func (rec *VerticalPodAutoScaleRecommendationController) updateVPARec(old, cur interface{}) {
	oldVPA, ok := old.(*apis.VerticalPodAutoscalerRecommendation)
	if !ok {
		klog.Errorf("[vpa-rec] cannot convert oldObj to *apis.VerticalPodAutoscalerRecommendation: %v", old)
		return
	}
	curVPA, ok := cur.(*apis.VerticalPodAutoscalerRecommendation)
	if !ok {
		klog.Errorf("[vpa-rec] cannot convert curObj to *apis.VerticalPodAutoscalerRecommendation: %v", cur)
		return
	}

	// only handle vpaRec whose spec was updated
	if !apiequality.Semantic.DeepEqual(oldVPA.Spec, curVPA.Spec) {
		klog.V(4).Infof("[vpa-rec] notice update of VerticalPodAutoscalerRecommendation %s", curVPA.Name)
		rec.enqueueVPARec(curVPA)
	}
}

func (rec *VerticalPodAutoScaleRecommendationController) enqueueVPARec(vpaRec *apis.VerticalPodAutoscalerRecommendation) {
	if vpaRec == nil {
		klog.Warning("[spd] trying to enqueue a nil vpaRec")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(vpaRec)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	rec.vpaRecQueue.Add(key)
}

func (rec *VerticalPodAutoScaleRecommendationController) vpaRecWorker() {
	for rec.processNextVpaRec() {
	}
}

func (rec *VerticalPodAutoScaleRecommendationController) processNextVpaRec() bool {
	key, quit := rec.vpaRecQueue.Get()
	if quit {
		return false
	}
	defer rec.vpaRecQueue.Done(key)

	err := rec.syncVPARec(key.(string))
	if err == nil {
		rec.vpaRecQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	rec.vpaRecQueue.AddRateLimited(key)

	return true
}

// syncVPARec is mainly responsible to sync recommended spec in vpaRec to vpa status,
// as well as to vpa status (if vpa status is successfully updated)
func (rec *VerticalPodAutoScaleRecommendationController) syncVPARec(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[vpa-rec] failed to split namespace and name from key %s", key)
		return err
	}

	begin := time.Now()
	defer func() {
		costs := time.Since(begin).Microseconds()
		klog.Infof("[vpa-rec] syncing vpaRec [%v/%v] costs %v us", namespace, name, costs)
		_ = rec.metricsEmitter.StoreInt64(metricNameVPARecControlVPASyncCosts, costs, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "vpa_namespace", Val: namespace},
			metrics.MetricTag{Key: "vpa_name", Val: name},
		)
	}()

	vpaRec, err := rec.vpaRecLister.VerticalPodAutoscalerRecommendations(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Warningf("[vpa-rec] vpaRec %s/%s is not found", namespace, name)
			return nil
		}

		klog.Errorf("[vpa-rec] vpaRec %s/%s get error: %v", namespace, name, err)
		return err
	}

	vpa, err := katalystutil.GetVPAForVPARec(vpaRec, rec.vpaLister)
	if err != nil {
		klog.Errorf("[vpa-rec] get vpa fpr vpaRec %s/%s error: %v", namespace, name, err)
		return err
	}

	podResources, containerResources, err := util.GetVPAResourceStatusWithRecommendation(vpa, vpaRec.Spec.PodRecommendations, vpaRec.Spec.ContainerRecommendations)
	if err != nil {
		klog.Errorf("[vpa-rec] generate vpa resource for vpa %s error: %v", vpa.Name, err)
		return err
	}

	if err := rec.updateVPAStatus(vpa, podResources, containerResources); err != nil {
		klog.Errorf("[vpa-rec] update vpa resource for vpa %s error: %v", vpa.Name, err)
		return err
	}
	return nil
}

// setVPAAnnotations add vpaRec name in vpa annotations
func (rec *VerticalPodAutoScaleRecommendationController) setVPAAnnotations(vpa *apis.KatalystVerticalPodAutoscaler, vpaRecName string) error {
	if vpa.Annotations[apiconsts.VPAAnnotationVPARecNameKey] == vpaRecName {
		return nil
	}

	vpaCopy := vpa.DeepCopy()
	if vpaCopy.Annotations == nil {
		vpaCopy.Annotations = make(map[string]string)
	}
	vpaCopy.Annotations[apiconsts.VPAAnnotationVPARecNameKey] = vpaRecName

	_, err := rec.vpaUpdater.PatchVPA(rec.ctx, vpa, vpaCopy)
	if err != nil {
		return err
	}

	return err
}

// clearVPAAnnotations removes vpaRec name in pod annotations
func (rec *VerticalPodAutoScaleRecommendationController) clearVPAAnnotations(vpa *apis.KatalystVerticalPodAutoscaler) error {
	if _, ok := vpa.Annotations[apiconsts.VPAAnnotationVPARecNameKey]; !ok {
		return nil
	}

	vpaCopy := vpa.DeepCopy()
	delete(vpaCopy.Annotations, apiconsts.VPAAnnotationVPARecNameKey)

	_, err := rec.vpaUpdater.PatchVPA(rec.ctx, vpa, vpaCopy)
	if err != nil {
		return err
	}

	return err
}

// updateVPAStatus is used to set status for vpa
func (rec *VerticalPodAutoScaleRecommendationController) updateVPAStatus(vpa *apis.KatalystVerticalPodAutoscaler,
	vpaPodResources []apis.PodResources, vpaContainerResources []apis.ContainerResources,
) error {
	vpaNew := vpa.DeepCopy()
	vpaNew.Status.PodResources = vpaPodResources
	vpaNew.Status.ContainerResources = vpaContainerResources
	if apiequality.Semantic.DeepEqual(vpaNew.Status, vpa.Status) {
		return nil
	}

	_, err := rec.vpaUpdater.PatchVPAStatus(rec.ctx, vpa, vpaNew)
	if err != nil {
		klog.Errorf("[vpa-rec] failed to set vpa %v status: %v", vpa.Name, err)
		return err
	}
	return nil
}

// updateVPAStatus is used to set status for vpaRec
func (rec *VerticalPodAutoScaleRecommendationController) updateVPARecStatus(vpaRec *apis.VerticalPodAutoscalerRecommendation,
	recPodResources []apis.RecommendedPodResources, recContainerResources []apis.RecommendedContainerResources,
) error {
	vpaRecNew := vpaRec.DeepCopy()
	vpaRecNew.Status.PodRecommendations = recPodResources
	vpaRecNew.Status.ContainerRecommendations = recContainerResources
	if apiequality.Semantic.DeepEqual(vpaRecNew.Status, vpaRec.Status) {
		for _, condition := range vpaRecNew.Status.Conditions {
			if condition.Type == apis.RecommendationUpdatedToVPA && condition.Status == core.ConditionTrue {
				return nil
			}
		}

		return util.PatchVPARecConditions(rec.ctx, rec.vpaRecUpdater, vpaRec, apis.RecommendationUpdatedToVPA, core.ConditionTrue, util.VPARecConditionReasonUpdated, "")
	}

	err := rec.vpaRecUpdater.PatchVPARecommendationStatus(rec.ctx, vpaRec, vpaRecNew)
	if err != nil {
		klog.Errorf("[vpa-rec] failed to set vpaRec %v status: %v", vpaRec.Name, err)
		if err := util.PatchVPARecConditions(rec.ctx, rec.vpaRecUpdater, vpaRec, apis.RecommendationUpdatedToVPA, core.ConditionFalse, util.VPARecConditionReasonUpdated, err.Error()); err != nil {
			return err
		}

		return err
	}
	return util.PatchVPARecConditions(rec.ctx, rec.vpaRecUpdater, vpaRec, apis.RecommendationUpdatedToVPA, core.ConditionTrue, util.VPARecConditionReasonUpdated, "")
}
