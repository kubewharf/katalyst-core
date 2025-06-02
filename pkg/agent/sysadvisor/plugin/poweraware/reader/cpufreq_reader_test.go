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

package reader

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type mockFreqReader struct {
	mock.Mock
}

func (m *mockFreqReader) GetNodeMetric(metricName string) (metric.MetricData, error) {
	args := m.Called(metricName)
	return args.Get(0).(metric.MetricData), args.Error(1)
}

func TestCPUFreqReader_get(t *testing.T) {
	moment := time.Date(2025, 5, 27, 6, 7, 30, 0, time.UTC)
	TwoSecBefore := time.Date(2025, 5, 27, 6, 7, 28, 0, time.UTC)
	FiveSecBefore := time.Date(2025, 5, 27, 6, 7, 25, 0, time.UTC)

	freqReaderMockerOK := new(mockFreqReader)
	freqReaderMockerOK.On("GetNodeMetric", "scaling.cur.freq.khz").Return(
		metric.MetricData{
			Value: 25000_000,
			Time:  &TwoSecBefore,
		},
		nil,
	)

	freqReaderMockerZero := new(mockFreqReader)
	freqReaderMockerZero.On("GetNodeMetric", "scaling.cur.freq.khz").Return(
		metric.MetricData{
			Value: 0,
			Time:  &TwoSecBefore,
		},
		nil,
	)

	freqReaderMockerStale := new(mockFreqReader)
	freqReaderMockerStale.On("GetNodeMetric", "scaling.cur.freq.khz").Return(
		metric.MetricData{
			Value: 25000_000,
			Time:  &FiveSecBefore,
		},
		nil,
	)

	t.Parallel()
	type fields struct {
		NodeMetricGetter NodeMetricGetter
	}
	type args struct {
		ctx context.Context
		now time.Time
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    int
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path",
			fields: fields{
				NodeMetricGetter: freqReaderMockerOK,
			},
			args: args{
				ctx: context.TODO(),
				now: moment,
			},
			want:    25000_000,
			wantErr: assert.NoError,
		},
		{
			name: "negative zero value",
			fields: fields{
				NodeMetricGetter: freqReaderMockerZero,
			},
			args: args{
				ctx: context.TODO(),
				now: moment,
			},
			want:    0,
			wantErr: assert.Error,
		},
		{
			name: "negative stale value",
			fields: fields{
				NodeMetricGetter: freqReaderMockerStale,
			},
			args: args{
				ctx: context.TODO(),
				now: moment,
			},
			want:    0,
			wantErr: assert.Error,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &cpuFreqReader{
				NodeMetricGetter: tt.fields.NodeMetricGetter,
			}
			got, err := m.get(tt.args.ctx, tt.args.now)
			if !tt.wantErr(t, err, fmt.Sprintf("get(%v, %v)", tt.args.ctx, tt.args.now)) {
				return
			}
			assert.Equalf(t, tt.want, got, "get(%v, %v)", tt.args.ctx, tt.args.now)
			mocker := tt.fields.NodeMetricGetter.(*mockFreqReader)
			mocker.AssertExpectations(t)
		})
	}
}
