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
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func TestMerge(t *testing.T) {
	t.Parallel()
	type args struct {
		plans []*MBAlloc
	}
	tests := []struct {
		name string
		args args
		want *MBAlloc
	}{
		{
			name: "happy path",
			args: args{
				plans: []*MBAlloc{
					{
						Plan: map[qosgroup.QoSGroup]map[int]int{
							"dedicated": {4: 12, 5: 13},
						},
					},
					{
						Plan: map[qosgroup.QoSGroup]map[int]int{
							"dedicated": {2: 20, 3: 21},
							"shared":    {0: 2, 1: 3, 6: 5, 7: 5},
							"relaimed":  {0: 2, 1: 2},
						},
					},
				},
			},
			want: &MBAlloc{
				Plan: map[qosgroup.QoSGroup]map[int]int{
					"dedicated": {2: 20, 3: 21, 4: 12, 5: 13},
					"shared":    {0: 2, 1: 3, 6: 5, 7: 5},
					"relaimed":  {0: 2, 1: 2},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Merge(tt.args.plans...); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Merge() = %v, want %v", got, tt.want)
			}
		})
	}
}
