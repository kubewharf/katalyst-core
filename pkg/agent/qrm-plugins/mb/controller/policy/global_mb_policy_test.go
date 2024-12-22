package policy

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_globalMBPolicy_sumHighQoSMB(t *testing.T) {
	t.Parallel()
	type fields struct {
		domainManager *mbdomain.MBDomainManager
	}
	type args struct {
		mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[int]int
	}{
		{
			name: "happy path",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					Domains: map[int]*mbdomain.MBDomain{
						1: {
							ID:        1,
							NumaNodes: []int{0, 1, 2, 3},
							NodeCCDs: map[int][]int{
								0: {0, 1},
								1: {2, 3},
								2: {4, 5},
								3: {6, 7},
							},
						},
						0: {
							ID:        0,
							NumaNodes: []int{4, 5, 6, 7},
							NodeCCDs: map[int][]int{
								4: {8, 9},
								5: {10, 11},
								6: {12, 13},
								7: {14, 15},
							},
						},
					},
					CCDDomain: map[int]int{
						0: 1, 1: 1, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1, 7: 1,
						8: 0, 9: 0, 10: 0, 11: 0, 12: 0, 13: 0, 14: 0, 15: 0,
					},
				},
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					"shared-50": {
						CCDMB: map[int]*monitor.MBData{
							1:  {TotalMB: 16_000},
							4:  {TotalMB: 12_000},
							12: {TotalMB: 21_000},
						},
					},
					"dedicated": {
						CCDMB: map[int]*monitor.MBData{
							0:  {TotalMB: 10_000},
							3:  {TotalMB: 9_000},
							15: {TotalMB: 22_000},
						},
					},
				},
			},
			want: map[int]int{
				0: 43_000,
				1: 47_000,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &globalMBPolicy{
				domainManager: tt.fields.domainManager,
			}
			if got := g.sumHighQoSMB(tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sumHighQoSMB() = %v, want %v", got, tt.want)
			}
		})
	}
}
