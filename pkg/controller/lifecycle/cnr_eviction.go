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
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/nodelifecycle/scheduler"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	informers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/node/v1alpha1"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	EvictionControllerName = "eviction"
)

const (
	stateNormal            = "Normal"
	stateFullDisruption    = "FullDisruption"
	statePartialDisruption = "PartialDisruption"
)

const (
	nodeNameKeyIndex = "spec.nodeName"
)

const (
	evictionLimiterQPS = 0.1
	unhealthyThreshold = 0.2
)

const (
	allAgentsFound     = "AllAgentsFound"
	withAgentsNotFound = "withAgentsNotFound"

	allAgentsReady     = "AllAgentsReady"
	withAgentsNotReady = "WithAgentsNotReady"
	withAgentsUnknown  = "WithAgentsUnknown"
)

// agentStatus is inner-defined status definition for agent
type agentStatus string

const (
	agentReady    agentStatus = "Ready"
	agentNotReady agentStatus = "NotReady"
	agentNotFound agentStatus = "NotFound"
)

const (
	metricsNameAgentNotReady      = "agent_not_ready"
	metricsNameAgentNotFound      = "agent_not_found"
	metricsNameAgentReadyTotal    = "agent_ready_total"
	metricsNameAgentNotReadyTotal = "agent_not_ready_total"
	metricsNameAgentNotFoundTotal = "agent_not_found_total"

	metricsNameHealthState = "health_state"

	metricsNameUntaintedCNRCount        = "untainted_cnr_count"
	metricsNameTaintedCNRCount          = "tainted_cnr_count"
	metricsNameEvictedReclaimedPodCount = "evicted_reclaimed_pod_count"

	metricsTagKeyAgentName = "agentName"
	metricsTagKeyNodeName  = "nodeName"
)

type cnrHealthData struct {
	probeTimestamp metav1.Time
	status         agentStatus
}

// cnrHeartBeatMap is used to store health related info
// for each CNR related health state
type cnrHeartBeatMap struct {
	lock        sync.RWMutex
	nodeHealths map[string]map[string]*cnrHealthData
}

func newCNRHeartBeatMap() *cnrHeartBeatMap {
	return &cnrHeartBeatMap{
		nodeHealths: make(map[string]map[string]*cnrHealthData),
	}
}

func (c *cnrHeartBeatMap) setHeartBeatInfo(name string, label string, status agentStatus, timestamp metav1.Time) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.nodeHealths[name] == nil {
		c.nodeHealths[name] = make(map[string]*cnrHealthData)
	}

	if c.nodeHealths[name][label] == nil {
		cnrHealth := &cnrHealthData{
			status:         status,
			probeTimestamp: timestamp,
		}
		c.nodeHealths[name][label] = cnrHealth
		return
	}

	if status == agentReady || status != c.nodeHealths[name][label].status {
		c.nodeHealths[name][label].probeTimestamp = timestamp
		c.nodeHealths[name][label].status = status
	}
}
func (c *cnrHeartBeatMap) getHeartBeatInfo(name string, label string) (cnrHealthData, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	if nodeHealth, nodeOk := c.nodeHealths[name]; nodeOk && nodeHealth != nil {
		if agentHealth, agentOk := nodeHealth[label]; agentOk && agentHealth != nil {
			return *agentHealth, true
		}
	}
	return cnrHealthData{}, false
}
func (c *cnrHeartBeatMap) rangeNode(f func(node string) bool) {
	c.lock.Lock()
	defer c.lock.Unlock()
	for node := range c.nodeHealths {
		shouldContinue := f(node)
		if !shouldContinue {
			break
		}
	}
}

