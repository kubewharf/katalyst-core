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

package handler

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/lifecycle/agent-healthz/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const AgentHandlerGeneric = "generic"

func init() {
	RegisterAgentHandlerFunc(AgentHandlerGeneric, NewGenericAgentHandler)
}

// GenericAgentHandler implements AgentHandler with generic
// actions: i.e. taint cnr and trigger eviction for reclaimed_cores
type GenericAgentHandler struct {
	ctx     context.Context
	agent   string
	emitter metrics.MetricEmitter

	nodeSelector labels.Selector
	qosConf      *generic.QoSConfiguration

	podIndexer cache.Indexer
	nodeLister corelisters.NodeLister
	cnrLister  listers.CustomNodeResourceLister

	checker *helper.HealthzHelper
}

func NewGenericAgentHandler(ctx context.Context, agent string, emitter metrics.MetricEmitter,
	genericConf *generic.GenericConfiguration, _ *controller.LifeCycleConfig, nodeSelector labels.Selector,
	podIndexer cache.Indexer, nodeLister corelisters.NodeLister, cnrLister listers.CustomNodeResourceLister,
	checker *helper.HealthzHelper,
) AgentHandler {
	return &GenericAgentHandler{
		ctx:     ctx,
		agent:   agent,
		emitter: emitter,

		nodeSelector: nodeSelector,
		qosConf:      genericConf.QoSConfiguration,

		podIndexer: podIndexer,
		nodeLister: nodeLister,
		cnrLister:  cnrLister,

		checker: checker,
	}
}

func (g *GenericAgentHandler) GetEvictionInfo(nodeName string) (*helper.EvictItem, bool) {
	node, err := g.nodeLister.Get(nodeName)
	if err != nil {
		klog.Errorf("get cnr %v failed: %v", node, err)
		return nil, false
	}

	if g.checker.CheckAgentReady(nodeName, g.agent) {
		// not to trigger eviction if agent is still ready
		return nil, false
	}

	pods := g.getNodeReclaimedPods(node)
	if len(pods) == 0 {
		// only need to evict reclaimed pods
		return nil, false
	}

	return &helper.EvictItem{
		PodKeys: map[string][]string{
			nodeName: pods,
		},
	}, true
}

func (g *GenericAgentHandler) GetCNRTaintInfo(nodeName string) (*helper.CNRTaintItem, bool) {
	cnr, err := g.cnrLister.Get(nodeName)
	if err != nil {
		klog.Errorf("get cnr %v failed: %v", nodeName, err)
		return nil, false
	}

	if g.checker.CheckAgentReady(nodeName, g.agent) {
		// not to trigger eviction if agent is still ready
		return nil, false
	} else if util.CNRTaintExists(cnr.Spec.Taints, helper.TaintNoScheduler) {
		// if taint already exists, not to trigger taints
		return nil, false
	}

	return &helper.CNRTaintItem{
		Taints: map[string]*apis.Taint{
			helper.TaintNameNoScheduler: helper.TaintNoScheduler,
		},
	}, true
}

// getNodeReclaimedPods returns reclaimed pods contained in the given node,
// only those nodes with reclaimed pods should be triggered with eviction/taint logic for generic agents
func (g *GenericAgentHandler) getNodeReclaimedPods(node *corev1.Node) (names []string) {
	pods, err := native.GetPodsAssignedToNode(node.Name, g.podIndexer)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("unable to list pods from node %q: %v", node.Name, err))
		return
	}

	for _, pod := range pods {
		if ok, err := g.qosConf.CheckReclaimedQoSForPod(pod); err == nil && ok {
			names = append(names, pod.Name)
		}
	}
	return
}
