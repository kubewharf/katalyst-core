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

package task

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

func TestHistogramTask_AddSample(t1 *testing.T) {
	type newTask func() *HistogramTask
	type sample struct {
		sampleTime   time.Time
		sampleValue  float64
		sampleWeight float64
	}
	type want struct {
		lastSampleTime    time.Time
		firstSampleTime   time.Time
		totalSamplesCount int
	}
	tests := []struct {
		name    string
		newTask newTask
		samples []sample
		want
	}{
		{
			name: "case1",
			newTask: func() *HistogramTask {
				t, _ := NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
				return t
			},
			samples: []sample{
				{
					sampleTime:   time.Unix(1694270256, 0),
					sampleValue:  1,
					sampleWeight: 1,
				},
				{
					sampleTime:   time.Unix(1694270765, 0),
					sampleValue:  1,
					sampleWeight: 1,
				},
				{
					sampleTime:   time.Unix(1694278758, 0),
					sampleValue:  1,
					sampleWeight: 1,
				},
				{
					sampleTime:   time.Unix(1694278700, 0),
					sampleValue:  1,
					sampleWeight: 1,
				},
			},
			want: want{
				lastSampleTime:    time.Unix(1694278758, 0),
				firstSampleTime:   time.Unix(1694270256, 0),
				totalSamplesCount: 4,
			},
		},
	}
	for _, tt := range tests {
		t1.Run(tt.name, func(t1 *testing.T) {
			t := tt.newTask()
			for _, sample := range tt.samples {
				t.AddSample(sample.sampleTime, sample.sampleValue, sample.sampleWeight)
			}
			if tt.lastSampleTime != t.lastSampleTime {
				t1.Errorf("AddSample() got lastSampleTime=%s, want lastSampleTime=%s", t.lastSampleTime, tt.lastSampleTime)
			}
			if tt.firstSampleTime != t.firstSampleTime {
				t1.Errorf("AddSample() got firstSampleTime=%s, want firstSampleTime=%s", t.firstSampleTime, tt.firstSampleTime)
			}
			if tt.totalSamplesCount != t.totalSamplesCount {
				t1.Errorf("AddSample() got totalSamplesCount=%d, want totalSamplesCount=%d", t.totalSamplesCount, tt.totalSamplesCount)
			}
		})
	}
}

func TestHistogramTask_AddRangeSample(t1 *testing.T) {
	type newTask func() *HistogramTask
	type sample struct {
		firstSampleTime time.Time
		lastSampleTime  time.Time
		sampleValue     float64
		sampleWeight    float64
	}
	type want struct {
		lastSampleTime    time.Time
		firstSampleTime   time.Time
		totalSamplesCount int
	}
	tests := []struct {
		name    string
		newTask newTask
		samples []sample
		want
	}{
		{
			name: "case1",
			newTask: func() *HistogramTask {
				t, _ := NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
				return t
			},
			samples: []sample{
				{
					firstSampleTime: time.Unix(1694270256, 0),
					lastSampleTime:  time.Unix(1694270768, 0),
					sampleValue:     1,
					sampleWeight:    1,
				},
				{
					firstSampleTime: time.Unix(1694270769, 0),
					lastSampleTime:  time.Unix(1694271769, 0),
					sampleValue:     1,
					sampleWeight:    1,
				},
			},
			want: want{
				lastSampleTime:    time.Unix(1694271769, 0),
				firstSampleTime:   time.Unix(1694270256, 0),
				totalSamplesCount: 2,
			},
		},
	}
	for _, tt := range tests {
		t1.Run(tt.name, func(t1 *testing.T) {
			t := tt.newTask()
			for _, sample := range tt.samples {
				t.AddRangeSample(sample.firstSampleTime, sample.lastSampleTime, sample.sampleValue, sample.sampleWeight)
			}
			if tt.lastSampleTime != t.lastSampleTime {
				t1.Errorf("AddSample() got lastSampleTime=%s, want lastSampleTime=%s", t.lastSampleTime, tt.lastSampleTime)
			}
			if tt.firstSampleTime != t.firstSampleTime {
				t1.Errorf("AddSample() got firstSampleTime=%s, want firstSampleTime=%s", t.firstSampleTime, tt.firstSampleTime)
			}
			if tt.totalSamplesCount != t.totalSamplesCount {
				t1.Errorf("AddSample() got totalSamplesCount=%d, want totalSamplesCount=%d", t.totalSamplesCount, tt.totalSamplesCount)
			}
		})
	}
}

type mockDatasourceEmptyQuery struct{}

func (m1 *mockDatasourceEmptyQuery) QueryTimeSeries(_ *datasourcetypes.Query, _ time.Time, _ time.Time, _ time.Duration) (*datasourcetypes.TimeSeries, error) {
	return &datasourcetypes.TimeSeries{Samples: []datasourcetypes.Sample{}}, nil
}

