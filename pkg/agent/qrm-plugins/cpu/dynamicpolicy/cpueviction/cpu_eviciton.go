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

package cpueviction

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func init() {
	RegisterCPUEvictionInitializer(strategy.EvictionNameLoad, strategy.NewCPUPressureLoadEviction)
	RegisterCPUEvictionInitializer(strategy.EvictionNameSuppression, strategy.NewCPUPressureSuppressionEviction)
	RegisterCPUEvictionInitializer(strategy.EvictionNameNumaCpuPressure, strategy.NewCPUPressureUsageEviction)
	RegisterCPUEvictionInitializer(strategy.EvictionNameNumaSysCpuPressure, strategy.NewSysCPUPressureUsageEviction)
}

var cpuEvictionInitializers sync.Map

// InitFunc is used to initialize a particular cpu eviction plugin.
type InitFunc func(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState) (strategy.CPUPressureEviction, error)

func RegisterCPUEvictionInitializer(name string, initFunc InitFunc) {
	cpuEvictionInitializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[string]InitFunc {
	initializers := make(map[string]InitFunc)
	cpuEvictionInitializers.Range(func(key, value interface{}) bool {
		initializers[key.(string)] = value.(InitFunc)
		return true
	})
	return initializers
}

type CPUPressureEviction struct {
	plugins map[string]agent.Component
}

func (c *CPUPressureEviction) Run(ctx context.Context) {
	for _, p := range c.plugins {
		go p.Run(ctx)
	}
	<-ctx.Done()
}

func NewCPUPressureEviction(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState,
) (*CPUPressureEviction, error) {
	eviction, err := newCPUPressureEviction(emitter, metaServer, conf, state)
	if err != nil {
		return nil, fmt.Errorf("create cpu eviction plugin failed: %s", err)
	}

	return eviction, nil
}

func newCPUPressureEviction(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState,
) (*CPUPressureEviction, error) {
	var errList []error

	plugins := make(map[string]agent.Component)
	for name, f := range GetRegisteredInitializers() {
		plugin, err := f(emitter, metaServer, conf, state)
		if err != nil {
			errList = append(errList, err)
			continue
		}

		wrappedEmitter := emitter.WithTags(name)
		pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(strategy.NewCPUPressureEvictionPlugin(plugin, wrappedEmitter),
			[]string{conf.PluginRegistrationDir},
			func(key string, value int64) {
				_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
			})
		if err != nil {
			errList = append(errList, err)
			continue
		}

		plugins[name] = &agent.PluginWrapper{GenericPlugin: pluginWrapper}
	}

	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}

	return &CPUPressureEviction{
		plugins: plugins,
	}, nil
}
