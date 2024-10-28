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

package controller

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/time/rate"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8stypes "k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	reclister "github.com/kubewharf/katalyst-api/pkg/client/listers/recommendation/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource/prometheus"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/oom"
	processormanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/manager"
	recommendermanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/recommender/manager"
	conditionstypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/conditions"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
	recommendationtypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/recommendation"
)

const (
	resourceRecommendControllerName        = "resourceRecommend"
	exponentialFailureRateLimiterBaseDelay = time.Minute
	exponentialFailureRateLimiterMaxDelay  = 30 * time.Minute
	defaultRecommendInterval               = 24 * time.Hour
)

type ResourceRecommendController struct {
	ctx  context.Context
	conf *controller.ResourceRecommenderConfig

	recUpdater control.ResourceRecommendUpdater

	recLister reclister.ResourceRecommendLister

	recQueue       workqueue.RateLimitingInterface
	recSyncWorkers int
	recSyncPeriod  time.Duration

	syncedFunc []cache.InformerSynced

	client     dynamic.Interface
	restMapper *restmapper.DeferredDiscoveryRESTMapper

	OOMRecorder        *oom.PodOOMRecorder
	ProcessorManager   *processormanager.Manager
	RecommenderManager *recommendermanager.Manager
}

func NewResourceRecommendController(ctx context.Context,
	controlCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	recConf *controller.ResourceRecommenderConfig,
	OOMRecorder *oom.PodOOMRecorder,
) (*ResourceRecommendController, error) {
	if controlCtx == nil {
		return nil, fmt.Errorf("controlCtx is invalid")
	}

	recInformer := controlCtx.InternalInformerFactory.Recommendation().V1alpha1().ResourceRecommends()

	recController := &ResourceRecommendController{
		ctx: ctx,

		recUpdater: &control.DummyResourceRecommendUpdater{},
		recLister:  recInformer.Lister(),
		recQueue: workqueue.NewNamedRateLimitingQueue(
			workqueue.NewMaxOfRateLimiter(
				// For syncRec failures(i.e. doRecommend return err), the retry time is (2*minutes)*2^<num-failures>
				// The maximum retry time is 24 hours
				workqueue.NewItemExponentialFailureRateLimiter(exponentialFailureRateLimiterBaseDelay, exponentialFailureRateLimiterMaxDelay),
				// 10 qps, 100 bucket size. This is only for retry speed, it's only the overall factor (not per item)
				&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			"resourceRecommend"),
		recSyncWorkers: recConf.RecSyncWorkers,
		recSyncPeriod:  recConf.RecSyncPeriod,

		syncedFunc: []cache.InformerSynced{
			recInformer.Informer().HasSynced,
		},

		client:     controlCtx.Client.DynamicClient,
		restMapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(controlCtx.Client.DiscoveryClient)),
	}

	for _, wf := range controlCtx.DynamicResourcesManager.GetDynamicInformers() {
		recController.syncedFunc = append(recController.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	recInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    recController.addRec,
		UpdateFunc: recController.updateRec,
		DeleteFunc: recController.deleteRec,
	})

	if !genericConf.DryRun {
		recController.recUpdater = control.NewRealResourceRecommendUpdater(controlCtx.Client.InternalClient)
	}

	// todo: add metricsEmitter

	dataProxy := initDataSources(recConf)
	klog.Infof("[resource-recommend] successfully init data proxy %v", *dataProxy)

	recController.ProcessorManager = processormanager.NewManager(dataProxy, recController.recLister)
	recController.OOMRecorder = OOMRecorder
	recController.RecommenderManager = recommendermanager.NewManager(*recController.ProcessorManager, recController.OOMRecorder)

	return recController, nil
}

func (rrc *ResourceRecommendController) Run() {
	defer utilruntime.HandleCrash()
	defer rrc.recQueue.ShutDown()

	defer klog.Infof("[resource-recommend] shutting down %s controller", resourceRecommendControllerName)

	if !cache.WaitForCacheSync(rrc.ctx.Done(), rrc.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", resourceRecommendControllerName))
		return
	}

	// Start Processor
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := errors.Errorf("start processor panic: %v", r.(error))
				klog.Error(err)
				panic(err)
			}
		}()
		rrc.ProcessorManager.StartProcess(rrc.ctx)
	}()

	klog.Infof("[resource-recommend] caches are synced for %s controller", resourceRecommendControllerName)
	klog.Infof("[resource-recommend] start %v recSyncWorkers", rrc.recSyncWorkers)

	for i := 0; i < rrc.recSyncWorkers; i++ {
		go wait.Until(rrc.recWorker, time.Second, rrc.ctx.Done())
	}

	<-rrc.ctx.Done()
}

func (rrc *ResourceRecommendController) addRec(obj interface{}) {
	v, ok := obj.(*v1alpha1.ResourceRecommend)
	if !ok {
		klog.Errorf("[resource-recommend] cannot convert obj to *apis.ResourceRecommend: %v", obj)
		return
	}
	klog.V(4).Infof("[resource-recommend] notice addition of ResourceRecommend %s", v.Name)
	rrc.enqueueRec(v)
}

