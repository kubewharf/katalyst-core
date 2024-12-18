package readmb

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb/rmbtype"
	"reflect"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
)

type mockMonGrpReader struct {
	mock.Mock
}

func (m *mockMonGrpReader) ReadMB(monGroup string, dies []int) (map[int]rmbtype.MBStat, error) {
	args := m.Called(monGroup, dies)
	return args.Get(0).(map[int]rmbtype.MBStat), args.Error(1)
}

func TestQoSGroupMBReader_GetMB(t *testing.T) {
	t.Parallel()

	mockMGReader := new(mockMonGrpReader)
	mockMGReader.On("ReadMB", "/sys/fs/resctrl/system", []int{4, 5}).
		Return(map[int]rmbtype.MBStat{4: {Total: 111}, 5: {Total: 222}}, nil)

	type fields struct {
		ccds           []int
		monGroupReader resctrl.MonGroupReader
	}
	type args struct {
		qosGroup string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    map[int]rmbtype.MBStat
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				ccds:           []int{4, 5},
				monGroupReader: mockMGReader,
			},
			args: args{
				qosGroup: "system",
			},
			want:    map[int]rmbtype.MBStat{4: {Total: 111}, 5: {Total: 222}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q := &QoSGroupMBReader{
				ccds:           tt.fields.ccds,
				monGroupReader: tt.fields.monGroupReader,
			}
			got, err := q.GetMB(tt.args.qosGroup)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetMB() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetMB() got = %v, want %v", got, tt.want)
			}
		})
	}
}
