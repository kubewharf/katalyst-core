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

package plan

import (
	"fmt"
	"reflect"
	"testing"
)

func TestCCDPlan_ToSchemataInstruction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		c    GroupCCDPlan
		want []byte
	}{
		{
			name: "happy path",
			c: GroupCCDPlan{
				0: 4_000, 2: 4_500,
			},
			want: []byte("MB:0=32;2=36;\n"),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.c.ToSchemataInstruction(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ToSchemataInstruction() = %v, want %v", string(got), string(tt.want))
			}
		})
	}
}

func TestMBPlan_String(t *testing.T) {
	t.Parallel()
	type fields struct {
		MBGroups map[string]GroupCCDPlan
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "happy path",
			fields: fields{
				MBGroups: map[string]GroupCCDPlan{
					"dedicated": {
						3: 3_333,
						5: 5_555,
					},
					"shared-50": {
						2: 20_000,
						3: 30_000,
					},
				},
			},
			want: "[mb-plan]\n\t[dedicated]\t3:3333,5:5555,\n\t[shared-50]\t2:20000,3:30000,\n",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &MBPlan{
				MBGroups: tt.fields.MBGroups,
			}
			if got := fmt.Sprintf("%v", m); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
			t.Logf("plan=%s", m)
		})
	}
}
