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

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	informers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/node/v1alpha1"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	cnrLifecycleControllerName = "cnr-lifecycle"
	cnrLifeCycleWorkerCount    = 1
)

const (
	clearCNRPeriod = 30 * time.Second
)

type CNRLifecycle struct {
	ctx context.Context

	client     *client.GenericClientSet
	cnrControl control.CNRControl

	nodeListerSynced cache.InformerSynced
	nodeLister       corelisters.NodeLister
	cnrListerSynced  cache.InformerSynced
	cnrLister        listers.CustomNodeResourceLister

	//queue for node
	syncQueue workqueue.RateLimitingInterface

	// metricsEmitter for emit metrics
	metricsEmitter metrics.MetricEmitter
}

func NewCNRLifecycle(ctx context.Context,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	_ *controller.CNRLifecycleConfig,
	client *client.GenericClientSet,
	nodeInformer coreinformers.NodeInformer,
	cnrInformer informers.CustomNodeResourceInformer,
	metricsEmitter metrics.MetricEmitter) (*CNRLifecycle, error) {

	cnrLifecycle := &CNRLifecycle{
		ctx:    ctx,
		client: client,
		syncQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(),
			cnrLifecycleControllerName),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cnrLifecycle.addNodeEventHandle,
		UpdateFunc: cnrLifecycle.updateNodeEventHandle,
	})
	cnrLifecycle.nodeListerSynced = nodeInformer.Informer().HasSynced
	cnrLifecycle.nodeLister = nodeInformer.Lister()

	cnrInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cnrLifecycle.addCNREventHandle,
		UpdateFunc: cnrLifecycle.updateCNREventHandle,
		DeleteFunc: cnrLifecycle.deleteCNREventHandle,
	})
	cnrLifecycle.cnrLister = cnrInformer.Lister()
	cnrLifecycle.cnrListerSynced = cnrInformer.Informer().HasSynced

	if metricsEmitter == nil {
		cnrLifecycle.metricsEmitter = metrics.DummyMetrics{}
	} else {
		cnrLifecycle.metricsEmitter = metricsEmitter.WithTags(cnrLifecycleControllerName)
	}

	cnrLifecycle.cnrControl = control.DummyCNRControl{}
	if !genericConf.DryRun {
		cnrLifecycle.cnrControl = control.NewCNRControlImpl(client.InternalClient)
	}

	return cnrLifecycle, nil
}

func (cl *CNRLifecycle) Run() {
	defer utilruntime.HandleCrash()
	defer cl.syncQueue.ShutDown()

	defer klog.Infof("Shutting down %s controller", cnrLifecycleControllerName)

	if !cache.WaitForCacheSync(cl.ctx.Done(), cl.nodeListerSynced, cl.cnrListerSynced) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", cnrLifecycleControllerName))
		return
	}
	klog.Infof("Caches are synced for %s controller", cnrLifecycleControllerName)
	klog.Infof("start %d workers for %s controller", cnrLifeCycleWorkerCount, cnrLifecycleControllerName)

	go wait.Until(cl.clearUnexpectedCNR, clearCNRPeriod, cl.ctx.Done())
	for i := 0; i < cnrLifeCycleWorkerCount; i++ {
		go wait.Until(cl.worker, time.Second, cl.ctx.Done())
	}

	<-cl.ctx.Done()
}

func (cl *CNRLifecycle) addNodeEventHandle(obj interface{}) {
	n, ok := obj.(*corev1.Node)
	if !ok {
		klog.Errorf("cannot convert obj to *corev1.Node: %v", obj)
		return
	}
	klog.V(4).Infof("notice addition of Node %s", n.Name)
	cl.enqueueWorkItem(n)
}

