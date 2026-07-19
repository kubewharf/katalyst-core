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

package registry

import (
	"fmt"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/cpusetmems"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/cpusettopology"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/systemservice"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/workqueue"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

var defaultPluginFactories = []bulkheadapi.PluginFactory{
	cpusettopology.NewCPUSetTopologyPlugin,
	cpusetmems.NewCPUSetMemsPlugin,
	workqueue.NewWorkqueuePlugin,
	systemservice.NewSystemServicePlugin,
}

func NewDefaultPlugins(conf *config.Configuration) ([]bulkheadapi.Plugin, error) {
	plugins := make([]bulkheadapi.Plugin, 0, len(defaultPluginFactories))
	seen := make(map[string]struct{}, len(defaultPluginFactories))
	for _, factory := range defaultPluginFactories {
		plugin := factory(conf)
		if plugin == nil {
			return nil, fmt.Errorf("bulkhead plugin factory returned nil plugin")
		}
		name := plugin.Name()
		if name == "" {
			return nil, fmt.Errorf("bulkhead plugin name must not be empty")
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("bulkhead plugin %q registered more than once", name)
		}
		seen[name] = struct{}{}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}
