package ccdtarget

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func TestCCDGroupPlanner_GetFixedPlan(t *testing.T) {
	t.Parallel()
	type fields struct {
		ccdMBMin int
		ccdMBMax int
	}
	type args struct {
		fixed       int
		mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *plan.MBAlloc
	}{
		{
			name: "multiple QoSes",
			fields: fields{
				ccdMBMin: 4_000,
				ccdMBMax: 35_000,
			},
			args: args{
				fixed: 35_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {CCDs: sets.Int{12: sets.Empty{}, 11: sets.Empty{}}},
					"shared-50": {CCDs: sets.Int{8: sets.Empty{}}},
					"shared-30": {CCDs: sets.Int{8: sets.Empty{}, 9: sets.Empty{}, 13: sets.Empty{}}},
				},
			},
			want: &plan.MBAlloc{
				Plan: map[qosgroup.QoSGroup]map[int]int{
					"dedicated": {12: 35_000, 11: 35_000},
					"shared-50": {8: 35_000},
					"shared-30": {8: 35_000, 9: 35_000, 13: 35_000},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &linearDistributor{
				CCDMBMin: tt.fields.ccdMBMin,
				CCDMBMax: tt.fields.ccdMBMax,
			}
			if got := c.GetFixedPlan(tt.args.fixed, tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetFixedPlan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCCDGroupPlanner_getCCDMBPlan(t *testing.T) {
	t.Parallel()
	type fields struct {
		CCDMBMin int
		CCDMBMax int
	}
	type args struct {
		target int
		ccdMB  map[int]*stat.MBData
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[int]int
	}{
		{
			name: "0 if ok",
			fields: fields{
				CCDMBMin: 4_000,
				CCDMBMax: 35_000,
			},
			args: args{
				target: 25_475,
				ccdMB: map[int]*stat.MBData{
					4: {
						TotalMB:      0,
						LocalTotalMB: 0,
					},
				},
			},
			want: map[int]int{4: 4_000},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &linearDistributor{
				CCDMBMin: tt.fields.CCDMBMin,
				CCDMBMax: tt.fields.CCDMBMax,
			}
			if got := c.getCCDMBPlan(tt.args.target, tt.args.ccdMB); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getCCDMBPlan() = %v, want %v", got, tt.want)
			}
		})
	}
}
