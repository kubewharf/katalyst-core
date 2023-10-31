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

package overcommitmentaware

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/overcommitmentaware/realtime"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/overcommitmentaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

const (
	PluginName = "overcommitment-aware-plugin"
)

// OvercommitmentAwarePlugin calculates node overcommitment ratio,
// values will be reported to node KCNR annotations by the reporter.
type OvercommitmentAwarePlugin struct {
	name string

	realtimeAdvisor *realtime.RealtimeOvercommitmentAdvisor
	reporter        reporter.OvercommitRatioReporter

	emitter metrics.MetricEmitter
}

func NewOvercommitmentAwarePlugin(
	pluginName string, conf *config.Configuration,
	_ interface{},
	emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer,
	_ metacache.MetaCache,
) (plugin.SysAdvisorPlugin, error) {
	emitter := emitterPool.GetDefaultMetricsEmitter()

	realtimeOvercommitmentAdvisor := realtime.NewRealtimeOvercommitmentAdvisor(conf, metaServer, emitter)

	overcommitRatioReporter, err := reporter.NewOvercommitRatioReporter(emitter, conf, realtimeOvercommitmentAdvisor)
	if err != nil {
		return nil, err
	}

	op := &OvercommitmentAwarePlugin{
		name: pluginName,

		realtimeAdvisor: realtimeOvercommitmentAdvisor,
		reporter:        overcommitRatioReporter,
	}

	return op, nil
}

func (op *OvercommitmentAwarePlugin) Run(ctx context.Context) {
	go op.realtimeAdvisor.Run(ctx)

	go op.reporter.Run(ctx)
}

func (op *OvercommitmentAwarePlugin) Name() string {
	return op.name
}

func (op *OvercommitmentAwarePlugin) Init() error {
	return nil
}
