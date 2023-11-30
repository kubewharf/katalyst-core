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
	"k8s.io/utils/pointer"

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
	"github.com/kubewharf/katalyst-core/pkg/util/general"
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
	ctx       context.Context
	conf      *controller.SPDConfig
	qosConfig *generic.QoSConfiguration

	podUpdater      control.PodUpdater
	spdControl      control.ServiceProfileControl
	workloadControl control.UnstructuredControl

	spdIndexer cache.Indexer
	podIndexer cache.Indexer

	podLister           corelisters.PodLister
	spdLister           apiListers.ServiceProfileDescriptorLister
	workloadLister      map[schema.GroupVersionResource]cache.GenericLister
	spdWorkloadInformer map[schema.GroupVersionResource]native.DynamicInformer

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
	conf *controller.SPDConfig, qosConfig *generic.QoSConfiguration, extraConf interface{}) (*SPDController, error) {
	if conf == nil || controlCtx.Client == nil || genericConf == nil {
		return nil, fmt.Errorf("client, conf and generalConf can't be nil")
	}

	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()
	spdInformer := controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors()

	spdController := &SPDController{
		ctx:                 ctx,
		conf:                conf,
		qosConfig:           qosConfig,
		podUpdater:          &control.DummyPodUpdater{},
		spdControl:          &control.DummySPDControl{},
		workloadControl:     &control.DummyUnstructuredControl{},
		spdQueue:            workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "spd"),
		workloadSyncQueue:   workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "workload"),
		metricsEmitter:      controlCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(spdControllerName),
		workloadLister:      make(map[schema.GroupVersionResource]cache.GenericLister),
		spdWorkloadInformer: make(map[schema.GroupVersionResource]native.DynamicInformer),
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
			klog.Errorf("spd concerned workload %s not found in dynamic GVR resources", workload)
			continue
		}

		spdController.spdWorkloadInformer[wf.GVR] = wf
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

	// spd controller need watch pod create and delete to update its spd baseline percentile key
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    spdController.addPod,
		UpdateFunc: spdController.updatePod,
		DeleteFunc: spdController.deletePod,
	})

	if !genericConf.DryRun {
		spdController.podUpdater = control.NewRealPodUpdater(controlCtx.Client.KubeClient)
		spdController.spdControl = control.NewSPDControlImp(controlCtx.Client.InternalClient)
		spdController.workloadControl = control.NewRealUnstructuredControl(controlCtx.Client.DynamicClient)
	}

	if err := spdController.initializeIndicatorPlugins(controlCtx, extraConf); err != nil {
		return nil, err
	}

	native.WithPodTransformer(podTransformerFunc)
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

	initializers := indicator_plugin.GetPluginInitializers()
	for _, pluginName := range sc.conf.IndicatorPlugins {
		if initFunc, ok := initializers[pluginName]; ok {
			plugin, err := initFunc(sc.ctx, sc.conf, extraConf, sc.spdWorkloadInformer,
				controlCtx, sc.indicatorManager)
			if err != nil {
				return err
			}

			general.InfoS("indicator initialized", "plugin", pluginName)
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

	if !util.WorkloadSPDEnabled(workload) {
		if err := sc.cleanPodListSPDAnnotation(podList); err != nil {
			klog.Errorf("[spd] clear pod list annotations for workload %s/%s failed: %v", namespace, name, err)
			return err
		}
		return nil
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
	return nil
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
	klog.V(5).Infof("[spd] syncing spd [%v]", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[spd] failed to split namespace and name from spd key %s", key)
		return err
	}

	spd, err := sc.spdLister.ServiceProfileDescriptors(namespace).Get(name)
	if err != nil {
		klog.Errorf("[spd] failed to get spd [%v]", key)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// update baseline percentile
	newSPD := spd.DeepCopy()
	err = sc.updateBaselineSentinel(newSPD)
	if err != nil {
		return err
	}

	_, err = sc.spdControl.PatchSPD(sc.ctx, spd, newSPD)
	if err != nil {
		return err
	}

	return nil
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
			if !util.WorkloadSPDEnabled(workload) {
				needDelete = true

				klog.Warningf("[spd] clear un-wanted spd annotation %v for workload %v", spd.Name, workload.GetName())
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

// defaultBaselinePercent returns default baseline ratio based on the qos level of workload,
// and if the configured data cannot be found, we will return 1.0,
// which signifies that the resources of this workload cannot be reclaimed to reclaimed_cores.
func (sc *SPDController) defaultBaselinePercent(workload *unstructured.Unstructured) *int32 {
	annotations, err := native.GetUnstructuredTemplateAnnotations(workload)
	if err != nil {
		general.ErrorS(err, "failed to GetUnstructuredTemplateAnnotations")
		return pointer.Int32(100)
	}
	qosLevel, err := sc.qosConfig.GetQoSLevel(annotations)
	if err != nil {
		general.ErrorS(err, "failed to GetQoSLevel")
		return pointer.Int32(100)
	}
	baselinePercent, ok := sc.conf.BaselinePercent[qosLevel]
	if !ok {
		general.InfoS("failed to get default baseline percent", "qosLevel", qosLevel)
		return pointer.Int32(100)
	}
	return pointer.Int32(int32(baselinePercent))
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
			spd := &apiworkload.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name:            workload.GetName(),
					Namespace:       workload.GetNamespace(),
					OwnerReferences: []metav1.OwnerReference{ownerRef},
					Labels:          workload.GetLabels(),
				},
				Spec: apiworkload.ServiceProfileDescriptorSpec{
					TargetRef: v1alpha1.CrossVersionObjectReference{
						Name:       ownerRef.Name,
						Kind:       ownerRef.Kind,
						APIVersion: ownerRef.APIVersion,
					},
					BaselinePercent: sc.defaultBaselinePercent(workload),
				},
				Status: apiworkload.ServiceProfileDescriptorStatus{
					AggMetrics: []apiworkload.AggPodMetrics{},
				},
			}
			sc.updateBaselineSentinel(spd)

			return sc.spdControl.CreateSPD(sc.ctx, spd, metav1.CreateOptions{})
		}

		return nil, err
	}

	return spd, nil
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
	if _, ok := pod.GetAnnotations()[apiconsts.PodAnnotationSPDNameKey]; !ok {
		return nil
	}

	podCopy := pod.DeepCopy()
	annotations := podCopy.GetAnnotations()
	delete(annotations, apiconsts.PodAnnotationSPDNameKey)
	podCopy.SetAnnotations(annotations)

	err := sc.podUpdater.PatchPod(sc.ctx, pod, podCopy)
	if err != nil {
		return err
	}

	klog.Infof("[spd] successfully clear annotations for pod %v", pod.GetName())
	return nil
}

func (sc *SPDController) addPod(obj interface{}) {
	pod, ok := obj.(*core.Pod)
	if !ok {
		klog.Errorf("[spd] cannot convert obj to *core.Pod")
		return
	}
	sc.enqueuePod(pod)
}

func (sc *SPDController) deletePod(obj interface{}) {
	pod, ok := obj.(*core.Pod)
	if !ok {
		klog.Errorf("[spd] cannot convert obj to *core.Pod")
		return
	}
	sc.enqueuePod(pod)
}

func (sc *SPDController) updatePod(_ interface{}, newObj interface{}) {
	pod, ok := newObj.(*core.Pod)
	if !ok {
		klog.Errorf("[spd] cannot convert obj to *core.Pod")
		return
	}
	sc.enqueuePod(pod)
}

func (sc *SPDController) enqueuePod(pod *core.Pod) {
	name, err := util.GetPodSPDName(pod)
	if err != nil {
		return
	}

	spd, err := sc.spdLister.ServiceProfileDescriptors(pod.Namespace).Get(name)
	if err != nil {
		return
	}
	sc.enqueueSPD(spd)
}

func podTransformerFunc(src, dest *core.Pod) {
	dest.Spec.NodeName = src.Spec.NodeName
	dest.Status.Phase = src.Status.Phase
	containerStatusesTransformerFunc(&src.Status.ContainerStatuses, &dest.Status.ContainerStatuses)
}

func containerStatusesTransformerFunc(src, dst *[]core.ContainerStatus) {
	if src == nil || len(*src) == 0 {
		return
	}

	if len(*dst) == 0 {
		*dst = make([]core.ContainerStatus, len(*src))
	}

	for i, c := range *src {
		(*dst)[i].State = c.State
	}
}
