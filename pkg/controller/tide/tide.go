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

package tide

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/autoscaler/cluster-autoscaler/simulator"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	podv1 "k8s.io/kubernetes/pkg/api/v1/pod"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	nodeutil "sigs.k8s.io/descheduler/pkg/descheduler/node"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/tide/v1alpha1"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/tide/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	tideControllerName   = "tide"
	tideCycleWorkerCount = 1
)

const (
	tidePeriod = 10 * time.Second
)

type OnlinePodChecker func(pod *corev1.Pod) bool

type NodeInfo struct {
	NodeUsage
}

// NodeUsage stores a node's info, pods on it, thresholds and its resource usage
type NodeUsage struct {
	node    *corev1.Node
	usage   map[corev1.ResourceName]*resource.Quantity
	allPods []*corev1.Pod
}

type Tide struct {
	ctx context.Context

	client *client.GenericClientSet

	checker simulator.PredicateChecker

	nodeListerSynced cache.InformerSynced
	nodeLister       corelisters.NodeLister
	podListerSynced  cache.InformerSynced
	podLister        corelisters.PodLister
	tideListerSynced cache.InformerSynced
	tideLister       listers.TideNodePoolLister

	//queue for node
	syncQueue workqueue.RateLimitingInterface

	// metricsEmitter for emit metrics
	metricsEmitter metrics.MetricEmitter
}

func NewTide(ctx context.Context,
	controlCtx *katalystbase.GenericContext,
	_ *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration) (*Tide, error) {

	tide := &Tide{
		ctx:    ctx,
		client: controlCtx.Client,
		syncQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(),
			tideControllerName),
	}
	checker, err := simulator.NewSchedulerBasedPredicateChecker(controlCtx.Client.KubeClient, ctx.Done())
	if err != nil {
		return nil, err
	}
	tide.checker = checker

	controlCtx.KubeInformerFactory.Core().V1().Nodes().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    tide.addNodeEventHandle,
		UpdateFunc: tide.updateNodeEventHandle,
	})
	tide.nodeListerSynced = controlCtx.KubeInformerFactory.Core().V1().Nodes().Informer().HasSynced
	tide.nodeLister = controlCtx.KubeInformerFactory.Core().V1().Nodes().Lister()

	tide.podListerSynced = controlCtx.KubeInformerFactory.Core().V1().Pods().Informer().HasSynced
	tide.podLister = controlCtx.KubeInformerFactory.Core().V1().Pods().Lister()

	controlCtx.InternalInformerFactory.Tide()
	controlCtx.InternalInformerFactory.Tide().V1alpha1().TideNodePools().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    tide.addTideNodePoolEventHandle,
		UpdateFunc: tide.updateTideNodePoolEventHandle,
		DeleteFunc: tide.deleteTideNodePoolEventHandle,
	})
	tide.tideLister = controlCtx.InternalInformerFactory.Tide().V1alpha1().TideNodePools().Lister()
	tide.tideListerSynced = controlCtx.InternalInformerFactory.Tide().V1alpha1().TideNodePools().Informer().HasSynced

	tide.metricsEmitter = controlCtx.EmitterPool.GetDefaultMetricsEmitter()

	return tide, nil
}

func (t *Tide) Run() {
	defer utilruntime.HandleCrash()
	defer t.syncQueue.ShutDown()

	defer klog.Infof("Shutting down %s controller", tideControllerName)

	if !cache.WaitForCacheSync(t.ctx.Done(), t.nodeListerSynced, t.tideListerSynced, t.podListerSynced) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", tideControllerName))
		return
	}
	klog.Infof("Caches are synced for %s controller", tideControllerName)
	klog.Infof("start %d workers for %s controller", tideCycleWorkerCount, tideControllerName)

	go wait.Until(t.periodSync, tidePeriod, t.ctx.Done())
	for i := 0; i < tideCycleWorkerCount; i++ {
		go wait.Until(t.worker, time.Second, t.ctx.Done())
	}

	<-t.ctx.Done()
}

