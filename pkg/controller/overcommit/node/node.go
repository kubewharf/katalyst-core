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

package node

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	v1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/listers/overcommit/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/node/matcher"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	overcommitutil "github.com/kubewharf/katalyst-core/pkg/util/overcommit"
)

const nodeOvercommitControllerName = "noc"

var resourceAnnotationKey = map[corev1.ResourceName]string{
	corev1.ResourceCPU:    consts.NodeAnnotationCPUOvercommitRatioKey,
	corev1.ResourceMemory: consts.NodeAnnotationMemoryOvercommitRatioKey,
}

// NodeOvercommitController is responsible to update node overcommit annotation
// according to NodeOvercommitConfig
type NodeOvercommitController struct {
	ctx context.Context

	nodeLister           v1.NodeLister
	nodeOvercommitLister v1alpha1.NodeOvercommitConfigLister
	cnrLister            nodev1alpha1.CustomNodeResourceLister
	nodeUpdater          control.NodeUpdater
	nocUpdater           control.NocUpdater

	syncedFunc []cache.InformerSynced

	matcher         matcher.Matcher
	nocSyncQueue    workqueue.RateLimitingInterface
	nodeSyncQueue   workqueue.RateLimitingInterface
	cnrSyncQueue    workqueue.RateLimitingInterface
	workerCount     int
	reconcilePeriod time.Duration
	firstReconcile  bool

	metricsEmitter metrics.MetricEmitter
}

func NewNodeOvercommitController(
	ctx context.Context,
	controlCtx *katalyst_base.GenericContext,
	genericConf *generic.GenericConfiguration,
	overcommitConf *controller.OvercommitConfig,
) (*NodeOvercommitController, error) {

	nodeInformer := controlCtx.KubeInformerFactory.Core().V1().Nodes()
	nodeOvercommitInformer := controlCtx.InternalInformerFactory.Overcommit().V1alpha1().NodeOvercommitConfigs()
	err := nodeOvercommitInformer.Informer().AddIndexers(cache.Indexers{
		matcher.LabelSelectorValIndex: func(obj interface{}) ([]string, error) {
			noc, ok := obj.(*configv1alpha1.NodeOvercommitConfig)
			if !ok {
				return []string{}, nil
			}
			return []string{noc.Spec.NodeOvercommitSelectorVal}, nil
		},
	})
	if err != nil {
		return nil, err
	}
	genericClient := controlCtx.Client

	cnrInformer := controlCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources()

	nodeOvercommitConfigController := &NodeOvercommitController{
		ctx:                  ctx,
		nodeLister:           nodeInformer.Lister(),
		nodeOvercommitLister: nodeOvercommitInformer.Lister(),
		cnrLister:            cnrInformer.Lister(),
		nodeUpdater:          &control.DummyNodeUpdater{},
		nocUpdater:           &control.DummyNocUpdater{},
		nocSyncQueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "noc"),
		nodeSyncQueue:        workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "node"),
		cnrSyncQueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "cnr"),
		workerCount:          overcommitConf.Node.SyncWorkers,
		syncedFunc: []cache.InformerSynced{
			nodeInformer.Informer().HasSynced,
			nodeOvercommitInformer.Informer().HasSynced,
			cnrInformer.Informer().HasSynced,
		},
		matcher:         &matcher.DummyMatcher{},
		reconcilePeriod: overcommitConf.Node.ConfigReconcilePeriod,
	}

	nodeOvercommitConfigController.metricsEmitter = controlCtx.EmitterPool.GetDefaultMetricsEmitter()
	if nodeOvercommitConfigController.metricsEmitter == nil {
		nodeOvercommitConfigController.metricsEmitter = metrics.DummyMetrics{}
	}

	if !genericConf.DryRun {
		nodeOvercommitConfigController.matcher = matcher.NewMatcher(nodeInformer.Lister(), nodeOvercommitInformer.Lister(), nodeOvercommitInformer.Informer().GetIndexer())
		nodeOvercommitConfigController.nodeUpdater = control.NewRealNodeUpdater(genericClient.KubeClient)
		nodeOvercommitConfigController.nocUpdater = control.NewRealNocUpdater(genericClient.InternalClient)
	}

	// add handlers
	nodeOvercommitInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nodeOvercommitConfigController.addNodeOvercommitConfig,
		UpdateFunc: nodeOvercommitConfigController.updateNodeOvercommitConfig,
		DeleteFunc: nodeOvercommitConfigController.deleteNodeOvercommitConfig,
	})

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nodeOvercommitConfigController.addNode,
		UpdateFunc: nodeOvercommitConfigController.updateNode,
	})

	cnrInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nodeOvercommitConfigController.addCNR,
		UpdateFunc: nodeOvercommitConfigController.updateCNR,
	})

	return nodeOvercommitConfigController, nil
}