func (cl *CNRLifecycle) updateNodeEventHandle(old, cur interface{}) {
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

func (cl *CNRLifecycle) addCNREventHandle(obj interface{}) {
	c, ok := obj.(*apis.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert obj to *apis.CNR: %v", obj)
		return
	}
	klog.V(4).Infof("notice addition of cnr %s", c.Name)

	cl.enqueueWorkItem(obj)
}

func (cl *CNRLifecycle) updateCNREventHandle(_, new interface{}) {
	c, ok := new.(*apis.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert oldObj to *apis.CNR: %v", c)
		return
	}
	klog.V(4).Infof("notice addition of cnr %s", c.Name)

	cl.enqueueWorkItem(new)
}

func (cl *CNRLifecycle) deleteCNREventHandle(obj interface{}) {
	c, ok := obj.(*apis.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert oldObj to *apis.CNR: %v", c)
		return
	}
	klog.V(4).Infof("notice addition of cnr %s", c.Name)

	cl.enqueueWorkItem(obj)

}

func (cl *CNRLifecycle) worker() {
	for cl.processNextWorkItem() {
	}
}

// processNextWorkItem dequeues items, processes them, and marks them done.
// It enforces that the sync is never invoked concurrently with the same key.
func (cl *CNRLifecycle) processNextWorkItem() bool {
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
func (cl *CNRLifecycle) enqueueWorkItem(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Cound't get key for object %+v: %v", obj, err))
		return
	}
	cl.syncQueue.Add(key)
}

// sync syncs the given node.
func (cl *CNRLifecycle) sync(key string) error {
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

	err = cl.updateOrCreateCNR(node)
	if err != nil {
		return err
	}

	return nil
}

// clearUnexpectedCNR is used to clear unexpected cnr
// for instance, orphaned cnr due to unexpected node deletion options or manually creation
func (cl *CNRLifecycle) clearUnexpectedCNR() {
	targetCNRSelector := labels.Everything()
	cnrs, err := cl.cnrLister.List(targetCNRSelector)
	if err != nil {
		klog.Errorf("failed to list all cnr")
		return
	}

	for _, cnr := range cnrs {
		_, err := cl.nodeLister.Get(cnr.Name)
		if errors.IsNotFound(err) {
			// double check if this node is deleted
			_, nErr := cl.client.KubeClient.CoreV1().Nodes().Get(cl.ctx, cnr.Name, metav1.GetOptions{ResourceVersion: "0"})
			if !errors.IsNotFound(nErr) {
				continue
			}

			if dErr := cl.cnrControl.DeleteCNR(cl.ctx, cnr.Name); dErr != nil {
				klog.Errorf("delete unexpected cnr %s failed: %v", cnr.Name, dErr)
			}
			continue
		} else if err != nil {
			klog.Errorf("get node for CNR %v failed in clear: %v", cnr.Name, err)
			continue
		}
	}
}

func (cl *CNRLifecycle) updateOrCreateCNR(node *corev1.Node) error {
	cnr, err := cl.cnrLister.Get(node.Name)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get cnr from lister %s: %v", node.Name, err)
	}
	if errors.IsNotFound(err) {
		cnr = &apis.CustomNodeResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:   node.Name,
				Labels: node.Labels,
			},
		}

		setCNROwnerReference(cnr, node)
		_, err = cl.cnrControl.CreateCNR(cl.ctx, cnr)
		if err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create cnr %s: %v", cnr.Name, err)
		}
		if errors.IsAlreadyExists(err) {
			cnr, err = cl.client.InternalClient.NodeV1alpha1().CustomNodeResources().Get(cl.ctx, node.Name, metav1.GetOptions{ResourceVersion: "0"})
			if err != nil {
				return fmt.Errorf("failed to get cnr from apiserver %s: %v", node.Name, err)
			}
		}
	}

	newCNR := cnr.DeepCopy()
	newCNR.Labels = node.Labels
	setCNROwnerReference(newCNR, node)
	if apiequality.Semantic.DeepEqual(newCNR, cnr) {
		return nil
	}

	_, err = cl.cnrControl.PatchCNRSpecAndMetadata(cl.ctx, cnr.Name, cnr, newCNR)
	return err
}

func setCNROwnerReference(cnr *apis.CustomNodeResource, node *corev1.Node) {
	if cnr == nil || node == nil {
		return
	}

	blocker := true
	cnr.OwnerReferences = []metav1.OwnerReference{
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
