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

package lifecycle

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	configinformers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/config/v1alpha1"
	configlisters "github.com/kubewharf/katalyst-api/pkg/client/listers/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	cncLifecycleControllerName = "cnc-lifecycle"
	cncLifeCycleWorkerCount    = 1
)

const (
	clearCNCPeriod = 30 * time.Second
)

type CNCLifecycle struct {
	ctx context.Context

	client     *client.GenericClientSet
	cncControl control.CNCControl

	nodeListerSynced cache.InformerSynced
	nodeLister       corelisters.NodeLister
	cncListerSynced  cache.InformerSynced
	cncLister        configlisters.CustomNodeConfigLister

	//queue for node
	syncQueue workqueue.RateLimitingInterface

	// metricsEmitter for emit metrics
	metricsEmitter metrics.MetricEmitter
}

func NewCNCLifecycle(ctx context.Context,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	_ *controller.CNCLifecycleConfig,
	client *client.GenericClientSet,
	nodeInformer coreinformers.NodeInformer,
	cncInformer configinformers.CustomNodeConfigInformer,
	metricsEmitter metrics.MetricEmitter) (*CNCLifecycle, error) {

	cncLifecycle := &CNCLifecycle{
		ctx:    ctx,
		client: client,
		syncQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(),
			cncLifecycleControllerName),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cncLifecycle.addNodeEventHandle,
		UpdateFunc: cncLifecycle.updateNodeEventHandle,
	})
	cncLifecycle.nodeListerSynced = nodeInformer.Informer().HasSynced
	cncLifecycle.nodeLister = nodeInformer.Lister()

	cncInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cncLifecycle.addCNCEventHandle,
		UpdateFunc: cncLifecycle.updateCNCEventHandle,
		DeleteFunc: cncLifecycle.deleteCNCEventHandle,
	})
	cncLifecycle.cncListerSynced = cncInformer.Informer().HasSynced
	cncLifecycle.cncLister = cncInformer.Lister()

	if metricsEmitter == nil {
		cncLifecycle.metricsEmitter = metrics.DummyMetrics{}
	} else {
		cncLifecycle.metricsEmitter = metricsEmitter.WithTags(cncLifecycleControllerName)
	}

	cncLifecycle.cncControl = control.DummyCNCControl{}
	if !genericConf.DryRun {
		cncLifecycle.cncControl = control.NewRealCNCControl(client.InternalClient)
	}

	return cncLifecycle, nil
}

func (cl *CNCLifecycle) Run() {
	defer utilruntime.HandleCrash()
	defer cl.syncQueue.ShutDown()

	defer klog.Infof("Shutting down %s controller", cncLifecycleControllerName)

	if !cache.WaitForCacheSync(cl.ctx.Done(), cl.nodeListerSynced, cl.cncListerSynced) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", cncLifecycleControllerName))
		return
	}
	klog.Infof("Caches are synced for %s controller", cncLifecycleControllerName)
	klog.Infof("start %d workers for %s controller", cncLifeCycleWorkerCount, cncLifecycleControllerName)

	go wait.Until(cl.clearUnexpectedCNC, clearCNCPeriod, cl.ctx.Done())
	for i := 0; i < cncLifeCycleWorkerCount; i++ {
		go wait.Until(cl.worker, time.Second, cl.ctx.Done())
	}

	<-cl.ctx.Done()
}

func (cl *CNCLifecycle) addNodeEventHandle(obj interface{}) {
	n, ok := obj.(*corev1.Node)
	if !ok {
		klog.Errorf("cannot convert obj to *corev1.Node: %v", obj)
		return
	}
	klog.V(4).Infof("notice addition of Node %s", n.Name)
	cl.enqueueWorkItem(n)
}

func (cl *CNCLifecycle) updateNodeEventHandle(old, cur interface{}) {
	oldNode, ok := old.(*corev1.Node)
	if !ok {
		klog.Errorf("cannot convert oldObj to *corev1.Node: %v", old)
		return
	}

	curNode, ok := cur.(*corev1.Node)
	if !ok {
		klog.Errorf("cannot convert curObj to *corev1.Node: %v", cur)
		return
	}

	if curNode.Labels == nil {
		return
	}

	if !general.CheckMapEqual(oldNode.Labels, curNode.Labels) {
		cl.enqueueWorkItem(curNode)
	}
}

func (cl *CNCLifecycle) addCNCEventHandle(obj interface{}) {
	c, ok := obj.(*apis.CustomNodeConfig)
	if !ok {
		klog.Errorf("cannot convert obj to *apis.CustomNodeConfig: %v", obj)
		return
	}
	klog.V(4).Infof("notice addition of cnc %s", c.Name)

	cl.enqueueWorkItem(obj)
}