func (m1 *mockDatasourceEmptyQuery) ConvertMetricToQuery(_ datasourcetypes.Metric) (*datasourcetypes.Query, error) {
	return nil, nil
}

type mockDatasourcePanic struct{}

func (m1 *mockDatasourcePanic) QueryTimeSeries(_ *datasourcetypes.Query, _ time.Time, _ time.Time, _ time.Duration) (*datasourcetypes.TimeSeries, error) {
	panic("test panic")
}

func (m1 *mockDatasourcePanic) ConvertMetricToQuery(_ datasourcetypes.Metric) (*datasourcetypes.Query, error) {
	return nil, nil
}

type mockDatasource struct{}

func (m1 *mockDatasource) QueryTimeSeries(_ *datasourcetypes.Query, _ time.Time, _ time.Time, _ time.Duration) (*datasourcetypes.TimeSeries, error) {
	return &datasourcetypes.TimeSeries{Samples: []datasourcetypes.Sample{
		{
			Timestamp: 1694270256,
			Value:     1,
		},
		{
			Timestamp: 1694270765,
			Value:     2,
		},
		{
			Timestamp: 1694278758,
			Value:     3,
		},
		{
			Timestamp: 1694278700,
			Value:     4,
		},
	}}, nil
}

func (m1 *mockDatasource) ConvertMetricToQuery(_ datasourcetypes.Metric) (*datasourcetypes.Query, error) {
	return nil, nil
}

