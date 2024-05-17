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

package resource_portrait

import (
	"context"
	"testing"

	promapiv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	datasourceprometheus "github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

func TestGetPromqlFromMapping(t *testing.T) {
	t.Parallel()
	type args struct {
		metric      string
		workload    string
		ns          string
		durationStr string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "cpu_utilization_usage_seconds metric",
			args: args{
				metric:      "cpu_utilization_usage_seconds",
				workload:    "metric-server",
				ns:          "kube-system",
				durationStr: "5m",
			},
			want: "sum(rate(container_cpu_usage_seconds_total{namespace=\"kube-system\",pod=~\"metric-server.*\",container!=\"\"}[5m]))",
		},
		{
			name: "cpu_utilization_usage_seconds metric with .*",
			args: args{
				metric:      "cpu_utilization_usage_seconds",
				workload:    "metric-server.*",
				ns:          "kube-system",
				durationStr: "5m",
			},
			want: "sum(rate(container_cpu_usage_seconds_total{namespace=\"kube-system\",pod=~\"metric-server.*\",container!=\"\"}[5m]))",
		},
		{
			name: "memory_utilization",
			args: args{
				metric:      "memory_utilization",
				workload:    "metric-server.*",
				ns:          "kube-system",
				durationStr: "5m",
			},
			want: "sum(container_memory_working_set_bytes{namespace=\"kube-system\",pod=~\"metric-server.*\", container!=\"\"})",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			promql := getPromqlFromMapping(tt.args.metric, tt.args.workload, tt.args.ns, tt.args.durationStr)
			if promql != tt.want {
				t.Errorf("getPromqlFromMapping() get %s, want %s", promql, tt.want)
			}
		})
	}
}

func TestNewPromClient(t *testing.T) {
	t.Parallel()
	type args struct {
		config *datasourceprometheus.PromConfig
	}
	tests := []struct {
		name    string
		args    args
		wantNil bool
		wantErr bool
	}{
		{
			name: "normal",
			args: args{config: &datasourceprometheus.PromConfig{
				Address: "prometheus:9090",
			}},
			wantNil: false,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := newPromClient(tt.args.config)
			if (c != nil) == tt.wantNil {
				t.Errorf("NewPromClient() got nil prom client")
			}
			if (err == nil) == tt.wantErr {
				t.Errorf("NewPromClient() got error=%v", err)
			}
		})
	}
}

func TestPromClientQuery(t *testing.T) {
	t.Parallel()

	valuesF := func(num int) []model.SamplePair {
		var values []model.SamplePair
		for i := 0; i < num; i++ {
			values = append(values, model.SamplePair{Timestamp: 1500000000 + model.Time(i)*1000, Value: model.SampleValue(i) * 10})
		}
		return values
	}

	type args struct {
		API promapiv1.API
		rpc *apiconfig.ResourcePortraitConfig

		workloadName      string
		workloadNamespace string
	}
	tests := []struct {
		name string
		args args
		want map[string][]model.SamplePair
	}{
		{
			name: "normal",
			args: args{
				API: &datasourceprometheus.MockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						matrix := model.Matrix{
							&model.SampleStream{
								Metric: model.Metric{
									"label1": "value1",
									"label2": "value2",
								},
								Values: valuesF(10080 + 1),
							},
						}
						return matrix, nil, nil
					},
				},
				rpc: &apiconfig.ResourcePortraitConfig{
					AlgorithmConfig: apiconfig.AlgorithmConfig{
						TimeWindow: apiconfig.TimeWindow{
							Input:        60,
							Output:       60,
							Aggregator:   apiconfig.Avg,
							HistorySteps: 10080,
						},
					},
					Metrics: []string{"cpu_utilization_usage_seconds_max"},
				},
			},
			want: map[string][]model.SamplePair{
				"cpu_utilization_usage_seconds_max": valuesF(10080 + 1),
			},
		},
		{
			name: "not sample ret",
			args: args{
				API: &datasourceprometheus.MockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						matrix := model.Matrix{
							&model.SampleStream{
								Metric: model.Metric{
									"label1": "value1",
									"label2": "value2",
								},
								Values: valuesF(0),
							},
						}
						return matrix, nil, nil
					},
				},
				rpc: &apiconfig.ResourcePortraitConfig{
					AlgorithmConfig: apiconfig.AlgorithmConfig{
						TimeWindow: apiconfig.TimeWindow{
							Input:      60,
							Output:     60,
							Aggregator: apiconfig.Avg,
						},
					},
					Metrics: []string{"cpu_utilization_usage_seconds_max"},
				},
			},
			want: map[string][]model.SamplePair{},
		},
		{
			name: "one metric ret",
			args: args{
				API: &datasourceprometheus.MockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						matrix := model.Matrix{
							&model.SampleStream{
								Metric: model.Metric{
									"label1": "value1",
									"label2": "value2",
								},
								Values: valuesF(1),
							},
						}
						return matrix, nil, nil
					},
				},
				rpc: &apiconfig.ResourcePortraitConfig{
					AlgorithmConfig: apiconfig.AlgorithmConfig{
						TimeWindow: apiconfig.TimeWindow{
							Input:        60,
							Output:       60,
							Aggregator:   apiconfig.Avg,
							HistorySteps: 1,
						},
					},
					Metrics: []string{"cpu_utilization_usage_seconds_max"},
				},
			},
			want: map[string][]model.SamplePair{},
		},
		{
			name: "empty algorithm conf",
			args: args{
				API: &datasourceprometheus.MockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						matrix := model.Matrix{
							&model.SampleStream{
								Metric: model.Metric{
									"label1": "value1",
									"label2": "value2",
								},
								Values: valuesF(10080 + 1),
							},
						}
						return matrix, nil, nil
					},
				},
				rpc: nil,
			},
			want: nil,
		},
		{
			name: "no promql",
			args: args{
				API: &datasourceprometheus.MockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						matrix := model.Matrix{
							&model.SampleStream{
								Metric: model.Metric{
									"label1": "value1",
									"label2": "value2",
								},
								Values: valuesF(10080 + 1),
							},
						}
						return matrix, nil, nil
					},
				},
				rpc: &apiconfig.ResourcePortraitConfig{
					AlgorithmConfig: apiconfig.AlgorithmConfig{
						TimeWindow: apiconfig.TimeWindow{
							Input:      60,
							Output:     60,
							Aggregator: apiconfig.Avg,
						},
					},
				},
			},
			want: nil,
		},
		{
			name: "no match preset promql",
			args: args{
				API: &datasourceprometheus.MockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						matrix := model.Matrix{
							&model.SampleStream{
								Metric: model.Metric{
									"label1": "value1",
									"label2": "value2",
								},
								Values: valuesF(10080 + 1),
							},
						}
						return matrix, nil, nil
					},
				},
				rpc: &apiconfig.ResourcePortraitConfig{
					AlgorithmConfig: apiconfig.AlgorithmConfig{
						TimeWindow: apiconfig.TimeWindow{
							Input:      60,
							Output:     60,
							Aggregator: apiconfig.Avg,
						},
					},
					Metrics: []string{"cpu_utilization_usage_seconds_ma"},
				},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			prom := &promClientImpl{}
			prom.API = tt.args.API
			res := prom.Query(tt.args.workloadName, tt.args.workloadNamespace, tt.args.rpc)
			assert.Equal(t, res, tt.want)
		})
	}
}