func (cl *CNCLifecycle) updateCNCEventHandle(_, new interface{}) {
	c, ok := new.(*apis.CustomNodeConfig)
	if !ok {
		klog.Errorf("cannot convert oldObj to *apis.CustomNodeConfig: %v", c)
		return
	}
	klog.V(4).Infof("notice addition of cnc %s", c.Name)

	cl.enqueueWorkItem(new)
}

func (cl *CNCLifecycle) deleteCNCEventHandle(obj interface{}) {
	c, ok := obj.(*apis.CustomNodeConfig)
	if !ok {
		klog.Errorf("cannot convert oldObj to *apis.CNC: %v", c)
		return
	}
	klog.V(4).Infof("notice addition of cnc %s", c.Name)

	cl.enqueueWorkItem(obj)
}

func (cl *CNCLifecycle) worker() {
	for cl.processNextWorkItem() {
	}
}

// processNextWorkItem dequeues items, processes them, and marks them done.
// It enforces that the sync is never invoked concurrently with the same key.
func (cl *CNCLifecycle) processNextWorkItem() bool {
	key, quit := cl.syncQueue.Get()
	if quit {
		return false
	}
	defer cl.syncQueue.Done(key)

	err := cl.sync(key.(string))
	if err == nil {
		cl.syncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	cl.syncQueue.AddRateLimited(key)

	return true
}

// enqueueWorkItem enqueues the given node in the work queue.
func (cl *CNCLifecycle) enqueueWorkItem(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Cound't get key for object %+v: %v", obj, err))
		return
	}
	cl.syncQueue.Add(key)
}

// sync syncs the given node.
func (cl *CNCLifecycle) sync(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	node, err := cl.nodeLister.Get(name)
	if errors.IsNotFound(err) {
		klog.Infof("node has been deleted %v", key)
		return nil
	}
	if err != nil {
		return err
	}

	err = cl.updateOrCreateCNC(node)
	if err != nil {
		return err
	}

	return nil
}

// clearUnexpectedCNC is used to clear unexpected cnc
// for instance, orphaned cnc due to unexpected node deletion options or manually creation
func (cl *CNCLifecycle) clearUnexpectedCNC() {
	targetCNCSelector := labels.Everything()
	cncs, err := cl.cncLister.List(targetCNCSelector)
	if err != nil {
		klog.Errorf("failed to list all cnc")
		return
	}

	for _, cnc := range cncs {
		_, err := cl.nodeLister.Get(cnc.Name)
		if errors.IsNotFound(err) {
			// double check if this node is deleted
			_, nErr := cl.client.KubeClient.CoreV1().Nodes().Get(cl.ctx, cnc.Name, metav1.GetOptions{ResourceVersion: "0"})
			if !errors.IsNotFound(nErr) {
				continue
			}

			if dErr := cl.cncControl.DeleteCNC(cl.ctx, cnc.Name, metav1.DeleteOptions{}); dErr != nil {
				klog.Errorf("delete unexpected cnc %s failed: %v", cnc.Name, dErr)
			}
			continue
		} else if err != nil {
			klog.Errorf("get node for CNC %v failed in clear: %v", cnc.Name, err)
			continue
		}
	}
}

func (cl *CNCLifecycle) updateOrCreateCNC(node *corev1.Node) error {
	cnc, err := cl.cncLister.Get(node.Name)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get cnc from lister %s: %v", node.Name, err)
	}
	if errors.IsNotFound(err) {
		cnc = &apis.CustomNodeConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:   node.Name,
				Labels: node.Labels,
			},
		}

		setCNCOwnerReference(cnc, node)
		_, err = cl.cncControl.CreateCNC(cl.ctx, cnc, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create cnc %s: %v", cnc.Name, err)
		}
		if errors.IsAlreadyExists(err) {
			cnc, err = cl.client.InternalClient.ConfigV1alpha1().CustomNodeConfigs().Get(cl.ctx, node.Name, metav1.GetOptions{ResourceVersion: "0"})
			if err != nil {
				return fmt.Errorf("failed to get cnc from apiserver %s: %v", node.Name, err)
			}
		}
	}

	newCNC := cnc.DeepCopy()
	newCNC.Labels = node.Labels
	setCNCOwnerReference(newCNC, node)
	if apiequality.Semantic.DeepEqual(newCNC, cnc) {
		return nil
	}

	_, err = cl.cncControl.PatchCNC(cl.ctx, cnc.Name, cnc, newCNC)
	return err
}

func setCNCOwnerReference(cnc *apis.CustomNodeConfig, node *corev1.Node) {
	if cnc == nil || node == nil {
		return
	}

	blocker := true
	cnc.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion:         "v1",
			Kind:               "Node",
			Name:               node.Name,
			UID:                node.GetUID(),
			Controller:         &blocker,
			BlockOwnerDeletion: &blocker,
		},
	}
}
