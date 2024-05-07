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

package helper

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
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

	metricsTagKeyAgentName = "agentName"
	metricsTagKeyNodeName  = "nodeName"
)

type healthData struct {
	probeTimestamp metav1.Time
	status         agentStatus
}

// heartBeatMap is used to store health related info for each agent
type heartBeatMap struct {
	lock        sync.RWMutex
	nodeHealths map[string]map[string]*healthData // map from node->pod->data
}

func newHeartBeatMap() *heartBeatMap {
	return &heartBeatMap{
		nodeHealths: make(map[string]map[string]*healthData),
	}
}

func (c *heartBeatMap) setHeartBeatInfo(node, agent string, status agentStatus, timestamp metav1.Time) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.nodeHealths[node] == nil {
		c.nodeHealths[node] = make(map[string]*healthData)
	}

	if c.nodeHealths[node][agent] == nil {
		nodeHealth := &healthData{
			status:         status,
			probeTimestamp: timestamp,
		}
		c.nodeHealths[node][agent] = nodeHealth
		return
	}

	if status != c.nodeHealths[node][agent].status {
		c.nodeHealths[node][agent].probeTimestamp = timestamp
		c.nodeHealths[node][agent].status = status
	}
}

func (c *heartBeatMap) getHeartBeatInfo(node, agent string) (healthData, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	if nodeHealth, nodeOk := c.nodeHealths[node]; nodeOk && nodeHealth != nil {
		if agentHealth, agentOk := nodeHealth[agent]; agentOk && agentHealth != nil {
			return *agentHealth, true
		}
	}
	return healthData{}, false
}

func (c *heartBeatMap) rangeNode(f func(node string) bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	for node := range c.nodeHealths {
		shouldContinue := f(node)
		if !shouldContinue {
			break
		}
	}
}

type HealthzHelper struct {
	ctx     context.Context
	emitter metrics.MetricEmitter

	checkWindow          time.Duration
	unhealthyPeriod      time.Duration
	agentUnhealthyPeriod map[string]time.Duration
	nodeSelector         labels.Selector
	agentSelectors       map[string]labels.Selector

	podIndexer cache.Indexer
	nodeLister corelisters.NodeLister
	cnrLister  listers.CustomNodeResourceLister

	healthzMap *heartBeatMap
}

// NewHealthzHelper todo add logic here
func NewHealthzHelper(ctx context.Context, conf *controller.LifeCycleConfig, emitter metrics.MetricEmitter,
	nodeSelector labels.Selector, agentSelectors map[string]labels.Selector, podIndexer cache.Indexer,
	nodeLister corelisters.NodeLister, cnrLister listers.CustomNodeResourceLister,
) *HealthzHelper {
	return &HealthzHelper{
		ctx:     ctx,
		emitter: emitter,

		checkWindow:          conf.CheckWindow,
		unhealthyPeriod:      conf.UnhealthyPeriods,
		agentUnhealthyPeriod: conf.AgentUnhealthyPeriods,
		nodeSelector:         nodeSelector,
		agentSelectors:       agentSelectors,

		podIndexer: podIndexer,
		nodeLister: nodeLister,
		cnrLister:  cnrLister,

		healthzMap: newHeartBeatMap(),
	}
}

func (h *HealthzHelper) Run() {
	go wait.Until(h.syncHeartBeatMap, h.checkWindow, h.ctx.Done())
}

// CheckAllAgentReady checks whether all agents are ready
func (h *HealthzHelper) CheckAllAgentReady(node string) bool {
	for agent := range h.agentSelectors {
		if !h.CheckAgentReady(node, agent) {
			return false
		}
	}
	return true
}

// CheckAgentReady checks whether the given agent is ready
func (h *HealthzHelper) CheckAgentReady(node string, agent string) bool {
	period := h.unhealthyPeriod
	if p, ok := h.agentUnhealthyPeriod[agent]; ok {
		period = p
	}

	health, found := h.healthzMap.getHeartBeatInfo(node, agent)
	if found && health.status != agentReady && metav1.Now().After(health.probeTimestamp.Add(period)) {
		return false
	}
	return true
}

// syncHeartBeatMap is used to periodically sync health state ans s
func (h *HealthzHelper) syncHeartBeatMap() {
	nodes, err := h.nodeLister.List(h.nodeSelector)
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
		pods, err := native.GetPodsAssignedToNode(node.Name, h.podIndexer)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("unable to list pods from node %q: %v", node.Name, err))
			continue
		}

		currentNodes.Insert(node.Name)
		for agent, selector := range h.agentSelectors {
			agentFound := false
			metricsTags := append(baseTags, metrics.MetricTag{Key: metricsTagKeyAgentName, Val: agent})
			// todo: it may be too time-consuming to walk through all pods
			for _, pod := range pods {
				if !selector.Matches(labels.Set(pod.Labels)) {
					continue
				}
				agentFound = true

				klog.Infof("agent %v for node %v found: %v", agent, node.Name, pod.Name)
				if native.PodIsReady(pod) {
					h.healthzMap.setHeartBeatInfo(node.Name, agent, agentReady, metav1.Now())
					totalReadyNode[agent]++
					klog.Infof("agent %v for node %v found: %v, is ready", agent, node.Name, pod.Name)
					break
				} else {
					h.healthzMap.setHeartBeatInfo(node.Name, agent, agentNotReady, metav1.Now())
					totalNotReadyNode[agent]++
					klog.Errorf("agent %v for node %v is not ready", agent, node.Name)
					_ = h.emitter.StoreInt64(metricsNameAgentNotReady, 1, metrics.MetricTypeNameRaw, metricsTags...)
				}
			}

			if !agentFound {
				h.healthzMap.setHeartBeatInfo(node.Name, agent, agentNotFound, metav1.Now())
				totalNotFoundNode[agent]++
				klog.Errorf("agent %v for node %v is not found", agent, node.Name)
				_ = h.emitter.StoreInt64(metricsNameAgentNotFound, 1, metrics.MetricTypeNameRaw, metricsTags...)
			}
		}
	}

	for agent := range h.agentSelectors {
		tag := metrics.MetricTag{Key: metricsTagKeyAgentName, Val: agent}
		_ = h.emitter.StoreInt64(metricsNameAgentReadyTotal, totalReadyNode[agent],
			metrics.MetricTypeNameRaw, tag)
		_ = h.emitter.StoreInt64(metricsNameAgentNotReadyTotal, totalNotReadyNode[agent],
			metrics.MetricTypeNameRaw, tag)
		_ = h.emitter.StoreInt64(metricsNameAgentNotFoundTotal, totalNotFoundNode[agent],
			metrics.MetricTypeNameRaw, tag)
	}

	h.healthzMap.rangeNode(func(node string) bool {
		if !currentNodes.Has(node) {
			delete(h.healthzMap.nodeHealths, node)
		}
		return false
	})
}
