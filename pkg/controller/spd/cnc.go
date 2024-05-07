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
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	configapis "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	configinformers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/workload/v1alpha1"
	configlisters "github.com/kubewharf/katalyst-api/pkg/client/listers/config/v1alpha1"
	apiListers "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	metricsNameSyncCNCCacheCost        = "sync_cnc_cache_cost"
	metricsNameClearUnusedCNCCacheCost = "clear_unused_cnc_cache_cost"

	cncWorkerCount = 1
)

type cncCacheController struct {
	ctx  context.Context
	conf *controller.SPDConfig

	cncControl control.CNCControl

	spdIndexer cache.Indexer
	podIndexer cache.Indexer

	podLister         corelisters.PodLister
	spdLister         apiListers.ServiceProfileDescriptorLister
	cncLister         configlisters.CustomNodeConfigLister
	workloadGVKLister map[schema.GroupVersionKind]cache.GenericLister
	workloadLister    map[schema.GroupVersionResource]cache.GenericLister

	cncSyncQueue workqueue.RateLimitingInterface

	metricsEmitter metrics.MetricEmitter
}

func newCNCCacheController(ctx context.Context,
	podInformer coreinformers.PodInformer,
	cncInformer configinformers.CustomNodeConfigInformer,
	spdInformer v1alpha1.ServiceProfileDescriptorInformer,
	workloadGVKLister map[schema.GroupVersionKind]cache.GenericLister,
	workloadLister map[schema.GroupVersionResource]cache.GenericLister,
	cncControl control.CNCControl,
	metricsEmitter metrics.MetricEmitter,
	conf *controller.SPDConfig,
) (*cncCacheController, error) {
	c := &cncCacheController{
		ctx:               ctx,
		conf:              conf,
		cncControl:        cncControl,
		spdIndexer:        spdInformer.Informer().GetIndexer(),
		podIndexer:        podInformer.Informer().GetIndexer(),
		podLister:         podInformer.Lister(),
		cncLister:         cncInformer.Lister(),
		spdLister:         spdInformer.Lister(),
		workloadGVKLister: workloadGVKLister,
		workloadLister:    workloadLister,
		cncSyncQueue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "spd-cnc"),
		metricsEmitter:    metricsEmitter,
	}

	// if cnc cache is disabled all the event handler is not need,
	// and it will clear all cnc spd config
	if !c.conf.EnableCNCCache {
		return c, nil
	}
	general.Infof("cnc cache is enable")

	// build index: node ---> pod
	err := native.AddNodeNameIndexerForPod(podInformer)
	if err != nil {
		return nil, fmt.Errorf("failed to add node name index for pod: %v", err)
	}

	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addPod,
		UpdateFunc: c.updatePod,
	})

	cncInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addCNC,
		UpdateFunc: c.updateCNC,
	})

	spdInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addSPD,
		UpdateFunc: c.updateSPD,
	})

	return c, nil
}

func (c *cncCacheController) Run() {
	defer c.cncSyncQueue.ShutDown()

	if c.conf.EnableCNCCache {
		for i := 0; i < cncWorkerCount; i++ {
			go wait.Until(c.cncWorker, time.Second, c.ctx.Done())
		}
	}

	go wait.Until(c.clearUnusedConfig, time.Hour*1, c.ctx.Done())

	<-c.ctx.Done()
}

func (c *cncCacheController) cncWorker() {
	for c.processNextCNC() {
	}
}

