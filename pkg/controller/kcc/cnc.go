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
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apisv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	configinformers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/listers/config/v1alpha1"
	kcclient "github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	kcctarget "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
	"github.com/kubewharf/katalyst-core/pkg/controller/kcc/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	cncControllerName = "cnc"
)

const (
	cncWorkerCount = 16
)

type CustomNodeConfigController struct {
	ctx       context.Context
	dryRun    bool
	kccConfig *controller.KCCConfig

	client              *kcclient.GenericClientSet
	cncControl          control.CNCControl
	unstructuredControl control.UnstructuredControl

	// customNodeConfigLister can list/get CustomNodeConfig from the shared informer's store
	customNodeConfigLister v1alpha1.CustomNodeConfigLister
	// customNodeConfigSyncQueue queue for CustomNodeConfig
	customNodeConfigSyncQueue workqueue.RateLimitingInterface

	syncedFunc []cache.InformerSynced

	// targetHandler store gvr of kcc and gvr
	targetHandler *kcctarget.KatalystCustomConfigTargetHandler

	// metricsEmitter for emit metrics
	metricsEmitter metrics.MetricEmitter
}

func NewCustomNodeConfigController(
	ctx context.Context,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	kccConfig *controller.KCCConfig,
	client *kcclient.GenericClientSet,
	customNodeConfigInformer configinformers.CustomNodeConfigInformer,
	metricsEmitter metrics.MetricEmitter,
	targetHandler *kcctarget.KatalystCustomConfigTargetHandler,
) (*CustomNodeConfigController, error) {
	c := &CustomNodeConfigController{
		ctx:                       ctx,
		client:                    client,
		dryRun:                    genericConf.DryRun,
		kccConfig:                 kccConfig,
		customNodeConfigLister:    customNodeConfigInformer.Lister(),
		targetHandler:             targetHandler,
		customNodeConfigSyncQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), cncControllerName),
		syncedFunc: []cache.InformerSynced{
			customNodeConfigInformer.Informer().HasSynced,
		},
	}

	customNodeConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addCustomNodeConfigEventHandle,
		UpdateFunc: c.updateCustomNodeConfigEventHandle,
	})

	if metricsEmitter == nil {
		c.metricsEmitter = metrics.DummyMetrics{}
	} else {
		c.metricsEmitter = metricsEmitter.WithTags(cncControllerName)
	}

	c.cncControl = control.DummyCNCControl{}
	c.unstructuredControl = control.DummyUnstructuredControl{}
	if !c.dryRun {
		c.cncControl = control.NewRealCNCControl(client.InternalClient)
		c.unstructuredControl = control.NewRealUnstructuredControl(client.DynamicClient)
	}

	// register kcc-target informer handler
	targetHandler.RegisterTargetHandler(cncControllerName, c.katalystCustomConfigTargetHandler)
	return c, nil
}

func (c *CustomNodeConfigController) Run() {
	defer utilruntime.HandleCrash()
	defer func() {
		c.customNodeConfigSyncQueue.ShutDown()
	}()

	defer klog.Infof("shutting down %s controller", cncControllerName)

	if !cache.WaitForCacheSync(c.ctx.Done(), c.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", cncControllerName))
		return
	}
	klog.Infof("caches are synced for %s controller", cncControllerName)
	klog.Infof("start %d workers for %s controller", cncWorkerCount, cncControllerName)

	for i := 0; i < cncWorkerCount; i++ {
		go wait.Until(c.worker, 10*time.Millisecond, c.ctx.Done())
	}
	go wait.Until(c.clearUnusedConfig, 5*time.Minute, c.ctx.Done())

	<-c.ctx.Done()
}

