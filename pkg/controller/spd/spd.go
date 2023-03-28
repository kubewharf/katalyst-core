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

package spd

import (
	"context"
	"fmt"
	"sync"
	"time"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiListers "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	indicator_plugin "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const spdControllerName = "spd"

const (
	workloadWorkerCount        = 1
	spdWorkerCount             = 1
	indicatorSpecWorkerCount   = 1
	indicatorStatusWorkerCount = 1
)

// SPDController is responsible to maintain lifecycle of SPD CR,
// and sync and store the data represented in SPD.
//
// although we use informer index mechanism to speed up the looking
// efficiency, we can't assume that all function callers MUST use an
// indexed informer to look up objects.
type SPDController struct {
	ctx  context.Context
	conf *controller.SPDConfig

	podUpdater      control.PodUpdater
	spdControl      control.ServiceProfileControl
	workloadControl control.UnstructuredControl

	spdIndexer cache.Indexer
	podIndexer cache.Indexer

	podLister      corelisters.PodLister
	spdLister      apiListers.ServiceProfileDescriptorLister
	workloadLister map[schema.GroupVersionResource]cache.GenericLister

	syncedFunc        []cache.InformerSynced
	spdQueue          workqueue.RateLimitingInterface
	workloadSyncQueue workqueue.RateLimitingInterface

	metricsEmitter metrics.MetricEmitter

	indicatorManager         *indicator_plugin.IndicatorManager
	indicatorPlugins         map[string]indicator_plugin.IndicatorPlugin
	indicatorsSpecBusiness   map[apiworkload.ServiceBusinessIndicatorName]interface{}
	indicatorsSpecSystem     map[apiworkload.TargetIndicatorName]interface{}
	indicatorsStatusBusiness map[apiworkload.ServiceBusinessIndicatorName]interface{}
}

func NewSPDController(ctx context.Context, controlCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration, _ *controller.GenericControllerConfiguration,
	conf *controller.SPDConfig, extraConf interface{}) (*SPDController, error) {
	if conf == nil || controlCtx.Client == nil || genericConf == nil {
		return nil, fmt.Errorf("client, conf and generalConf can't be nil")
	}

	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()
	spdInformer := controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors()

	spdController := &SPDController{
		ctx:               ctx,
		conf:              conf,
		podUpdater:        &control.DummyPodUpdater{},
		spdControl:        &control.DummySPDControl{},
		workloadControl:   &control.DummyUnstructuredControl{},
		spdQueue:          workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "spd"),
		workloadSyncQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "workload"),
		workloadLister:    make(map[schema.GroupVersionResource]cache.GenericLister),
	}

	spdController.podLister = podInformer.Lister()
	spdController.syncedFunc = append(spdController.syncedFunc, podInformer.Informer().HasSynced)

	spdController.spdLister = spdInformer.Lister()
	spdController.syncedFunc = append(spdController.syncedFunc, spdInformer.Informer().HasSynced)

	workloadInformers := controlCtx.DynamicResourcesManager.GetDynamicInformers()
	for _, wf := range workloadInformers {
		spdController.workloadLister[wf.GVR] = wf.Informer.Lister()
		spdController.syncedFunc = append(spdController.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	for _, workload := range conf.SPDWorkloadGVResources {
		wf, ok := workloadInformers[workload]
		if !ok {
			return nil, fmt.Errorf("spd concerned workload %s not found in dynamic GVR resources", workload)
		}
		wf.Informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    spdController.addWorkload(workload),
			UpdateFunc: spdController.updateWorkload(workload),
		})
	}

	spdInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    spdController.addSPD,
		UpdateFunc: spdController.updateSPD,
	}, conf.ReSyncPeriod)

	// build index: workload ---> spd
	spdController.spdIndexer = spdInformer.Informer().GetIndexer()
	if _, exist := spdController.spdIndexer.GetIndexers()[consts.TargetReferenceIndex]; !exist {
		err := spdController.spdIndexer.AddIndexers(cache.Indexers{
			consts.TargetReferenceIndex: util.SPDTargetReferenceIndex,
		})
		if err != nil {
			klog.Errorf("[spd] failed to add target reference index for spd: %v", err)
			return nil, err
		}
	}

	// build index: workload ---> pod
	spdController.podIndexer = podInformer.Informer().GetIndexer()
	for _, key := range conf.SPDPodLabelIndexerKeys {
		indexer := native.PodLabelIndexer(key)
		if _, ok := spdController.podIndexer.GetIndexers()[key]; !ok {
			err := spdController.podIndexer.AddIndexers(cache.Indexers{
				key: indexer.IndexFunc,
			})
			if err != nil {
				klog.Errorf("[spd] failed to add label index for pod: %v", err)
				return nil, err
			}
		}
	}

	if !genericConf.DryRun {
		spdController.podUpdater = control.NewRealPodUpdater(controlCtx.Client.KubeClient)
		spdController.spdControl = control.NewSPDControlImp(controlCtx.Client.InternalClient)
		spdController.workloadControl = control.NewRealUnstructuredControl(controlCtx.Client.DynamicClient)
	}

	spdController.metricsEmitter = controlCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(spdControllerName)
	if spdController.metricsEmitter == nil {
		spdController.metricsEmitter = metrics.DummyMetrics{}
	}

	if err := spdController.initializeIndicatorPlugins(controlCtx, extraConf); err != nil {
		return nil, err
	}

	return spdController, nil
}