func (c *cncCacheController) processNextCNC() bool {
	key, quit := c.cncSyncQueue.Get()
	if quit {
		return false
	}
	defer c.cncSyncQueue.Done(key)

	err := c.syncCNC(key.(string))
	if err == nil {
		c.cncSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	c.cncSyncQueue.AddRateLimited(key)

	return true
}

func (c *cncCacheController) syncCNC(key string) error {
	klog.V(5).Infof("[spd] syncing cnc [%v]", key)
	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		klog.V(5).Infof("[spd] finished sync cnc %q (%v)", key, costs)
		_ = c.metricsEmitter.StoreInt64(metricsNameSyncCNCCacheCost, costs.Microseconds(),
			metrics.MetricTypeNameRaw, metrics.MetricTag{Key: "name", Val: key})
	}()

	cnc, err := c.cncLister.Get(key)
	if err != nil {
		general.Errorf("failed to get cnc [%v]", key)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	spdMap, err := c.getSPDMapForCNC(cnc)
	if err != nil {
		return err
	}

	setCNC := func(cnc *configapis.CustomNodeConfig) {
		for _, spd := range spdMap {
			applySPDTargetConfigToCNC(cnc, spd)
		}

		sort.SliceStable(cnc.Status.ServiceProfileConfigList, func(i, j int) bool {
			if cnc.Status.ServiceProfileConfigList[i].ConfigNamespace == cnc.Status.ServiceProfileConfigList[j].ConfigNamespace {
				return cnc.Status.ServiceProfileConfigList[i].ConfigName < cnc.Status.ServiceProfileConfigList[j].ConfigName
			}
			return cnc.Status.ServiceProfileConfigList[i].ConfigNamespace < cnc.Status.ServiceProfileConfigList[j].ConfigNamespace
		})
	}

	_, err = c.patchCNC(cnc, setCNC)
	if err != nil {
		return err
	}

	return nil
}

func (c *cncCacheController) clearUnusedConfig() {
	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		general.Infof("finished (%v)", costs)
		_ = c.metricsEmitter.StoreInt64(metricsNameClearUnusedCNCCacheCost, costs.Microseconds(),
			metrics.MetricTypeNameRaw)
	}()

	cncList, err := c.cncLister.List(labels.Everything())
	if err != nil {
		general.Errorf("clear unused config list all custom node config failed")
		return
	}

	// func for clear cnc config if spd config not exists or cnc cache is disabled
	setFunc := func(cnc *configapis.CustomNodeConfig) {
		spdMap := make(map[string]*apiworkload.ServiceProfileDescriptor)
		// if disable cnc cache, it will clear all cnc spd configs
		if c.conf.EnableCNCCache {
			spdMap, err = c.getSPDMapForCNC(cnc)
			if err != nil {
				general.Errorf("get spd map for cnc %s failed, %v", cnc.Name, err)
				return
			}
		}

		cnc.Status.ServiceProfileConfigList = util.RemoveUnusedTargetConfig(cnc.Status.ServiceProfileConfigList,
			func(config configapis.TargetConfig) bool {
				spdKey := native.GenerateNamespaceNameKey(config.ConfigNamespace, config.ConfigName)
				if _, ok := spdMap[spdKey]; !ok {
					return true
				}
				return false
			})
	}

	clearCNCConfigs := func(i int) {
		cnc := cncList[i]
		_, err = c.patchCNC(cnc, setFunc)
		if err != nil {
			general.Errorf("patch cnc %s failed", cnc.GetName())
			return
		}
	}

	// parallelize to clear cnc configs
	workqueue.ParallelizeUntil(c.ctx, 16, len(cncList), clearCNCConfigs)
}

func (c *cncCacheController) addPod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		general.Errorf("cannot convert obj to *core.Pod")
		return
	}

	c.enqueueCNCForPod(pod)
}

func (c *cncCacheController) updatePod(oldObj interface{}, newObj interface{}) {
	oldPod, ok := oldObj.(*v1.Pod)
	if !ok {
		general.Errorf("cannot convert obj to *core.Pod")
		return
	}

	newPod, ok := newObj.(*v1.Pod)
	if !ok {
		general.Errorf("cannot convert obj to *core.Pod")
		return
	}

	if oldPod.Spec.NodeName == "" && newPod.Spec.NodeName != "" {
		c.enqueueCNCForPod(newPod)
	}
}

func (c *cncCacheController) addSPD(obj interface{}) {
	spd, ok := obj.(*apiworkload.ServiceProfileDescriptor)
	if !ok {
		general.Errorf("cannot convert obj to *apiworkload.ServiceProfileDescriptor")
		return
	}
	c.enqueueCNCForSPD(spd)
}

func (c *cncCacheController) updateSPD(oldObj, newObj interface{}) {
	oldSPD, ok := oldObj.(*apiworkload.ServiceProfileDescriptor)
	if !ok {
		general.Errorf("cannot convert obj to *apiworkload.ServiceProfileDescriptor")
		return
	}

	newSPD, ok := newObj.(*apiworkload.ServiceProfileDescriptor)
	if !ok {
		general.Errorf("cannot convert obj to *apiworkload.ServiceProfileDescriptor")
		return
	}

	if util.GetSPDHash(oldSPD) != util.GetSPDHash(newSPD) {
		c.enqueueCNCForSPD(newSPD)
	}
}

