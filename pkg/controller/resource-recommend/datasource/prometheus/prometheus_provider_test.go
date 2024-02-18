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
	"fmt"
	"reflect"
	"testing"
	"time"

	promapiv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"

	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

func Test_prometheus_ConvertMetricToQuery(t *testing.T) {
	p := &prometheus{
		config: &PromConfig{},
	}
	tests := []struct {
		name          string
		metric        datasourcetypes.Metric
		expectedQuery *datasourcetypes.Query
		expectedError error
	}{
		{
			name: "CPU metric",
			metric: datasourcetypes.Metric{
				Resource:      corev1.ResourceCPU,
				Namespace:     "my-namespace",
				WorkloadName:  "my-workload",
				Kind:          "Deployment",
				ContainerName: "my-container",
			},
			expectedQuery: &datasourcetypes.Query{
				Prometheus: &datasourcetypes.PrometheusQuery{
					Query: GetContainerCpuUsageQueryExp("my-namespace", "my-workload", "Deployment", "my-container", ""),
				},
			},
			expectedError: nil,
		},
		{
			name: "Memory metric",
			metric: datasourcetypes.Metric{
				Resource:      corev1.ResourceMemory,
				Namespace:     "my-namespace",
				WorkloadName:  "my-workload",
				Kind:          "Deployment",
				ContainerName: "my-container",
			},
			expectedQuery: &datasourcetypes.Query{
				Prometheus: &datasourcetypes.PrometheusQuery{
					Query: GetContainerMemUsageQueryExp("my-namespace", "my-workload", "Deployment", "my-container", ""),
				},
			},
			expectedError: nil,
		},
		{
			name: "Unsupported metric",
			metric: datasourcetypes.Metric{
				Resource:      "Unsupported",
				Namespace:     "my-namespace",
				WorkloadName:  "my-workload",
				Kind:          "Deployment",
				ContainerName: "my-container",
			},
			expectedQuery: nil,
			expectedError: fmt.Errorf("query for resource type Unsupported is not supported"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotQuery, gotErr := p.ConvertMetricToQuery(tt.metric)

			if !reflect.DeepEqual(gotQuery, tt.expectedQuery) {
				t.Errorf("ConvertMetricToQuery() gotQuery = %v, want %v", gotQuery, tt.expectedQuery)
			}

			if (gotErr == nil && tt.expectedError != nil) || (gotErr != nil && tt.expectedError == nil) || (gotErr != nil && tt.expectedError != nil && gotErr.Error() != tt.expectedError.Error()) {
				t.Errorf("ConvertMetricToQuery() gotErr = %v, wantErr %v", gotErr, tt.expectedError)
			}
		})
	}
}