type EvictionController struct {
	ctx        context.Context
	client     *client.GenericClientSet
	now        func() metav1.Time
	cnrControl control.CNRControl

	nodeListerSynced cache.InformerSynced
	nodeLister       corelisters.NodeLister
	podListerSynced  cache.InformerSynced
	podLister        corelisters.PodLister
	cnrListerSynced  cache.InformerSynced
	cnrLister        listers.CustomNodeResourceLister

	// getPodsAssignedToNode is helper function to be used for pod filtering
	getPodsAssignedToNode func(nodeName string) ([]*corev1.Pod, error)
	reclaimedPodFilter    func(pod *corev1.Pod) (bool, error)

	// cnrHeartBeatMap is per CNR map storing last observed health together with a local time when it was observed.
	cnrHeartBeatMap *cnrHeartBeatMap

	// cnrUpdateTimeWindow controlling Controller updating HeartBeatMap TimeWindow.
	cnrUpdateTimeWindow time.Duration
	// cnrMonitorPeriod controlling Controller monitoring period, i.e. how often does Controller
	// check cnr agent health signal posted from kubelet. This value should be lower than
	// cnrMonitorGracePeriod and larger than cnrUpdateTimeWindow
	cnrMonitorPeriod time.Duration
	// cnrMonitorTaintPeriod must be less than cnrMonitorGracePeriod,
	// we only taint unschedulable in cnr.
	cnrMonitorTaintPeriod time.Duration
	// cnrMonitorGracePeriod must be N times more than the cnr health signal
	// update frequency, where N means number of retries allowed for agent to
	// report cnr status. And it can't be too large.
	cnrMonitorGracePeriod time.Duration

	cnrAgentSelector []string
	nodeSelector     labels.Selector

	cnrTaintQueue *scheduler.RateLimitedTimedQueue
	cnrEvictQueue *scheduler.RateLimitedTimedQueue

	unhealthyThreshold float32
	evictionLimiterQPS float32

	// metricsEmitter for emit metrics
	metricsEmitter metrics.MetricEmitter
}

func NewEvictionController(ctx context.Context,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	conf *controller.LifeCycleConfig,
	client *client.GenericClientSet,
	nodeInformer coreinformers.NodeInformer,
	podInformer coreinformers.PodInformer,
	cnrInformer informers.CustomNodeResourceInformer,
	metricsEmitter metrics.MetricEmitter) (*EvictionController, error) {
	ec := &EvictionController{
		ctx:                   ctx,
		now:                   metav1.Now,
		client:                client,
		cnrControl:            control.DummyCNRControl{},
		cnrUpdateTimeWindow:   conf.CNRUpdateTimeWindow,
		cnrMonitorPeriod:      conf.CNRMonitorPeriod,
		cnrMonitorTaintPeriod: conf.CNRMonitorTaintPeriod,
		cnrMonitorGracePeriod: conf.CNRMonitorGracePeriod,
		evictionLimiterQPS:    evictionLimiterQPS,
		unhealthyThreshold:    unhealthyThreshold,
		cnrHeartBeatMap:       newCNRHeartBeatMap(),
		cnrTaintQueue:         scheduler.NewRateLimitedTimedQueue(flowcontrol.NewTokenBucketRateLimiter(evictionLimiterQPS, scheduler.EvictionRateLimiterBurst)),
		cnrEvictQueue:         scheduler.NewRateLimitedTimedQueue(flowcontrol.NewTokenBucketRateLimiter(evictionLimiterQPS, scheduler.EvictionRateLimiterBurst)),
		reclaimedPodFilter:    generic.NewQoSConfiguration().CheckReclaimedQoSForPod,
	}

	if !genericConf.DryRun && !conf.DryRun {
		ec.cnrControl = control.NewCNRControlImpl(client.InternalClient)
	}

	nodeSelector, err := labels.Parse(conf.NodeSelector)
	if err != nil {
		return nil, err
	}
	ec.nodeSelector = nodeSelector

	for _, labelSelector := range conf.CNRAgentSelector {
		if labelSelector == "" {
			continue
		}

		ec.cnrAgentSelector = append(ec.cnrAgentSelector, labelSelector)
	}

	ec.nodeListerSynced = nodeInformer.Informer().HasSynced
	ec.nodeLister = nodeInformer.Lister()

	ec.cnrLister = cnrInformer.Lister()
	ec.cnrListerSynced = cnrInformer.Informer().HasSynced

	ec.podListerSynced = podInformer.Informer().HasSynced
	ec.podLister = podInformer.Lister()
	err = podInformer.Informer().AddIndexers(cache.Indexers{
		nodeNameKeyIndex: func(obj interface{}) ([]string, error) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return []string{}, nil
			}
			if len(pod.Spec.NodeName) == 0 {
				return []string{}, nil
			}
			return []string{pod.Spec.NodeName}, nil
		},
	})
	if err != nil {
		return nil, err
	}

	podIndexer := podInformer.Informer().GetIndexer()
	ec.getPodsAssignedToNode = func(nodeName string) ([]*corev1.Pod, error) {
		objs, err := podIndexer.ByIndex(nodeNameKeyIndex, nodeName)
		if err != nil {
			return nil, err
		}

		pods := make([]*corev1.Pod, 0, len(objs))
		for _, obj := range objs {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				continue
			}
			pods = append(pods, pod)
		}
		return pods, nil
	}

	if metricsEmitter == nil {
		ec.metricsEmitter = metrics.DummyMetrics{}
	} else {
		ec.metricsEmitter = metricsEmitter.WithTags("agent-monitor")
	}

	native.WithPodTransformer(podTransformerFunc)
	return ec, nil
}