func (t *Tide) addNodeEventHandle(obj interface{}) {
	tideNodePoolList, err := t.tideLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("list tide hybrid node pool failed: %v", err)
		return
	}
	n, ok := obj.(*corev1.Node)
	if !ok {
		klog.Errorf("cannot convert obj to *apis.TideNodePool: %v", obj)
		return
	}
	for _, tideNodePool := range tideNodePoolList {
		if labels.SelectorFromSet(tideNodePool.Spec.NodeConfigs.NodeSelector).
			Matches(labels.Set(n.GetLabels())) {
			klog.Infof("start to sync node pool, name: %s", tideNodePool.Name)
			t.enqueueWorkItem(tideNodePool)
		}
	}
}

func (t *Tide) updateNodeEventHandle(old, cur interface{}) {
	tideNodePoolList, err := t.tideLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("list tide hybrid node pool failed: %v", err)
		return
	}
	n, ok := old.(*corev1.Node)
	if !ok {
		klog.Errorf("cannot convert obj to *apis.TideNodePool: %v", old)
		return
	}
	for _, tideNodePool := range tideNodePoolList {
		if labels.SelectorFromSet(tideNodePool.Spec.NodeConfigs.NodeSelector).
			Matches(labels.Set(n.GetLabels())) {
			klog.Infof("start to sync node pool, name: %s", tideNodePool.Name)
			t.enqueueWorkItem(tideNodePool)
		}
	}
}

func (t *Tide) addTideNodePoolEventHandle(obj interface{}) {
	c, ok := obj.(*apis.TideNodePool)
	if !ok {
		klog.Errorf("cannot convert obj to *apis.TideNodePool: %v", obj)
		return
	}
	klog.V(4).Infof("notice addition of tide node pool %s", c.Name)

	t.enqueueWorkItem(obj)
}

func (t *Tide) updateTideNodePoolEventHandle(_, new interface{}) {
	c, ok := new.(*apis.TideNodePool)
	if !ok {
		klog.Errorf("cannot convert oldObj to *apis.TideNodePool: %v", c)
		return
	}
	klog.V(4).Infof("notice addition of tide node pool %s", c.Name)

	t.enqueueWorkItem(new)
}

func (t *Tide) deleteTideNodePoolEventHandle(obj interface{}) {
	c, ok := obj.(*apis.TideNodePool)
	if !ok {
		klog.Errorf("cannot convert oldObj to *apis.TideNodePool: %v", c)
		return
	}
	klog.V(4).Infof("notice addition of tide node pool %s", c.Name)

	t.enqueueWorkItem(obj)

}

func (t *Tide) worker() {
	for t.processNextWorkItem(context.Background()) {
	}
}

// processNextWorkItem dequeues items, processes them, and marks them done.
// It enforces that the sync is never invoked concurrently with the same key.
func (t *Tide) processNextWorkItem(ctx context.Context) bool {
	key, quit := t.syncQueue.Get()
	if quit {
		return false
	}
	defer t.syncQueue.Done(key)

	err := t.sync(ctx, key.(string))
	if err == nil {
		t.syncQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("sync %q failed with %v", key, err))
	t.syncQueue.AddRateLimited(key)

	return true
}

// enqueueWorkItem enqueues the given node in the work queue.
func (t *Tide) enqueueWorkItem(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Cound't get key for object %+v: %v", obj, err))
		return
	}
	t.syncQueue.Add(key)
}

// sync syncs the given node.
func (t *Tide) sync(ctx context.Context, key string) error {
	// TODO
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	tideNodePool, err := t.tideLister.Get(name)
	if errors.IsNotFound(err) {
		klog.Infof("node has been deleted %v", key)
		return nil
	}
	if err != nil {
		return err
	}

	err = t.Reconcile(ctx, tideNodePool.DeepCopy())
	if err != nil {
		return err
	}

	return nil
}