func (nc *NodeOvercommitController) Run() {
	defer utilruntime.HandleCrash()
	defer func() {
		nc.nocSyncQueue.ShutDown()
		nc.nodeSyncQueue.ShutDown()
		nc.cnrSyncQueue.ShutDown()
		klog.Infof("Shutting down %s controller", nodeOvercommitControllerName)
	}()

	if !cache.WaitForCacheSync(nc.ctx.Done(), nc.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", nodeOvercommitControllerName))
		return
	}

	klog.Infof("caches are synced for %s controller", nodeOvercommitControllerName)

	err := nc.matcher.Reconcile()
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("controller %s Reconcile fail: %v", nodeOvercommitControllerName, err))
		return
	}

	klog.Infof("%s controller start process, workerCount: %v, reconcilePeriod: %v", nodeOvercommitControllerName, nc.workerCount, nc.reconcilePeriod)
	for i := 0; i < nc.workerCount; i++ {
		// config matching and node configs sorting are handled asynchronously in different worker
		go wait.Until(nc.nodeWorker, time.Second, nc.ctx.Done())

		go wait.Until(nc.worker, time.Second, nc.ctx.Done())

		go wait.Until(nc.cnrWorker, time.Second, nc.ctx.Done())
	}

	nc.reconcile()

	<-nc.ctx.Done()
}

func (nc *NodeOvercommitController) reconcile() {
	go wait.Until(func() {
		if nc.firstReconcile {
			nc.firstReconcile = false
			return
		}
		err := nc.matcher.Reconcile()
		if err != nil {
			klog.Error(err)
			return
		}

		nodeList, err := nc.nodeLister.List(labels.Everything())
		if err != nil {
			klog.Error(err)
			return
		}
		for _, node := range nodeList {
			err = nc.setNodeOvercommitAnnotations(node.Name, nil)
			if err != nil {
				klog.Errorf("%s controller reconcile set node annotation fail: %v", nodeOvercommitControllerName, err)
				continue
			}
		}

		configList, err := nc.nodeOvercommitLister.List(labels.Everything())
		if err != nil {
			klog.Error(err)
			return
		}
		for _, config := range configList {
			err = nc.patchNodeOvercommitConfigStatus(config.Name)
			if err != nil {
				klog.Errorf("%s controller reconcile patch noc status fail: %v")
				continue
			}
		}
	}, nc.reconcilePeriod, nc.ctx.Done())
}

func (nc *NodeOvercommitController) worker() {
	for nc.processNextEvent() {
	}
}

func (nc *NodeOvercommitController) nodeWorker() {
	for nc.processNextNode() {
	}
}

func (nc *NodeOvercommitController) cnrWorker() {
	for nc.processNextCNR() {
	}
}

