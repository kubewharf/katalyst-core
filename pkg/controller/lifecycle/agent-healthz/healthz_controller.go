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

package agent_healthz

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/nodelifecycle/scheduler"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	informers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/node/v1alpha1"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/lifecycle/agent-healthz/handler"
	"github.com/kubewharf/katalyst-core/pkg/controller/lifecycle/agent-healthz/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const AgentHealthzControllerName = "agent-healthz"

const metricsNameHealthState = "health_state"

const (
	stateNormal            = "Normal"
	stateFullDisruption    = "FullDisruption"
	statePartialDisruption = "PartialDisruption"
)

type HealthzController struct {
	ctx     context.Context
	emitter metrics.MetricEmitter

	nodeLister       corelisters.NodeLister
	cnrLister        listers.CustomNodeResourceLister
	nodeListerSynced cache.InformerSynced
	podListerSynced  cache.InformerSynced
	cnrListerSynced  cache.InformerSynced

	taintThreshold     float32
	taintLimiterQOS    float32
	evictThreshold     float32
	evictionLimiterQPS float32
	nodeSelector       labels.Selector

	taintQueue *scheduler.RateLimitedTimedQueue
	evictQueue *scheduler.RateLimitedTimedQueue

	taintHelper   *helper.CNRTaintHelper
	evictHelper   *helper.EvictHelper
	healthzHelper *helper.HealthzHelper
	handlers      map[string]handler.AgentHandler
}

func NewHealthzController(ctx context.Context,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	conf *controller.LifeCycleConfig,
	client *client.GenericClientSet,
	nodeInformer coreinformers.NodeInformer,
	podInformer coreinformers.PodInformer,
	cnrInformer informers.CustomNodeResourceInformer,
	metricsEmitter metrics.MetricEmitter,
) (*HealthzController, error) {
	ec := &HealthzController{
		ctx:     ctx,
		emitter: metricsEmitter,

		taintThreshold:     conf.DisruptionTaintThreshold,
		taintLimiterQOS:    conf.TaintQPS,
		evictThreshold:     conf.DisruptionEvictThreshold,
		evictionLimiterQPS: conf.EvictQPS,
		nodeSelector:       conf.NodeSelector,

		taintQueue: scheduler.NewRateLimitedTimedQueue(flowcontrol.NewTokenBucketRateLimiter(conf.TaintQPS, scheduler.EvictionRateLimiterBurst)),
		evictQueue: scheduler.NewRateLimitedTimedQueue(flowcontrol.NewTokenBucketRateLimiter(conf.EvictQPS, scheduler.EvictionRateLimiterBurst)),

		handlers: make(map[string]handler.AgentHandler),
	}

	var (
		cnrControl control.CNRControl = control.DummyCNRControl{}
		podControl control.PodEjector = control.DummyPodEjector{}
	)
	if !genericConf.DryRun && !conf.DryRun {
		cnrControl = control.NewCNRControlImpl(client.InternalClient)
		podControl = control.NewRealPodEjector(client.KubeClient)
	}

	ec.nodeListerSynced = nodeInformer.Informer().HasSynced
	ec.nodeLister = nodeInformer.Lister()

	ec.cnrLister = cnrInformer.Lister()
	ec.cnrListerSynced = cnrInformer.Informer().HasSynced

	ec.podListerSynced = podInformer.Informer().HasSynced
	podIndexer := podInformer.Informer().GetIndexer()

	if err := native.AddNodeNameIndexerForPod(podInformer); err != nil {
		return nil, err
	}

	if metricsEmitter == nil {
		ec.emitter = metrics.DummyMetrics{}
	} else {
		ec.emitter = metricsEmitter.WithTags("agent-healthz")
	}

	ec.healthzHelper = helper.NewHealthzHelper(ctx, conf, ec.emitter, ec.nodeSelector, conf.AgentSelector, podIndexer, ec.nodeLister, ec.cnrLister)
	ec.taintHelper = helper.NewTaintHelper(ctx, ec.emitter, cnrControl, ec.nodeLister, ec.cnrLister, ec.taintQueue, ec.healthzHelper)
	ec.evictHelper = helper.NewEvictHelper(ctx, ec.emitter, podControl, ec.nodeLister, ec.cnrLister, ec.evictQueue, ec.healthzHelper)

	registeredHandlerFuncs := handler.GetRegisterAgentHandlerFuncs()
	for agent := range conf.AgentSelector {
		initFunc := handler.NewGenericAgentHandler

		if handlerName, handlerExist := conf.AgentHandlers[agent]; handlerExist {
			if handlerFunc, funcExist := registeredHandlerFuncs[handlerName]; funcExist {
				initFunc = handlerFunc
			}
		}

		ec.handlers[agent] = initFunc(ctx, agent, ec.emitter, genericConf, conf,
			ec.nodeSelector, podIndexer, ec.nodeLister, ec.cnrLister, ec.healthzHelper)
	}

	native.SetPodTransformer(podTransformerFunc)
	return ec, nil
}

func (ec *HealthzController) Run() {
	defer utilruntime.HandleCrash()
	defer klog.Infof("shutting down %s controller", AgentHealthzControllerName)

	if !cache.WaitForCacheSync(ec.ctx.Done(), ec.nodeListerSynced, ec.cnrListerSynced, ec.podListerSynced) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", AgentHealthzControllerName))
		return
	}
	klog.Infof("caches are synced for %s controller", AgentHealthzControllerName)

	go wait.Until(ec.syncAgentHealth, time.Second*30, ec.ctx.Done())
	ec.healthzHelper.Run()
	ec.taintHelper.Run()
	ec.evictHelper.Run()
	<-ec.ctx.Done()
}

