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

package util

import (
	"context"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// AllocationHandler and HintHandler are used as standard functions
// for qrm plugins to acquire resource allocation/hint info
type AllocationHandler func(context.Context, *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error)
type HintHandler func(context.Context, *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error)

// EnhancementHandler is used as standard function for qrm plugins
// to do resource enhancement for the specific lifecycle phase of qrm
type EnhancementHandler func(ctx context.Context,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	req interface{},
	podResourceEntries interface{}) error

// ResourceEnhancementHandlerMap is used to store enhancement handlers for specific resource
// the first key is the lifecycle phase of qrm, like QRMPhaseGetTopologyHints, QRMPhaseRemovePod etc.
// the second key is the last level of enhancement key, like PodAnnotationMemoryEnhancementOOMPriority in
// memory enhancement
type ResourceEnhancementHandlerMap map[apiconsts.QRMPhase]map[string]EnhancementHandler

// Register registers a handler for the specified phase and last level enhancement key
func (r ResourceEnhancementHandlerMap) Register(
	phase apiconsts.QRMPhase, enhancementKey string, handler EnhancementHandler) {
	if _, ok := r[phase]; !ok {
		r[phase] = make(map[string]EnhancementHandler)
	}

	r[phase][enhancementKey] = handler
}
