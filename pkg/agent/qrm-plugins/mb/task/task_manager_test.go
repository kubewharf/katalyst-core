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
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/sets"

	resctrlconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
)

type mockDataCleaner struct {
	mock.Mock
}

func (m *mockDataCleaner) Cleanup(monGroup string) {
	args := m.Called(monGroup)
	_ = args
}

func Test_manager_GetTasks(t *testing.T) {
	t.Parallel()
	task1 := &Task{
		PodUID: "123",
	}
	type fields struct {
		tasks map[string]*Task
	}
	tests := []struct {
		name   string
		fields fields
		want   []*Task
	}{
		{
			name: "happy path",
			fields: fields{
				tasks: map[string]*Task{"123": task1},
			},
			want: []*Task{task1},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &manager{
				tasks: tt.fields.tasks,
			}
			if got := m.GetTasks(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetTasks() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_refreshTasks(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("/sys/fs/resctrl/system/mon_groups/pod123-456", resctrlconsts.FolderPerm)
	_ = fs.MkdirAll("/sys/fs/resctrl/system/mon_groups/pod505-999", resctrlconsts.FolderPerm)
	_ = afero.WriteFile(fs, "/sys/fs/cgroup/cpuset/kubepods/burstable/pod505-999/cpuset.mems", []byte("8"), resctrlconsts.FilePerm)
	_ = afero.WriteFile(fs, "/sys/fs/cgroup/cpuset/kubepods/burstable/pod505-999/cpuset.cpus", []byte("16-19,116-119"), resctrlconsts.FilePerm)

	dataCleaner := new(mockDataCleaner)
	dataCleaner.On("Cleanup", "/sys/fs/resctrl/system/mon_groups/pod789-000")

	type fields struct {
		rawStateCleaner state.MBRawDataCleaner
		nodeCCDs        map[int]sets.Int
		cpuCCD          map[int]int
		tasks           map[string]*Task
		taskQoS         map[string]QoSGroup
		fs              afero.Fs
	}
	type args struct {
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wants   map[string]*Task
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path new 1 remove 1",
			fields: fields{
				rawStateCleaner: dataCleaner,
				cpuCCD:          map[int]int{16: 5, 17: 5, 18: 6, 19: 6, 116: 35, 117: 35, 118: 36, 119: 36},
				tasks: map[string]*Task{
					"pod123-456": {
						QoSGroup: "system",
						PodUID:   "pod123-456",
					},
					"pod789-000": {
						QoSGroup: "system",
						PodUID:   "pod789-000",
					},
				},
				taskQoS: make(map[string]QoSGroup),
			},
			args: args{},
			wants: map[string]*Task{
				"pod123-456": {
					QoSGroup: "system",
					PodUID:   "pod123-456",
				},
				"pod505-999": {
					QoSGroup:  "system",
					PodUID:    "pod505-999",
					NumaNodes: []int{8},
					CCDs:      []int{5, 6, 35, 36},
					CPUs:      []int{16, 17, 18, 19, 116, 117, 118, 119},
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &manager{
				rawStateCleaner: tt.fields.rawStateCleaner,
				nodeCCDs:        tt.fields.nodeCCDs,
				cpuCCD:          tt.fields.cpuCCD,
				tasks:           tt.fields.tasks,
				taskQoS:         tt.fields.taskQoS,
				fs:              fs,
			}
			err := m.RefreshTasks()
			if !tt.wantErr(t, err, fmt.Sprintf("refreshTasks()")) {
				return
			}
			assert.Equal(t, tt.wants, m.tasks)
			dataCleaner.AssertExpectations(t)
		})
	}
}

func TestParseMonGroup(t *testing.T) {
	t.Parallel()
	type args struct {
		path string
	}
	tests := []struct {
		name    string
		args    args
		want    QoSGroup
		want1   string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path",
			args: args{
				path: "/sys/fs/resctrl/dedicated/mon_groups/pod123-456-7890",
			},
			want:    "dedicated",
			want1:   "pod123-456-7890",
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, got1, err := ParseMonGroup(tt.args.path)
			if !tt.wantErr(t, err, fmt.Sprintf("ParseMonGroup(%v)", tt.args.path)) {
				return
			}
			assert.Equalf(t, tt.want, got, "ParseMonGroup(%v)", tt.args.path)
			assert.Equalf(t, tt.want1, got1, "ParseMonGroup(%v)", tt.args.path)
		})
	}
}

func Test_manager_DeleteTask(t *testing.T) {
	t.Parallel()
	dataCleaner := new(mockDataCleaner)
	dataCleaner.On("Cleanup", "/sys/fs/resctrl/dedicated/mon_groups/pod123")

	task := &Task{
		QoSGroup: "dedicated",
		PodUID:   "pod123",
	}

	type fields struct {
		rawStateCleaner state.MBRawDataCleaner
		nodeCCDs        map[int]sets.Int
		cpuCCD          map[int]int
		rwLock          sync.RWMutex
		tasks           map[string]*Task
		taskQoS         map[string]QoSGroup
		fs              afero.Fs
	}
	type args struct {
		task *Task
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "happy path",
			fields: fields{
				rawStateCleaner: dataCleaner,
				tasks: map[string]*Task{
					"pod123": task,
				},
				taskQoS: map[string]QoSGroup{
					"pod123": "dedicated_cores",
				},
			},
			args: args{
				task: task,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &manager{
				rawStateCleaner: tt.fields.rawStateCleaner,
				tasks:           tt.fields.tasks,
				taskQoS:         tt.fields.taskQoS,
			}
			m.DeleteTask(tt.args.task)
			assert.Empty(t, m.tasks)
			assert.Empty(t, m.taskQoS)
		})
	}
}