func (ec *EvictionController) Run() {
	defer utilruntime.HandleCrash()
	defer klog.Infof("Shutting down %s controller", EvictionControllerName)

	if !cache.WaitForCacheSync(ec.ctx.Done(), ec.nodeListerSynced, ec.cnrListerSynced, ec.podListerSynced) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", EvictionControllerName))
		return
	}
	klog.Infof("Caches are synced for %s controller", EvictionControllerName)

	go wait.Until(ec.tryUpdateCNRHeartBeatMap, ec.cnrUpdateTimeWindow, ec.ctx.Done())
	go wait.Until(ec.syncAgentHealth, ec.cnrMonitorPeriod, ec.ctx.Done())
	go wait.Until(ec.doTaint, scheduler.NodeEvictionPeriod, ec.ctx.Done())
	go wait.Until(ec.doEviction, scheduler.NodeEvictionPeriod, ec.ctx.Done())
	<-ec.ctx.Done()
}

// doTaint is used to pop nodes from to-be-tainted queue,
// and then trigger the taint actions
func (ec *EvictionController) doTaint() {
	ec.cnrTaintQueue.Try(func(value scheduler.TimedValue) (bool, time.Duration) {
		node, err := ec.nodeLister.Get(value.Value)
		if errors.IsNotFound(err) {
			klog.Warningf("Node %v no longer present in nodeLister!", value.Value)
			return true, 0
		} else if err != nil {
			klog.Warningf("Failed to get Node %v from the nodeLister: %v", value.Value, err)
			// retry in 50 millisecond
			return false, 50 * time.Millisecond
		}

		// second confirm that we should taint cnr
		cnr, err := ec.cnrLister.Get(value.Value)
		if errors.IsNotFound(err) {
			klog.Warningf("cnr %v no longer present in cnrLister!", value.Value)
			return true, 0
		} else if err != nil {
			klog.Errorf("Cannot find cnr for node %v err %v", node.Name, err)
			return false, 50 * time.Millisecond
		}

		currentHealthCondition := ec.checkCNRAgentReady(cnr, ec.cnrMonitorTaintPeriod)
		if !currentHealthCondition {
			if err := ec.taintCNR(cnr); err != nil {
				klog.Warningf("Failed to taint for cnr %v: %v", value.Value, err)
				return false, 0
			}
		}
		return true, 0
	})
}