func (t *Tide) reconcileDelete(ctx context.Context, tideNodePool *apis.TideNodePool) error {
	nodes, err := t.nodeLister.List(labels.SelectorFromSet(map[string]string{LabelNodePoolKey: tideNodePool.Name}))
	if err != nil {
		klog.Errorf("fail to list nodes: %v", err)
		return err
	}
	for _, node := range nodes {
		if err := t.cleanNode(ctx, node.DeepCopy(), tideNodePool); err != nil {
			return err
		}
	}
	// Remove finalizer first
	controllerutil.RemoveFinalizer(tideNodePool, NodePoolFinalizer)
	_, err = t.client.InternalClient.TideV1alpha1().TideNodePools().Update(ctx, tideNodePool, metav1.UpdateOptions{})
	return err
}

func (t *Tide) cleanNode(ctx context.Context, node *corev1.Node, pool *apis.TideNodePool) error {
	nodePoolWrapper := NewNodePoolWrapper(pool)
	var foundIndexes []int
	for i := range node.Spec.Taints {
		if node.Spec.Taints[i].Key == nodePoolWrapper.GetEvictOnlinePodTaint().Key ||
			node.Spec.Taints[i].Key == nodePoolWrapper.GetEvictOfflinePodTaint().Key {
			foundIndexes = append(foundIndexes, i)
		}
	}
	if len(foundIndexes) >= 0 {
		for i := len(foundIndexes) - 1; i >= 0; i-- {
			s := foundIndexes[i]
			node.Spec.Taints = append(node.Spec.Taints[:s], node.Spec.Taints[s+1:]...)
		}
	}

	delete(node.Labels, nodePoolWrapper.GetOnlineLabel().Key)
	delete(node.Labels, nodePoolWrapper.GetOfflineLabel().Key)
	delete(node.Labels, nodePoolWrapper.GetTideLabel().Key)
	delete(node.Labels, LabelReverseNode)
	delete(node.Labels, LabelNodeTypeKey)
	delete(node.Labels, LabelNodePoolKey)
	_, err := t.client.KubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

func (t *Tide) Reconcile(ctx context.Context, tideNodePool *apis.TideNodePool) error {
	logger := klog.FromContext(ctx).WithValues("tideNodePool", tideNodePool.GetName())
	logger.V(2).Info("start Reconcile")
	defer logger.V(2).Info("end Reconcile")
	// Add finalizer first
	if !controllerutil.ContainsFinalizer(tideNodePool, NodePoolFinalizer) && tideNodePool.DeletionTimestamp.IsZero() {
		controllerutil.AddFinalizer(tideNodePool, NodePoolFinalizer)
		tideNodePool, err := t.client.InternalClient.TideV1alpha1().TideNodePools().Update(ctx, tideNodePool, metav1.UpdateOptions{})
		if err != nil {
			klog.ErrorS(err, "fail to add finalizer", "rule", tideNodePool.Name)
			return err
		}
	}
	// process deletion
	if !tideNodePool.DeletionTimestamp.IsZero() {
		return t.reconcileDelete(ctx, tideNodePool)
	}
	nodes, err := t.nodeLister.List(labels.SelectorFromSet(tideNodePool.Spec.NodeConfigs.NodeSelector))
	if err != nil {
		klog.Errorf("fail to list nodes: %v", err)
		return err
	}
	onlineNodesExpectCount, err := intstr.GetScaledValueFromIntOrPercent(tideNodePool.Spec.NodeConfigs.Reverse.Online, len(nodes), true)
	if err != nil {
		klog.Errorf("fail to get online nodes number: %v", err)
		return err
	}
	offlineNodesExpectCount, err := intstr.GetScaledValueFromIntOrPercent(tideNodePool.Spec.NodeConfigs.Reverse.Offline, len(nodes), false)
	if err != nil {
		klog.Errorf("fail to get offline nodes number: %v", err)
		return err
	}
	nodePoolWrapper := NewNodePoolWrapper(tideNodePool)
	reverseOnlineNodes, reverseOfflineNodes, tideNodes, unknownNodes := classifyNodes(nodes, NewNodePoolWrapper(tideNodePool))
	onlineNodeCount, offlineNodeCount := len(reverseOnlineNodes), len(reverseOfflineNodes)
	for i := 0; i < len(reverseOnlineNodes) && onlineNodeCount > onlineNodesExpectCount; i++ {
		nodePoolWrapper.SetNodeToTide(reverseOnlineNodes[i])
		if _, err := t.client.KubeClient.CoreV1().Nodes().Update(ctx, reverseOnlineNodes[i], metav1.UpdateOptions{}); err != nil {
			klog.Errorf("fail to convert online reverse nodes to tide: %v", err)
			return err
		}
		onlineNodeCount--
	}

	for i := 0; i < len(reverseOfflineNodes) && offlineNodeCount > offlineNodesExpectCount; i++ {
		nodePoolWrapper.SetNodeToTide(reverseOfflineNodes[i])
		if _, err := t.client.KubeClient.CoreV1().Nodes().Update(ctx, reverseOfflineNodes[i], metav1.UpdateOptions{}); err != nil {
			klog.Errorf("fail to convert offline reverse nodes to tide: %v", err)
			return err
		}
		offlineNodeCount--
	}

	for i := range unknownNodes {
		if onlineNodeCount < onlineNodesExpectCount {
			nodePoolWrapper.SetNodeToOnlineReverse(unknownNodes[i])
			if _, err := t.client.KubeClient.CoreV1().Nodes().Update(ctx, unknownNodes[i], metav1.UpdateOptions{}); err != nil {
				klog.Errorf("fail to convert new nodes to reverse: %v", err)
				return err
			}
			reverseOnlineNodes = append(reverseOnlineNodes, unknownNodes[i])
			onlineNodeCount++
		} else if offlineNodeCount < offlineNodesExpectCount {
			nodePoolWrapper.SetNodeToOfflineReverse(unknownNodes[i])
			if _, err := t.client.KubeClient.CoreV1().Nodes().Update(ctx, unknownNodes[i], metav1.UpdateOptions{}); err != nil {
				klog.Errorf("fail to convert new nodes to reverse: %v", err)
				return err
			}
			reverseOfflineNodes = append(reverseOfflineNodes, unknownNodes[i])
			offlineNodeCount++
		} else {
			nodePoolWrapper.SetNodeToTideOnline(unknownNodes[i])
			if _, err := t.client.KubeClient.CoreV1().Nodes().Update(ctx, unknownNodes[i], metav1.UpdateOptions{}); err != nil {
				klog.Errorf("fail to convert offline reverse nodes to tide: %v", err)
				return err
			}
			tideNodes = append(tideNodes, unknownNodes[i])
		}
	}

	if err := t.UpdateStatusByNodes(ctx, tideNodePool, reverseOnlineNodes, reverseOfflineNodes, tideNodes); err != nil {
		return err
	}
	onlineLabelSet := labels.SelectorFromSet(map[string]string{LabelPodTypeKey: LabelOnlinePodValue})
	onlinePodChecker := func(pod *corev1.Pod) bool {
		return onlineLabelSet.Matches(labels.Set(pod.GetLabels()))
	}

	if err := t.RunOnce(ctx,
		onlinePodChecker,
		nodePoolWrapper); err != nil {
		klog.Errorf("try to balance node failed: %v", err)
		return err
	}
	return nil
}

func (t *Tide) UpdateStatusByNodes(ctx context.Context, tideNodePool *apis.TideNodePool, reverseOnlineNodes, reverseOfflineNodes, tideNodes []*corev1.Node) error {
	newTideNodePool := tideNodePool.DeepCopy()

	var onlineNodeNames, offlineNodeNames, tideNodeNames []string
	for i := range reverseOnlineNodes {
		onlineNodeNames = append(onlineNodeNames, reverseOnlineNodes[i].Name)
	}
	for i := range reverseOfflineNodes {
		offlineNodeNames = append(offlineNodeNames, reverseOfflineNodes[i].Name)
	}

	for i := range tideNodes {
		tideNodeNames = append(tideNodeNames, tideNodes[i].Name)
	}

	sortNodeName(onlineNodeNames)
	sortNodeName(offlineNodeNames)
	sortNodeName(tideNodeNames)
	newTideNodePool.Status.ReverseNodes.OnlineNodes = onlineNodeNames
	newTideNodePool.Status.ReverseNodes.OfflineNodes = offlineNodeNames
	newTideNodePool.Status.TideNodes.Nodes = tideNodeNames
	if reflect.DeepEqual(newTideNodePool.Status, tideNodePool.Status) {
		return nil
	}
	_, err := t.client.InternalClient.TideV1alpha1().TideNodePools().UpdateStatus(ctx, newTideNodePool, metav1.UpdateOptions{})
	return err
}

func sortNodeName(data []string) {
	sort.SliceStable(data, func(i, j int) bool {
		return data[i] > data[j]
	})
}

func classifyNodes(nodes []*corev1.Node, tideNodePool NodePoolWrapper) (
	reverseOnlineNodes []*corev1.Node,
	reverseOfflineNodes []*corev1.Node,
	tideNodes []*corev1.Node,
	unknownNodes []*corev1.Node) {
	for i, node := range nodes {
		nodeLabels := labels.Set(node.GetLabels())
		switch {
		case tideNodePool.GetOnlineReverseNodeSelector().Matches(nodeLabels):
			reverseOnlineNodes = append(reverseOnlineNodes, nodes[i].DeepCopy())
		case tideNodePool.GetOfflineReverseNodeSelector().Matches(nodeLabels):
			reverseOfflineNodes = append(reverseOfflineNodes, nodes[i].DeepCopy())
		case tideNodePool.GetTideNodeSelector().Matches(nodeLabels):
			tideNodes = append(tideNodes, nodes[i].DeepCopy())
		case !tideNodePool.GetNodePoolSelector().Matches(nodeLabels):
			unknownNodes = append(unknownNodes, nodes[i].DeepCopy())
		default:
			// do nothing
		}
	}
	return
}

func (t *Tide) GetNodePoolInfo(nodes []*corev1.Node, onlinePodChecker OnlinePodChecker) (simulator.ClusterSnapshot, []*corev1.Pod, error) {
	clusterSnapshot := simulator.NewBasicClusterSnapshot()
	pods, err := t.podLister.List(labels.Everything())
	if err != nil {
		return nil, nil, err
	}
	var pendingPods []*corev1.Pod
	knownNodes := map[string]bool{}
	for i := range nodes {
		if err := clusterSnapshot.AddNode(nodes[i].DeepCopy()); err != nil {
			return nil, nil, err
		}
		knownNodes[nodes[i].Name] = true
	}

	for i, pod := range pods {
		if knownNodes[pod.Spec.NodeName] {
			if err := clusterSnapshot.AddPod(pods[i].DeepCopy(), pod.Spec.NodeName); err != nil {
				return nil, nil, err
			}
		} else if checkPendingOnlinePod(pods[i], onlinePodChecker) {
			pendingPods = append(pendingPods, pods[i])
		}

	}
	return clusterSnapshot, pendingPods, nil
}

func checkPendingOnlinePod(pod *corev1.Pod, onlinePodChecker OnlinePodChecker) bool {
	if !(pod.Spec.NodeName == "" && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed) {
		return false
	}
	_, condition := podv1.GetPodCondition(&pod.Status, corev1.PodScheduled)
	if condition == nil {
		return false
	}
	return condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable && onlinePodChecker(pod)
}

func (t *Tide) RunOnce(ctx context.Context, onlinePodChecker OnlinePodChecker, tideNodePool NodePoolWrapper) error {
	logger := klog.FromContext(ctx).WithValues("tideNodePool", tideNodePool.GetName())
	nodeList, err := t.nodeLister.List(labels.Everything())
	if err != nil {
		return err
	}

	clusterSnapshot, pendingPods, err := t.GetNodePoolInfo(nodeList, onlinePodChecker)
	if err != nil {
		return err
	}
	// assuming that the online business is pending
	// prioritizing the resolution of the online business pending issue is recommended
	if len(pendingPods) != 0 {
		offlineNodesInfos, err := getNodeUsageWithSelector(clusterSnapshot, []corev1.ResourceName{"cpu", "memory"}, tideNodePool.GetOfflineTideNodeSelector())
		if err != nil {
			return err
		}
		if len(offlineNodesInfos) <= 0 {
			logger.Info("no offline node in tidal")
			return nil
		}
		for _, pod := range pendingPods {
			_, err := t.checker.FitsAnyNode(clusterSnapshot, pod)
			if err == nil {
				logger.Info("pod can fit node", "pod", types.NamespacedName{
					Namespace: pod.Namespace,
					Name:      pod.Name,
				})
				continue
			}
			for j := range offlineNodesInfos {
				offlineNodesInfo := offlineNodesInfos[j]
				nodeInfo, err := clusterSnapshot.NodeInfos().Get(offlineNodesInfo.node.Name)
				if err != nil {
					return err
				}
				clusterSnapshot.RemoveNode(offlineNodesInfo.node.Name)
				node := t.changeNodeToOnline(nodeInfo.Node(), tideNodePool)
				clusterSnapshot.AddNode(node)
				_, err = t.checker.FitsAnyNode(clusterSnapshot, pod)
				if err != nil {
					logger.Info("pod not fit offline node after release offline node, skip", "pod", types.NamespacedName{
						Namespace: pod.Namespace,
						Name:      pod.Name,
					})
					// rollback node to offline
					clusterSnapshot.RemoveNode(offlineNodesInfo.node.Name)
					node := t.changeNodeToOffline(nodeInfo.Node(), tideNodePool)
					clusterSnapshot.AddNode(node)
					continue
				}
				if _, err := t.client.KubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
					return fmt.Errorf("update node offline to online failed: %v", err)
				} else {
					logger.Info("release offline node to online node", "pod", types.NamespacedName{
						Namespace: pod.Namespace,
						Name:      pod.Name,
					}, "node", node.Name)
					return nil
				}
			}
		}
		logger.Info("not need release offline node")
	}
	// 1. select the online node with the lowest usage
	// 2. pre-schedule all online pods (request from largest to smallest) and check whether they can all be scheduled normally
	// 3. start triggering scheduling by tainting
	onlineNodesInfos, err := getNodeUsageWithSelector(clusterSnapshot, []corev1.ResourceName{"cpu", "memory"}, tideNodePool.GetOnlineTideNodeSelector())
	if err != nil {
		return err
	}
	// skip if no online node
	if len(onlineNodesInfos) <= 1 {
		logger.Info("no online node in tidal")
		return nil
	}
	onlineNodesInfo := onlineNodesInfos[0]
	podsInNode := onlineNodesInfo.allPods
	nodeInfo, err := clusterSnapshot.NodeInfos().Get(onlineNodesInfo.node.Name)
	if err != nil {
		return err
	}
	clusterSnapshot.RemoveNode(onlineNodesInfo.node.Name)
	for _, pod := range podsInNode {
		if onlinePodChecker(pod) {
			pod.Spec.NodeName = ""
			nodeName, err := t.checker.FitsAnyNode(clusterSnapshot, pod)
			if err != nil {
				logger.Info("can not release online node to offline", "node", onlineNodesInfo.node.Name)
				return nil
			}
			pod.Spec.NodeName = nodeName
			clusterSnapshot.AddPod(pod, nodeName)
		}
	}
	node := t.changeNodeToOffline(nodeInfo.Node(), tideNodePool)
	if _, err := t.client.KubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return err
	}
	logger.Info("release online node success", "node", node.Name)
	return nil
}

