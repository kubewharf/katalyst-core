package crossdomain

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain/quotasourcing"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy/domaintarget"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb/rmbtype"
)

type mockThrottleEase struct {
	mock.Mock
	domaintarget.DomainMBAdjuster
}

func (m *mockThrottleEase) GetQuota(capacity, currentUsage int) int {
	args := m.Called(capacity, currentUsage)
	return args.Int(0)
}

func Test_globalMBPolicy_guessDamianTarget(t *testing.T) {
	t.Parallel()

	mockPlanner := new(mockThrottleEase)
	// for under pressure
	mockPlanner.On("GetQuota", 42_000, 40_000).Return(4_000)
	// for at ease
	mockPlanner.On("GetQuota", 62_000, 15_000).Return(26_500)

	type fields struct {
		throttler domaintarget.DomainMBAdjuster
		easer     domaintarget.DomainMBAdjuster
	}
	type args struct {
		hiQoSMB int
		leafMB  int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name: "under pressure",
			fields: fields{
				throttler: mockPlanner,
			},
			args: args{
				hiQoSMB: 80_000,
				leafMB:  40_000,
			},
			want: 4_000,
		},
		{
			name: "at ease",
			fields: fields{
				easer: mockPlanner,
			},
			args: args{
				hiQoSMB: 60_000,
				leafMB:  15_000,
			},
			want: 26_500,
		},
		{
			name:   "noop",
			fields: fields{},
			args: args{
				hiQoSMB: 70_000,
				leafMB:  45_000,
			},
			want: 45_000,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &globalMBPolicy{
				throttler: tt.fields.throttler,
				easer:     tt.fields.easer,
			}
			if got := g.calcDomainLeafTarget(tt.args.hiQoSMB, tt.args.leafMB); got != tt.want {
				t.Errorf("calcDomainLeafTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_globalMBPolicy_adjustSocketCCDMB(t *testing.T) {
	t.Parallel()
	type fields struct {
		domainManager *mbdomain.MBDomainManager
	}
	type args struct {
		mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}{
		{
			name: "0 admission 0 incubation",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					Domains: map[int]*mbdomain.MBDomain{
						0: {
							ID:        0,
							NumaNodes: []int{0, 1, 2, 3},
							NodeCCDs: map[int][]int{
								0: []int{0, 1},
								1: []int{2, 3},
								2: []int{4, 5},
								3: []int{6, 7},
							},
							PreemptyNodes: sets.Int{},
							MBQuota:       0,
						},
					},
				},
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {
						CCDMB: map[int]*stat.MBData{
							6: {TotalMB: 6_000, LocalTotalMB: 1_006},
							7: {TotalMB: 7_000, LocalTotalMB: 1_007},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				"dedicated": {
					CCDMB: map[int]*stat.MBData{
						6: {TotalMB: 6_000, LocalTotalMB: 1_006},
						7: {TotalMB: 7_000, LocalTotalMB: 1_007},
					},
				},
			},
		},
		{
			name: "having incubation",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					Domains: map[int]*mbdomain.MBDomain{
						0: {
							ID: 0,
							CCDIncubateds: map[int]time.Time{
								6: time.Now().Add(time.Minute),
							},
							NumaNodes: []int{0, 1, 2, 3},
							NodeCCDs: map[int][]int{
								0: []int{0, 1},
								1: []int{2, 3},
								2: []int{4, 5},
								3: []int{6, 7},
							},
							PreemptyNodes: sets.Int{},
							MBQuota:       0,
						},
					},
				},
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {
						CCDs: sets.Int{6: sets.Empty{}, 7: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
							6: {TotalMB: 6_000, LocalTotalMB: 1_006},
							7: {TotalMB: 7_000, LocalTotalMB: 1_007},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				"dedicated": {
					CCDs: sets.Int{6: sets.Empty{}, 7: sets.Empty{}},
					CCDMB: map[int]*stat.MBData{
						6: {TotalMB: 17_500, LocalTotalMB: 1_006},
						7: {TotalMB: 7_000, LocalTotalMB: 1_007},
					},
				},
			},
		},
		{
			name: "having admission",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					Domains: map[int]*mbdomain.MBDomain{
						0: {
							ID: 0,
							CCDIncubateds: map[int]time.Time{
								2: time.Now().Add(time.Minute),
								3: time.Now().Add(time.Minute),
							},
							PreemptyNodes: sets.Int{1: sets.Empty{}},
						},
					},
				},
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {
						CCDs: sets.Int{6: sets.Empty{}, 7: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
							6: {TotalMB: 6_000, LocalTotalMB: 1_006},
							7: {TotalMB: 7_000, LocalTotalMB: 1_007},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				"dedicated": {
					CCDs: sets.Int{2: sets.Empty{}, 3: sets.Empty{}, 6: sets.Empty{}, 7: sets.Empty{}},
					CCDMB: map[int]*stat.MBData{
						2: {TotalMB: 17_500},
						3: {TotalMB: 17_500},
						6: {TotalMB: 6_000, LocalTotalMB: 1_006},
						7: {TotalMB: 7_000, LocalTotalMB: 1_007},
					},
				},
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
			if got := g.adjustSocketCCDMBWithIncubates(tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("adjustSocketCCDMBWithIncubates() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getPolicySourcerArgs(t *testing.T) {
	t.Parallel()
	type args struct {
		args []quotasourcing.DomainMB
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "happy path",
			args: args{
				args: []quotasourcing.DomainMB{
					{
						Target:         35_678,
						MBSource:       51_224,
						MBSourceRemote: 4_567,
					},
					{
						Target:         80_432,
						MBSource:       71_765,
						MBSourceRemote: 20_909,
					},
				},
			},
			want: "domain: 0 target: 35678, sending total: 51224, sending to remote: 4567\ndomain: 1 target: 80432, sending total: 71765, sending to remote: 20909\n",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stringifyPolicySourceInfo(tt.args.args)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_getTotalLocalRemoteSummary(t *testing.T) {
	t.Parallel()
	type args struct {
		qosMBStat map[qosgroup.QoSGroup]rmbtype.MBStat
	}
	tests := []struct {
		name       string
		args       args
		wantTotal  int
		wantLocal  int
		wantRemote int
	}{
		{
			name: "happy path",
			args: args{
				qosMBStat: map[qosgroup.QoSGroup]rmbtype.MBStat{
					"dedicated": {
						Total: 12_345,
						Local: 10_000,
					},
					"system": {
						Total: 9_000,
						Local: 3_333,
					},
				},
			},
			wantTotal:  12_345 + 9_000,
			wantLocal:  10_000 + 3_333,
			wantRemote: 12_345 + 9_000 - (10_000 + 3_333),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotTotal, gotLocal, gotRemote := getTotalLocalRemoteMBStatSummary(tt.args.qosMBStat)
			assert.Equalf(t, tt.wantTotal, gotTotal, "getTotalLocalRemoteSummary(%v)", tt.args.qosMBStat)
			assert.Equalf(t, tt.wantLocal, gotLocal, "getTotalLocalRemoteSummary(%v)", tt.args.qosMBStat)
			assert.Equalf(t, tt.wantRemote, gotRemote, "getTotalLocalRemoteSummary(%v)", tt.args.qosMBStat)
		})
	}
}

func Test_globalMBPolicy_hasHighQoSMB(t *testing.T) {
	t.Parallel()
	type fields struct {
		zombieMB int
	}
	type args struct {
		mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   bool
	}{
		{
			name: "happy path positive",
			fields: fields{
				zombieMB: 100,
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {
						CCDMB: map[int]*stat.MBData{
							2: {TotalMB: 55},
						},
					},
					"shared-50": {
						CCDMB: map[int]*stat.MBData{
							15: {TotalMB: 101},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "happy path negative",
			fields: fields{
				zombieMB: 100,
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"system": {
						CCDMB: map[int]*stat.MBData{
							15: {TotalMB: 300},
						},
					},
					"dedicated": {
						CCDMB: map[int]*stat.MBData{
							2: {TotalMB: 55},
						},
					},
					"shared-30": {
						CCDMB: map[int]*stat.MBData{
							15: {TotalMB: 101},
						},
					},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &globalMBPolicy{
				zombieMB: tt.fields.zombieMB,
			}
			assert.Equalf(t, tt.want, g.hasHighQoSMB(tt.args.mbQoSGroups), "hasHighQoSMB(%v)", tt.args.mbQoSGroups)
		})
	}
}