func (sc *SPDController) Run() {
	defer utilruntime.HandleCrash()
	defer sc.workloadSyncQueue.ShutDown()
	defer sc.spdQueue.ShutDown()
	defer klog.Infof("shutting down %s controller", spdControllerName)

	if !cache.WaitForCacheSync(sc.ctx.Done(), sc.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", spdControllerName))
		return
	}
	klog.Infof("caches are synced for %s controller", spdControllerName)

	for i := 0; i < workloadWorkerCount; i++ {
		go wait.Until(sc.workloadWorker, time.Second, sc.ctx.Done())
	}
	for i := 0; i < spdWorkerCount; i++ {
		go wait.Until(sc.spdWorker, time.Second, sc.ctx.Done())
	}
	go wait.Until(sc.cleanSPD, time.Minute*5, sc.ctx.Done())
	go wait.Until(sc.monitor, time.Second*30, sc.ctx.Done())

	for _, plugin := range sc.indicatorPlugins {
		go plugin.Run()
	}
	for i := 0; i < indicatorSpecWorkerCount; i++ {
		go wait.Until(sc.syncIndicatorSpec, time.Second, sc.ctx.Done())
	}
	for i := 0; i < indicatorStatusWorkerCount; i++ {
		go wait.Until(sc.syncIndicatorStatus, time.Second, sc.ctx.Done())
	}

	<-sc.ctx.Done()
}

func (sc *SPDController) GetIndicatorPlugins() (plugins []indicator_plugin.IndicatorPlugin) {
	for _, p := range sc.indicatorPlugins {
		plugins = append(plugins, p)
	}
	return plugins
}

