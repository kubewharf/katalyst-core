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

package strategy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	nodev1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	util "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/threshold"
)

func Test_convertThreshold(t *testing.T) {
	t.Parallel()
	type args struct {
		origin map[string]float64
	}
	tests := []struct {
		name string
		args args
		want map[string]float64
	}{
		{
			name: "test",
			args: args{
				origin: map[string]float64{
					metricthreshold.NUMACPUUsageRatioThreshold: 1,
					metricthreshold.NUMACPULoadRatioThreshold:  2,
					"xxx": 3,
				},
			},
			want: map[string]float64{
				consts.MetricCPUUsageContainer: 1,
				consts.MetricLoad1MinContainer: 2,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, convertThreshold(tt.args.origin), "convertThreshold(%v)", tt.args.origin)
		})
	}
}

func TestNumaCPUPressureEviction_pullThresholds(t *testing.T) {
	t.Parallel()

	type fields struct {
		emitter            metrics.MetricEmitter
		conf               *config.Configuration
		numaPressureConfig *NumaPressureConfig
		thresholds         map[string]float64
		metricsHistory     *util.NumaMetricHistory
		overloadNumaCount  int
		enabled            bool
	}
	tests := []struct {
		name          string
		fields        fields
		setFakeMetric func(store *metric.FakeMetricsFetcher)
		wantEnabled   bool
	}{
		{
			name: "enabled when sgc not enabled, fallback to static config 1",
			fields: fields{
				conf:    generatePluginConfig(true, false, true),
				enabled: false,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: true,
		},
		{
			name: "enabled when sgc not enabled, fallback to static config 2",
			fields: fields{
				conf:    generatePluginConfig(true, false, false),
				enabled: false,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: true,
		},
		{
			name: "enabled when sgc enabled, sgc configured",
			fields: fields{
				conf:    generatePluginConfig(true, true, true),
				enabled: false,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: true,
		},
		{
			name: "disabled when sgc enabled, sgc not configured",
			fields: fields{
				conf:    generatePluginConfig(true, true, false),
				enabled: false,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "disabled when default enabled 0",
			fields: fields{
				conf:    generatePluginConfig(false, false, false),
				enabled: false,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "disabled when default enabled 1",
			fields: fields{
				conf:    generatePluginConfig(false, true, false),
				enabled: false,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "disabled when default enabled 2",
			fields: fields{
				conf:    generatePluginConfig(false, false, true),
				enabled: false,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "disabled when default enabled 3",
			fields: fields{
				conf:    generatePluginConfig(false, true, true),
				enabled: false,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "enabled when sgc not enabled, fallback to static config 1",
			fields: fields{
				conf:    generatePluginConfig(true, false, true),
				enabled: true,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: true,
		},
		{
			name: "enabled when sgc not enabled, fallback to static config 2",
			fields: fields{
				conf:    generatePluginConfig(true, false, false),
				enabled: true,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: true,
		},
		{
			name: "enabled when sgc enabled, sgc configured",
			fields: fields{
				conf:    generatePluginConfig(true, true, true),
				enabled: true,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: true,
		},
		{
			name: "disabled when sgc enabled, sgc not configured",
			fields: fields{
				conf:    generatePluginConfig(true, true, false),
				enabled: true,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor: 1,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "disabled when default enabled 0",
			fields: fields{
				conf:    generatePluginConfig(false, false, false),
				enabled: true,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "disabled when default enabled 1",
			fields: fields{
				conf:    generatePluginConfig(false, true, false),
				enabled: true,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "disabled when default enabled 2",
			fields: fields{
				conf:    generatePluginConfig(false, false, true),
				enabled: true,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
		{
			name: "disabled when default enabled 3",
			fields: fields{
				conf:    generatePluginConfig(false, true, true),
				enabled: true,
				numaPressureConfig: &NumaPressureConfig{
					ExpandFactor:   1,
					MetricRingSize: 2,
				},
			},
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantEnabled: false,
		},
	}
	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			store := metricsFetcher.(*metric.FakeMetricsFetcher)
			tt.setFakeMetric(store)
			ms := makeMetaServer(metricsFetcher, nil)
			ms.NPDFetcher = &npd.DummyNPDFetcher{
				NPD: &nodev1.NodeProfileDescriptor{
					Status: nodev1.NodeProfileDescriptorStatus{
						NodeMetrics: []nodev1.ScopedNodeMetrics{
							{
								Scope: threshold.ScopeMetricThreshold,
								Metrics: []nodev1.MetricValue{
									{
										MetricName: metricthreshold.NUMACPUUsageRatioThreshold,
										Value:      resource.MustParse("1"),
									},
								},
							},
						},
					},
				},
			}
			p := &NumaCPUPressureEviction{
				conf:               tt.fields.conf,
				numaPressureConfig: tt.fields.numaPressureConfig,
				thresholds:         tt.fields.thresholds,
				metricsHistory:     util.NewMetricHistory(tt.fields.numaPressureConfig.MetricRingSize),
				enabled:            tt.fields.enabled,
				metaServer:         ms,
			}
			p.pullThresholds(context.TODO())
			assert.Equalf(t, tt.wantEnabled, p.enabled, "pullThresholds")
		})
	}
}

func generatePluginConfig(staticEnabled bool, sgcEnabled bool, sgcConfigured bool) *config.Configuration {
	testConfiguration := config.NewConfiguration()

	d := dynamic.NewConfiguration()
	d.NumaCPUPressureEvictionConfiguration.EnableEviction = staticEnabled
	d.StrategyGroupConfiguration.EnableStrategyGroup = sgcEnabled
	if sgcConfigured {
		d.StrategyGroupConfiguration.EnabledStrategies = []v1alpha1.Strategy{
			{
				Name: pointer.String(consts.StrategyNameNumaCpuPressureEviction),
			},
		}
	}

	testConfiguration.AgentConfiguration.DynamicAgentConfiguration.SetDynamicConfiguration(d)

	return testConfiguration
}
