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
	"reflect"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
)

type mockMonGrpReader struct {
	mock.Mock
}

func (m *mockMonGrpReader) ReadMB(monGroup string, dies []int) (map[int]int, error) {
	args := m.Called(monGroup, dies)
	return args.Get(0).(map[int]int), args.Error(1)
}

func Test_taskMBReader_ReadMB(t1 *testing.T) {
	t1.Parallel()

	mockMGReader := new(mockMonGrpReader)
	mockMGReader.On("ReadMB", "/sys/fs/resctrl/reclaimed/mon_groups/pod123-321-1122", []int{4, 5}).
		Return(map[int]int{4: 111, 5: 222}, nil)

	type fields struct {
		monGroupReader resctrl.MonGroupReader
	}
	type args struct {
		task *Task
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    map[int]int
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				monGroupReader: mockMGReader,
			},
			args: args{
				task: &Task{
					QoSGroup: "reclaimed",
					PodUID:   "pod123-321-1122",
					CPUs:     []int{16, 17},
					CCDs:     []int{4, 5},
				},
			},
			want:    map[int]int{4: 111, 5: 222},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := taskMBReader{
				monGroupReader: tt.fields.monGroupReader,
			}
			got, err := t.GetMB(tt.args.task)
			if (err != nil) != tt.wantErr {
				t1.Errorf("GetMB() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("GetMB() got = %v, want %v", got, tt.want)
			}
		})
	}
}
