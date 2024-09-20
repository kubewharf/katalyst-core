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

	"k8s.io/apimachinery/pkg/util/sets"
)

func TestTask_GetResctrlCtrlGroup(t1 *testing.T) {
	t1.Parallel()
	type fields struct {
		QoSLevel QoSLevel
	}
	tests := []struct {
		name    string
		fields  fields
		want    string
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				QoSLevel: "shared_cores",
			},
			want:    "/sys/fs/resctrl/shared",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := Task{
				QoSLevel: tt.fields.QoSLevel,
			}
			got, err := t.GetResctrlCtrlGroup()
			if (err != nil) != tt.wantErr {
				t1.Errorf("GetResctrlCtrlGroup() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t1.Errorf("GetResctrlCtrlGroup() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTask_GetResctrlMonGroup(t1 *testing.T) {
	t1.Parallel()
	type fields struct {
		PodUID   string
		QoSLevel QoSLevel
	}
	tests := []struct {
		name    string
		fields  fields
		want    string
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				PodUID:   "111-222-333",
				QoSLevel: "dedicated_cores",
			},
			want:    "/sys/fs/resctrl/dedicated/mon_groups/pod111-222-333",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := Task{
				PodUID:   tt.fields.PodUID,
				QoSLevel: tt.fields.QoSLevel,
			}
			got, err := t.GetResctrlMonGroup()
			if (err != nil) != tt.wantErr {
				t1.Errorf("GetResctrlMonGroup() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t1.Errorf("GetResctrlMonGroup() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTask_GetCCDs(t1 *testing.T) {
	t1.Parallel()
	type fields struct {
		NumaNode []int
		nodeCCDs map[int]sets.Int
	}
	tests := []struct {
		name   string
		fields fields
		want   []int
	}{
		{
			name: "happy path",
			fields: fields{
				NumaNode: []int{2},
				nodeCCDs: map[int]sets.Int{0: {0: sets.Empty{}, 1: sets.Empty{}}, 2: {4: sets.Empty{}, 5: sets.Empty{}}},
			},
			want: []int{4, 5},
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := Task{
				NumaNode: tt.fields.NumaNode,
				nodeCCDs: tt.fields.nodeCCDs,
			}
			if got := t.GetCCDs(); !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("GetCCDs() = %v, want %v", got, tt.want)
			}
		})
	}
}