func (t *Tide) changeNodeToOnline(node *corev1.Node, pool NodePoolWrapper) *corev1.Node {
	found := false
	for i := range node.Spec.Taints {
		if node.Spec.Taints[i].Key == pool.GetEvictOnlinePodTaint().Key || node.Spec.Taints[i].Key == pool.GetEvictOfflinePodTaint().Key {
			node.Spec.Taints[i].Key = pool.GetEvictOfflinePodTaint().Key
			node.Spec.Taints[i].Value = pool.GetEvictOfflinePodTaint().Value
			node.Spec.Taints[i].Effect = corev1.TaintEffect(pool.GetEvictOfflinePodTaint().Effect)
			found = true
		}
	}
	if !found {
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key:    pool.GetEvictOfflinePodTaint().Key,
			Value:  pool.GetEvictOnlinePodTaint().Value,
			Effect: corev1.TaintEffect(pool.GetEvictOnlinePodTaint().Effect),
		})
	}
	delete(node.Labels, pool.GetOfflineLabel().Key)
	node.Labels[pool.GetOnlineLabel().Key] = pool.GetOnlineLabel().Value
	return node
}

func (t *Tide) changeNodeToOffline(node *corev1.Node, pool NodePoolWrapper) *corev1.Node {
	found := false
	for i := range node.Spec.Taints {
		if node.Spec.Taints[i].Key == pool.GetEvictOnlinePodTaint().Key || node.Spec.Taints[i].Key == pool.GetEvictOfflinePodTaint().Key {
			node.Spec.Taints[i].Key = pool.GetEvictOnlinePodTaint().Key
			node.Spec.Taints[i].Value = pool.GetEvictOnlinePodTaint().Value
			node.Spec.Taints[i].Effect = corev1.TaintEffect(pool.GetEvictOnlinePodTaint().Effect)
			found = true
		}
	}
	if !found {
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key:    pool.GetEvictOfflinePodTaint().Key,
			Value:  pool.GetEvictOnlinePodTaint().Value,
			Effect: corev1.TaintEffect(pool.GetEvictOnlinePodTaint().Effect),
		})
	}
	delete(node.Labels, pool.GetOnlineLabel().Key)
	node.Labels[pool.GetOfflineLabel().Key] = pool.GetOfflineLabel().Value
	return node
}

