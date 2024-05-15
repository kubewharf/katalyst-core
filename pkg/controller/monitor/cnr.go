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

package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/cri-api/pkg/errors"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	informers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/node/v1alpha1"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	cnrMonitorControllerName = "cnr-monitor"
	cnrMonitorWorkerCount    = 1
)

const (
	// maxToleranceLantency is the max tolerance lantency for cnr report lantency
	maxToleranceLantency = 5 * time.Minute
)

type CNRMonitorController struct {
	ctx context.Context

	client *client.GenericClientSet

	cnrListerSynced  cache.InformerSynced
	cnrLister        listers.CustomNodeResourceLister
	nodeListerSynced cache.InformerSynced
	nodeLister       corelisters.NodeLister
	podListerSynced  cache.InformerSynced
	podLister        corelisters.PodLister

	// queue for cnr
	cnrSyncQueue workqueue.RateLimitingInterface

	// metricsEmitter for emit metrics
	metricsEmitter metrics.MetricEmitter

	// podTimeMap for record pod scheduled time
	podTimeMap sync.Map
}

// NewCNRMonitorController create a new CNRMonitorController
func NewCNRMonitorController(
	ctx context.Context,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	_ *controller.CNRMonitorConfig,
	client *client.GenericClientSet,
	nodeInformer coreinformers.NodeInformer,
	podInformer coreinformers.PodInformer,
	cnrInformer informers.CustomNodeResourceInformer,
	metricsEmitter metrics.MetricEmitter,
) (*CNRMonitorController, error) {
	cnrMonitorController := &CNRMonitorController{
		ctx:          ctx,
		client:       client,
		cnrSyncQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), cnrMonitorControllerName),
		podTimeMap:   sync.Map{},
	}

	// init cnr informer
	cnrInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cnrMonitorController.addCNREventHandler,
		UpdateFunc: cnrMonitorController.updateCNREventHandler,
	})
	// init cnr lister
	cnrMonitorController.cnrLister = cnrInformer.Lister()
	// init cnr synced
	cnrMonitorController.cnrListerSynced = cnrInformer.Informer().HasSynced

	// init node lister
	cnrMonitorController.nodeLister = nodeInformer.Lister()
	// init node synced
	cnrMonitorController.nodeListerSynced = nodeInformer.Informer().HasSynced

	// init pod informer
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: cnrMonitorController.updatePodEventHandler,
	})
	// init pod lister
	cnrMonitorController.podLister = podInformer.Lister()
	// init pod synced
	cnrMonitorController.podListerSynced = podInformer.Informer().HasSynced

	if metricsEmitter == nil {
		// if metricsEmitter is nil, use dummy metrics
		cnrMonitorController.metricsEmitter = metrics.DummyMetrics{}
	} else {
		// if metricsEmitter is not nil, use metricsEmitter with tags
		cnrMonitorController.metricsEmitter = metricsEmitter.WithTags(cnrMonitorControllerName)
	}

	return cnrMonitorController, nil
}

func (ctrl *CNRMonitorController) Run() {
	defer utilruntime.HandleCrash()
	defer ctrl.cnrSyncQueue.ShutDown()
	defer klog.Infof("Shutting down %s controller", cnrMonitorControllerName)

	// wait for cnr cache sync
	if !cache.WaitForCacheSync(ctrl.ctx.Done(), ctrl.cnrListerSynced, ctrl.nodeListerSynced, ctrl.podListerSynced) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", cnrMonitorControllerName))
		return
	}

	klog.Infof("Caches are synced for %s controller", cnrMonitorControllerName)
	klog.Infof("start %d workers for %s controller", cnrMonitorWorkerCount, cnrMonitorControllerName)

	for i := 0; i < cnrMonitorWorkerCount; i++ {
		go wait.Until(ctrl.cnrMonitorWorker, time.Second, ctrl.ctx.Done())
	}

	// gc podTimeMap
	klog.Infof("start gc podTimeMap...")
	go wait.Until(ctrl.gcPodTimeMap, maxToleranceLantency, ctrl.ctx.Done())

	<-ctrl.ctx.Done()
}

func (ctrl *CNRMonitorController) cnrMonitorWorker() {
	for ctrl.processNextCNR() {
	}
}

// processNextCNR dequeues items, processes them, and marks them done.
// It enforces that the sync is never invoked concurrently with the same key.
func (ctrl *CNRMonitorController) processNextCNR() bool {
	key, quit := ctrl.cnrSyncQueue.Get()
	if quit {
		return false
	}
	defer ctrl.cnrSyncQueue.Done(key)

	err := ctrl.syncCNR(key.(string))
	if err == nil {
		ctrl.cnrSyncQueue.Forget(key)
		return true
	}

	// if err is not nil, requeue the key
	utilruntime.HandleError(fmt.Errorf("sync %s failed with %v", key, err))
	ctrl.cnrSyncQueue.AddRateLimited(key)

	return true
}