// katalystCustomConfigTargetHandler process object of kcc target type from targetAccessor, and
// KatalystCustomConfigTargetAccessor will call this handler when some update event on target is added.
func (c *CustomNodeConfigController) katalystCustomConfigTargetHandler(gvr metav1.GroupVersionResource, target *unstructured.Unstructured) error {
	for _, syncFunc := range c.syncedFunc {
		if !syncFunc() {
			return fmt.Errorf("informer has not synced")
		}
	}

	targetAccessor, ok := c.targetHandler.GetTargetAccessorByGVR(gvr)
	if !ok || targetAccessor == nil {
		return fmt.Errorf("%s cnc target accessor not found", gvr)
	}

	klog.V(4).Infof("[cnc] gvr: %s, target: %s updated", gvr.String(), native.GenerateUniqObjectNameKey(target))

	if target.GetDeletionTimestamp() != nil {
		err := c.handleKCCTargetFinalizer(gvr, target)
		if err != nil {
			return err
		}
		return nil
	}

	target, err := util.EnsureKCCTargetFinalizer(c.ctx, c.unstructuredControl, consts.KatalystCustomConfigTargetFinalizerCNC, gvr, target)
	if err != nil {
		return err
	}

	err = c.enqueueAllRelatedCNCForTargetConfig(target)
	if err != nil {
		return err
	}

	return nil
}

func (c *CustomNodeConfigController) handleKCCTargetFinalizer(gvr metav1.GroupVersionResource, target *unstructured.Unstructured) error {
	if !controllerutil.ContainsFinalizer(target, consts.KatalystCustomConfigTargetFinalizerCNC) {
		return nil
	}

	klog.Infof("[cnc] handling gvr: %s kcc target %s finalizer", gvr.String(), native.GenerateUniqObjectNameKey(target))
	err := c.enqueueAllRelatedCNCForTargetConfig(target)
	if err != nil {
		return err
	}

	err = util.RemoveKCCTargetFinalizer(c.ctx, c.unstructuredControl, consts.KatalystCustomConfigTargetFinalizerCNC, gvr, target)
	if err != nil {
		return err
	}

	klog.Infof("[cnc] success remove gvr: %s kcc target %s finalizer", gvr.String(), native.GenerateUniqObjectNameKey(target))
	return nil
}

func (c *CustomNodeConfigController) enqueueAllRelatedCNCForTargetConfig(target *unstructured.Unstructured) error {
	relatedCNCs, err := util.GetRelatedCNCForTargetConfig(c.customNodeConfigLister, target)
	if err != nil {
		return err
	}

	for _, cnc := range relatedCNCs {
		c.enqueueCustomNodeConfig(cnc)
	}

	return nil
}

func (c *CustomNodeConfigController) addCustomNodeConfigEventHandle(obj interface{}) {
	t, ok := obj.(*apisv1alpha1.CustomNodeConfig)
	if !ok {
		klog.Errorf("[cnc] cannot convert obj to *CustomNodeConfig: %v", obj)
		return
	}

	klog.V(4).Infof("[cnc] notice addition of CustomNodeConfig %s", native.GenerateUniqObjectNameKey(t))
	c.enqueueCustomNodeConfig(t)
}

func (c *CustomNodeConfigController) updateCustomNodeConfigEventHandle(_, new interface{}) {
	newCNC, ok := new.(*apisv1alpha1.CustomNodeConfig)
	if !ok {
		klog.Errorf("[cnc] cannot convert obj to *CustomNodeConfig: %v", new)
		return
	}

	klog.V(4).Infof("[cnc] notice update of CustomNodeConfig %s", native.GenerateUniqObjectNameKey(newCNC))
	c.enqueueCustomNodeConfig(newCNC)
}

func (c *CustomNodeConfigController) enqueueCustomNodeConfig(cnc *apisv1alpha1.CustomNodeConfig) {
	if cnc == nil {
		klog.Warning("[cnc] trying to enqueue a nil cnc")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cnc)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", cnc, err))
		return
	}

	c.customNodeConfigSyncQueue.Add(key)
}

func (c *CustomNodeConfigController) worker() {
	for c.processNextKatalystCustomConfigWorkItem() {
	}
}

