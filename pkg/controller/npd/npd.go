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

package npd

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	npdlisters "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	indicator_plugin "github.com/kubewharf/katalyst-core/pkg/controller/npd/indicator-plugin"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const npdControllerName = "npd"

type NPDController struct {
	ctx context.Context

	conf *controller.NPDConfig

	npdControl control.NodeProfileControl
	npdLister  npdlisters.NodeProfileDescriptorLister
	nodeLister corev1.NodeLister

	indicatorManager    indicator_plugin.IndicatorGetter
	indicatorPlugins    map[string]indicator_plugin.IndicatorPlugin
	supportedNodeScopes map[string]struct{}
	supportedPodScopes  map[string]struct{}

	nodeQueue workqueue.RateLimitingInterface

	metricsEmitter metrics.MetricEmitter
	syncedFunc     []cache.InformerSynced
}

func NewNPDController(
	ctx context.Context,
	controlCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	conf *controller.NPDConfig,
	extraConf interface{},
) (*NPDController, error) {
	nodeInformer := controlCtx.KubeInformerFactory.Core().V1().Nodes()
	npdInformer := controlCtx.InternalInformerFactory.Node().V1alpha1().NodeProfileDescriptors()

	npdController := &NPDController{
		ctx:            ctx,
		conf:           conf,
		npdControl:     &control.DummyNPDControl{},
		nodeQueue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "npd"),
		metricsEmitter: controlCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(npdControllerName),
	}

	npdController.nodeLister = nodeInformer.Lister()
	npdController.syncedFunc = append(npdController.syncedFunc, nodeInformer.Informer().HasSynced)

	npdController.npdLister = npdInformer.Lister()
	npdController.syncedFunc = append(npdController.syncedFunc, npdInformer.Informer().HasSynced)

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    npdController.onNodeAdd,
		UpdateFunc: npdController.onNodeUpdate,
		DeleteFunc: npdController.onNodeDelete,
	})

	if !genericConf.DryRun {
		npdController.npdControl = control.NewNPDControlImp(controlCtx.Client.InternalClient)
	}

	if err := npdController.initializeIndicatorPlugins(controlCtx, extraConf); err != nil {
		return nil, err
	}

	return npdController, nil
}

func (nc *NPDController) Run() {
	defer utilruntime.HandleCrash()
	defer nc.nodeQueue.ShutDown()
	defer klog.Infof("shutting down %s controller", npdControllerName)

	if !cache.WaitForCacheSync(nc.ctx.Done(), nc.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", npdControllerName))
		return
	}
	klog.Infof("caches are synced for %s controller", npdControllerName)

	go wait.Until(nc.nodeWorker, time.Second, nc.ctx.Done())

	for _, plugin := range nc.indicatorPlugins {
		go plugin.Run()
	}
	for i := 0; i < nc.conf.SyncWorkers; i++ {
		go wait.Until(nc.syncIndicatorStatus, time.Second, nc.ctx.Done())
	}

	go wait.Until(nc.cleanNPD, time.Hour, nc.ctx.Done())

	<-nc.ctx.Done()
}

func (nc *NPDController) initializeIndicatorPlugins(controlCtx *katalystbase.GenericContext, extraConf interface{}) error {
	indicatorManager := indicator_plugin.NewIndicatorManager()
	nc.indicatorManager = indicatorManager
	nc.indicatorPlugins = make(map[string]indicator_plugin.IndicatorPlugin)
	nc.supportedNodeScopes = make(map[string]struct{})
	nc.supportedPodScopes = make(map[string]struct{})

	initializers := indicator_plugin.GetPluginInitializers()
	for _, pluginName := range nc.conf.NPDIndicatorPlugins {
		if initFunc, ok := initializers[pluginName]; ok {
			plugin, err := initFunc(nc.ctx, nc.conf, extraConf, controlCtx, indicatorManager)
			if err != nil {
				return err
			}

			klog.Infof("[npd] init indicator plugin: %v", pluginName)
			nc.indicatorPlugins[pluginName] = plugin

			for _, scope := range plugin.GetSupportedNodeMetricsScope() {
				if _, ok := nc.supportedNodeScopes[scope]; !ok {
					nc.supportedNodeScopes[scope] = struct{}{}
				} else {
					if nc.conf.EnableScopeDuplicated {
						klog.Warningf("[npd] node scope %v is supported by multi plugins, metrics might be overwrite", scope)
					} else {
						err := fmt.Errorf("[npd] node scope %v is supported by multi plugins", scope)
						klog.Error(err)
						return err
					}
				}
			}

			for _, scope := range plugin.GetSupportedPodMetricsScope() {
				if _, ok := nc.supportedPodScopes[scope]; !ok {
					nc.supportedPodScopes[scope] = struct{}{}
				} else {
					if nc.conf.EnableScopeDuplicated {
						klog.Warningf("[npd] pod scope %v is supported by multi plugins, metrics might be overwrite", scope)
					} else {
						err := fmt.Errorf("[npd] pod scope %v is supported by multi plugins", scope)
						klog.Error(err)
						return err
					}
				}
			}
		}
	}

	return nil
}

func (nc *NPDController) nodeWorker() {
	for nc.processNextNode() {
	}
}

func (nc *NPDController) processNextNode() bool {
	key, quit := nc.nodeQueue.Get()
	if quit {
		return false
	}
	defer nc.nodeQueue.Done(key)

	err := nc.syncNode(key.(string))
	if err == nil {
		nc.nodeQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %v fail with %v", key, err))
	nc.nodeQueue.AddRateLimited(key)

	return true
}

func (nc *NPDController) syncNode(key string) error {
	_, nodeName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("[npd] faild to split key %v: %v", key, err)
		return err
	}

	_, err = nc.getOrCreateNPDForNode(nodeName)
	if err != nil {
		klog.Errorf("getOrCreateNPDForNode %v fail: %v", nodeName, err)
		return err
	}

	return nil
}

func (nc *NPDController) getOrCreateNPDForNode(nodeName string) (*v1alpha1.NodeProfileDescriptor, error) {
	npd, err := nc.npdLister.Get(nodeName)
	if err == nil {
		return npd, nil
	}

	if errors.IsNotFound(err) {
		npd := &v1alpha1.NodeProfileDescriptor{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
			},
			Spec: v1alpha1.NodeProfileDescriptorSpec{},
			Status: v1alpha1.NodeProfileDescriptorStatus{
				NodeMetrics: []v1alpha1.ScopedNodeMetrics{},
				PodMetrics:  []v1alpha1.ScopedPodMetrics{},
			},
		}
		return nc.npdControl.CreateNPD(nc.ctx, npd, metav1.CreateOptions{})
	} else {
		err = fmt.Errorf("get npd %v fail: %v", nodeName, err)
		return nil, err
	}
}

func (nc *NPDController) cleanNPD() {
	npdList, err := nc.npdLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("[npd] failed to list npd")
		return
	}

	for _, npd := range npdList {
		_, err := nc.nodeLister.Get(npd.Name)
		if err == nil {
			continue
		}

		if err != nil {
			if errors.IsNotFound(err) {
				// delete npd
				err = nc.deleteNPD(npd.Name)
				if err != nil {
					klog.Errorf("[npd] delete npd %v fail: %v", npd.Name, err)
				}
			} else {
				klog.Errorf("[npd] get node %v fail: %v", npd.Name, err)
			}
		}
	}
}

func (nc *NPDController) deleteNPD(nodeName string) error {
	return nc.npdControl.DeleteNPD(nc.ctx, nodeName, metav1.DeleteOptions{})
}
