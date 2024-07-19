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

package metrics_plugin

import (
	"context"
	"sync"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

type MetricsPlugin interface {
	Run()
	Name() string

	GetSupportedNodeMetricsScope() []string
	GetSupportedPodMetricsScope() []string
}

var pluginInitializers sync.Map

type InitFunc func(ctx context.Context, conf *controller.NPDConfig, extraConf interface{},
	controlCtx *katalystbase.GenericContext, updater MetricsUpdater) (MetricsPlugin, error)

// RegisterPluginInitializer is used to register user-defined metrics plugins
func RegisterPluginInitializer(name string, initFunc InitFunc) {
	pluginInitializers.Store(name, initFunc)
}

// GetPluginInitializers returns initialized functions of metrics plugins
func GetPluginInitializers() map[string]InitFunc {
	plugins := make(map[string]InitFunc)
	pluginInitializers.Range(func(key, value any) bool {
		plugins[key.(string)] = value.(InitFunc)
		return true
	})
	return plugins
}

type DummyMetricsPlugin struct {
	NodeMetricsScopes []string
	PodMetricsScopes  []string
}

var _ MetricsPlugin = DummyMetricsPlugin{}

func (d DummyMetricsPlugin) Run() {}

func (d DummyMetricsPlugin) Name() string { return "dummy-metrics-plugin" }

func (d DummyMetricsPlugin) GetSupportedNodeMetricsScope() []string {
	return d.NodeMetricsScopes
}

func (d DummyMetricsPlugin) GetSupportedPodMetricsScope() []string {
	return d.PodMetricsScopes
}