func (c *cncCacheController) addCNC(obj interface{}) {
	cnc, ok := obj.(*configapis.CustomNodeConfig)
	if !ok {
		general.Errorf("cannot convert obj to *configapis.CustomNodeConfig")
		return
	}

	c.enqueueCNC(cnc)
}

func (c *cncCacheController) updateCNC(oldObj interface{}, newObj interface{}) {
	oldCNC, ok := oldObj.(*configapis.CustomNodeConfig)
	if !ok {
		general.Errorf("cannot convert obj to *configapis.CustomNodeConfig")
		return
	}

	newCNC, ok := newObj.(*configapis.CustomNodeConfig)
	if !ok {
		general.Errorf("cannot convert obj to *configapis.CustomNodeConfig")
		return
	}

	if !apiequality.Semantic.DeepEqual(oldCNC.Status.ServiceProfileConfigList,
		newCNC.Status.ServiceProfileConfigList) {
		c.enqueueCNC(newCNC)
	}
}

func (c *cncCacheController) enqueueCNCForSPD(spd *apiworkload.ServiceProfileDescriptor) {
	if util.GetSPDHash(spd) == "" {
		return
	}

	podList, err := util.GetPodListForSPD(spd, c.podIndexer, c.conf.SPDPodLabelIndexerKeys,
		c.workloadLister, c.podLister)
	if err != nil {
		return
	}

	for _, pod := range podList {
		if pod == nil {
			continue
		}

		c.enqueueCNCForPod(pod)
	}
}

func (c *cncCacheController) enqueueCNCForPod(pod *v1.Pod) {
	if pod.Spec.NodeName == "" {
		return
	}

	cnc, err := c.cncLister.Get(pod.Spec.NodeName)
	if err != nil {
		return
	}

	c.enqueueCNC(cnc)
}

func (c *cncCacheController) enqueueCNC(cnc *configapis.CustomNodeConfig) {
	if cnc == nil {
		general.Warningf("trying to enqueue a nil cnc")
		return
	}

	c.cncSyncQueue.Add(cnc.Name)
}

func (c *cncCacheController) getSPDMapForCNC(cnc *configapis.CustomNodeConfig) (map[string]*apiworkload.ServiceProfileDescriptor, error) {
	podList, err := native.GetPodsAssignedToNode(cnc.Name, c.podIndexer)
	if err != nil {
		return nil, err
	}

	spdMap := make(map[string]*apiworkload.ServiceProfileDescriptor)
	for _, pod := range podList {
		if native.PodIsTerminated(pod) {
			continue
		}

		spd, err := util.GetSPDForPod(pod, c.spdIndexer, c.workloadGVKLister, c.spdLister, false)
		if err != nil && !errors.IsNotFound(err) {
			return nil, err
		}

		if spd == nil {
			continue
		}

		spdKey := native.GenerateUniqObjectNameKey(spd)
		spdMap[spdKey] = spd
	}

	return spdMap, nil
}

func (c *cncCacheController) patchCNC(cnc *configapis.CustomNodeConfig, setFunc func(*configapis.CustomNodeConfig)) (*configapis.CustomNodeConfig, error) {
	cncCopy := cnc.DeepCopy()
	setFunc(cncCopy)
	if apiequality.Semantic.DeepEqual(cnc, cncCopy) {
		return cnc, nil
	}

	general.Infof("cnc %s config changed need to patch", cnc.GetName())
	return c.cncControl.PatchCNCStatus(c.ctx, cnc.Name, cnc, cncCopy)
}

func applySPDTargetConfigToCNC(cnc *configapis.CustomNodeConfig,
	spd *apiworkload.ServiceProfileDescriptor,
) {
	if cnc == nil || spd == nil {
		return
	}

	idx := 0
	serviceProfileConfigList := cnc.Status.ServiceProfileConfigList
	// find target config
	for ; idx < len(serviceProfileConfigList); idx++ {
		if serviceProfileConfigList[idx].ConfigNamespace == spd.Namespace &&
			serviceProfileConfigList[idx].ConfigName == spd.Name {
			break
		}
	}

	targetConfig := configapis.TargetConfig{
		ConfigNamespace: spd.Namespace,
		ConfigName:      spd.Name,
		Hash:            util.GetSPDHash(spd),
	}

	// update target config if the spd config is already existed
	if idx < len(serviceProfileConfigList) {
		serviceProfileConfigList[idx] = targetConfig
	} else {
		serviceProfileConfigList = append(serviceProfileConfigList, targetConfig)
		cnc.Status.ServiceProfileConfigList = serviceProfileConfigList
	}
}
