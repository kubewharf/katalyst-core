package domaintarget

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy/ccdtarget"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_fullEasePlanner_GetPlan(t1 *testing.T) {
	type fields struct {
		ccdGroupPlanner *ccdtarget.CCDGroupPlanner
	}
	type args struct {
		capacity    int
		mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
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
				ccdGroupPlanner: ccdtarget.NewCCDGroupPlanner(4_000, 35_000),
			},
			args: args{
				capacity: 20_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"shared-30": {
						CCDs: sets.Int{0: sets.Empty{}, 1: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
							0: {TotalMB: 3_000},
							1: {TotalMB: 3_000},
						},
					},
				},
			},
			want: &plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{"shared-30": {0: 5500, 1: 5500}}},
		},
	}
	for _, tt := range tests {
		t1.Run(tt.name, func(t1 *testing.T) {
			t := fullEasePlanner{
				ccdGroupPlanner: tt.fields.ccdGroupPlanner,
			}
			if got := t.GetPlan(tt.args.capacity, tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("GetPlan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_halfEasePlanner_GetPlan(t *testing.T) {
	type fields struct {
		innerPlanner fullEasePlanner
	}
	type args struct {
		capacity    int
		mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
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
				innerPlanner: fullEasePlanner{ccdGroupPlanner: ccdtarget.NewCCDGroupPlanner(4_000, 35_000)},
			},
			args: args{
				capacity: 20_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"shared-30": {
						CCDs: sets.Int{0: sets.Empty{}, 1: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
							0: {TotalMB: 3_000},
							1: {TotalMB: 3_000},
						},
					},
				},
			},
			want: &plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{"shared-30": {0: 3_000 + 5_000/2/2, 1: 3_000 + 5_000/2/2}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := halfEasePlanner{
				innerPlanner: tt.fields.innerPlanner,
			}
			if got := s.GetPlan(tt.args.capacity, tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetPlan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_halfEasePlanner_GetQuota(t *testing.T) {
	t.Parallel()
	type fields struct {
		innerPlanner fullEasePlanner
	}
	type args struct {
		capacity     int
		currentUsage int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name:   "little socket traffic, small shared-30",
			fields: fields{},
			args: args{
				capacity:     122_000 - 35,
				currentUsage: 18490 + (14121 - 5180),
			},
			want: 70198,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := halfEasePlanner{
				innerPlanner: tt.fields.innerPlanner,
			}
			if got := s.GetQuota(tt.args.capacity, tt.args.currentUsage); got != tt.want {
				t.Errorf("GetQuota() = %v, want %v", got, tt.want)
			}
		})
	}
}