func (rrc *ResourceRecommendController) updateRec(oldObj, newObj interface{}) {
	oldRec, oldOk := oldObj.(*v1alpha1.ResourceRecommend)
	newRec, newOk := newObj.(*v1alpha1.ResourceRecommend)
	if !oldOk || !newOk {
		klog.Errorf("[resource-recommend] cannot convert obj to *apis.ResourceRecommend: %v", newObj)
		return
	}

	// Only enqueue when the spec changes, ignore status changes.
	if !reflect.DeepEqual(oldRec.Spec, newRec.Spec) {
		klog.V(4).Infof("[resource-recommend] notice spec update of ResourceRecommend %s", newRec.Name)
		rrc.enqueueRec(newRec)
	}
}

func (rrc *ResourceRecommendController) deleteRec(obj interface{}) {
	var rec *v1alpha1.ResourceRecommend = nil
	switch t := obj.(type) {
	case *v1alpha1.ResourceRecommend:
		rec = t
	case cache.DeletedFinalStateUnknown:
		ok := false
		rec, ok = t.Obj.(*v1alpha1.ResourceRecommend)
		if !ok {
			klog.ErrorS(nil, "[resource-recommend] cannot convert obj to *apis.ResourceRecommend: %v", "Obj", t)
			return
		}
	default:
		klog.ErrorS(nil, "Cannot convert to *v1alpha1.ResourceRecommend", "Obj", t)
		return
	}
	klog.V(4).Infof("[resource-recommend] notice deletion of ResourceRecommend %s", rec.Name)
	rrc.dequeueRec(rec)
}

func (rrc *ResourceRecommendController) enqueueRec(rec *v1alpha1.ResourceRecommend) {
	if rec == nil {
		klog.Warningf("[resource-recommend] trying to enqueue a nil rec")
	}
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(rec)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	rrc.recQueue.Add(key)
}

func (rrc *ResourceRecommendController) dequeueRec(rec *v1alpha1.ResourceRecommend) {
	if rec == nil {
		klog.Warningf("[resource-recommend] trying to dequeue a nil rec")
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(rec)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	rrc.recQueue.Forget(key)
	rrc.recQueue.Done(key)
}

// recWorker continuously processes tasks from the queue.
// Multiple workers can be configured to run concurrently via goroutines in the config, enhancing throughput.
func (rrc *ResourceRecommendController) recWorker() {
	for rrc.ProcessNextResourceRecommend() {
	}
	klog.V(4).Infof("finish recworker")
}

func (rrc *ResourceRecommendController) ProcessNextResourceRecommend() bool {
	key, quit := rrc.recQueue.Get()
	if quit {
		return false
	}
	defer rrc.recQueue.Done(key)

	timeAfter, err := rrc.syncRec(key.(string))
	if err == nil {
		rrc.recQueue.Forget(key)
		rrc.recQueue.AddAfter(key, timeAfter)
		return true
	}
	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	rrc.recQueue.AddRateLimited(key)
	return true
}

// syncRec processes resource recommendations. On success, it clears the queue item; on failure, it retries.
// Returns -1 upon error, otherwise the interval in seconds until the next execution for the same recommendation.
func (rrc *ResourceRecommendController) syncRec(key string) (time.Duration, error) {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[resource-recommend] failed to split namespace and name from key %s", key)
		return -1, err
	}

	begin := time.Now()
	defer func() {
		costs := time.Since(begin).Microseconds()
		klog.Infof("[resource-recommend] syncing resource [%v/%v] costs %v us", namespace, name, costs)
		// todo: add metricsEmitter
	}()

	resourceRecommend, err := rrc.recLister.ResourceRecommends(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.Warningf("[resource-recommend] recommendation %s/%s is not found", namespace, name)
			_ = rrc.CancelTasks(k8stypes.NamespacedName{
				Namespace: namespace,
				Name:      name,
			})
			return rrc.recSyncPeriod, nil
		}
		return -1, err
	}
	if recommender := resourceRecommend.Spec.ResourcePolicy.AlgorithmPolicy.Recommender; recommender != "" &&
		recommender != recommendationtypes.DefaultRecommenderType {
		klog.InfoS("ResourceRecommend is not controlled by the default controller")
		return rrc.recSyncPeriod, nil
	}

	if lastRecommendationTime := resourceRecommend.Status.LastRecommendationTime; lastRecommendationTime != nil {
		requeueAfter := time.Until(lastRecommendationTime.Add(defaultRecommendInterval))
		observedGeneration := resourceRecommend.Status.ObservedGeneration
		if requeueAfter > time.Duration(0) && observedGeneration == resourceRecommend.GetGeneration() {
			klog.InfoS("no spec change and not time to sync, skipping this sync",
				"requeueAfter", requeueAfter,
				"observedGeneration", observedGeneration,
				"generation", resourceRecommend.GetGeneration(),
				"resourceRecommendName", resourceRecommend.GetName())
			return requeueAfter, nil
		}
	}

	err = rrc.doRecommend(rrc.ctx, k8stypes.NamespacedName{Namespace: namespace, Name: name}, resourceRecommend)
	if err != nil {
		klog.ErrorS(err, "failed to sync recommendation ", namespace, "/", name)
		return -1, err
	}
	return rrc.recSyncPeriod, nil
}

