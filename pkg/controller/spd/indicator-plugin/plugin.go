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

	"k8s.io/apimachinery/pkg/runtime/schema"

	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// IndicatorPlugin represent an implementation for indicator sources;
// each plugin will implement its own indicator-obtain logic and use
// IndicatorUpdater to set the latest indicator info.
type IndicatorPlugin interface {
	// Run starts the main loop for indicator plugin
	Run()
	Name() string

	// GetSupportedBusinessIndicatorSpec + GetSupportedSystemIndicatorSpec + GetSupportedBusinessIndicatorStatus
	// Those methods below returns the supported indicator names, if any name
	// is not supported by any indicator plugin, the controller will clear in CR.
	GetSupportedBusinessIndicatorSpec() []apiworkload.ServiceBusinessIndicatorName
	GetSupportedSystemIndicatorSpec() []apiworkload.ServiceSystemIndicatorName
	GetSupportedBusinessIndicatorStatus() []apiworkload.ServiceBusinessIndicatorName
}

type DummyIndicatorPlugin struct {
	SystemSpecNames     []apiworkload.ServiceSystemIndicatorName
	BusinessSpecNames   []apiworkload.ServiceBusinessIndicatorName
	BusinessStatusNames []apiworkload.ServiceBusinessIndicatorName
}

var _ IndicatorPlugin = DummyIndicatorPlugin{}

func (d DummyIndicatorPlugin) Run()         {}
func (d DummyIndicatorPlugin) Name() string { return "dummy-indicator-plugin" }
func (d DummyIndicatorPlugin) GetSupportedBusinessIndicatorSpec() []apiworkload.ServiceBusinessIndicatorName {
	return d.BusinessSpecNames
}
func (d DummyIndicatorPlugin) GetSupportedSystemIndicatorSpec() []apiworkload.ServiceSystemIndicatorName {
	return d.SystemSpecNames
}
func (d DummyIndicatorPlugin) GetSupportedBusinessIndicatorStatus() []apiworkload.ServiceBusinessIndicatorName {
	return d.BusinessStatusNames
}

// pluginInitializers is used to store the initializing function for each plugin
var pluginInitializers sync.Map

type InitFunc func(ctx context.Context, conf *controller.SPDConfig, extraConf interface{},
	spdWorkloadInformer map[schema.GroupVersionResource]native.DynamicInformer,
	controlCtx *katalystbase.GenericContext, updater IndicatorUpdater) (IndicatorPlugin, error)

// RegisterPluginInitializer is used to register user-defined indicator plugins
func RegisterPluginInitializer(name string, initFunc InitFunc) {
	pluginInitializers.Store(name, initFunc)
}

// GetPluginInitializers returns initialized functions of indicator plugins
func GetPluginInitializers() map[string]InitFunc {
	plugins := make(map[string]InitFunc)
	pluginInitializers.Range(func(key, value interface{}) bool {
		plugins[key.(string)] = value.(InitFunc)
		return true
	})
	return plugins
}
