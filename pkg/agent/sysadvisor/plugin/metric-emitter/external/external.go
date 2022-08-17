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

	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const MetricSyncerNameExternal = "external"

type MetricSyncerExternal struct {
	conf *metricemitter.MetricEmitterPluginConfiguration

	metricEmitter metrics.MetricEmitter
	dataEmitter   metrics.MetricEmitter

	metaServer *metaserver.MetaServer
	metaCache  *metacache.MetaCache
}

func NewMetricSyncerExternal(conf *config.Configuration, metricEmitter, dataEmitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer, metaCache *metacache.MetaCache) *MetricSyncerExternal {
	return &MetricSyncerExternal{
		conf: conf.AgentConfiguration.MetricEmitterPluginConfiguration,

		metricEmitter: metricEmitter,
		dataEmitter:   dataEmitter,
		metaServer:    metaServer,
		metaCache:     metaCache,
	}
}

func (e *MetricSyncerExternal) Name() string {
	return MetricSyncerNameExternal
}

func (e *MetricSyncerExternal) Run(ctx context.Context) {
	// todo: replace demo metrics for real external-metrics produced by sys-advisor
	go wait.Until(func() {
		_ = e.dataEmitter.StoreFloat64("external.demo", float64(rand.Intn(100)), metrics.MetricTypeNameRaw, []metrics.MetricTag{
			{
				Key: "timestamp",
				Val: fmt.Sprintf("%v", time.Now().UnixMilli()),
			},
			{
				Key: fmt.Sprintf("%stest", data.CustomMetricLabelSelectorPrefixKey),
				Val: "fake",
			},
		}...)
	}, time.Second*10, ctx.Done())
}
