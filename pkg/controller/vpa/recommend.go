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

	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreListers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	workloadlister "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/algorithm"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/algorithm/recommenders"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const resourceRecommendControllerName = "resourceRecommend"

const metricNameRecommendControlVPASyncCosts = "res_rec_vpa_sync_costs"

// rs stores all the in-tree recommendation algorithm implementations
var rs = []algorithm.ResourceRecommender{
	recommenders.NewCPURecommender(),
}

func init() {
	for _, r := range rs {
		algorithm.RegisterRecommender(r)
	}
}

// ResourceRecommendController is responsible to use in-tree algorithm implementations
// to export those recommended results to vpa-rec according to vpa config.
//
// although we use informer index mechanism to speed up the looking
// efficiency, we can't assume that all function callers MUST use an
// indexed informer to look up objects.
type ResourceRecommendController struct {
	ctx  context.Context
	conf *controller.VPAConfig

	vpaUpdater    control.VPAUpdater
	vpaRecUpdater control.VPARecommendationUpdater

	spdIndexer    cache.Indexer
	vpaRecIndexer cache.Indexer
	podIndexer    cache.Indexer

	podLister      coreListers.PodLister
	spdLister      workloadlister.ServiceProfileDescriptorLister
	vpaLister      autoscalelister.KatalystVerticalPodAutoscalerLister
	vpaRecLister   autoscalelister.VerticalPodAutoscalerRecommendationLister
	workloadLister map[schema.GroupVersionKind]cache.GenericLister

	syncedFunc []cache.InformerSynced
	vpaQueue   workqueue.RateLimitingInterface

	metricsEmitter metrics.MetricEmitter

	vpaSyncWorkers int
}

func NewResourceRecommendController(ctx context.Context, controlCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration, _ *controller.GenericControllerConfiguration,
	config *controller.VPAConfig,
) (*ResourceRecommendController, error) {
	if controlCtx == nil {
		return nil, fmt.Errorf("controlCtx is invalid")
	}

	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()
	spdInformer := controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors()
	vpaInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()
	vpaRecInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().VerticalPodAutoscalerRecommendations()

	genericClient := controlCtx.Client
	recController := &ResourceRecommendController{
		ctx:            ctx,
		conf:           config,
		vpaUpdater:     &control.DummyVPAUpdater{},
		vpaRecUpdater:  &control.DummyVPARecommendationUpdater{},
		spdIndexer:     spdInformer.Informer().GetIndexer(),
		vpaRecIndexer:  vpaRecInformer.Informer().GetIndexer(),
		podIndexer:     podInformer.Informer().GetIndexer(),
		podLister:      podInformer.Lister(),
		spdLister:      spdInformer.Lister(),
		vpaLister:      vpaInformer.Lister(),
		vpaRecLister:   vpaRecInformer.Lister(),
		workloadLister: make(map[schema.GroupVersionKind]cache.GenericLister),
		vpaQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "vpa"),
		syncedFunc: []cache.InformerSynced{
			podInformer.Informer().HasSynced,
			spdInformer.Informer().HasSynced,
			vpaInformer.Informer().HasSynced,
			vpaRecInformer.Informer().HasSynced,
		},
		vpaSyncWorkers: config.VPASyncWorkers,
	}

	for _, wf := range controlCtx.DynamicResourcesManager.GetDynamicInformers() {
		recController.workloadLister[wf.GVK] = wf.Informer.Lister()
		recController.syncedFunc = append(recController.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	klog.Infof("vpa resync period %v", config.VPAReSyncPeriod)

	vpaInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    recController.addVPA,
		UpdateFunc: recController.updateVPA,
	}, config.VPAReSyncPeriod)

	// build index: workload ---> spd
	if _, ok := spdInformer.Informer().GetIndexer().GetIndexers()[consts.TargetReferenceIndex]; !ok {
		err := spdInformer.Informer().GetIndexer().AddIndexers(cache.Indexers{
			consts.TargetReferenceIndex: katalystutil.SPDTargetReferenceIndex,
		})
		if err != nil {
			klog.Errorf("failed to add spd target reference index: %v", err)
			return nil, err
		}
	}

	// build index: vpa ---> vpaRec
	if _, ok := vpaRecInformer.Informer().GetIndexer().GetIndexers()[consts.OwnerReferenceIndex]; !ok {
		err := vpaRecInformer.Informer().GetIndexer().AddIndexers(cache.Indexers{
			consts.OwnerReferenceIndex: native.ObjectOwnerReferenceIndex,
		})
		if err != nil {
			klog.Errorf("[vpa-rec] failed to add owner vpa index: %v", err)
			return nil, err
		}
	}

	// build index: workload ---> pod
	for _, key := range config.VPAPodLabelIndexerKeys {
		indexer := native.PodLabelIndexer(key)
		if _, ok := recController.podIndexer.GetIndexers()[key]; !ok {
			err := recController.podIndexer.AddIndexers(cache.Indexers{
				key: indexer.IndexFunc,
			})
			if err != nil {
				klog.Errorf("[vpa-rec] failed to add label index for pod: %v", err)
				return nil, err
			}
		}
	}

	recController.metricsEmitter = controlCtx.EmitterPool.GetDefaultMetricsEmitter()
	if recController.metricsEmitter == nil {
		recController.metricsEmitter = metrics.DummyMetrics{}
	}

	if !genericConf.DryRun {
		recController.vpaUpdater = control.NewRealVPAUpdater(genericClient.InternalClient)
		recController.vpaRecUpdater = control.NewRealVPARecommendationUpdater(genericClient.InternalClient)
	}

	return recController, nil
}