func (nc *NodeOvercommitController) processNextEvent() bool {
	key, quit := nc.nocSyncQueue.Get()
	if quit {
		return false
	}
	defer nc.nocSyncQueue.Done(key)

	var (
		event = key.(nodeOvercommitEvent)
		err   error
	)
	// both config change and node label change may cause the matching relationship to change,
	// but they are handled in different ways
	switch event.eventType {
	case nodeEvent:
		err = nc.syncNodeEvent(event.nodeKey)
	case configEvent:
		err = nc.syncConfigEvent(event.configKey)
	default:
		nc.nocSyncQueue.Forget(key)
		klog.Errorf("unkonw event type: %s", event.eventType)
		return true
	}
	if err == nil {
		nc.nocSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	nc.nocSyncQueue.AddRateLimited(key)

	return true
}

func (nc *NodeOvercommitController) processNextNode() bool {
	key, quit := nc.nodeSyncQueue.Get()
	if quit {
		return false
	}
	defer nc.nodeSyncQueue.Done(key)

	err := nc.syncNode(key.(string))
	if err == nil {
		nc.nodeSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	nc.nodeSyncQueue.AddRateLimited(key)

	return true
}

func (nc *NodeOvercommitController) processNextCNR() bool {
	key, quit := nc.cnrSyncQueue.Get()
	if quit {
		return false
	}
	defer nc.cnrSyncQueue.Done(key)

	err := nc.syncCNR(key.(string))
	if err == nil {
		nc.cnrSyncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	nc.cnrSyncQueue.AddRateLimited(key)

	return true
}

func (nc *NodeOvercommitController) syncConfigEvent(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("failed to split namespace and name from key %s", key)
		return err
	}

	nodeNames, err := nc.matcher.MatchConfig(name)
	if err != nil {
		klog.Errorf("failed to update config, configName: %v, err: %v", name, err)
		return err
	}

	for _, nodeName := range nodeNames {
		nc.nodeSyncQueue.Add(nodeName)
	}

	return nc.patchNodeOvercommitConfigStatus(name)
}

func (nc *NodeOvercommitController) syncNodeEvent(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("failed to split namespace and name from key %s", key)
		return err
	}
	_, err = nc.nodeLister.Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			nc.matcher.DelNode(name)
			return nil
		} else {
			return err
		}
	}

	nodeOverCommitConfigList, err := nc.nodeOvercommitLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list nodeOverCommitConfig: %v", err)
		return err
	}
	for _, config := range nodeOverCommitConfigList {
		_, err := nc.matcher.MatchConfig(config.Name)
		if err != nil {
			klog.Errorf("failed to match config %s: %v", config.Name, err)
			return err
		}
		err = nc.patchNodeOvercommitConfigStatus(config.Name)
		if err != nil {
			// fail of patching nodeOvercommitConfigStatus will not affect node overcommit ratio
			// can be fixed by reconcile
			klog.Warning("failed to patch %s nodeOvercommitConfigStatus: %v", config.Name, err)
			continue
		}
	}

	nc.nodeSyncQueue.Add(name)
	return nil
}

func (nc *NodeOvercommitController) syncNode(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("failed to split namespace and name from key %s", key)
		return err
	}

	config, err := nc.matcher.MatchNode(name)
	if err != nil {
		klog.Errorf("matchNode %v fail: %v", name, err)
		return err
	}

	return nc.setNodeOvercommitAnnotations(name, config)
}

func (nc *NodeOvercommitController) syncCNR(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("failed to split namespace and name from key %s", key)
		return err
	}

	return nc.setNodeOvercommitAnnotations(name, nil)
}

func (nc *NodeOvercommitController) patchNodeOvercommitConfigStatus(configName string) error {
	oldConfig, err := nc.nodeOvercommitLister.Get(configName)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Warning("nodeOvercommitConfig %v has been deleted.")
			return nil
		}
		klog.Errorf("get nodeOvercommitConfig %v fail: %v", configName, err)
		return err
	}

	nodeNames := nc.matcher.GetNodes(configName)
	newConfig := oldConfig.DeepCopy()
	newConfig.Status.MatchedNodeList = nodeNames

	_, err = nc.nocUpdater.PatchNocStatus(nc.ctx, oldConfig, newConfig)
	if err != nil {
		klog.Error(err)
		return err
	}
	return nil
}

