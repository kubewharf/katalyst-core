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

package poweraware

import (
	"context"
	"errors"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

var PluginDisabledError = errors.New("plugin disabled")

type powerAwarePlugin struct {
	name       string
	disabled   bool
	dryRun     bool
	emitter    metrics.MetricEmitter
	controller component.PowerAwareController
}

func (p powerAwarePlugin) Name() string {
	return p.name
}

func (p powerAwarePlugin) Init() error {
	if p.disabled {
		klog.V(4).Infof("pap is disabled")
		return PluginDisabledError
	}
	klog.V(6).Infof("pap initialized")
	return nil
}

func (p powerAwarePlugin) Run(ctx context.Context) {
	klog.V(6).Infof("pap running")
	p.controller.Run(ctx)
	klog.V(6).Infof("pap ran and finished")
}

func NewPowerAwarePlugin(
	pluginName string,
	conf *config.Configuration,
	_ interface{},
	emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer,
	_ metacache.MetaCache,
) (plugin.SysAdvisorPlugin, error) {
	return &powerAwarePlugin{
		name:     pluginName,
		disabled: conf.PowerAwarePluginOptions.Disabled,
		dryRun:   conf.PowerAwarePluginOptions.DryRun,
		emitter:  emitterPool.GetDefaultMetricsEmitter(),
		controller: component.NewController(
			conf.PowerAwarePluginOptions.DryRun,
			metaServer.NodeFetcher,
			metaServer.PodFetcher,
			conf.QoSConfiguration,
			metaServer.ExternalManager,
		),
	}, nil
}