func (c *CustomNodeConfigController) processNextKatalystCustomConfigWorkItem() bool {
	key, quit := c.customNodeConfigSyncQueue.Get()
	if quit {
		return false
	}
	defer c.customNodeConfigSyncQueue.Done(key)

	err := c.syncCustomNodeConfig(key.(string))
	if err == nil {
		c.customNodeConfigSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync kcc %q failed with %v", key, err))
	c.customNodeConfigSyncQueue.AddRateLimited(key)

	return true
}

func (c *CustomNodeConfigController) syncCustomNodeConfig(key string) error {
	klog.V(5).Infof("[cnc] processing cnc key %s", key)
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[cnc] failed to split namespace and name from key %s", key)
		return err
	}

	cnc, err := c.customNodeConfigLister.Get(name)
	if apierrors.IsNotFound(err) {
		klog.Warningf("[cnc] cnc %s is not found", key)
		return nil
	} else if err != nil {
		klog.Errorf("[cnc] cnc %s get error: %v", key, err)
		return err
	}

	_, err = c.patchCNC(cnc, c.updateCustomNodeConfig)
	if err != nil {
		return err
	}

	return nil
}

func (c *CustomNodeConfigController) patchCNC(cnc *apisv1alpha1.CustomNodeConfig, setFunc func(*apisv1alpha1.CustomNodeConfig)) (*apisv1alpha1.CustomNodeConfig, error) {
	cncCopy := cnc.DeepCopy()
	setFunc(cncCopy)
	if apiequality.Semantic.DeepEqual(cnc, cncCopy) {
		return cnc, nil
	}
	klog.Infof("[cnc] cnc %s config changed need to patch", cnc.GetName())
	return c.cncControl.PatchCNCStatus(c.ctx, cnc.Name, cnc, cncCopy)
}

func (c *CustomNodeConfigController) updateCustomNodeConfig(cnc *apisv1alpha1.CustomNodeConfig) {
	c.targetHandler.RangeGVRTargetAccessor(func(gvr metav1.GroupVersionResource, targetAccessor kcctarget.KatalystCustomConfigTargetAccessor) bool {
		matchedTarget, err := util.FindMatchedKCCTargetConfigForNode(cnc, targetAccessor)
		if err != nil {
			klog.Errorf("[cnc] gvr %s find matched target failed: %s", gvr, err)
			return true
		}

		util.ApplyKCCTargetConfigToCNC(cnc, gvr, matchedTarget)
		return true
	})
}

func (c *CustomNodeConfigController) clearUnusedConfig() {
	cncList, err := c.customNodeConfigLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("[cnc] clear unused config list all custom node config failed")
		return
	}

	// save all gvr to map
	configGVRSet := make(map[string]metav1.GroupVersionResource)
	c.targetHandler.RangeGVRTargetAccessor(func(gvr metav1.GroupVersionResource, _ kcctarget.KatalystCustomConfigTargetAccessor) bool {
		configGVRSet[gvr.String()] = gvr
		return true
	})

	needToDeleteFunc := func(gvr metav1.GroupVersionResource) bool {
		if _, ok := configGVRSet[gvr.String()]; !ok {
			return true
		}
		return false
	}

	// func for clear cnc config if gvr config not exists
	setFunc := func(cnc *apisv1alpha1.CustomNodeConfig) {
		util.ClearUnNeededConfigForNode(cnc, needToDeleteFunc)
	}

	clearCNCConfigs := func(i int) {
		cnc := cncList[i]
		_, err = c.patchCNC(cnc, setFunc)
		if err != nil {
			klog.Errorf("[cnc] clearUnusedConfig patch cnc %s failed", cnc.GetName())
			return
		}
	}

	// parallelize to clear cnc configs
	workqueue.ParallelizeUntil(c.ctx, 16, len(cncList), clearCNCConfigs)
}
