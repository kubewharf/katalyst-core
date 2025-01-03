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

package controller

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func TestController_getDedicatedNodes(t *testing.T) {
	t.Parallel()
	type fields struct {
		domainManager *mbdomain.MBDomainManager
		CurrQoSCCDMB  map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		want   sets.Int
	}{
		{
			name: "happy path",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					CCDNode: map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3},
				},
				CurrQoSCCDMB: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupDedicated: {
						CCDMB: map[int]*stat.MBData{
							2: {
								TotalMB: 1_234,
							},
							3: {
								TotalMB: 1_333,
							},
						},
					},
				},
			},
			want: sets.Int{1: sets.Empty{}},
		},
		{
			name: "happy path of 0 traffic",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					CCDNode: map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3},
				},
				CurrQoSCCDMB: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupDedicated: {
						CCDMB: map[int]*stat.MBData{
							6: {
								TotalMB: 0,
							},
						},
					},
				},
			},
			want: sets.Int{3: sets.Empty{}},
		},
		{
			name: "happy path of no dedicated qos",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					CCDNode: map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3},
				},
				CurrQoSCCDMB: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupDedicated: {
						CCDMB: map[int]*stat.MBData{},
					},
				},
			},
			want: sets.Int{},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &Controller{
				DomainManager: tt.fields.domainManager,
				MBStat: &MBStatKeeper{
					currQoSCCDMB: tt.fields.CurrQoSCCDMB,
				},
			}
			if got := c.guessDedicatedNodesByCheckingActiveMBStat(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("guessDedicatedNodesByCheckingActiveMBStat() = %v, want %v", got, tt.want)
			}
		})
	}
}