func (ec *EvictionController) taintCNR(cnr *apis.CustomNodeResource) error {
	newCNR, ok, err := util.AddOrUpdateCNRTaint(cnr, &util.NoScheduleForReclaimedTasksTaint)
	if err != nil {
		return err
	}

	if ok {
		_, err = ec.cnrControl.PatchCNRSpecAndMetadata(ec.ctx, cnr.Name, cnr, newCNR)
		if err != nil {
			_ = ec.metricsEmitter.StoreInt64(metricsNameTaintedCNRCount, 1, metrics.MetricTypeNameCount,
				[]metrics.MetricTag{
					{Key: "status", Val: "failed"},
					{Key: "name", Val: cnr.Name},
				}...)
			return err
		}
		_ = ec.metricsEmitter.StoreInt64(metricsNameTaintedCNRCount, 1, metrics.MetricTypeNameCount,
			[]metrics.MetricTag{
				{Key: "status", Val: "success"},
				{Key: "name", Val: cnr.Name},
			}...)
	}
	return nil
}

// unTaintCNR is used to delete taint info from CNR
func (ec *EvictionController) unTaintCNR(cnr *apis.CustomNodeResource) error {
	newCNR, ok, err := util.RemoveCNRTaint(cnr, &util.NoScheduleForReclaimedTasksTaint)
	if err != nil {
		return err
	}

	if ok {
		_, err = ec.cnrControl.PatchCNRSpecAndMetadata(ec.ctx, cnr.Name, cnr, newCNR)
		if err != nil {
			_ = ec.metricsEmitter.StoreInt64(metricsNameUntaintedCNRCount, 1, metrics.MetricTypeNameCount,
				[]metrics.MetricTag{
					{Key: "status", Val: "failed"},
					{Key: "name", Val: cnr.Name},
				}...)
			return err
		}
		_ = ec.metricsEmitter.StoreInt64(metricsNameUntaintedCNRCount, 1, metrics.MetricTypeNameCount,
			[]metrics.MetricTag{
				{Key: "status", Val: "success"},
				{Key: "name", Val: cnr.Name},
			}...)
	}

	return nil
}

// doEviction is used to pop nodes from to-be-evicted queue,
// and then trigger the taint actions
func (ec *EvictionController) doEviction() {
	ec.cnrEvictQueue.Try(func(value scheduler.TimedValue) (bool, time.Duration) {
		node, err := ec.nodeLister.Get(value.Value)
		if errors.IsNotFound(err) {
			klog.Warningf("Node %v no longer present in nodeLister!", value.Value)
			return true, 0
		} else if err != nil {
			klog.Warningf("Failed to get Node %v from the nodeLister: %v", value.Value, err)
			// retry in 50 millisecond
			return false, 50 * time.Millisecond
		}

		cnr, err := ec.cnrLister.Get(value.Value)
		if errors.IsNotFound(err) {
			klog.Warningf("cnr %v no longer present in cnrLister!", value.Value)
			return true, 0
		} else if err != nil {
			klog.Errorf("Cannot find cnr for node %v err %v", node.Name, err)
			return false, 50 * time.Millisecond
		}

		//second confirm that we should evict reclaimed pods
		currentHealthCondition := ec.checkCNRAgentReady(cnr, ec.cnrMonitorGracePeriod)
		if !currentHealthCondition {
			if err := ec.evictReclaimedPods(node); err != nil {
				klog.Warningf("Failed to evict pods for cnr %v: %v", value.Value, err)
				return true, 5 * time.Second
			}
		}
		return true, 0
	})
}

// checkCNRAgentReady is used to check whether those agents (related to CNR)
// are all ready, and return false if any agent works not as expected
func (ec *EvictionController) checkCNRAgentReady(cnr *apis.CustomNodeResource, gracePeriod time.Duration) bool {
	conditionReady := true
	for _, labelSelector := range ec.cnrAgentSelector {
		health, found := ec.cnrHeartBeatMap.getHeartBeatInfo(cnr.Name, labelSelector)
		if found && ec.now().After(health.probeTimestamp.Add(gracePeriod)) {
			conditionReady = false
			break
		}
	}
	return conditionReady
}

