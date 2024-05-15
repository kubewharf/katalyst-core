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

package external

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/syncer"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

type MetricSyncerExternal struct {
	conf *metricemitter.MetricEmitterPluginConfiguration
	node *v1.Node

	metricEmitter metrics.MetricEmitter
	dataEmitter   metrics.MetricEmitter

	metaServer *metaserver.MetaServer
	metaReader metacache.MetaReader
}

func NewMetricSyncerExternal(conf *config.Configuration, _ interface{},
	metricEmitter metrics.MetricEmitter, emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer, metaReader metacache.MetaReader,
) (syncer.CustomMetricSyncer, error) {
	dataEmitter, err := emitterPool.GetMetricsEmitter(metricspool.PrometheusMetricOptions{
		Path: metrics.PrometheusMetricPathNameCustomMetric,
	})
	if err != nil {
		klog.Errorf("[cus-metric-emitter] failed to init metric emitter: %v", err)
		return nil, err
	}

	return &MetricSyncerExternal{
		conf: conf.AgentConfiguration.MetricEmitterPluginConfiguration,

		metricEmitter: metricEmitter,
		dataEmitter:   dataEmitter,
		metaServer:    metaServer,
		metaReader:    metaReader,
	}, nil
}

func (e *MetricSyncerExternal) Name() string {
	return types.MetricSyncerNameExternal
}

func (e *MetricSyncerExternal) Run(ctx context.Context) {
	// todo: replace demo metrics for real external-metrics produced by sys-advisor
	go wait.Until(func() {
		if e.node == nil {
			node, err := e.metaServer.GetNode(ctx)
			if err != nil {
				klog.Warningf("get current node failed: %v", err)
				return
			}
			e.node = node
		}

		tags := []metrics.MetricTag{
			{
				Key: "timestamp",
				Val: fmt.Sprintf("%v", time.Now().UnixMilli()),
			},
			{
				Key: fmt.Sprintf("%stest", data.CustomMetricLabelSelectorPrefixKey),
				Val: "fake",
			},
			{
				Key: fmt.Sprintf("%stest", data.CustomMetricLabelSelectorPrefixKey),
				Val: "fake",
			},
		}
		if e.node != nil {
			tags = append(tags, metrics.MetricTag{
				Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyObjectName),
				Val: e.node.Name,
			})
		}

		_ = e.dataEmitter.StoreFloat64("external.demo", float64(rand.Intn(100)), metrics.MetricTypeNameRaw, tags...)
	}, time.Minute, ctx.Done())
}
