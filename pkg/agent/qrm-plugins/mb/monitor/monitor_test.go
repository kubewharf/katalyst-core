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

package monitor

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type mockTaskManager struct {
	mock.Mock
	task.Manager
}

func (m *mockTaskManager) GetTasks() []*task.Task {
	args := m.Called()
	return args.Get(0).([]*task.Task)
}

type mockTaskMBReader struct {
	mock.Mock
}

func (m *mockTaskMBReader) ReadMB(task *task.Task) (map[int]int, error) {
	args := m.Called(task)
	return args.Get(0).(map[int]int), args.Error(1)
}

func Test_mbMonitor_GetQoSMBs(t1 *testing.T) {
	t1.Parallel()

	taskManager := new(mockTaskManager)
	taskManager.On("GetTasks").Return([]*task.Task{{
		PodUID:   "123-45-6789",
		QoSLevel: "test",
	}})

	taskMBReader := new(mockTaskMBReader)
	taskMBReader.On("ReadMB", &task.Task{
		PodUID:   "123-45-6789",
		QoSLevel: "test",
	}).Return(map[int]int{
		0: 1_000, 1: 2_500,
	}, nil)

	type fields struct {
		taskManager task.Manager
		mbReader    task.TaskMBReader
	}
	tests := []struct {
		name    string
		fields  fields
		want    map[task.QoSLevel]map[int]int
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				taskManager: taskManager,
				mbReader:    taskMBReader,
			},
			want:    map[task.QoSLevel]map[int]int{"test": {0: 1000, 1: 2500}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := mbMonitor{
				taskManager: tt.fields.taskManager,
				mbReader:    tt.fields.mbReader,
			}
			got, err := t.getQoSMBs()
			if (err != nil) != tt.wantErr {
				t1.Errorf("getQoSMBs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("getQoSMBs() got = %v, want %v", got, tt.want)
			}
		})
	}
}
