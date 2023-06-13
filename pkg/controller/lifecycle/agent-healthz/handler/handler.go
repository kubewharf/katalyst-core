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
	"sync"

	"k8s.io/apimachinery/pkg/labels"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/lifecycle/agent-healthz/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// AgentHandler defines the standard interface to handle
// unhealthy states for each individual agent
type AgentHandler interface {
	// GetEvictionInfo returns eviction item for the given node-name
	GetEvictionInfo(node string) (*helper.EvictItem, bool)

	// GetCNRTaintInfo returns a map mapping for the given node-name
	GetCNRTaintInfo(node string) (*helper.CNRTaintItem, bool)
}

type InitFunc func(ctx context.Context, agent string, emitter metrics.MetricEmitter,
	_ *generic.GenericConfiguration, _ *controller.LifeCycleConfig, nodeSelector labels.Selector,
	podIndexer cache.Indexer, nodeLister corelisters.NodeLister,
	cnrLister listers.CustomNodeResourceLister,
	checker *helper.HealthzHelper) AgentHandler

var handlerMap sync.Map

func RegisterAgentHandlerFunc(name string, f InitFunc) {
	handlerMap.Store(name, f)
}

func GetRegisterAgentHandlerFuncs() map[string]InitFunc {
	handlers := make(map[string]InitFunc)
	handlerMap.Range(func(key, value interface{}) bool {
		handlers[key.(string)] = value.(InitFunc)
		return true
	})
	return handlers
}
