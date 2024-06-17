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

package sampling

import (
	"context"
	"errors"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metrics_pool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type mockMBMonitor struct {
	MBMonitorAdaptor
	numaFakeConfigured bool
	errInit            bool
	errGlobalStats     bool
	hasData            bool
}

func (m mockMBMonitor) FakeNumaConfigured() bool {
	return m.numaFakeConfigured
}

func (m mockMBMonitor) Init() error {
	if m.errInit {
		return errors.New("test init error")
	}
	return nil
}

func (m mockMBMonitor) GlobalStats(ctx context.Context, refreshRate uint64) error {
	if m.errGlobalStats {
		return errors.New("test GlobalStats error")
	}
	return nil
}

func (m mockMBMonitor) GetPackageNUMA() map[int][]int {
	return map[int][]int{
		0: {0, 1},
		1: {2, 3},
	}
}

func (m mockMBMonitor) GetNUMACCD() map[int][]int {
	return map[int][]int{
		0: {0, 1, 2},
		1: {0, 1, 2},
		2: {3, 4, 5},
		3: {6, 7, 8},
	}
}

func (m mockMBMonitor) GetMemoryBandwidthOfPackages() []machine.PackageMB {
	if !m.hasData {
		return nil
	}

	return []machine.PackageMB{
		{
			RMB:       700,
			RMB_Delta: 400,
			WMB:       300,
			WMB_Delta: 250,
			Total:     1000,
		},
		{
			RMB:       888,
			RMB_Delta: 567,
			WMB:       555,
			WMB_Delta: 321,
			Total:     1450,
		},
	}
}

func (m mockMBMonitor) GetMemoryBandwidthOfNUMAs() []machine.NumaMB {
	if !m.hasData {
		return nil
	}

	return []machine.NumaMB{
		{
			Package: 0,
			LRMB:    100,
			RRMB:    100,
			TRMB:    50,
			Total:   250,
		},
		{
			Package: 0,
			LRMB:    111,
			RRMB:    99,
			TRMB:    66,
			Total:   256,
		},
		{},
		{},
	}
}

func (m mockMBMonitor) GetCCDL3Latency() []float64 {
	if !m.hasData {
		return nil
	}

	return []float64{
		111.1, 45.3, 78.6,
		9.99, 4.8, 67.1,
		532, 76.6, 8.43,
	}
}

func Test_mbwSampler_Startup(t *testing.T) {
	t.Parallel()
	type fields struct {
		monitor     MBMonitorAdaptor
		metricStore *utilmetric.MetricStore
		emitter     metrics.MetricEmitter
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "happy path no error",
			fields: fields{
				monitor:     &mockMBMonitor{numaFakeConfigured: true},
				metricStore: nil,
				emitter:     nil,
			},
			args: args{
				ctx: context.TODO(),
			},
			wantErr: false,
		},
		{
			name: "negative path of not fake numa",
			fields: fields{
				monitor: &mockMBMonitor{numaFakeConfigured: false},
			},
			args: args{
				ctx: context.TODO(),
			},
			wantErr: true,
		},
		{
			name: "negative path of Init error",
			fields: fields{
				monitor: &mockMBMonitor{numaFakeConfigured: true, errInit: true},
			},
			args: args{
				ctx: context.TODO(),
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := mbwSampler{
				monitor:     tt.fields.monitor,
				metricStore: tt.fields.metricStore,
				emitter:     tt.fields.emitter,
			}
			if err := m.Startup(tt.args.ctx); (err != nil) != tt.wantErr {
				t.Errorf("Startup() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_mbwSampler_Sample(t *testing.T) {
	t.Parallel()
	type fields struct {
		monitor     MBMonitorAdaptor
		metricStore *utilmetric.MetricStore
		emitter     metrics.MetricEmitter
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "nil return is ok",
			fields: fields{
				monitor: &mockMBMonitor{},
			},
			args: args{},
		},
		{
			name: "data returned is the best",
			fields: fields{
				monitor:     &mockMBMonitor{hasData: true},
				metricStore: utilmetric.NewMetricStore(),
				emitter:     metrics_pool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter(),
			},
			args: args{},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := New(tt.fields.monitor, tt.fields.metricStore, tt.fields.emitter)
			m.Sample(tt.args.ctx)
		})
	}
}

func Test_averageLatency(t *testing.T) {
	t.Parallel()
	type args struct {
		count        float64
		ccds         []int
		ccdL3Latency []float64
	}
	tests := []struct {
		name string
		args args
		want float64
	}{
		{
			name: "happy single one",
			args: args{
				count:        1,
				ccds:         []int{5, 6, 7},
				ccdL3Latency: []float64{100, 100, 100, 100, 100, 5.5, 6.88, 7.43, 100},
			},
			want: 19.81,
		},
		{
			name: "happy twin",
			args: args{
				count:        2,
				ccds:         []int{0, 1, 2, 3},
				ccdL3Latency: []float64{1.1, 2.2, 3.3, 4.4},
			},
			want: 5.5,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := averageLatency(tt.args.count, tt.args.ccds, tt.args.ccdL3Latency); got != tt.want {
				t.Errorf("averageLatency() = %v, want %v", got, tt.want)
			}
		})
	}
}