func (rrc *ResourceRecommendController) Run() {
	defer utilruntime.HandleCrash()
	defer rrc.vpaQueue.ShutDown()

	defer klog.Infof("[resource-rec] shutting down %s controller", resourceRecommendControllerName)

	if !cache.WaitForCacheSync(rrc.ctx.Done(), rrc.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", resourceRecommendControllerName))
		return
	}
	klog.Infof("[resource-rec] caches are synced for %s controller", resourceRecommendControllerName)
	klog.Infof("[resource-rec] start %d workers for %s controller", rrc.vpaSyncWorkers, resourceRecommendControllerName)

	for i := 0; i < rrc.vpaSyncWorkers; i++ {
		go wait.Until(rrc.vpaWorker, time.Second, rrc.ctx.Done())
	}

	<-rrc.ctx.Done()
}

func (rrc *ResourceRecommendController) addVPA(obj interface{}) {
	v, ok := obj.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("[resource-rec] cannot convert obj to *apis.VerticalPodAutoscaler: %v", obj)
		return
	}

	klog.V(4).Infof("[resource-rec] notice addition of vpa %s", v.Name)
	rrc.enqueueVPA(v)
}

func (rrc *ResourceRecommendController) updateVPA(_, cur interface{}) {
	v, ok := cur.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		klog.Errorf("[resource-rec] cannot convert curObj to *apis.VerticalPodAutoscaler: %v", cur)
		return
	}

	klog.V(4).Infof("[resource-rec] notice update of vpa %s", v.Name)
	rrc.enqueueVPA(v)
}

func (rrc *ResourceRecommendController) enqueueVPA(vpa *apis.KatalystVerticalPodAutoscaler) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(vpa)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", vpa, err))
		return
	}
	rrc.vpaQueue.Add(key)
}

func (rrc *ResourceRecommendController) vpaWorker() {
	for rrc.processNextVPA() {
	}
}