func TestHistogramTask_Run(t1 *testing.T) {
	type newTask func() *HistogramTask
	type args struct {
		ctx             context.Context
		datasourceProxy func() *datasource.Proxy
	}
	type want struct {
		lastSampleTime    time.Time
		firstSampleTime   time.Time
		totalSamplesCount int
	}
	tests := []struct {
		name                string
		newTask             newTask
		args                args
		wantNextRunInterval time.Duration
		wantErr             error
		want                want
	}{
		{
			name: "empty samples",
			newTask: func() *HistogramTask {
				task, _ := NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
				return task
			},
			args: args{
				ctx: context.Background(),
				datasourceProxy: func() *datasource.Proxy {
					proxy := datasource.NewProxy()
					proxy.RegisterDatasource(datasource.PrometheusDatasource, &mockDatasourceEmptyQuery{})
					return proxy
				},
			},
			wantNextRunInterval: 0,
			wantErr:             QuerySamplesIsEmptyErr,
		},
		{
			name: "panic",
			newTask: func() *HistogramTask {
				task, _ := NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
				return task
			},
			args: args{
				ctx: context.Background(),
				datasourceProxy: func() *datasource.Proxy {
					proxy := datasource.NewProxy()
					proxy.RegisterDatasource(datasource.PrometheusDatasource, &mockDatasourcePanic{})
					return proxy
				},
			},
			wantNextRunInterval: 0,
			wantErr:             HistogramTaskRunPanicErr,
		},
		{
			name: "cpu run",
			newTask: func() *HistogramTask {
				task, _ := NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
				return task
			},
			args: args{
				ctx: context.Background(),
				datasourceProxy: func() *datasource.Proxy {
					proxy := datasource.NewProxy()
					proxy.RegisterDatasource(datasource.PrometheusDatasource, &mockDatasource{})
					return proxy
				},
			},
			wantNextRunInterval: DefaultCPUTaskProcessInterval,
			wantErr:             nil,
			want: want{
				lastSampleTime:    time.Unix(1694278758, 0),
				firstSampleTime:   time.Unix(1694270256, 0),
				totalSamplesCount: 4,
			},
		},
		{
			name: "mem run",
			newTask: func() *HistogramTask {
				task, _ := NewTask(datasourcetypes.Metric{Resource: v1.ResourceMemory}, "")
				return task
			},
			args: args{
				ctx: context.Background(),
				datasourceProxy: func() *datasource.Proxy {
					proxy := datasource.NewProxy()
					proxy.RegisterDatasource(datasource.PrometheusDatasource, &mockDatasource{})
					return proxy
				},
			},
			wantNextRunInterval: DefaultMemTaskProcessInterval,
			wantErr:             nil,
			want: want{
				lastSampleTime:    time.Unix(1694278758, 0),
				firstSampleTime:   time.Unix(1694270256, 0),
				totalSamplesCount: 1,
			},
		},
	}
	for _, tt := range tests {
		t1.Run(tt.name, func(t1 *testing.T) {
			t := tt.newTask()
			gotNextRunInterval, err := t.Run(tt.args.ctx, tt.args.datasourceProxy())
			if tt.wantErr != nil && err.Error() != tt.wantErr.Error() {
				t1.Errorf("Run() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr == nil {
				if gotNextRunInterval != tt.wantNextRunInterval {
					t1.Errorf("Run() gotNextRunInterval = %v, want %v", gotNextRunInterval, tt.wantNextRunInterval)
				}
				if tt.want.lastSampleTime != t.lastSampleTime {
					t1.Errorf("Run() got lastSampleTime=%s, want lastSampleTime=%s", t.lastSampleTime, tt.want.lastSampleTime)
				}
				if tt.want.firstSampleTime != t.firstSampleTime {
					t1.Errorf("Run() got firstSampleTime=%s, want firstSampleTime=%s", t.firstSampleTime, tt.want.firstSampleTime)
				}
				if tt.want.totalSamplesCount != t.totalSamplesCount {
					t1.Errorf("Run() got totalSamplesCount=%d, want totalSamplesCount=%d", t.totalSamplesCount, tt.want.totalSamplesCount)
				}
			}
		})
	}
}

func TestHistogramTask_QueryPercentileValue(t1 *testing.T) {
	type newTask func() *HistogramTask
	type args struct {
		ctx        context.Context
		percentile float64
	}
	tests := []struct {
		name    string
		newTask newTask
		args    args
		want    float64
		wantErr error
	}{
		{
			name: "case0",
			newTask: func() *HistogramTask {
				return &HistogramTask{
					lastSampleTime: time.Now().Add(-25 * time.Hour),
				}
			},
			args: args{
				ctx:        context.Background(),
				percentile: 0.9,
			},
			want:    0.0,
			wantErr: DataPreparingErr,
		},
		{
			name: "case1",
			newTask: func() *HistogramTask {
				return &HistogramTask{
					firstSampleTime: time.Now().Add(-26 * time.Hour),
					lastSampleTime:  time.Now().Add(-25 * time.Hour),
				}
			},
			args: args{
				ctx:        context.Background(),
				percentile: 0.9,
			},
			want:    0.0,
			wantErr: SampleExpirationErr,
		},
		{
			name: "case2",
			newTask: func() *HistogramTask {
				return &HistogramTask{
					lastSampleTime:  time.Now(),
					firstSampleTime: time.Now().Add(-20 * time.Hour),
				}
			},
			args: args{
				ctx:        context.Background(),
				percentile: 0.9,
			},
			want:    0.0,
			wantErr: InsufficientSampleErr,
		},
		{
			name: "case3",
			newTask: func() *HistogramTask {
				task, _ := NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
				sampleTime := time.Now()
				task.AddSample(sampleTime.Add(-time.Hour*24), 20, 1000)
				task.AddSample(sampleTime, 10, 1)
				return task
			},
			args: args{
				ctx:        context.Background(),
				percentile: 0.9,
			},
			want:    20.40693,
			wantErr: nil,
		},
		{
			name: "case4",
			newTask: func() *HistogramTask {
				task, _ := NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
				sampleTime := time.Now()
				task.AddSample(sampleTime.Add(-time.Hour*24*20), 20, 1000)
				task.AddSample(sampleTime, 10, 1)
				return task
			},
			args: args{
				ctx:        context.Background(),
				percentile: 0.9,
			},
			want:    10.207902,
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t1.Run(tt.name, func(t1 *testing.T) {
			t := tt.newTask()
			got, err := t.QueryPercentileValue(tt.args.ctx, tt.args.percentile)
			if tt.wantErr != nil && err.Error() != tt.wantErr.Error() {
				t1.Errorf("QueryPercentileValue() error = %v, WantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr == nil {
				assert.InEpsilon(t1, tt.want, got, 1e-5)
			}
		})
	}
}

func TestHistogramTask_IsTimeoutNotExecute(t1 *testing.T) {
	type newTask func() *HistogramTask
	tests := []struct {
		name    string
		newFunc newTask
		want    bool
	}{
		{
			name: "case1",
			newFunc: func() *HistogramTask {
				lastRunTime := time.Now()
				return &HistogramTask{
					lastRunTime: lastRunTime,
					createTime:  lastRunTime.Add(-48 * time.Minute),
				}
			},
			want: false,
		},
		{
			name: "case1",
			newFunc: func() *HistogramTask {
				return &HistogramTask{
					lastRunTime: time.Now().Add(-(MaxOfAllowNotExecutionTime + time.Minute)),
				}
			},
			want: true,
		},
		{
			name: "case2",
			newFunc: func() *HistogramTask {
				return &HistogramTask{
					createTime: time.Now().Add(-(MaxOfAllowNotExecutionTime + time.Minute)),
				}
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t1.Run(tt.name, func(t1 *testing.T) {
			t := tt.newFunc()
			if got := t.IsTimeoutNotExecute(); got != tt.want {
				t1.Errorf("IsTimeoutNotExecute() = %v, want %v", got, tt.want)
			}
		})
	}
}