func (sc *SPDController) initializeIndicatorPlugins(controlCtx *katalystbase.GenericContext, extraConf interface{}) error {
	sc.indicatorManager = indicator_plugin.NewIndicatorManager()
	sc.indicatorPlugins = make(map[string]indicator_plugin.IndicatorPlugin)
	sc.indicatorsSpecBusiness = make(map[apiworkload.ServiceBusinessIndicatorName]interface{})
	sc.indicatorsSpecSystem = make(map[apiworkload.TargetIndicatorName]interface{})
	sc.indicatorsStatusBusiness = make(map[apiworkload.ServiceBusinessIndicatorName]interface{})

	for pluginName, initFunc := range indicator_plugin.GetPluginInitializers() {
		plugin, err := initFunc(sc.ctx, sc.conf, extraConf, sc.workloadLister, controlCtx, sc.indicatorManager)
		if err != nil {
			return err
		}

		klog.Infof("indicator plugin %s initialized", pluginName)
		sc.indicatorPlugins[pluginName] = plugin
		for _, name := range plugin.GetSupportedBusinessIndicatorSpec() {
			sc.indicatorsSpecBusiness[name] = struct{}{}
		}
		for _, name := range plugin.GetSupportedSystemIndicatorSpec() {
			sc.indicatorsSpecSystem[name] = struct{}{}
		}
		for _, name := range plugin.GetSupportedBusinessIndicatorStatus() {
			sc.indicatorsStatusBusiness[name] = struct{}{}
		}
	}
	return nil
}

func (sc *SPDController) addWorkload(workloadGVR string) func(obj interface{}) {
	return func(obj interface{}) {
		workload, ok := obj.(metav1.Object)
		if !ok {
			klog.Errorf("[spd] cannot convert obj to metav1.Object")
			return
		}
		sc.enqueueWorkload(workloadGVR, workload)
	}
}

func (sc *SPDController) updateWorkload(workloadGVR string) func(oldObj, newObj interface{}) {
	return func(_, cur interface{}) {
		workload, ok := cur.(metav1.Object)
		if !ok {
			klog.Errorf("[spd] cannot convert cur obj to metav1.Object")
			return
		}
		sc.enqueueWorkload(workloadGVR, workload)
	}
}

func (sc *SPDController) enqueueWorkload(workloadGVR string, workload metav1.Object) {
	if workload == nil {
		klog.Warning("[spd] trying to enqueue a nil spd")
		return
	}

	key, err := native.GenerateUniqGVRNameKey(workloadGVR, workload)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	sc.workloadSyncQueue.Add(key)
}

func (sc *SPDController) workloadWorker() {
	for sc.processNextWorkload() {
	}
}

func (sc *SPDController) processNextWorkload() bool {
	key, quit := sc.workloadSyncQueue.Get()
	if quit {
		return false
	}
	defer sc.workloadSyncQueue.Done(key)

	err := sc.syncWorkload(key.(string))
	if err == nil {
		sc.workloadSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	sc.workloadSyncQueue.AddRateLimited(key)

	return true
}

// syncWorkload is mainly responsible to maintain the lifecycle of spd for each
// workload, without handling the service profile calculation logic.
func (sc *SPDController) syncWorkload(key string) error {
	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		klog.V(5).Infof("[spd] finished syncing workload %q (%v)", key, costs)
		_ = sc.metricsEmitter.StoreInt64(metricsNameSyncWorkloadCost, costs.Microseconds(),
			metrics.MetricTypeNameRaw, metrics.MetricTag{Key: "name", Val: key})
	}()

	klog.V(5).Infof("[spd] syncing workload [%v]", key)

	workloadGVR, namespace, name, err := native.ParseUniqGVRNameKey(key)
	if err != nil {
		klog.Errorf("[spd] failed to parse key %s to workload", key)
		return err
	}

	gvr, _ := schema.ParseResourceArg(workloadGVR)
	if gvr == nil {
		err = fmt.Errorf("[spd] ParseResourceArg worload %v failed", workloadGVR)
		klog.Error(err)
		return err
	}

	workload, err := sc.getWorkload(*gvr, namespace, name)
	if err != nil {
		klog.Errorf("[spd] failed to get workload %s/%s", namespace, name)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	podList, err := native.GetPodListForWorkload(workload, sc.podIndexer, sc.conf.SPDPodLabelIndexerKeys, sc.podLister)
	if err != nil {
		klog.Errorf("[spd] get pod list for workload %s/%s failed: %v", namespace, name, err)
		return err
	}

	if !util.CheckWorkloadSPDEnabled(workload) {
		if err := sc.cleanPodListSPDAnnotation(podList); err != nil {
			klog.Errorf("[spd] clear pod list annotations for workload %s/%s failed: %v", namespace, name, err)
			return err
		}
		return sc.cleanWorkloadSPDAnnotation(workload, *gvr)
	}

	spd, err := sc.getOrCreateSPDForWorkload(workload)
	if err != nil {
		klog.Errorf("[spd] get or create spd for workload %s/%s failed: %v", namespace, name, err)
		return err
	}

	if err := sc.setPodListSPDAnnotation(podList, spd.Name); err != nil {
		klog.Errorf("[spd] set pod list annotations for workload %s/%s failed: %v", namespace, name, err)
		return err
	}
	return sc.setWorkloadSPDAnnotation(workload, *gvr, spd.Name)
}

func (sc *SPDController) addSPD(obj interface{}) {
	spd, ok := obj.(*apiworkload.ServiceProfileDescriptor)
	if !ok {
		klog.Errorf("[spd] cannot convert obj to *apiworkload.ServiceProfileDescriptor")
		return
	}
	sc.enqueueSPD(spd)
}

func (sc *SPDController) updateSPD(_, newObj interface{}) {
	spd, ok := newObj.(*apiworkload.ServiceProfileDescriptor)
	if !ok {
		klog.Errorf("[spd] cannot convert obj to *apiworkload.ServiceProfileDescriptor")
		return
	}
	sc.enqueueSPD(spd)
}

func (sc *SPDController) enqueueSPD(spd *apiworkload.ServiceProfileDescriptor) {
	if spd == nil {
		klog.Warning("[spd] trying to enqueue a nil spd")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(spd)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("[spd] couldn't get key for workload %#v: %v", spd, err))
		return
	}

	sc.spdQueue.Add(key)
}

