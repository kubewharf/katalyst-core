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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

func TestAggreSeries(t *testing.T) {
	t.Parallel()
	t.Run("normal avg", func(t *testing.T) {
		t.Parallel()
		input := []timeSeriesItem{
			{Timestamp: 1, Value: 10},
			{Timestamp: 2, Value: 20},
		}
		output := aggreSeries(input, apiconfig.Avg)
		if output.Value != 15 {
			t.Errorf("aggreSeries got=%v, want=%v", output, 15)
		}
	})
	t.Run("normal max", func(t *testing.T) {
		t.Parallel()
		input := []timeSeriesItem{
			{Timestamp: 1, Value: 10},
			{Timestamp: 2, Value: 20},
		}
		output := aggreSeries(input, apiconfig.Max)
		if output.Value != 20 {
			t.Errorf("aggreSeries got=%v, want=%v", output, 20)
		}
	})
}

func TestGetSPDExtendedIndicators(t *testing.T) {
	t.Parallel()

	type args struct {
		spd        *apiworkload.ServiceProfileDescriptor
		indicators *apiconfig.ResourcePortraitIndicators
	}

	type want struct {
		indicator *apiconfig.ResourcePortraitIndicators
		baseline  *int32
	}

	rpIndicators := &apiconfig.ResourcePortraitIndicators{
		TypeMeta: metav1.TypeMeta{},
		Configs: []apiconfig.ResourcePortraitConfig{
			{
				AlgorithmConfig: apiconfig.AlgorithmConfig{
					Method:       "",
					Params:       nil,
					TimeWindow:   apiconfig.TimeWindow{},
					ResyncPeriod: 0,
				},
			},
		},
	}

	raw, err := json.Marshal(rpIndicators)
	assert.Nil(t, err)

	tests := []struct {
		name    string
		args    args
		want    want
		wantErr bool
	}{
		{
			name: "empty raw",
			args: args{
				spd: &apiworkload.ServiceProfileDescriptor{
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						ExtendedIndicator: []apiworkload.ServiceExtendedIndicatorSpec{
							{
								Name: ResourcePortraitExtendedSpecName,
							},
						},
					},
				},
			},
			want:    want{},
			wantErr: true,
		},
		{
			name: "error extend indicator name",
			args: args{
				spd: &apiworkload.ServiceProfileDescriptor{
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						ExtendedIndicator: []apiworkload.ServiceExtendedIndicatorSpec{
							{
								Name: ResourcePortraitExtendedSpecName + "x",
							},
						},
					},
				},
				indicators: &apiconfig.ResourcePortraitIndicators{},
			},
			want:    want{indicator: &apiconfig.ResourcePortraitIndicators{}},
			wantErr: true,
		},
		{
			name: "error indicators object",
			args: args{
				spd: &apiworkload.ServiceProfileDescriptor{
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						ExtendedIndicator: []apiworkload.ServiceExtendedIndicatorSpec{
							{
								Name: ResourcePortraitExtendedSpecName,
								Indicators: runtime.RawExtension{
									Object: &v1.ConfigMap{
										Data: map[string]string{},
									},
								},
							},
						},
					},
				},
			},
			want:    want{},
			wantErr: true,
		},
		{
			name: "normal",
			args: args{
				spd: &apiworkload.ServiceProfileDescriptor{
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						ExtendedIndicator: []apiworkload.ServiceExtendedIndicatorSpec{
							{
								Name: ResourcePortraitExtendedSpecName,
								Indicators: runtime.RawExtension{
									Raw: raw,
								},
							},
						},
					},
				},
				indicators: &apiconfig.ResourcePortraitIndicators{},
			},
			want: want{
				indicator: &apiconfig.ResourcePortraitIndicators{
					TypeMeta: metav1.TypeMeta{},
					Configs: []apiconfig.ResourcePortraitConfig{
						{
							AlgorithmConfig: apiconfig.AlgorithmConfig{
								Method:       "",
								Params:       nil,
								TimeWindow:   apiconfig.TimeWindow{},
								ResyncPeriod: 0,
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := util.GetSPDExtendedIndicators(tt.args.spd, tt.args.indicators)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.Nil(t, err)
			}
			assert.Equal(t, tt.want.indicator, tt.args.indicators)
		})
	}
}