// evictReclaimedPods must filter out those pods that should be managed
// todo: do we need some protection logic when during eviction?
func (ec *EvictionController) evictReclaimedPods(node *corev1.Node) error {
	pods, err := ec.getPodsAssignedToNode(node.Name)
	if err != nil {
		return fmt.Errorf("unable to list pods from node %q: %v", node.Name, err)
	}

	var errList []error
	for _, pod := range pods {
		if ok, err := ec.reclaimedPodFilter(pod); err == nil && ok {
			err := ec.client.KubeClient.CoreV1().Pods(pod.Namespace).Delete(ec.ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil {
				_ = ec.metricsEmitter.StoreInt64(metricsNameEvictedReclaimedPodCount, 1, metrics.MetricTypeNameCount,
					[]metrics.MetricTag{
						{Key: "status", Val: "failed"},
						{Key: "name", Val: node.Name},
					}...)
				errList = append(errList, err)
				continue
			}
			_ = ec.metricsEmitter.StoreInt64(metricsNameEvictedReclaimedPodCount, 1, metrics.MetricTypeNameCount,
				[]metrics.MetricTag{
					{Key: "status", Val: "success"},
					{Key: "name", Val: node.Name},
				}...)
		}
	}
	if len(errList) > 0 {
		return utilerrors.NewAggregate(errList)
	}

	return nil
}

// tryUpdateCNRHeartBeatMap is used to periodically sync health state
// of CNR related agent, and store in local cache
func (ec *EvictionController) tryUpdateCNRHeartBeatMap() {
	nodes, err := ec.nodeLister.List(ec.nodeSelector)
	if err != nil {
		klog.Errorf("List nodes error: %v", err)
		return
	}

	totalReadyNode := make(map[string]int64)
	totalNotReadyNode := make(map[string]int64)
	totalNotFoundNode := make(map[string]int64)
	currentNodes := sets.String{}
	for _, node := range nodes {
		baseTags := []metrics.MetricTag{{Key: metricsTagKeyNodeName, Val: node.Name}}
		pods, err := ec.getPodsAssignedToNode(node.Name)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("unable to list pods from node %q: %v", node.Name, err))
			continue
		}

		currentNodes.Insert(node.Name)
		for _, labelSelector := range ec.cnrAgentSelector {
			label, err := labels.Parse(labelSelector)
			if err != nil {
				klog.Errorf("parse label %v error %v", labelSelector, err)
				continue
			}

			agentFound := false
			metricsTags := append(baseTags, metrics.MetricTag{Key: metricsTagKeyAgentName, Val: labelSelector})
			for _, pod := range pods {
				if !label.Matches(labels.Set(pod.Labels)) {
					continue
				}
				agentFound = true

				if native.PodIsReady(pod) {
					ec.cnrHeartBeatMap.setHeartBeatInfo(node.Name, labelSelector, agentReady, ec.now())
					totalReadyNode[labelSelector]++
					break
				} else {
					ec.cnrHeartBeatMap.setHeartBeatInfo(node.Name, labelSelector, agentNotReady, ec.now())
					totalNotReadyNode[labelSelector]++
					klog.Errorf("Agent %v for node %v is not ready", labelSelector, node.Name)
					_ = ec.metricsEmitter.StoreInt64(metricsNameAgentNotReady, 1, metrics.MetricTypeNameRaw, metricsTags...)
				}
			}
			if !agentFound {
				ec.cnrHeartBeatMap.setHeartBeatInfo(node.Name, labelSelector, agentNotFound, ec.now())
				totalNotFoundNode[labelSelector]++
				klog.Errorf("Agent %v for node %v is not found", labelSelector, node.Name)
				_ = ec.metricsEmitter.StoreInt64(metricsNameAgentNotFound, 1, metrics.MetricTypeNameRaw, metricsTags...)
			}
		}
	}

	for _, labelSelector := range ec.cnrAgentSelector {
		tag := metrics.MetricTag{Key: metricsTagKeyAgentName, Val: labelSelector}
		_ = ec.metricsEmitter.StoreInt64(metricsNameAgentReadyTotal, totalReadyNode[labelSelector],
			metrics.MetricTypeNameRaw, tag)
		_ = ec.metricsEmitter.StoreInt64(metricsNameAgentNotReadyTotal, totalNotReadyNode[labelSelector],
			metrics.MetricTypeNameRaw, tag)
		_ = ec.metricsEmitter.StoreInt64(metricsNameAgentNotFoundTotal, totalNotFoundNode[labelSelector],
			metrics.MetricTypeNameRaw, tag)
	}

	ec.cnrHeartBeatMap.rangeNode(func(node string) bool {
		if !currentNodes.Has(node) {
			delete(ec.cnrHeartBeatMap.nodeHealths, node)
		}
		return false
	})
}

