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

package advisor

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

func Test_maskPlanWithNoThrottles(t *testing.T) {
	t.Parallel()
	type args struct {
		plan             *plan.MBPlan
		groupNoThrottles sets.String
		noThrottleCCDMB  int
	}
	tests := []struct {
		name string
		args args
		want *plan.MBPlan
	}{
		{
			name: "happy path of no change",
			args: args{
				plan: &plan.MBPlan{
					MBGroups: map[string]plan.GroupCCDPlan{
						"/":         {0: 100, 1: 100},
						"shared-90": {0: 900, 1: 800},
					},
				},
				noThrottleCCDMB: 9_999,
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"/":         {0: 100, 1: 100},
					"shared-90": {0: 900, 1: 800},
				},
			},
		},
		{
			name: "happy path of no-throttle changes",
			args: args{
				plan: &plan.MBPlan{
					MBGroups: map[string]plan.GroupCCDPlan{
						"/":         {0: 100, 1: 100},
						"shared-90": {0: 900, 1: 800},
					},
				},
				groupNoThrottles: sets.NewString("shared-90"),
				noThrottleCCDMB:  9_999,
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"/":         {0: 100, 1: 100},
					"shared-90": {0: 9999, 1: 9999},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := maskPlanWithNoThrottles(tt.args.plan, tt.args.groupNoThrottles, tt.args.noThrottleCCDMB); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("maskPlanWithNoThrottles() = %v, want %v", got, tt.want)
			}
		})
	}
}
