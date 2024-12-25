package metricstore

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type mockMetricFetcher struct {
	mock.Mock
	types.MetricsFetcher
}

func (m *mockMetricFetcher) GetByStringIndex(metricName string) interface{} {
	args := m.Called(metricName)
	return args.Get(0)
}

func Test_mbReader_getMetricByQoSCCD(t *testing.T) {
	t.Parallel()

	mockFatcher := new(mockMetricFetcher)
	dummyMetricData := map[string]map[int][]metric.MetricData{
		"dedicated": {
			2: {
				{
					Value: 996,
				},
				{
					Value: 0.888,
				},
			},
		},
	}
	mockFatcher.On("GetByStringIndex", "foo.bar").Return(dummyMetricData)

	type fields struct {
		metricsFetcher types.MetricsFetcher
	}
	type args struct {
		metricKey string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    map[string]map[int][]metric.MetricData
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				metricsFetcher: mockFatcher,
			},
			args: args{
				metricKey: "foo.bar",
			},
			want:    dummyMetricData,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &mbReader{
				metricsFetcher: tt.fields.metricsFetcher,
			}
			got, err := m.getMetricByQoSCCD(tt.args.metricKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("getMetricByQoSCCD() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getMetricByQoSCCD() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_mbReader_GetMBQoSGroups(t *testing.T) {
	t.Parallel()
	mockFatcher := new(mockMetricFetcher)
	dummyMetricData := map[string]map[int][]metric.MetricData{
		"dedicated": {
			2: {{Value: 2222}, {Value: 1.0}},
			3: {{Value: 3333}, {Value: 0.88}},
		},
		"shared-30": {
			4: {{Value: 1234}, {Value: 1.0}},
			5: {{Value: 1235}, {Value: 0.5}},
			0: {{Value: 1230}, {Value: 0}},
			1: {{Value: 1231}, {Value: 0.95}},
		},
	}
	mockFatcher.On("GetByStringIndex", "mb.total.qosgroup").Return(dummyMetricData)

	type fields struct {
		metricsFetcher types.MetricsFetcher
	}
	tests := []struct {
		name    string
		fields  fields
		want    map[qosgroup.QoSGroup]*stat.MBQoSGroup
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				metricsFetcher: mockFatcher,
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				"dedicated": {
					CCDs: sets.Int{2: sets.Empty{}, 3: sets.Empty{}},
					CCDMB: map[int]*stat.MBData{
						2: {TotalMB: 2222, LocalTotalMB: 2222},
						3: {TotalMB: 3333, LocalTotalMB: 2933},
					},
				},
				"shared-30": {
					CCDs: sets.Int{0: sets.Empty{}, 1: sets.Empty{}, 4: sets.Empty{}, 5: sets.Empty{}},
					CCDMB: map[int]*stat.MBData{
						0: {TotalMB: 1230, LocalTotalMB: 0},
						1: {TotalMB: 1231, LocalTotalMB: 1169},
						4: {TotalMB: 1234, LocalTotalMB: 1234},
						5: {TotalMB: 1235, LocalTotalMB: 617},
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
			m := &mbReader{
				metricsFetcher: tt.fields.metricsFetcher,
			}
			got, err := m.GetMBQoSGroups()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetMBQoSGroups() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