func (sc *SPDController) spdWorker() {
	for sc.processNextSPD() {
	}
}

func (sc *SPDController) processNextSPD() bool {
	key, quit := sc.spdQueue.Get()
	if quit {
		return false
	}
	defer sc.spdQueue.Done(key)

	err := sc.syncSPD(key.(string))
	if err == nil {
		sc.spdQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	sc.spdQueue.AddRateLimited(key)

	return true
}

// syncSPD is mainly responsible to handle the service profile calculation logic for each
// spd existed, and it will always assume that all spd is valid.
func (sc *SPDController) syncSPD(key string) error {
	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		klog.Infof("[spd] finished syncing spd %q (%v)", key, costs)
		_ = sc.metricsEmitter.StoreInt64(metricsNameSyncSPDCost, costs.Microseconds(),
			metrics.MetricTypeNameRaw, metrics.MetricTag{Key: "name", Val: key})
	}()

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[spd] failed to split namespace and name from spd key %s", key)
		return err
	}

	_, err = sc.spdLister.ServiceProfileDescriptors(namespace).Get(name)
	if err != nil {
		klog.Errorf("[spd] failed to get spd [%v/%v]", namespace, name)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	klog.Infof("[spd] syncing spd [%v/%v]", namespace, name)

	return nil
	// todo: sync metrics data from custom metrics apiServer and update into spd status
}

