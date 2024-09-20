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
)

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
