package strategy

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_halfThrottlePlanner_GetPlan(t1 *testing.T) {
	t1.Parallel()
	type fields struct {
		ccdGroupPlanner *CCDGroupPlanner
	}
	type args struct {
		capacity    int
		mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *plan.MBAlloc
	}{
		{
			name: "happy path of halving",
			fields: fields{
				ccdGroupPlanner: NewCCDGroupPlanner(8_000, 35_000),
			},
			args: args{
				capacity: 30_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					"shared-30": {
						CCDs: sets.Int{0: sets.Empty{}, 1: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{
							0: {TotalMB: 28_000},
						},
					},
				},
			},
			want: &plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{
				"shared-30": {0: 14_000},
			}},
		},
		{
			name: "halving is bounded by easement bar",
			fields: fields{
				ccdGroupPlanner: NewCCDGroupPlanner(2_000, 35_000),
			},
			args: args{
				capacity: 12_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					"shared-30": {
						CCDs: sets.Int{0: sets.Empty{}, 1: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{
							0: {TotalMB: 10_000},
						},
					},
				},
			},
			want: &plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{
				"shared-30": {0: 3_000},
			}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := halfThrottlePlanner{
				ccdGroupPlanner: tt.fields.ccdGroupPlanner,
			}
			if got := t.GetPlan(tt.args.capacity, tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("GetPlan() = %v, want %v", got, tt.want)
			}
		})
	}
}