// cleanSPD is mainly responsible to clean all spd CR that should not exist if its workload
// is deleted or no longer enabled with service profiling logic.
func (sc *SPDController) cleanSPD() {
	spdList, err := sc.spdLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("[spd] failed to list all spd: %v", err)
	}

	for _, spd := range spdList {
		gvr, _ := meta.UnsafeGuessKindToResource(schema.FromAPIVersionAndKind(spd.Spec.TargetRef.APIVersion, spd.Spec.TargetRef.Kind))
		workloadLister, ok := sc.workloadLister[gvr]
		if !ok {
			klog.Errorf("[spd] spd %s without workload lister", spd.Name)
			continue
		}

		needDelete := false
		workloadObj, err := util.GetWorkloadForSPD(spd, workloadLister)
		if err != nil {
			if errors.IsNotFound(err) {
				needDelete = true
			} else {
				klog.Errorf("[spd] get workload for spd %s error: %v", spd.Name, err)
			}
		} else {
			workload := workloadObj.(*unstructured.Unstructured)
			if !util.CheckWorkloadSPDEnabled(workload) {
				needDelete = true

				klog.Warningf("[spd] clear un-wanted spd annotation %v for workload %v", spd.Name, workload.GetName())
				if err := sc.cleanWorkloadSPDAnnotation(workload, gvr); err != nil {
					klog.Errorf("[spd] clear un-wanted spd annotation %s for workload %v error: %v", spd.Name, workload.GetName(), err)
				}
			}
		}

		if needDelete {
			klog.V(5).Infof("[spd] delete un-wanted spd %v", spd.Name)
			if err := sc.spdControl.DeleteSPD(sc.ctx, spd, metav1.DeleteOptions{}); err != nil {
				klog.Warningf("[spd] delete un-wanted spd %v err: %v", spd.Name, err)
			}
		}
	}
}

// getWorkload is used to get workload info from dynamic lister according to the given GVR
func (sc *SPDController) getWorkload(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if _, ok := sc.workloadLister[gvr]; !ok {
		return nil, fmt.Errorf("can't find gvr %s from listers", gvr.String())
	}

	workloadLister := sc.workloadLister[gvr]
	workloadObj, err := workloadLister.ByNamespace(namespace).Get(name)
	if err != nil {
		return nil, err
	}

	workload, ok := workloadObj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("failed to convert workload to *unstructured.Unstructured")
	}

	return workload, nil
}

// getOrCreateSPDForWorkload get workload's spd or create one if the spd doesn't exist
func (sc *SPDController) getOrCreateSPDForWorkload(workload *unstructured.Unstructured) (*apiworkload.ServiceProfileDescriptor, error) {
	gvk := workload.GroupVersionKind()
	ownerRef := metav1.OwnerReference{
		Name:       workload.GetName(),
		Kind:       gvk.Kind,
		APIVersion: gvk.GroupVersion().String(),
		UID:        workload.GetUID(),
	}

	spd, err := util.GetSPDForWorkload(workload, sc.spdIndexer, sc.spdLister)
	if err != nil {
		if errors.IsNotFound(err) {
			spd := &apiworkload.ServiceProfileDescriptor{}
			spd.SetOwnerReferences([]metav1.OwnerReference{ownerRef})
			spd.SetNamespace(workload.GetNamespace())
			spd.SetName(workload.GetName())
			spd.Spec.TargetRef = v1alpha1.CrossVersionObjectReference{
				Name:       ownerRef.Name,
				Kind:       ownerRef.Kind,
				APIVersion: ownerRef.APIVersion,
			}
			spd.Status.AggMetrics = []apiworkload.AggPodMetrics{}

			return sc.spdControl.CreateSPD(sc.ctx, spd, metav1.CreateOptions{})
		}

		return nil, err
	}

	return spd, nil
}

// setWorkloadSPDAnnotation add spd name in workload annotations
func (sc *SPDController) setWorkloadSPDAnnotation(workload *unstructured.Unstructured, gvr schema.GroupVersionResource, spdName string) error {
	if workload.GetAnnotations()[apiconsts.WorkloadAnnotationSPDNameKey] == spdName {
		return nil
	}

	workloadCopy := workload.DeepCopy()
	annotations := workloadCopy.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[apiconsts.WorkloadAnnotationSPDNameKey] = spdName
	workloadCopy.SetAnnotations(annotations)

	workloadGVR := metav1.GroupVersionResource{Version: gvr.Version, Group: gvr.Group, Resource: gvr.Resource}
	_, err := sc.workloadControl.PatchUnstructured(sc.ctx, workloadGVR, workload, workloadCopy)
	if err != nil {
		return err
	}

	klog.Infof("[spd] successfully set annotations for workload %v to %v", workload.GetName(), spdName)
	return nil
}