// syncAgentHealth is the main logic of health checking,
// and it will push those to-be-handled node into a standard queue
func (ec *EvictionController) syncAgentHealth() {
	nodes, err := ec.nodeLister.List(ec.nodeSelector)
	if err != nil {
		klog.Errorf("list node with select %s err %v", ec.nodeSelector.String(), err)
		return
	}

	readyNodes := 0
	notReadyNodes := 0
	for _, node := range nodes {
		cnr, err := ec.cnrLister.Get(node.Name)
		if err != nil {
			klog.Errorf("get cnr %v failed: %v", node.Name, err)
			continue
		}

		readyCondition, err := ec.tryUpdateCNRCondition(cnr)
		if err != nil {
			klog.Errorf("try update cnr %v error %v", cnr.Name, err)
			continue
		}

		withoutTaintCondition := ec.checkCNRAgentReady(cnr, ec.cnrMonitorTaintPeriod)
		if !readyCondition && !withoutTaintCondition {
			notReadyNodes++
			if !util.CNRTaintExists(cnr.Spec.Taints, &util.NoScheduleForReclaimedTasksTaint) {
				ec.cnrTaintQueue.Add(cnr.Name, string(cnr.UID))
			} else {
				if !ec.checkCNRAgentReady(cnr, ec.cnrMonitorGracePeriod) &&
					ec.checkNodeHasReclaimedPods(node) {
					ec.cnrEvictQueue.Add(cnr.Name, string(cnr.UID))
				}
			}
		} else if readyCondition && withoutTaintCondition {
			readyNodes++
			if err := ec.unTaintCNR(cnr); err != nil {
				klog.Errorf("try de-taint cnr %v error %v", cnr.Name, err)
				continue
			}
		}
	}

	klog.Infof("There are %v ready nodes, %v not ready nodes.", readyNodes, notReadyNodes)
	healthState := ec.computeClusterState(readyNodes, notReadyNodes)
	ec.handleDisruption(healthState)
}

// checkNodeHasReclaimedPods is used to check whether the node contains reclaimed pods,
// only those nodes with reclaimed pods should be triggered with eviction/taint logic
func (ec *EvictionController) checkNodeHasReclaimedPods(node *corev1.Node) bool {
	pods, err := ec.getPodsAssignedToNode(node.Name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("unable to list pods from node %q: %v", node.Name, err))
		return false
	}

	reclaimedPodExist := false
	for _, pod := range pods {
		if ok, err := ec.reclaimedPodFilter(pod); err == nil && ok {
			reclaimedPodExist = true
			break
		}
	}
	return reclaimedPodExist
}

