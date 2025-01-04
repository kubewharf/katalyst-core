package ccdtarget

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_logarithmicScalePlanner_GetPlan(t *testing.T) {
	t.Parallel()
	type args struct {
		total int
		ccdMB map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name string
		args args
		want *plan.MBAlloc
	}{
		{
			name: "happy path of equality",
			args: args{
				total: 36_000,
				ccdMB: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"shared-30": {
						CCDMB: map[int]*stat.MBData{
							2: {TotalMB: 4_000},
							3: {TotalMB: 4_000},
							6: {TotalMB: 4_000},
							7: {TotalMB: 4_000},
						},
					},
				},
			},
			want: &plan.MBAlloc{
				Plan: map[qosgroup.QoSGroup]map[int]int{
					"shared-30": {
						2: 9_000,
						3: 9_000,
						6: 9_000,
						7: 9_000,
					},
				},
			},
		},
		{
			name: "happy path of decaying",
			args: args{
				total: 36_000,
				ccdMB: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"shared-30": {
						CCDMB: map[int]*stat.MBData{
							2: {TotalMB: 6_000},
							3: {TotalMB: 6_000},
							6: {TotalMB: 2_000},
							7: {TotalMB: 1_000},
						},
					},
				},
			},
			want: &plan.MBAlloc{
				Plan: map[qosgroup.QoSGroup]map[int]int{
					"shared-30": map[int]int{
						2: 10_046,
						3: 10_046,
						6: 8_372,
						7: 7_534,
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := NewLogarithmicScalePlanner(4_000, 35_000)
			if got := l.GetPlan(tt.args.total, tt.args.ccdMB); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetPlan() = %v, want %v", got, tt.want)
			}
		})
	}
}
