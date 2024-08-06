/*
Copyright 2024 The Katalyst Authors.

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
	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	reclister "github.com/kubewharf/katalyst-api/pkg/client/listers/recommendation/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource/prometheus"
	processormanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/manager"
	recommendermanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/recommender/manager"
	conditionstypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/conditions"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
	recommendationtypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/recommendation"
	"github.com/pkg/errors"
	"golang.org/x/time/rate"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8stypes "k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"runtime/debug"
	"time"
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

	//podIndexer cache.Indexer
	podLister corelisters.PodLister             // 暂时没有用，测试通过后删除
	recLister reclister.ResourceRecommendLister // 用于 Processor 获取当前全部 CR
	//workloadLister map[schema.GroupVersionKind]cache.GenericLister

	recQueue       workqueue.RateLimitingInterface // 资源推荐队列
	recSyncWorkers int                             // 同时处理队列的协程数量

	syncedFunc []cache.InformerSynced // 需要被同步的函数列表

	client kubernetes.Interface // 用于获取 Deployment

	OOMRecorder        *PodOOMRecorder // todo: 暂时使用同一个包下的Recorder，需要修改
	ProcessorManager   *processormanager.Manager
	RecommenderManager *recommendermanager.Manager
}

func NewResourceRecommendController(ctx context.Context,
	controlCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	recConf *controller.ResourceRecommenderConfig,
	OOMRecorder *PodOOMRecorder) (*ResourceRecommendController, error) {
	if controlCtx == nil {
		return nil, fmt.Errorf("controlCtx is invalid")
	}

	// 感觉和 pod 没什么关系，这里先保留
	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()
	recInformer := controlCtx.InternalInformerFactory.Recommendation().V1alpha1().ResourceRecommends()

	// 初始化控制器结构体
	recController := &ResourceRecommendController{
		ctx: ctx,

		recUpdater: &control.DummyResourceRecommendUpdater{},

		//podIndexer: podInformer.Informer().GetIndexer(),
		podLister: podInformer.Lister(),
		recLister: recInformer.Lister(),
		//workloadLister: make(map[schema.GroupVersionKind]cache.GenericLister),

		recQueue: workqueue.NewNamedRateLimitingQueue(
			workqueue.NewMaxOfRateLimiter(
				// todo: 这里是针对 pod Reconcile 失败时，可能需要修改，留着备注一下
				// For reconcile failures(i.e. reconcile return err), the retry time is (2*minutes)*2^<num-failures>
				// The maximum retry time is 24 hours
				workqueue.NewItemExponentialFailureRateLimiter(exponentialFailureRateLimiterBaseDelay, exponentialFailureRateLimiterMaxDelay),
				// 10 qps, 100 bucket size. This is only for retry speed, it's only the overall factor (not per item)
				&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			"resourceRecommend"),
		recSyncWorkers: recConf.RecSyncWorkers,

		syncedFunc: []cache.InformerSynced{
			podInformer.Informer().HasSynced,
			recInformer.Informer().HasSynced,
		},

		client: controlCtx.Client.KubeClient,
	}

	// todo: 没有完全理解这部分的作用，似乎是用于同步 katalyst 的 其他 cr？
	// todo: 似乎不需要 workloadLister ，在 vpa 中会用于获得 SPD 和 Pod List，暂时注释掉
	for _, wf := range controlCtx.DynamicResourcesManager.GetDynamicInformers() {
		//recController.workloadLister[wf.GVK] = wf.Informer.Lister()
		recController.syncedFunc = append(recController.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	// 注册相应的回调事件
	recInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    recController.addRec,
		UpdateFunc: recController.updateRec,
	}, recConf.RecSyncPeriod)

	// 检查是否是测试
	if !genericConf.DryRun {
		recController.recUpdater = control.NewRealResourceRecommendUpdater(controlCtx.Client.InternalClient)
	}

	// todo: 如果是和 vpa 协同工作，这里是否需要尝试给 vpa 构建索引呢？
	// todo: 还有一个 metricsEmitter，但是原控制器似乎没有这部分的内容，这里感觉也可以补上

	// 初始化数据源，创建新 ProcessorManager
	dataProxy := initDataSources(recConf)
	klog.Infof("successfully init data proxy %v", *dataProxy)

	recController.ProcessorManager = processormanager.DevNewManager(dataProxy, recController.recLister)
	recController.OOMRecorder = OOMRecorder
	recController.RecommenderManager = recommendermanager.NewManager(*recController.ProcessorManager, recController.OOMRecorder)

	return recController, nil
}

func (rrc *ResourceRecommendController) Run() {
	defer utilruntime.HandleCrash()
	defer rrc.recQueue.ShutDown()

	defer klog.Infof("[resource-recommend] shutting down %s controller", resourceRecommendControllerName)

	// 缓存同步
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

	// 开多个协程进行并发接任务，应该是为了提升吞吐率
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

// updateRec 似乎会因为我们使用了AddEventHandlerWithReSyncPeriod而被定时触发，需要进行测试
func (rrc *ResourceRecommendController) updateRec(_, newObj interface{}) {
	v, ok := newObj.(*v1alpha1.ResourceRecommend)
	if !ok {
		klog.Errorf("[resource-recommend] cannot convert obj to *apis.ResourceRecommend: %v", newObj)
		return
	}
	klog.V(4).Infof("[resource-recommend] notice update of ResourceRecommend %s", v.Name)
	rrc.enqueueRec(v)
}

// 将资源推荐任务入队
func (rrc *ResourceRecommendController) enqueueRec(rec *v1alpha1.ResourceRecommend) {
	if rec == nil {
		// todo: 这里 vpa 的源代码用的是 spd，为什么不使用 vpa？
		klog.Warningf("[resource-recommend] trying to enqueue a nil vpaRec")
	}
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(rec)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	rrc.recQueue.Add(key)
}

// recWorker 会不断地处理来自队列的任务，可以在 config 中配置多个 worker 以协程的形式同时运行
// 这样可以增加吞吐率
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

	err := rrc.syncRec(key.(string))
	if err == nil {
		rrc.recQueue.Forget(key)
		return true
	}
	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	rrc.recQueue.AddRateLimited(key) // 失败时会执行避退策略，一段时间后做重试
	return true
}

// syncRec 会进行资源推荐，如果成功则完成队列，失败则会不断重试
func (rrc *ResourceRecommendController) syncRec(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[resource-recommend] failed to split namespace and name from key %s", key)
		return err
	}

	begin := time.Now()
	defer func() {
		costs := time.Since(begin).Microseconds()
		klog.Infof("[resource-recommend] syncing resource [%v/%v] costs %v us", namespace, name, costs)
		// todo: 添加指标发送内容
	}()

	resourceRecommend, err := rrc.recLister.ResourceRecommends(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.Warningf("[resource-recommend] recommendation %s/%s is not found", namespace, name)
			// todo: 什么情况下会在有这个 namespace/name 但是没有找到这个 recommendation 呢？ 这里暂时当返回 nil 来看待
			_ = rrc.CancelTasks(k8stypes.NamespacedName{
				Namespace: namespace,
				Name:      name,
			})
			return nil
		}
		return err
	}
	// 后面代码就是接入 Reconcile 和 doReconcile 的内容了
	// 一些函数会带上 Dev 开头，是因为做了修改，独立了一个基于原函数的最小修改新函数
	if recommender := resourceRecommend.Spec.ResourcePolicy.AlgorithmPolicy.Recommender; recommender != "" &&
		recommender != recommendationtypes.DefaultRecommenderType {
		klog.InfoS("ResourceRecommend is not controlled by the default controller")
		return nil
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
			//return nil // todo: for test
		}
	}

	err = rrc.doRecommend(rrc.ctx, k8stypes.NamespacedName{Namespace: namespace, Name: name}, resourceRecommend)
	if err != nil {
		klog.ErrorS(err, "failed to sync recommendation ", namespace, "/", name)
		return err
	}
	return nil
}

// doRecommend 实现了和 doReconcile 类似的功能
func (rrc *ResourceRecommendController) doRecommend(ctx context.Context, namespacedName k8stypes.NamespacedName,
	resourceRecommend *v1alpha1.ResourceRecommend) (err error) {
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

	validationError := recommendation.DevSetConfig(ctx, rrc.client, resourceRecommend)
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
	// todo: 为什么这边写死了用 Percentile
	processor := rrc.ProcessorManager.GetProcessor(v1alpha1.AlgorithmPercentile)
	err := processor.Cancel(&processortypes.ProcessKey{ResourceRecommendNamespacedName: namespacedName})
	if err != nil {
		klog.ErrorS(err, "cancel processor task failed", "namespacedName", namespacedName)
	}
	return err
}

func (rrc *ResourceRecommendController) UpdateRecommendationStatus(namespacedName k8stypes.NamespacedName, recommendation *recommendationtypes.Recommendation) error {
	// 获取旧的 Recommendation
	oldRec, err := rrc.recLister.ResourceRecommends(namespacedName.Namespace).Get(namespacedName.Name)
	if err != nil {
		klog.ErrorS(err, "get old resourceRecommend failed")
		return err
	}

	// 做拷贝，并设置新的内容
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