// syncAgentHealth is the main logic of health checking,
// and it will push those to-be-handled node into a standard queue
func (ec *HealthzController) syncAgentHealth() {
	nodes, err := ec.nodeLister.List(ec.nodeSelector)
	if err != nil {
		klog.Errorf("list node with select %s err %v", ec.nodeSelector.String(), err)
		return
	}

	taints := make(map[string]*helper.CNRTaintItem)
	evicts := make(map[string]*helper.EvictItem)
	for _, node := range nodes {
		for _, h := range ec.handlers {
			if item, ok := h.GetCNRTaintInfo(node.Name); ok && item != nil && item.Taints != nil {
				if _, exist := taints[node.Name]; !exist {
					taints[node.Name] = &helper.CNRTaintItem{
						Taints: make(map[string]*apis.Taint),
					}
				}

				for t, taint := range item.Taints {
					taints[node.Name].Taints[t] = taint
				}
			}

			if item, ok := h.GetEvictionInfo(node.Name); ok && item != nil && len(item.PodKeys) > 0 {
				if _, exist := evicts[node.Name]; !exist {
					evicts[node.Name] = &helper.EvictItem{
						PodKeys: make(map[string][]string),
					}
				}

				for agent, pods := range item.PodKeys {
					evicts[node.Name].PodKeys[agent] = pods
				}
			}
		}
	}

	klog.Infof("we need to taint %v nodes, evict %v nodes in total", len(taints), len(evicts))

	taintState := ec.computeClusterState(len(nodes), len(taints), ec.taintThreshold)
	ec.handleTaintDisruption(taintState)
	for _, node := range nodes {
		if item, ok := taints[node.Name]; ok {
			ec.taintQueue.Add(node.Name, item)
		} else {
			ec.taintQueue.Remove(node.Name)
			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				return ec.taintHelper.TryUNTaintCNR(node.Name)
			}); err != nil {
				klog.Infof("untaint %v err: %v", node.Name, err)
			}
		}
	}

	evictState := ec.computeClusterState(len(nodes), len(evicts), ec.evictThreshold)
	ec.handleEvictDisruption(evictState)
	for _, node := range nodes {
		if _, ok := evicts[node.Name]; ok {
			ec.evictQueue.Add(node.Name, evicts[node.Name])
		} else {
			ec.evictQueue.Remove(node.Name)
		}
	}
}

// computeClusterState returns a slice of conditions considering all nodes in a zone
// - fullyDisrupted if there are no Ready Nodes
// - partiallyDisrupted if at less than nc.unhealthyZoneThreshold percent of Nodes are not Ready
// - normal otherwise
func (ec *HealthzController) computeClusterState(readyNodes, notReadyNodes int, threshold float32) string {
	switch {
	case readyNodes == 0 && notReadyNodes > 0:
		return stateFullDisruption
	case notReadyNodes > 2 && float32(notReadyNodes)/float32(notReadyNodes+readyNodes) > threshold:
		return statePartialDisruption
	default:
		return stateNormal
	}
}

// handleTaintDisruption is used as a protection logic, if the cluster fall into
// unhealthy state in a large scope, perhaps something goes wrong, we should hold on tainting
func (ec *HealthzController) handleTaintDisruption(healthState string) {
	if healthState == stateFullDisruption || healthState == statePartialDisruption {
		ec.taintQueue.SwapLimiter(0)
	} else {
		ec.taintQueue.SwapLimiter(ec.taintLimiterQOS)
	}

	_ = ec.emitter.StoreInt64(metricsNameHealthState, 1, metrics.MetricTypeNameRaw,
		[]metrics.MetricTag{
			{Key: "action", Val: "taint"},
			{Key: "status", Val: healthState},
			{Key: "threshold", Val: fmt.Sprintf("%v", ec.taintThreshold)},
		}...)
	klog.Infof("controller detect taint states for nodes are %v", healthState)
}

// handleDisruption is used as a protection logic, if the cluster fall into
// unhealthy state in a large scope, perhaps something goes wrong, we should hold on evicting
func (ec *HealthzController) handleEvictDisruption(healthState string) {
	if healthState == stateFullDisruption || healthState == statePartialDisruption {
		ec.evictQueue.SwapLimiter(0)
	} else {
		ec.evictQueue.SwapLimiter(ec.taintLimiterQOS)
	}

	_ = ec.emitter.StoreInt64(metricsNameHealthState, 1, metrics.MetricTypeNameRaw,
		[]metrics.MetricTag{
			{Key: "action", Val: "evict"},
			{Key: "status", Val: healthState},
			{Key: "threshold", Val: fmt.Sprintf("%v", ec.evictThreshold)},
		}...)
	klog.Infof("controller detect taint states for nodes are %v", healthState)
}

func podTransformerFunc(src, dest *corev1.Pod) {
	dest.Spec.NodeName = src.Spec.NodeName
	containersTransformerFunc(&src.Spec.Containers, &dest.Spec.Containers)
	containerStatusesTransformerFunc(&src.Status.ContainerStatuses, &dest.Status.ContainerStatuses)
}

func containersTransformerFunc(src, dst *[]corev1.Container) {
	if src == nil || len(*src) == 0 {
		return
	}

	if len(*dst) == 0 {
		*dst = make([]corev1.Container, len(*src))
	}

	for i, c := range *src {
		(*dst)[i].Name = c.Name
	}
}

func containerStatusesTransformerFunc(src, dst *[]corev1.ContainerStatus) {
	if src == nil || len(*src) == 0 {
		return
	}

	if len(*dst) == 0 {
		*dst = make([]corev1.ContainerStatus, len(*src))
	}

	for i, c := range *src {
		(*dst)[i].Name = c.Name
		(*dst)[i].Ready = c.Ready
	}
}