func (rrc *ResourceRecommendController) doRecommend(ctx context.Context, namespacedName k8stypes.NamespacedName,
	resourceRecommend *v1alpha1.ResourceRecommend,
) (err error) {
	recommendation := recommendationtypes.NewRecommendation(resourceRecommend)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
			klog.ErrorS(err, "doRecommend panic", "resourceRecommend", namespacedName, "stack", string(debug.Stack()))
			return
		}
		updateStatusErr := rrc.UpdateRecommendationStatus(namespacedName, recommendation)
		if err == nil {
			err = updateStatusErr
		}
	}()

	validationError := recommendation.SetConfig(ctx, rrc.client, resourceRecommend, rrc.restMapper)
	if validationError != nil {
		klog.ErrorS(validationError, "failed to get Recommendation", "resourceRecommend", namespacedName)
		recommendation.Conditions.Set(*conditionstypes.ConvertCustomErrorToCondition(*validationError))
		return validationError
	}

	recommendation.Conditions.Set(*conditionstypes.ValidationSucceededCondition())

	if registerTaskErr := rrc.RegisterTasks(*recommendation); registerTaskErr != nil {
		klog.ErrorS(registerTaskErr, "Failed to register process task", "resourceRecommend", namespacedName)
		recommendation.Conditions.Set(*conditionstypes.ConvertCustomErrorToCondition(*registerTaskErr))
		return registerTaskErr
	}

	recommendation.Conditions.Set(*conditionstypes.InitializationSucceededCondition())

	defaultRecommender := rrc.RecommenderManager.NewRecommender(recommendation.AlgorithmPolicy.Algorithm)
	if recommendationError := defaultRecommender.Recommend(recommendation); recommendationError != nil {
		klog.ErrorS(recommendationError, "error when getting recommendation for resource", "resourceRecommend", namespacedName)
		recommendation.Conditions.Set(*conditionstypes.ConvertCustomErrorToCondition(*recommendationError))
		return recommendationError
	}

	recommendation.Conditions.Set(*conditionstypes.RecommendationReadyCondition())
	return nil
}

func (rrc *ResourceRecommendController) RegisterTasks(recommendation recommendationtypes.Recommendation) *errortypes.CustomError {
	processor := rrc.ProcessorManager.GetProcessor(recommendation.AlgorithmPolicy.Algorithm)
	for _, container := range recommendation.Containers {
		for _, containerConfig := range container.ContainerConfigs {
			processConfig := processortypes.NewProcessConfig(recommendation.NamespacedName,
				recommendation.Config.TargetRef, container.ContainerName,
				containerConfig.ControlledResource, "")
			if err := processor.Register(processConfig); err != nil {
				return errortypes.DataProcessRegisteredFailedError(err.Error())
			}
		}
	}
	return nil
}

// CancelTasks Cancel all process task
func (rrc *ResourceRecommendController) CancelTasks(namespacedName k8stypes.NamespacedName) *errortypes.CustomError {
	processor := rrc.ProcessorManager.GetProcessor(v1alpha1.AlgorithmPercentile)
	err := processor.Cancel(&processortypes.ProcessKey{ResourceRecommendNamespacedName: namespacedName})
	if err != nil {
		klog.ErrorS(err, "cancel processor task failed", "namespacedName", namespacedName)
	}
	return err
}

func (rrc *ResourceRecommendController) UpdateRecommendationStatus(namespacedName k8stypes.NamespacedName, recommendation *recommendationtypes.Recommendation) error {
	oldRec, err := rrc.recLister.ResourceRecommends(namespacedName.Namespace).Get(namespacedName.Name)
	if err != nil {
		klog.ErrorS(err, "get old resourceRecommend failed")
		return err
	}

	newRec := oldRec.DeepCopy()
	newRec.Status = recommendation.AsStatus()
	newRec.Status.ObservedGeneration = recommendation.ObservedGeneration

	return rrc.recUpdater.PatchResourceRecommend(rrc.ctx, oldRec, newRec)
}

func initDataSources(opts *controller.ResourceRecommenderConfig) *datasource.Proxy {
	dataProxy := datasource.NewProxy()
	for _, datasourceProvider := range opts.DataSource {
		switch datasourceProvider {
		case string(datasource.PrometheusDatasource):
			fallthrough
		default:
			// default is prom
			prometheusProvider, err := prometheus.NewPrometheus(&opts.DataSourcePromConfig)
			if err != nil {
				klog.Exitf("unable to create datasource provider %v, err: %v", prometheusProvider, err)
				panic(err)
			}
			dataProxy.RegisterDatasource(datasource.PrometheusDatasource, prometheusProvider)
		}
	}
	return dataProxy
}