func getNodeUsageWithSelector(
	nodes simulator.ClusterSnapshot,
	resourceNames []corev1.ResourceName,
	selector labels.Selector,
) ([]NodeUsage, error) {
	var nodeUsageList []NodeUsage
	nodeInfos, err := nodes.NodeInfos().List()
	if err != nil {
		return nil, err
	}
	for _, node := range nodeInfos {
		var pods []*corev1.Pod
		for i := range node.Pods {
			pods = append(pods, node.Pods[i].Pod)
		}
		if !selector.Matches(labels.Set(node.Node().GetLabels())) {
			continue
		}
		nodeUsageList = append(nodeUsageList, NodeUsage{
			node:    node.Node(),
			usage:   nodeutil.NodeUtilization(pods, resourceNames),
			allPods: pods,
		})
	}
	// nodes are sorted by usage rate
	sort.Slice(nodeUsageList, func(i, j int) bool {
		ti := nodeUsageList[i].usage[corev1.ResourceMemory].Value() + nodeUsageList[i].usage[corev1.ResourceCPU].MilliValue() + nodeUsageList[i].usage[corev1.ResourcePods].Value()
		tj := nodeUsageList[j].usage[corev1.ResourceMemory].Value() + nodeUsageList[j].usage[corev1.ResourceCPU].MilliValue() + nodeUsageList[j].usage[corev1.ResourcePods].Value()
		// extended resources
		for name := range nodeUsageList[i].usage {
			if !nodeutil.IsBasicResource(name) {
				ti = ti + nodeUsageList[i].usage[name].Value()
				tj = tj + nodeUsageList[j].usage[name].Value()
			}
		}
		return ti < tj
	})
	return nodeUsageList, nil
}

func (t *Tide) periodSync() {
	targetSelector := labels.Everything()
	tides, err := t.tideLister.List(targetSelector)
	if err != nil {
		klog.Errorf("failed to list all tide node pool")
		return
	}

	for _, tide := range tides {
		t.enqueueWorkItem(tide)
	}
}