func (rrc *ResourceRecommendController) processNextVPA() bool {
	key, quit := rrc.vpaQueue.Get()
	if quit {
		return false
	}
	defer rrc.vpaQueue.Done(key)

	err := rrc.syncVPA(key.(string))
	if err == nil {
		rrc.vpaQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	rrc.vpaQueue.AddRateLimited(key)

	return true
}

// syncVPA is mainly responsible to calculate resource recommendation for each vpa (with
// recommender setting as in-tree algorithms); since we will re-sync periodicallly, we
// won't return error in this function.
func (rrc *ResourceRecommendController) syncVPA(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[resource-rec] failed to split namespace and name from key %s", key)
		return err
	}

	begin := time.Now()
	defer func() {
		costs := time.Since(begin).Microseconds()
		klog.Infof("[resource-rec] syncing vpa [%v/%v] costs %v us", namespace, name, costs)
		_ = rrc.metricsEmitter.StoreInt64(metricNameRecommendControlVPASyncCosts, costs, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "vpa_namespace", Val: namespace},
			metrics.MetricTag{Key: "vpa_name", Val: name},
		)
	}()

	vpa, err := rrc.vpaLister.KatalystVerticalPodAutoscalers(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Warningf("[resource-rec] vpa %s/%s is not found", namespace, name)
			return nil
		}

		klog.Errorf("[resource-rec] vpa %s/%s get error: %v", namespace, name, err)
		return nil
	}
	klog.V(4).Infof("[resource-rec] syncing vpa %s", vpa.Name)

	gvk := schema.FromAPIVersionAndKind(vpa.Spec.TargetRef.APIVersion, vpa.Spec.TargetRef.Kind)
	workloadLister, ok := rrc.workloadLister[gvk]
	if !ok {
		klog.Errorf("[resource-rec] vpa %s/%s without workload lister", namespace, name)
		return nil
	}

	recommender := vpa.Spec.ResourcePolicy.AlgorithmPolicy.Recommender
	r, ok := algorithm.GetRecommender()[recommender]
	if !ok {
		klog.V(8).ErrorS(nil, fmt.Sprintf("[resource-rec] recommender %v not supported", recommender))
		return nil
	}

	pods, err := katalystutil.GetPodListForVPA(vpa, rrc.podIndexer, rrc.conf.VPAPodLabelIndexerKeys, workloadLister, rrc.podLister)
	if err != nil {
		klog.Errorf("[resource-rec] get pods for vpa %s/%s error: %v", namespace, name, err)
		return nil
	}

	spd, err := katalystutil.GetSPDForVPA(vpa, rrc.spdIndexer, workloadLister, rrc.spdLister)
	if err != nil {
		klog.Warningf("[resource-rec] get spd for vpa %s/%s error: %v", namespace, name, err)
		return nil
	}

	podResources, containerResources, err := r.GetRecommendedPodResources(spd, pods)
	if err != nil {
		klog.Errorf("[resource-rec] calculate resources for vpa %s/%s error: %v", namespace, name, err)
		return nil
	}

	vpaRec, err := rrc.getOrCreateVpaRec(vpa)
	if err != nil {
		klog.Errorf("[resource-rec] get vpaRec for vpa %s/%s error: %v", namespace, name, err)
		return nil
	}

	vpaRecNew := vpaRec.DeepCopy()
	vpaRecNew.Spec.PodRecommendations = podResources
	vpaRecNew.Spec.ContainerRecommendations = containerResources
	err = rrc.vpaRecUpdater.PatchVPARecommendation(rrc.ctx, vpaRec, vpaRecNew)
	if err != nil {
		klog.Errorf("[resource-rec] get vpaRec for vpa %s/%s error: %v", namespace, name, err)
		return nil
	}
	return nil
}

// cleanVPARec is mainly responsible to clean all vpaRec CR that should not exist
func (rrc *ResourceRecommendController) cleanVPARec() {
	recList, err := rrc.vpaRecLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("[resource-rec] failed to list all vpaRec: %v", err)
	}

	for _, vpaRec := range recList {
		needDelete := false
		vpa, err := katalystutil.GetVPAForVPARec(vpaRec, rrc.vpaLister)
		if err != nil {
			if errors.IsNotFound(err) {
				needDelete = true
			} else {
				klog.Errorf("[resource-rec] get vpa for vpaRec %s error: %v", vpaRec.Name, err)
			}
		} else {
			// delete vpa-rec if the recommender field has already erased
			recommender := vpa.Spec.ResourcePolicy.AlgorithmPolicy.Recommender
			if recommender == "" {
				needDelete = true
			}
		}

		if needDelete {
			klog.Warningf("[resource-rec] delete un-wanted vpaRec %v", vpaRec.Name)
			if err := rrc.vpaRecUpdater.DeleteVPARecommendation(rrc.ctx, vpaRec, metav1.DeleteOptions{}); err != nil {
				klog.Warningf("[resource-rec] delete un-wanted vpaRec %v err: %v", vpaRec.Name, err)
			}
		}
	}
}

// getOrCreateVpaRec is used to main the in-tree vpaRec objects if it doesn't exist
func (rrc *ResourceRecommendController) getOrCreateVpaRec(vpa *apis.KatalystVerticalPodAutoscaler) (*apis.VerticalPodAutoscalerRecommendation, error) {
	vpaRec, err := katalystutil.GetVPARecForVPA(vpa, rrc.vpaRecIndexer, rrc.vpaRecLister)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	} else {
		return vpaRec, nil
	}

	klog.Errorf("[resource-rec] create vpaRec for vpa %s/%s", vpa.Namespace, vpa.Name)
	ownerRef := metav1.OwnerReference{
		Name:       vpa.GetName(),
		Kind:       vpa.GroupVersionKind().Kind,
		APIVersion: vpa.GroupVersionKind().GroupVersion().String(),
		UID:        vpa.UID,
	}
	vpaRec = &apis.VerticalPodAutoscalerRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{ownerRef},
			Namespace:       vpa.GetNamespace(),
			Name:            vpa.GetName(),
			Labels:          general.DeepCopyMap(vpa.GetLabels()),
		},
		Spec: apis.VerticalPodAutoscalerRecommendationSpec{},
	}
	return rrc.vpaRecUpdater.CreateVPARecommendation(rrc.ctx, vpaRec, metav1.CreateOptions{})
}
