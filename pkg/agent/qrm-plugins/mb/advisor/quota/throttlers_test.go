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

package quota

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
)

func Test_throttler_GetGroupQuotas(t *testing.T) {
	t.Parallel()
	type fields struct {
		reservationRatio int
	}
	type args struct {
		groupLimits *resource.MBGroupLimits
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   resource.GroupSettings
	}{
		{
			name: "happy path",
			fields: fields{
				reservationRatio: 5,
			},
			args: args{
				groupLimits: &resource.MBGroupLimits{
					CapacityInMB: 6_000,
					FreeInMB:     0,
					GroupSorted: []sets.String{
						sets.NewString("shared-60"),
						sets.NewString("shared-50"),
					},
					GroupLimits: resource.GroupSettings{
						"shared-60": 6_000,
						"shared-50": 3_800,
					},
				},
			},
			want: resource.GroupSettings{
				"shared-60": 6_000,
				"shared-50": 3_800 - 6_000*5/100,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := throttler{
				reservationRatio: tt.fields.reservationRatio,
			}
			if got := th.GetGroupQuotas(tt.args.groupLimits); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetGroupQuotas() = %v, want %v", got, tt.want)
			}
		})
	}
}
