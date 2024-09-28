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
	"sort"
	"testing"
)

func TestTask_GetResctrlCtrlGroup(t1 *testing.T) {
	t1.Parallel()
	type fields struct {
		QoSLevel QoSGroup
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
				QoSLevel: "shared",
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
				QoSGroup: tt.fields.QoSLevel,
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
		QoSLevel QoSGroup
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
				PodUID:   "pod111-222-333",
				QoSLevel: "dedicated",
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
				QoSGroup: tt.fields.QoSLevel,
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
		CPUs []int
		CCDs []int
	}
	tests := []struct {
		name   string
		fields fields
		want   []int
	}{
		{
			name: "happy path",
			fields: fields{
				CPUs: []int{93, 94, 126, 127},
				CCDs: []int{24, 32, 33},
			},
			want: []int{24, 32, 33},
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := Task{
				CPUs: tt.fields.CPUs,
				CCDs: tt.fields.CCDs,
			}
			got := t.CCDs
			sort.Slice(got, func(i, j int) bool { return i < j })
			if !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("GetCCDs() = %v, want %v", got, tt.want)
			}
		})
	}
}
