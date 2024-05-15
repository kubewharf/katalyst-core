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

package metric_emitter

import (
	"context"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/syncer"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/syncer/external"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/syncer/node"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/syncer/pod"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func init() {
	syncer.RegisterMetricSyncInitializers(types.MetricSyncerNamePod, pod.NewMetricSyncerPod)
	syncer.RegisterMetricSyncInitializers(types.MetricSyncerNameNode, node.NewMetricSyncerNode)
	syncer.RegisterMetricSyncInitializers(types.MetricSyncerNameExternal, external.NewMetricSyncerExternal)
}

const PluginNameCustomMetricEmitter = "metric-emitter-plugin"

type CustomMetricEmitter struct {
	name    string
	syncers []syncer.CustomMetricSyncer
}

func NewCustomMetricEmitter(pluginName string, conf *config.Configuration, extraConf interface{}, emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer, metaCache metacache.MetaCache,
) (plugin.SysAdvisorPlugin, error) {
	metricEmitter := emitterPool.GetDefaultMetricsEmitter().WithTags("custom-metric")

	var syncers []syncer.CustomMetricSyncer
	for _, name := range conf.AgentConfiguration.SysAdvisorPluginsConfiguration.MetricSyncers {
		if f, ok := syncer.GetRegisteredMetricSyncers()[name]; ok {
			if s, err := f(conf, extraConf, metricEmitter, emitterPool, metaServer, metaCache); err != nil {
				return plugin.DummySysAdvisorPlugin{}, err
			} else {
				syncers = append(syncers, s)
			}
		}
	}

	return &CustomMetricEmitter{
		name:    pluginName,
		syncers: syncers,
	}, nil
}

func (cme *CustomMetricEmitter) Name() string {
	return cme.name
}

func (cme *CustomMetricEmitter) Init() error {
	return nil
}

// Run is the main logic to get metric data from multiple sources
// and emit through the standard emit interface.
func (cme *CustomMetricEmitter) Run(ctx context.Context) {
	klog.Info("custom metrics emitter stated")

	for _, s := range cme.syncers {
		s.Run(ctx)
		klog.Infof("run metric emitter %v successfully", s.Name())
	}
	klog.Infof("run all metric emitters successfully")
	<-ctx.Done()
}