// cleanWorkloadSPDAnnotation removes spd name in workload annotations
func (sc *SPDController) cleanWorkloadSPDAnnotation(workload *unstructured.Unstructured, gvr schema.GroupVersionResource) error {
	if _, ok := workload.GetAnnotations()[apiconsts.WorkloadAnnotationSPDNameKey]; !ok {
		return nil
	}

	workloadCopy := workload.DeepCopy()
	annotations := workloadCopy.GetAnnotations()
	delete(annotations, apiconsts.WorkloadAnnotationSPDNameKey)
	workloadCopy.SetAnnotations(annotations)

	workloadGVR := metav1.GroupVersionResource{Version: gvr.Version, Group: gvr.Group, Resource: gvr.Resource}
	_, err := sc.workloadControl.PatchUnstructured(sc.ctx, workloadGVR, workload, workloadCopy)
	if err != nil {
		return err
	}

	klog.Infof("[spd] successfully clear annotations for workload %v", workload.GetName())
	return nil
}

func (sc *SPDController) setPodListSPDAnnotation(podList []*core.Pod, spdName string) error {
	var mtx sync.Mutex
	var errList []error
	setPodAnnotations := func(i int) {
		err := sc.setPodSPDAnnotation(podList[i], spdName)
		if err != nil {
			mtx.Lock()
			errList = append(errList, err)
			mtx.Unlock()
			return
		}
	}
	workqueue.ParallelizeUntil(sc.ctx, 16, len(podList), setPodAnnotations)
	if len(errList) > 0 {
		err := utilerrors.NewAggregate(errList)
		klog.Errorf(err.Error())
		return err
	}

	return nil
}

// setPodSPDAnnotation add spd name in pod annotations
func (sc *SPDController) setPodSPDAnnotation(pod *core.Pod, spdName string) error {
	if pod.GetAnnotations()[apiconsts.PodAnnotationSPDNameKey] == spdName {
		return nil
	}

	podCopy := pod.DeepCopy()
	annotations := podCopy.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[apiconsts.PodAnnotationSPDNameKey] = spdName
	podCopy.SetAnnotations(annotations)

	err := sc.podUpdater.PatchPod(sc.ctx, pod, podCopy)
	if err != nil {
		return err
	}

	klog.Infof("[spd] successfully set annotations for pod %v to %v", pod.GetName(), spdName)
	return nil
}

func (sc *SPDController) cleanPodListSPDAnnotation(podList []*core.Pod) error {
	var mtx sync.Mutex
	var errList []error
	setPodAnnotations := func(i int) {
		err := sc.cleanPodSPDAnnotation(podList[i])
		if err != nil {
			mtx.Lock()
			errList = append(errList, err)
			mtx.Unlock()
			return
		}
	}
	workqueue.ParallelizeUntil(sc.ctx, 16, len(podList), setPodAnnotations)
	if len(errList) > 0 {
		err := utilerrors.NewAggregate(errList)
		klog.Errorf(err.Error())
		return err
	}

	return nil
}

// cleanPodSPDAnnotation removes pod name in workload annotations
func (sc *SPDController) cleanPodSPDAnnotation(pod *core.Pod) error {
	if _, ok := pod.GetAnnotations()[apiconsts.WorkloadAnnotationSPDNameKey]; !ok {
		return nil
	}

	podCopy := pod.DeepCopy()
	annotations := podCopy.GetAnnotations()
	delete(annotations, apiconsts.WorkloadAnnotationSPDNameKey)
	podCopy.SetAnnotations(annotations)

	err := sc.podUpdater.PatchPod(sc.ctx, pod, podCopy)
	if err != nil {
		return err
	}

	klog.Infof("[spd] successfully clear annotations for pod %v", pod.GetName())
	return nil
}
