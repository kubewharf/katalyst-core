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

package allocator

import (
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type mockCtrlGroupMBSetter struct {
	mock.Mock
}

func (m *mockCtrlGroupMBSetter) SetMB(ctrlGroup string, ccdMB map[int]int) error {
	args := m.Called(ctrlGroup, ccdMB)
	return args.Error(0)
}

func Test_planAllocator_Allocate(t *testing.T) {
	t.Parallel()

	ctrlGroupMBSetter := new(mockCtrlGroupMBSetter)
	ctrlGroupMBSetter.On("SetMB", "/sys/fs/resctrl/dedicated", map[int]int{2: 25_000, 3: 12_000}).Return(nil)

	type fields struct {
		ctrlGroupSetter resctrl.CtrlGroupMBSetter
	}
	type args struct {
		alloc *plan.MBAlloc
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				ctrlGroupSetter: ctrlGroupMBSetter,
			},
			args: args{
				alloc: &plan.MBAlloc{
					Plan: map[task.QoSGroup]map[int]int{task.QoSGroupDedicated: {2: 25_000, 3: 12_000}},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := planAllocator{
				ctrlGroupMBSetter: tt.fields.ctrlGroupSetter,
			}
			if err := p.Allocate(tt.args.alloc); (err != nil) != tt.wantErr {
				t.Errorf("Allocate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
