package crossdomain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain/mbsourcing"
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
				domainManager: &mbdomain.MBDomainManager{Domains: map[int]*mbdomain.MBDomain{0: {
					MBQuota: 122_000,
				}}},
				throttler: tt.fields.throttler,
				easer:     tt.fields.easer,
			}
			if got := g.calcDomainLeafTarget(tt.args.hiQoSMB, tt.args.leafMB); got != tt.want {
				t.Errorf("calcDomainLeafTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getPolicySourcerArgs(t *testing.T) {
	t.Parallel()
	type args struct {
		args []mbsourcing.DomainMBTargetSource
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "happy path",
			args: args{
				args: []mbsourcing.DomainMBTargetSource{
					{
						TargetIncoming: 35_678,
						MBSource:       51_224,
						MBSourceRemote: 4_567,
					},
					{
						TargetIncoming: 80_432,
						MBSource:       71_765,
						MBSourceRemote: 20_909,
					},
				},
			},
			want: "{domain: 0, target: 35678, sending total: 51224, sending to remote: 4567}, {domain: 1, target: 80432, sending total: 71765, sending to remote: 20909}, ",
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
