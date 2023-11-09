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

package disk

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/tools/events"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const EvictionPluginNameRootfsPressure = "rootfs-pressure-plugin"

func NewRootfsEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) plugin.EvictionPlugin {
	return &RootfsPressurePlugin{
		StopControl: process.NewStopControl(time.Time{}),

		emitter:       emitter,
		metaServer:    metaServer,
		dynamicConfig: conf.DynamicAgentConfiguration,
	}
}

// RootfsPressurePlugin implements the EvictionPlugin interface
// It triggers pod eviction based on the numa node pressure
type RootfsPressurePlugin struct {
	*process.StopControl

	emitter       metrics.MetricEmitter
	metaServer    *metaserver.MetaServer
	dynamicConfig *dynamic.DynamicAgentConfiguration
}

func (r *RootfsPressurePlugin) Start() { return }

func (r *RootfsPressurePlugin) Name() string { return EvictionPluginNameRootfsPressure }

func (r *RootfsPressurePlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	resp := &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}
	return resp, nil
}

func (r *RootfsPressurePlugin) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	} else if len(request.ActivePods) == 0 {
		general.Warningf("GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	resp := &pluginapi.GetTopEvictionPodsResponse{}
	return resp, nil
}

func (r *RootfsPressurePlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}
	return &pluginapi.GetEvictPodsResponse{}, nil
}