func TestConvertAlgorithmResultToAggMetrics(t *testing.T) {
	t.Parallel()

	type args struct {
		aggMetrics *apiworkload.AggPodMetrics
		algoConf   *apiconfig.ResourcePortraitConfig
		timeseries map[string][]timeSeriesItem
		groupData  map[string]float64
	}

	tests := []struct {
		name string
		args args
		want *apiworkload.AggPodMetrics
	}{
		{
			name: "nil",
			args: args{
				aggMetrics: &apiworkload.AggPodMetrics{},
				algoConf: &apiconfig.ResourcePortraitConfig{
					Source: "test",
					AlgorithmConfig: apiconfig.AlgorithmConfig{TimeWindow: apiconfig.TimeWindow{
						Input:           0,
						HistorySteps:    0,
						Aggregator:      "avg",
						Output:          0,
						PredictionSteps: 0,
					}},
					Metrics:       nil,
					CustomMetrics: nil,
				},
				timeseries: nil,
				groupData:  nil,
			},
			want: &apiworkload.AggPodMetrics{Aggregator: apiworkload.Aggregator("avg"), Items: []v1beta1.PodMetrics{}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := convertAlgorithmResultToAggMetrics(tt.args.aggMetrics, tt.args.algoConf, tt.args.timeseries, tt.args.groupData)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGeneratePodMetrics(t *testing.T) {
	t.Parallel()

	type args struct {
		aggMetrics *apiworkload.AggPodMetrics
		algoConf   *apiconfig.ResourcePortraitConfig
		timeseries map[string][]timeSeriesItem
		groupData  map[string]float64
	}

	tests := []struct {
		name string
		args args
		want []v1beta1.PodMetrics
	}{
		{
			name: "nil",
			args: args{
				aggMetrics: &apiworkload.AggPodMetrics{},
				algoConf: &apiconfig.ResourcePortraitConfig{
					Source: "test",
					AlgorithmConfig: apiconfig.AlgorithmConfig{TimeWindow: apiconfig.TimeWindow{
						Input:           0,
						HistorySteps:    0,
						Aggregator:      "avg",
						Output:          0,
						PredictionSteps: 0,
					}},
					Metrics:       nil,
					CustomMetrics: nil,
				},
				timeseries: nil,
				groupData:  nil,
			},
			want: []v1beta1.PodMetrics{},
		},
		{
			name: "normal",
			args: args{
				aggMetrics: &apiworkload.AggPodMetrics{
					Items: []v1beta1.PodMetrics{
						{
							Timestamp: metav1.Time{Time: time.Unix(1, 0)},
							Containers: []v1beta1.ContainerMetrics{
								{
									Name:  "test-predict",
									Usage: map[v1.ResourceName]resource.Quantity{},
								},
							},
						},
					},
				},
				algoConf: &apiconfig.ResourcePortraitConfig{
					Source: "test",
					AlgorithmConfig: apiconfig.AlgorithmConfig{TimeWindow: apiconfig.TimeWindow{
						Input:           0,
						HistorySteps:    0,
						Aggregator:      "avg",
						Output:          2,
						PredictionSteps: 0,
					}},
					Metrics:       nil,
					CustomMetrics: nil,
				},
				timeseries: map[string][]timeSeriesItem{
					"resourceX": {{1, 1}, {2, 2}, {3, 3}},
				},
				groupData: map[string]float64{"resourceY": 1},
			},
			want: []v1beta1.PodMetrics{},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := generatePodMetrics(tt.args.algoConf, tt.args.aggMetrics.Items, tt.args.timeseries, tt.args.groupData)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGenerateMetricsRefreshRecord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args *apiworkload.AggPodMetrics
		want map[string]metav1.Time
	}{
		{
			name: "normal",
			args: &apiworkload.AggPodMetrics{
				Aggregator: "",
				Scope:      "",
				Items: []v1beta1.PodMetrics{
					{
						Timestamp: metav1.Time{Time: time.Unix(1, 0)},
						Containers: []v1beta1.ContainerMetrics{
							{
								Name:  "test-predict",
								Usage: map[v1.ResourceName]resource.Quantity{"A": *resource.NewMilliQuantity(1, resource.DecimalSI)},
							},
						},
					},
					{
						Timestamp: metav1.Time{Time: time.Unix(2, 0)},
						Containers: []v1beta1.ContainerMetrics{
							{
								Name:  "test-predict",
								Usage: map[v1.ResourceName]resource.Quantity{"A": *resource.NewMilliQuantity(1, resource.DecimalSI)},
							},
						},
					},
				},
			},
			want: map[string]metav1.Time{
				"test-predict": {Time: time.Unix(1, 0)},
			},
		},
		{
			name: "empty",
			args: nil,
			want: nil,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := generateMetricsRefreshRecord(tt.args)
			assert.Equal(t, tt.want, got)
		})
	}
}