func (ctrl *CNRMonitorController) syncCNR(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	cnr, err := ctrl.cnrLister.Get(name)
	if errors.IsNotFound(err) {
		// cnr is deleted, so we can skip
		klog.Info("CNR has been deleted %v", key)
		return nil
	}
	if err != nil {
		return err
	}

	// hasAnomaly is used to record whether cnr has anomaly
	hasAnomaly := false
	// check numa exclusive anomaly
	klog.Infof("Check Numa Exclusive Anomaly...")
	if ctrl.checkNumaExclusiveAnomaly(cnr) {
		hasAnomaly = true
		klog.Infof("Emit Numa Exclusive Anomaly metric...")
		err = ctrl.emitCNRAnomalyMetric(cnr, reasonNumaExclusiveAnomaly)
		if err != nil {
			return err
		}
	}
	// check numa allocatable sum anomaly
	klog.Infof("Check Numa Allocatable Sum Anomaly...")
	if ctrl.checkNumaAllocatableSumAnomaly(cnr) {
		hasAnomaly = true
		klog.Infof("Emit Numa Allocatable Sum Anomaly metric...")
		err = ctrl.emitCNRAnomalyMetric(cnr, reasonNumaAllocatableSumAnomaly)
		if err != nil {
			return err
		}
	}

	// check pod allocation sum anomaly
	klog.Infof("Check Pod Allocation Sum Anomaly...")
	if ctrl.checkPodAllocationSumAnomaly(cnr) {
		hasAnomaly = true
		klog.Infof("Emit Pod Allocation Sum Anomaly metric...")
		err = ctrl.emitCNRAnomalyMetric(cnr, reasonPodAllocationSumAnomaly)
		if err != nil {
			return err
		}
	}

	// if hasAnomaly is true, re-enqueue cnr use AddAfter func with 30s duration
	if hasAnomaly {
		klog.Infof("CNR %s has anomaly, re-enqueue it after 30s", cnr.Name)
		ctrl.cnrSyncQueue.AddAfter(key, 30*time.Second)
	}

	return nil
}

// enqueueCNR enqueues the given CNR in the work queue.
func (ctrl *CNRMonitorController) enqueueCNR(cnr *apis.CustomNodeResource) {
	if cnr == nil {
		klog.Warning("trying to enqueue a nil cnr")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cnr)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Cound't get key for CNR %+v: %v", cnr, err))
		return
	}
	ctrl.cnrSyncQueue.Add(key)
}

func (ctrl *CNRMonitorController) addCNREventHandler(obj interface{}) {
	klog.Infof("CNR create event found...")
	cnr, ok := obj.(*apis.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert obj to *apis.CNR")
		return
	}
	klog.V(4).Infof("notice addition of cnr %s", cnr.Name)

	ctrl.enqueueCNR(cnr)
}

func (ctrl *CNRMonitorController) updateCNREventHandler(_, newObj interface{}) {
	klog.Infof("CNR update event found...")
	cnr, ok := newObj.(*apis.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert newObj to *apis.CNR")
		return
	}
	klog.V(4).Infof("notice update of cnr %s", cnr.Name)

	// check and emit cnr pod report lantency metric
	klog.Infof("Check and Emit CNR Report Lantency metric...")
	err := ctrl.checkAndEmitCNRReportLantencyMetric(cnr)
	if err != nil {
		klog.Errorf("check and emit cnr report lantency metric failed: %v", err)
	}

	ctrl.enqueueCNR(cnr)
}

func (ctrl *CNRMonitorController) updatePodEventHandler(oldObj, newObj interface{}) {
	klog.Infof("Pod update event found...")
	oldPod, ok := oldObj.(*corev1.Pod)
	if !ok {
		klog.Errorf("cannot convert oldObj to Pod")
		return
	}
	newPod, ok := newObj.(*corev1.Pod)
	if !ok {
		klog.Errorf("cannot convert newObj to Pod")
		return
	}
	if oldPod.Spec.NodeName == "" && newPod.Spec.NodeName != "" {
		klog.Infof("notice pod: %v scheduled to node: %v", newPod.Name, newPod.Spec.NodeName)
		// record pod scheduled time
		ctrl.podTimeMap.Store(native.GenerateUniqObjectUIDKey(newPod), time.Now())
	}
}

// gcPodTimeMap gc podTimeMap which over maxToleranceLantency not used
func (ctrl *CNRMonitorController) gcPodTimeMap() {
	klog.Infof("gc podTimeMap...")
	ctrl.podTimeMap.Range(func(key, value interface{}) bool {
		notUsedTime := time.Now().Sub(value.(time.Time))
		if notUsedTime > maxToleranceLantency {
			klog.Infof("gc podTimeMap: %v, which not used over %v minutes", key, notUsedTime.Minutes())
			ctrl.podTimeMap.Delete(key)
			namespace, podName, _, err := native.ParseUniqObjectUIDKey(key.(string))
			if err != nil {
				klog.Errorf("failed to parse uniq object uid key %s", key)
				return true
			}
			pod, err := ctrl.podLister.Pods(namespace).Get(podName)
			if err != nil {
				klog.Errorf("failed to get pod %s/%s", namespace, podName)
				return true
			}
			if !native.PodIsTerminated(pod) {
				klog.Infof("Emit Timeout CNR Report Lantency metric...")
				// emit timeout cnr report lantency metric
				ctrl.emitCNRReportLantencyMetric(pod.Spec.NodeName, key.(string), maxToleranceLantency.Milliseconds(), "true")
				if err != nil {
					klog.Errorf("emit cnr report lantency metric failed: %v", err)
				}
			}
		}
		return true // return true to continue iterating
	})
}