func (nc *NodeOvercommitController) setNodeOvercommitAnnotations(nodeName string, config *configv1alpha1.NodeOvercommitConfig) error {
	// get node from node index
	node, err := nc.nodeLister.Get(nodeName)
	if err != nil {
		klog.Errorf("get node %s fail: %v", nodeName, err)
		return err
	}

	// get noc if nil
	if config == nil {
		config = nc.matcher.GetConfig(nodeName)
	}
	nodeOvercommitConfig := emptyOvercommitConfig()
	if config != nil {
		nodeOvercommitConfig = config
	}

	nodeCopy := node.DeepCopy()
	nodeAnnotations := nodeCopy.GetAnnotations()
	if nodeAnnotations == nil {
		nodeAnnotations = make(map[string]string)
	}

	for resourceName, annotationKey := range resourceAnnotationKey {
		c, ok := nodeOvercommitConfig.Spec.ResourceOvercommitRatio[resourceName]
		if !ok {
			switch resourceName {
			case corev1.ResourceCPU:
				nodeAnnotations[annotationKey] = consts.DefaultNodeCPUOvercommitRatio
			case corev1.ResourceMemory:
				nodeAnnotations[annotationKey] = consts.DefaultNodeMemoryOvercommitRatio
			}
		} else {
			nodeAnnotations[annotationKey] = c
		}
	}

	nc.nodeRealtimeOvercommitRatio(nodeAnnotations, node)

	cpuAllocatable, cpuCapacity := nc.nodeOvercommitResource(node, validCPUOvercommitRatio(nodeAnnotations), corev1.ResourceCPU, consts.NodeAnnotationOriginalAllocatableCPUKey, consts.NodeAnnotationOriginalCapacityCPUKey)
	klog.V(5).Infof("node %s CPU allocatable: %v, CPU capacity: %v with bindcpu", node.Name, cpuAllocatable, cpuCapacity)
	if cpuAllocatable == "" {
		delete(nodeAnnotations, consts.NodeAnnotationOvercommitAllocatableCPUKey)
		delete(nodeAnnotations, consts.NodeAnnotationOvercommitCapacityCPUKey)
	} else {
		nodeAnnotations[consts.NodeAnnotationOvercommitAllocatableCPUKey] = cpuAllocatable
		nodeAnnotations[consts.NodeAnnotationOvercommitCapacityCPUKey] = cpuCapacity
	}

	memAllocatable, memCapacity := nc.nodeOvercommitResource(node, validMemoryOvercommitRatio(nodeAnnotations), corev1.ResourceMemory, consts.NodeAnnotationOriginalAllocatableMemoryKey, consts.NodeAnnotationOriginalCapacityMemoryKey)
	klog.V(5).Infof("node %s memory allocatable: %v, memory capacity: %v", node.Name, memAllocatable, memCapacity)
	if memAllocatable == "" {
		delete(nodeAnnotations, consts.NodeAnnotationOvercommitAllocatableMemoryKey)
		delete(nodeAnnotations, consts.NodeAnnotationOvercommitCapacityMemoryKey)
	} else {
		nodeAnnotations[consts.NodeAnnotationOvercommitAllocatableMemoryKey] = memAllocatable
		nodeAnnotations[consts.NodeAnnotationOvercommitCapacityMemoryKey] = memCapacity
	}

	nodeCopy.Annotations = nodeAnnotations
	if !reflect.DeepEqual(nodeAnnotations, node.Annotations) {
		return nc.nodeUpdater.PatchNode(nc.ctx, node, nodeCopy)
	}
	return nil
}

func emptyOvercommitConfig() *configv1alpha1.NodeOvercommitConfig {
	return &configv1alpha1.NodeOvercommitConfig{
		Spec: configv1alpha1.NodeOvercommitConfigSpec{
			ResourceOvercommitRatio: map[corev1.ResourceName]string{},
		},
	}
}

func (nc *NodeOvercommitController) nodeRealtimeOvercommitRatio(nodeAnnotation map[string]string, node *corev1.Node) {
	kcnr, err := nc.cnrLister.Get(node.Name)
	if err != nil {
		klog.Error(err)
		return
	}

	if len(kcnr.Annotations) == 0 {
		delete(nodeAnnotation, consts.NodeAnnotationRealtimeCPUOvercommitRatioKey)
		delete(nodeAnnotation, consts.NodeAnnotationRealtimeMemoryOvercommitRatioKey)
		return
	}

	realtimeCPU, ok := kcnr.Annotations[consts.NodeAnnotationCPUOvercommitRatioKey]
	if ok {
		nodeAnnotation[consts.NodeAnnotationRealtimeCPUOvercommitRatioKey] = realtimeCPU
	} else {
		delete(nodeAnnotation, consts.NodeAnnotationRealtimeCPUOvercommitRatioKey)
	}

	realtimeMem, ok := kcnr.Annotations[consts.NodeAnnotationMemoryOvercommitRatioKey]
	if ok {
		nodeAnnotation[consts.NodeAnnotationRealtimeMemoryOvercommitRatioKey] = realtimeMem
	} else {
		delete(nodeAnnotation, consts.NodeAnnotationRealtimeMemoryOvercommitRatioKey)
	}
}

