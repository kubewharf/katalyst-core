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

// Package plugin is the package that defines the eviction strategy, and
// those strategies must implement EvictPlugin interface, supporting both
// embedded plugin and external registered plugin.
package plugin // import "github.com/kubewharf/katalyst-core/pkg/evictionmanager/plugin"

import (
	"context"

	"k8s.io/client-go/tools/events"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	endpointpkg "github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	util "github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	fakePluginName = "fake-eviction-plugin"
)

// InitFunc is used to initialize a particular innter eviction plugin.
type InitFunc func(genericClient *client.GenericClientSet, recorder events.EventRecorder, metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter, conf *config.Configuration) EvictionPlugin

// EvictionPlugin performs eviction actions based on agent resources.
type EvictionPlugin interface {
	Name() string
	endpointpkg.Endpoint
}

type DummyEvictionPlugin struct {
	*util.StopControl
}

func (_ DummyEvictionPlugin) Name() string { return fakePluginName }
func (_ DummyEvictionPlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	return nil, nil
}

func (_ DummyEvictionPlugin) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return nil, nil
}

func (_ DummyEvictionPlugin) GetEvictPods(_ context.Context, _ *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	return nil, nil
}

func (_ DummyEvictionPlugin) Start() {
	return
}

var _ EvictionPlugin = DummyEvictionPlugin{}
