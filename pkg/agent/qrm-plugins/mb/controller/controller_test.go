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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_getApplicableQoSCCDMB(t *testing.T) {
	t.Parallel()
	type args struct {
		domain   *mbdomain.MBDomain
		qosccdmb map[qosgroup.QoSGroup]*monitor.MBQoSGroup
	}
	tests := []struct {
		name string
		args args
		want map[qosgroup.QoSGroup]*monitor.MBQoSGroup
	}{
		{
			name: "happy path of all applicable",
			args: args{
				domain: &mbdomain.MBDomain{
					ID:      3,
					CCDNode: map[int]int{16: 4, 17: 4, 22: 5, 23: 5},
				},
				qosccdmb: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					qosgroup.QoSGroupSystem: {
						CCDs: sets.Int{17: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{
							17: {
								ReadsMB:  12345,
								WritesMB: 11111,
							},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
				qosgroup.QoSGroupSystem: {
					CCDs: sets.Int{17: sets.Empty{}},
					CCDMB: map[int]*monitor.MBData{
						17: {
							ReadsMB:  12345,
							WritesMB: 11111,
						},
					},
				},
			},
		},
		{
			name: "happy path not applicable",
			args: args{
				domain: &mbdomain.MBDomain{
					ID:      3,
					CCDNode: map[int]int{16: 4, 17: 4, 22: 5, 23: 5},
				},
				qosccdmb: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					qosgroup.QoSGroupSystem: {
						CCDs: sets.Int{99: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{
							17: {
								ReadsMB:  12345,
								WritesMB: 11111,
							},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{},
		},
		{
			name: "mixed CCDs leading to partial applicable",
			args: args{
				domain: &mbdomain.MBDomain{
					ID:      3,
					CCDNode: map[int]int{16: 4, 17: 4, 22: 5, 23: 5},
				},
				qosccdmb: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					qosgroup.QoSGroupSystem: {
						CCDs: sets.Int{17: sets.Empty{}, 99: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{
							17: {
								ReadsMB:  12345,
								WritesMB: 11111,
							},
							99: {
								ReadsMB:  99999,
								WritesMB: 99999,
							},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
				qosgroup.QoSGroupSystem: {
					CCDs: sets.Int{17: sets.Empty{}},
					CCDMB: map[int]*monitor.MBData{
						17: {
							ReadsMB:  12345,
							WritesMB: 11111,
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := getApplicableQoSCCDMB(tt.args.domain, tt.args.qosccdmb); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getApplicableQoSCCDMB() = %v, want %v", got, tt.want)
			}
		})
	}
}