func (nc *NodeOvercommitController) nodeOvercommitResource(
	node *corev1.Node,
	overcommitRatio float64,
	resourceName corev1.ResourceName,
	originalAllocatableKey, originalCapacityKey string) (string, string) {
	var guaranteedResource int

	if overcommitRatio <= 1 {
		klog.V(5).Infof("node %v cpu overcommit ratio less than 1: %v", node.Name, overcommitRatio)
		overcommitRatio = 1.0
	}

	// node original allocatable and capacity should be always exists if overcommit webhook is running
	// node allocatable can not be calculated without original allocatable, just return empty string.
	nodeAllocatableAnnotation, ok := node.Annotations[originalAllocatableKey]
	if !ok {
		klog.V(5).Infof("node %s annotation %s missing", node.Name, originalAllocatableKey)
		return "", ""
	}
	nodeCapacityAnnotation, ok := node.Annotations[originalCapacityKey]
	if !ok {
		klog.V(5).Infof("node %s annotation %s missing", node.Name, originalCapacityKey)
		return "", ""
	}
	nodeAllocatable, err := resource.ParseQuantity(nodeAllocatableAnnotation)
	if err != nil {
		klog.Error(err)
		return "", ""
	}
	nodeCapacity, err := resource.ParseQuantity(nodeCapacityAnnotation)
	if err != nil {
		klog.Error(err)
		return "", ""
	}

	if resourceName == corev1.ResourceCPU {
		guaranteedCPU, err := nc.getGuaranteedCPU(node.Name)
		if err != nil {
			klog.Error(err)
		} else {
			guaranteedResource = guaranteedCPU
		}
	}

	guaranteedQuantity := resource.NewQuantity(int64(guaranteedResource), resource.DecimalSI)
	nodeAllocatable.Sub(*guaranteedQuantity)
	nodeAllocatable = native.MultiplyMilliQuantity(nodeAllocatable, overcommitRatio)
	nodeAllocatable.Add(*guaranteedQuantity)
	nodeCapacity.Sub(*guaranteedQuantity)
	nodeCapacity = native.MultiplyMilliQuantity(nodeCapacity, overcommitRatio)
	nodeCapacity.Add(*guaranteedQuantity)

	klog.V(5).Infof("node %s overcommitRatio: %v, guaranteedResource: %v, final allocatable: %v, capacity: %v",
		node.Name, overcommitRatio, guaranteedResource, nodeAllocatable.String(), nodeCapacity.String())
	return nodeAllocatable.String(), nodeCapacity.String()
}

func (nc *NodeOvercommitController) getGuaranteedCPU(nodeName string) (int, error) {
	kcnr, err := nc.cnrLister.Get(nodeName)
	if err != nil {
		klog.Error(err)
		return 0, err
	}

	if kcnr.Annotations == nil {
		klog.V(5).Infof("node %s with nil annotation", nodeName)
		return 0, nil
	}

	if kcnr.Annotations[consts.KCNRAnnotationCPUManager] != string(cpumanager.PolicyStatic) {
		klog.V(5).Infof("node %s not support cpu manager", kcnr.Name)
		return 0, nil
	}

	cpusAnnotation, ok := kcnr.Annotations[consts.KCNRAnnotationGuaranteedCPUs]
	if !ok {
		klog.V(5).Infof("node %s guaranteed cpus missing", kcnr.Name)
		return 0, nil
	}
	guaranteedCPUs, err := strconv.Atoi(cpusAnnotation)
	if err != nil {
		klog.Error(err)
		return 0, err
	}

	return guaranteedCPUs, nil
}

func validCPUOvercommitRatio(annotation map[string]string) float64 {
	res, err := overcommitutil.OvercommitRatioValidate(annotation, consts.NodeAnnotationCPUOvercommitRatioKey, consts.NodeAnnotationRealtimeCPUOvercommitRatioKey)
	if err != nil {
		klog.Error(err)
	}
	return res
}

func validMemoryOvercommitRatio(annotation map[string]string) float64 {
	res, err := overcommitutil.OvercommitRatioValidate(annotation, consts.NodeAnnotationMemoryOvercommitRatioKey, consts.NodeAnnotationRealtimeMemoryOvercommitRatioKey)
	if err != nil {
		klog.Error(err)
	}
	return res
}
