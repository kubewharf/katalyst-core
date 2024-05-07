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

package prometheus

import (
	"context"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/collector"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
)

const MetricCollectorNameShardingPrometheus = "sharding-prometheus-collector"

// prometheusShadingCollector implements MetricCollector with wrapped prometheusCollector,
// and it will be responsible for sharding split logic
type prometheusShadingCollector struct{}

var _ collector.MetricCollector = &prometheusShadingCollector{}

func NewPrometheusShadingCollector(ctx context.Context, baseCtx *katalystbase.GenericContext,
	conf *metric.CollectorConfiguration, metricStore store.MetricStore,
) (collector.MetricCollector, error) {
	return &prometheusShadingCollector{}, nil
}

func (p *prometheusShadingCollector) Name() string { return MetricCollectorNameShardingPrometheus }

func (p *prometheusShadingCollector) Start() error { return nil }

func (p *prometheusShadingCollector) Stop() error { return nil }
