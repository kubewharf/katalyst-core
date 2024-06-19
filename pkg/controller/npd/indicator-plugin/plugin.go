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

package indicator_plugin

import (
	"context"
	"sync"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

type IndicatorPlugin interface {
	Run()
	Name() string

	GetSupportedNodeMetricsScope() []string
	GetSupportedPodMetricsScope() []string
}

var pluginInitializers sync.Map

type InitFunc func(ctx context.Context, conf *controller.NPDConfig, extraConf interface{},
	controlCtx *katalystbase.GenericContext, updater IndicatorUpdater) (IndicatorPlugin, error)

// RegisterPluginInitializer is used to register user-defined indicator plugins
func RegisterPluginInitializer(name string, initFunc InitFunc) {
	pluginInitializers.Store(name, initFunc)
}

// GetPluginInitializers returns initialized functions of indicator plugins
func GetPluginInitializers() map[string]InitFunc {
	plugins := make(map[string]InitFunc)
	pluginInitializers.Range(func(key, value any) bool {
		plugins[key.(string)] = value.(InitFunc)
		return true
	})
	return plugins
}

type DummyIndicatorPlugin struct {
	NodeMetricsScopes []string
	PodMetricsScopes  []string
}

var _ IndicatorPlugin = DummyIndicatorPlugin{}

func (d DummyIndicatorPlugin) Run() {}

func (d DummyIndicatorPlugin) Name() string { return "dummy-indicator-plugin" }

func (d DummyIndicatorPlugin) GetSupportedNodeMetricsScope() []string {
	return d.NodeMetricsScopes
}

func (d DummyIndicatorPlugin) GetSupportedPodMetricsScope() []string {
	return d.PodMetricsScopes
}
