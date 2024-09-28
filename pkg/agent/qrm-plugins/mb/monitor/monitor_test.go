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
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/writemb"
)

type mockTaskManager struct {
	mock.Mock
	task.Manager
}

func (m *mockTaskManager) GetTasks() []*task.Task {
	args := m.Called()
	return args.Get(0).([]*task.Task)
}

func (m *mockTaskManager) RefreshTasks() error {
	args := m.Called()
	return args.Error(0)
}

type mockTaskMBReader struct {
	mock.Mock
}

func (m *mockTaskMBReader) GetMB(task *task.Task) (map[int]int, error) {
	args := m.Called(task)
	return args.Get(0).(map[int]int), args.Error(1)
}

type mockWriteMBReader struct {
	mock.Mock
}

func (m *mockWriteMBReader) GetMB(ccd int) (int, error) {
	args := m.Called(ccd)
	return args.Int(0), args.Error(1)
}

func Test_mbMonitor_GetQoSMBs(t1 *testing.T) {
	t1.Parallel()

	taskManager := new(mockTaskManager)
	taskManager.On("GetTasks").Return([]*task.Task{{
		PodUID:   "123-45-6789",
		QoSGroup: "test",
	}})

	taskMBReader := new(mockTaskMBReader)
	taskMBReader.On("GetMB", &task.Task{
		PodUID:   "123-45-6789",
		QoSGroup: "test",
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
		want    map[task.QoSGroup]map[int]int
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				taskManager: taskManager,
				mbReader:    taskMBReader,
			},
			want:    map[task.QoSGroup]map[int]int{"test": {0: 1000, 1: 2500}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := mbMonitor{
				taskManager: tt.fields.taskManager,
				rmbReader:   tt.fields.mbReader,
			}
			got, err := t.getReadsMBs()
			if (err != nil) != tt.wantErr {
				t1.Errorf("getReadsMBs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("getReadsMBs() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_mbMonitor_GetMBQoSGroups(t1 *testing.T) {
	t1.Parallel()

	testTask := &task.Task{
		QoSGroup: "foo",
		PodUID:   "123-4567",
	}

	taskManager := new(mockTaskManager)
	taskManager.On("RefreshTasks").Return(nil)
	taskManager.On("GetTasks").Return([]*task.Task{testTask})

	taskMBReader := new(mockTaskMBReader)
	taskMBReader.On("GetMB", testTask).Return(map[int]int{2: 200, 3: 300}, nil)

	wmbReader := new(mockWriteMBReader)
	wmbReader.On("GetMB", 2).Return(20, nil)
	wmbReader.On("GetMB", 3).Return(30, nil)

	type fields struct {
		taskManager task.Manager
		rmbReader   task.TaskMBReader
		wmbReader   writemb.WriteMBReader
	}
	tests := []struct {
		name    string
		fields  fields
		want    map[task.QoSGroup]*MBQoSGroup
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				taskManager: taskManager,
				rmbReader:   taskMBReader,
				wmbReader:   wmbReader,
			},
			want: map[task.QoSGroup]*MBQoSGroup{
				"foo": {
					CCDs: sets.Int{2: sets.Empty{}, 3: sets.Empty{}},
					CCDMB: map[int]*MBData{
						2: {ReadsMB: 200, WritesMB: 20},
						3: {ReadsMB: 300, WritesMB: 30},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := mbMonitor{
				taskManager: tt.fields.taskManager,
				rmbReader:   tt.fields.rmbReader,
				wmbReader:   tt.fields.wmbReader,
			}
			got, err := t.GetMBQoSGroups()
			if (err != nil) != tt.wantErr {
				t1.Errorf("GetMBQoSGroups() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("GetMBQoSGroups() got = %v, want %v", got, tt.want)
			}
		})
	}
}