func Test_prometheus_QueryTimeSeries(t *testing.T) {
	type fields struct {
		promAPIClient v1.API
		config        *PromConfig
	}
	type args struct {
		query *datasourcetypes.Query
		start time.Time
		end   time.Time
		step  time.Duration
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *datasourcetypes.TimeSeries
		wantErr bool
	}{
		{
			name: "Successful query time series",
			fields: fields{
				promAPIClient: &mockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						matrix := model.Matrix{
							&model.SampleStream{
								Metric: model.Metric{
									"label1": "value1",
									"label2": "value2",
								},
								Values: []model.SamplePair{
									{
										Timestamp: 1500000000,
										Value:     10,
									},
									{
										Timestamp: 1500001000,
										Value:     20,
									},
								},
							},
						}
						return matrix, nil, nil
					},
				},
				config: &PromConfig{
					Timeout: time.Second * 5,
				},
			},
			args: args{
				query: &datasourcetypes.Query{
					Prometheus: &datasourcetypes.PrometheusQuery{
						Query: "my_query",
					},
				},
				start: time.Unix(1500000000, 0),
				end:   time.Unix(1500002000, 0),
				step:  time.Second,
			},
			want: &datasourcetypes.TimeSeries{
				Samples: []datasourcetypes.Sample{
					{
						Value:     10,
						Timestamp: 1500000,
					},
					{
						Value:     20,
						Timestamp: 1500001,
					},
				},
				Labels: map[string]string{
					"label1": "value1",
					"label2": "value2",
				},
			},
			wantErr: false,
		},
		{
			name: "Query time series error",
			fields: fields{
				promAPIClient: &mockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						return nil, nil, fmt.Errorf("query error")
					},
				},
				config: &PromConfig{
					Timeout: time.Second * 5,
				},
			},
			args: args{
				query: &datasourcetypes.Query{
					Prometheus: &datasourcetypes.PrometheusQuery{
						Query: "my_query",
					},
				},
				start: time.Unix(1500000000, 0),
				end:   time.Unix(1500002000, 0),
				step:  time.Second,
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &prometheus{
				promAPIClient: tt.fields.promAPIClient,
				config:        tt.fields.config,
			}
			got, err := p.QueryTimeSeries(tt.args.query, tt.args.start, tt.args.end, tt.args.step)
			if (err != nil) != tt.wantErr {
				t.Errorf("QueryTimeSeries() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("QueryTimeSeries() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_prometheus_convertPromResultsToTimeSeries(t *testing.T) {
	type fields struct {
		promAPIClient v1.API
		config        *PromConfig
	}
	type args struct {
		value model.Value
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *datasourcetypes.TimeSeries
		wantErr bool
	}{
		{
			name: "Matrix value",
			fields: fields{
				promAPIClient: nil,
				config:        nil,
			},
			args: args{
				value: model.Matrix{
					&model.SampleStream{
						Metric: model.Metric{
							"label1": "value1",
							"label2": "value2",
						},
						Values: []model.SamplePair{
							{
								Timestamp: 1500000000,
								Value:     10,
							},
							{
								Timestamp: 1500001000,
								Value:     20,
							},
						},
					},
				},
			},
			want: &datasourcetypes.TimeSeries{
				Samples: []datasourcetypes.Sample{
					{
						Value:     10,
						Timestamp: 1500000,
					},
					{
						Value:     20,
						Timestamp: 1500001,
					},
				},
				Labels: map[string]string{
					"label1": "value1",
					"label2": "value2",
				},
			},
			wantErr: false,
		},
		{
			name: "Vector value",
			fields: fields{
				promAPIClient: nil,
				config:        nil,
			},
			args: args{
				value: model.Vector{
					&model.Sample{
						Metric: model.Metric{
							"label1": "value1",
							"label2": "value2",
						},
						Timestamp: 1500000000,
						Value:     10,
					},
				},
			},
			want: &datasourcetypes.TimeSeries{
				Samples: []datasourcetypes.Sample{
					{
						Value:     10,
						Timestamp: 1500000,
					},
				},
				Labels: map[string]string{
					"label1": "value1",
					"label2": "value2",
				},
			},
			wantErr: false,
		},
		{
			name: "Unsupported value type",
			fields: fields{
				promAPIClient: nil,
				config:        nil,
			},
			args: args{
				value: &model.Scalar{},
			},
			want:    datasourcetypes.NewTimeSeries(),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &prometheus{
				promAPIClient: tt.fields.promAPIClient,
				config:        tt.fields.config,
			}
			got, err := p.convertPromResultsToTimeSeries(tt.args.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("convertPromResultsToTimeSeries() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("convertPromResultsToTimeSeries() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_prometheus_queryRangeSync(t *testing.T) {
	type fields struct {
		promAPIClient v1.API
		config        *PromConfig
	}
	type args struct {
		ctx   context.Context
		query string
		start time.Time
		end   time.Time
		step  time.Duration
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *datasourcetypes.TimeSeries
		wantErr bool
	}{
		{
			name: "Successful query range",
			fields: fields{
				promAPIClient: &mockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						matrix := model.Matrix{
							&model.SampleStream{
								Metric: model.Metric{
									"label1": "value1",
									"label2": "value2",
								},
								Values: []model.SamplePair{
									{
										Timestamp: 1500000000,
										Value:     10,
									},
									{
										Timestamp: 1500001000,
										Value:     20,
									},
								},
							},
						}
						return matrix, nil, nil
					},
				},
				config: nil,
			},
			args: args{
				ctx:   context.TODO(),
				query: "my_query",
				start: time.Unix(1500000000, 0),
				end:   time.Unix(1500002000, 0),
				step:  time.Second,
			},
			want: &datasourcetypes.TimeSeries{
				Samples: []datasourcetypes.Sample{
					{
						Value:     10,
						Timestamp: 1500000,
					},
					{
						Value:     20,
						Timestamp: 1500001,
					},
				},
				Labels: map[string]string{
					"label1": "value1",
					"label2": "value2",
				},
			},
			wantErr: false,
		},
		{
			name: "Error in query range",
			fields: fields{
				promAPIClient: &mockPromAPIClient{
					QueryRangeFunc: func(ctx context.Context, query string, r promapiv1.Range) (model.Value, promapiv1.Warnings, error) {
						return nil, nil, fmt.Errorf("query error")
					},
				},
				config: nil,
			},
			args: args{
				ctx:   context.TODO(),
				query: "my_query",
				start: time.Unix(1500000000, 0),
				end:   time.Unix(1500002000, 0),
				step:  time.Second,
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &prometheus{
				promAPIClient: tt.fields.promAPIClient,
				config:        tt.fields.config,
			}
			got, err := p.queryRangeSync(tt.args.ctx, tt.args.query, tt.args.start, tt.args.end, tt.args.step)
			if (err != nil) != tt.wantErr {
				t.Errorf("queryRangeSync() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("queryRangeSync() got = %v, want %v", got, tt.want)
			}
		})
	}
}
