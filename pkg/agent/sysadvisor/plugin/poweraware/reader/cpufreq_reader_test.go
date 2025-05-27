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
	fourteenSecBefore := time.Date(2025, 5, 27, 6, 7, 16, 0, time.UTC)

	mockFreqReader := new(mockFreqReader)
	mockFreqReader.On("GetNodeMetric", "scaling.cur.freq.mhz").Return(
		metric.MetricData{
			Value: 25000,
			Time:  &fourteenSecBefore,
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
				NodeMetricGetter: mockFreqReader,
			},
			args: args{
				ctx: context.TODO(),
				now: moment,
			},
			want:    25000,
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &CPUFreqReader{
				NodeMetricGetter: tt.fields.NodeMetricGetter,
			}
			got, err := m.get(tt.args.ctx, tt.args.now)
			if !tt.wantErr(t, err, fmt.Sprintf("get(%v, %v)", tt.args.ctx, tt.args.now)) {
				return
			}
			assert.Equalf(t, tt.want, got, "get(%v, %v)", tt.args.ctx, tt.args.now)
		})
	}
}