// tryUpdateCNRCondition is used to check CNR related agent health states,
// and then set conditions for those components in CNR CR
func (ec *EvictionController) tryUpdateCNRCondition(cnr *apis.CustomNodeResource) (bool, error) {
	stateMap := map[agentStatus]sets.String{
		agentReady:    {},
		agentNotReady: {},
		agentNotFound: {},
	}
	for _, labelSelector := range ec.cnrAgentSelector {
		health, found := ec.cnrHeartBeatMap.getHeartBeatInfo(cnr.Name, labelSelector)
		if !found {
			return false, fmt.Errorf("node %s agent with label %v health beat not found", cnr.Name, labelSelector)
		}

		stateMap[health.status].Insert(labelSelector)
	}

	var reason, message string
	var status corev1.ConditionStatus

	newCNR := cnr.DeepCopy()
	now := ec.now()
	if stateMap[agentNotFound].Len() > 0 {
		status = corev1.ConditionTrue
		message = fmt.Sprintf("Agent with label %v not found.", stateMap[agentNotFound].List())
		reason = withAgentsNotFound
	} else {
		status = corev1.ConditionFalse
		message = fmt.Sprintf("All agents %v found.", ec.cnrAgentSelector)
		reason = allAgentsFound
	}
	util.SetCNRCondition(newCNR, apis.CNRAgentNotFound, status, reason, message, now)

	if stateMap[agentNotReady].Len() > 0 {
		status = corev1.ConditionFalse
		message = fmt.Sprintf("Agent with label %v not ready.", stateMap[agentNotReady].List())
		reason = withAgentsNotReady
	} else if stateMap[agentReady].Len() != len(ec.cnrAgentSelector) {
		status = corev1.ConditionUnknown
		message = fmt.Sprintf("Partially agent with label %v ready and %v unknown", stateMap[agentReady].List(), stateMap[agentNotFound].List())
		reason = withAgentsUnknown
	} else {
		status = corev1.ConditionTrue
		message = fmt.Sprintf("All agents %v ready.", ec.cnrAgentSelector)
		reason = allAgentsReady
	}
	util.SetCNRCondition(newCNR, apis.CNRAgentReady, status, reason, message, now)

	if !apiequality.Semantic.DeepEqual(cnr.Status, newCNR.Status) {
		_, err := ec.cnrControl.PatchCNRStatus(ec.ctx, cnr.Name, cnr, newCNR)
		if err != nil {
			klog.Errorf("update cnr %v status error %v", cnr.Name, err)
			return false, err
		}
	}

	return stateMap[agentNotReady].Len() == 0 && stateMap[agentNotFound].Len() == 0, nil
}

// computeClusterState returns a slice of CNRReadyConditions for all Nodes in cluster.
// The zone is considered:
// - fullyDisrupted if there are no Ready Nodes
// - partiallyDisrupted if at less than nc.unhealthyZoneThreshold percent of Nodes are not Ready
// - normal otherwise
func (ec *EvictionController) computeClusterState(readyNodes, notReadyNodes int) string {
	switch {
	case readyNodes == 0 && notReadyNodes > 0:
		return stateFullDisruption
	case notReadyNodes > 2 && float32(notReadyNodes)/float32(notReadyNodes+readyNodes) > ec.unhealthyThreshold:
		return statePartialDisruption
	default:
		return stateNormal
	}
}

// handleDisruption is used as a protection logic, if the cluster fall into
// unhealthy state in a large scope, perhaps something goes wrong, we should
// keep the evictions
func (ec *EvictionController) handleDisruption(healthState string) {
	if healthState == stateFullDisruption || healthState == statePartialDisruption {
		// stop all taint and evictions.
		ec.cnrTaintQueue.SwapLimiter(0)
		ec.cnrEvictQueue.SwapLimiter(0)
	} else {
		ec.cnrTaintQueue.SwapLimiter(evictionLimiterQPS)
		ec.cnrEvictQueue.SwapLimiter(evictionLimiterQPS)
	}

	_ = ec.metricsEmitter.StoreInt64(metricsNameHealthState, 1, metrics.MetricTypeNameRaw,
		[]metrics.MetricTag{
			{Key: "status", Val: healthState},
			{Key: "threshold", Val: fmt.Sprintf("%v", ec.unhealthyThreshold)},
		}...)
	klog.Infof("eviction controller detect nodes are %v.", healthState)
}

func podTransformerFunc(src, dest *corev1.Pod) {
	dest.Spec.NodeName = src.Spec.NodeName
}
