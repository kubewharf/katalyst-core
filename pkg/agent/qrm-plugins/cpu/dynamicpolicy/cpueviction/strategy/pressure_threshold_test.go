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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

func Test_expandThresholds(t *testing.T) {
	t.Parallel()
	type args struct {
		thresholds   map[string]float64
		expandFactor float64
	}
	tests := []struct {
		name string
		args args
		want map[string]float64
	}{
		{
			name: "test",
			args: args{
				thresholds: map[string]float64{
					"1": 1,
					"2": 2,
					"3": 3,
				},
				expandFactor: 2,
			},
			want: map[string]float64{
				"1": 2,
				"2": 4,
				"3": 6,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, expandThresholds(tt.args.thresholds, tt.args.expandFactor), "expandThresholds(%v, %v)", tt.args.thresholds, tt.args.expandFactor)
		})
	}
}

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

func Test_getOverLoadThreshold(t *testing.T) {
	t.Parallel()
	type args struct {
		globalThresholds *metricthreshold.MetricThreshold
		cpuCode          string
		isVM             bool
	}
	tests := []struct {
		name string
		args args
		want map[string]float64
	}{
		{
			name: "test",
			args: args{
				globalThresholds: &metricthreshold.MetricThreshold{
					Threshold: map[string]map[bool]map[string]float64{
						"abc": {
							true: {
								metricthreshold.NUMACPUUsageRatioThreshold: 1,
								metricthreshold.NUMACPULoadRatioThreshold:  2,
							},
							false: {
								metricthreshold.NUMACPUUsageRatioThreshold: 3,
								metricthreshold.NUMACPULoadRatioThreshold:  4,
							},
						},
						"def": {
							true: {
								metricthreshold.NUMACPUUsageRatioThreshold: 5,
								metricthreshold.NUMACPULoadRatioThreshold:  6,
							},
							false: {
								metricthreshold.NUMACPUUsageRatioThreshold: 7,
								metricthreshold.NUMACPULoadRatioThreshold:  8,
							},
						},
					},
				},
				cpuCode: "abc",
				isVM:    true,
			},
			want: map[string]float64{
				metricthreshold.NUMACPUUsageRatioThreshold: 1,
				metricthreshold.NUMACPULoadRatioThreshold:  2,
			},
		},
		{
			name: "test",
			args: args{
				globalThresholds: metricthreshold.NewMetricThreshold(),
				cpuCode:          "Intel_CascadeLake",
				isVM:             false,
			},
			want: map[string]float64{
				metricthreshold.NUMACPUUsageRatioThreshold: 0.55,
				metricthreshold.NUMACPULoadRatioThreshold:  0.68,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, getOverLoadThreshold(tt.args.globalThresholds, tt.args.cpuCode, tt.args.isVM), "getOverLoadThreshold(%v, %v, %v)", tt.args.globalThresholds, tt.args.cpuCode, tt.args.isVM)
		})
	}
}
