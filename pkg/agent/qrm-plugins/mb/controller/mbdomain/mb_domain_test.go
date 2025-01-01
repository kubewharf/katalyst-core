package mbdomain

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_getApplicableQoSCCDMB(t *testing.T) {
	t.Parallel()
	type args struct {
		domain   *MBDomain
		qosccdmb map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name string
		args args
		want map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}{
		{
			name: "happy path of all applicable",
			args: args{
				domain: &MBDomain{
					ID:      3,
					CCDNode: map[int]int{16: 4, 17: 4, 22: 5, 23: 5},
				},
				qosccdmb: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupSystem: {
						CCDs: sets.Int{17: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
							17: {
								ReadsMB:  12345,
								WritesMB: 11111,
							},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				qosgroup.QoSGroupSystem: {
					CCDs: sets.Int{17: sets.Empty{}},
					CCDMB: map[int]*stat.MBData{
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
				domain: &MBDomain{
					ID:      3,
					CCDNode: map[int]int{16: 4, 17: 4, 22: 5, 23: 5},
				},
				qosccdmb: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupSystem: {
						CCDs: sets.Int{99: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
							17: {
								ReadsMB:  12345,
								WritesMB: 11111,
							},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{},
		},
		{
			name: "mixed CCDs leading to partial applicable",
			args: args{
				domain: &MBDomain{
					ID:      3,
					CCDNode: map[int]int{16: 4, 17: 4, 22: 5, 23: 5},
				},
				qosccdmb: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupSystem: {
						CCDs: sets.Int{17: sets.Empty{}, 99: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
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
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				qosgroup.QoSGroupSystem: {
					CCDs: sets.Int{17: sets.Empty{}},
					CCDMB: map[int]*stat.MBData{
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
			if got := tt.args.domain.GetApplicableQoSCCDMB(tt.args.qosccdmb); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getApplicableQoSCCDMB() = %v, want %v", got, tt.want)
			}
		})
	}
}
