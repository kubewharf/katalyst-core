package policy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"reflect"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain/quotasourcing"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_globalMBPolicy_sumHighQoSMB(t *testing.T) {
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
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"shared-50": {
						CCDMB: map[int]*stat.MBData{
							1:  {TotalMB: 16_000},
							4:  {TotalMB: 12_000},
							12: {TotalMB: 21_000},
						},
					},
					"dedicated": {
						CCDMB: map[int]*stat.MBData{
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

func Test_globalMBPolicy_sumLeafDomainMB(t *testing.T) {
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
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"shared-50": {
						CCDMB: map[int]*stat.MBData{
							1:  {TotalMB: 16_000},
							4:  {TotalMB: 12_000},
							12: {TotalMB: 21_000},
						},
					},
					"shared-30": {
						CCDMB: map[int]*stat.MBData{
							0:  {TotalMB: 10_000},
							3:  {TotalMB: 9_000},
							15: {TotalMB: 22_000},
						},
					},
				},
			},
			want: map[int]int{0: 22_000, 1: 19_000},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &globalMBPolicy{
				domainManager: tt.fields.domainManager,
			}
			if got := g.sumLeafDomainMB(tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sumLeafDomainMBLocal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_globalMBPolicy_sumLeafDomainMBLocal(t *testing.T) {
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
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"shared-50": {
						CCDMB: map[int]*stat.MBData{
							1: {
								TotalMB: 16_000, LocalTotalMB: 3_000},
							4:  {TotalMB: 12_000},
							12: {TotalMB: 21_000},
						},
					},
					"shared-30": {
						CCDMB: map[int]*stat.MBData{
							0:  {TotalMB: 10_000, LocalTotalMB: 8_000},
							3:  {TotalMB: 9_000, LocalTotalMB: 1_500},
							15: {TotalMB: 22_000, LocalTotalMB: 12_000},
						},
					},
				},
			},
			want: map[int]int{0: 12_000, 1: 9_500},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &globalMBPolicy{
				domainManager: tt.fields.domainManager,
			}
			if got := g.sumLeafDomainMBLocal(tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sumLeafDomainMBLocal() = %v, want %v", got, tt.want)
			}
		})
	}
}

type mockThrottleEase struct {
	mock.Mock
	strategy.LowPrioPlanner
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
		throttler strategy.LowPrioPlanner
		easer     strategy.LowPrioPlanner
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

func Test_globalMBPolicy_getLeafMBTargets(t *testing.T) {
	t.Parallel()

	mockPlanner := new(mockThrottleEase)
	// for busy domain under pressure
	mockPlanner.On("GetQuota", 40_000, 38_000).Return(15_000)
	// for idle domain at ease
	mockPlanner.On("GetQuota", 122_000, 0).Return(61_000)

	type fields struct {
		domainManager *mbdomain.MBDomainManager
		throttler     strategy.LowPrioPlanner
		easer         strategy.LowPrioPlanner
	}
	type args struct {
		mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []quotasourcing.DomainMB
	}{
		{
			name: "one domain busy, the other idle",
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
				throttler: mockPlanner,
				easer:     mockPlanner,
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {
						CCDMB: map[int]*stat.MBData{
							0: {TotalMB: 20_000},
							1: {TotalMB: 21_000},
							4: {TotalMB: 20_000},
							5: {TotalMB: 21_000},
						},
					},
					"shared-30": {
						CCDMB: map[int]*stat.MBData{
							2: {TotalMB: 10_000, LocalTotalMB: 9_500},
							3: {TotalMB: 9_000, LocalTotalMB: 8_500},
							6: {TotalMB: 10_000, LocalTotalMB: 9_500},
							7: {TotalMB: 9_000, LocalTotalMB: 8_500},
						},
					},
				},
			},
			want: []quotasourcing.DomainMB{
				{
					Target:         61_000,
					MBSource:       0,
					MBSourceRemote: 0,
				},
				{
					Target:         15_000,
					MBSource:       38_000,
					MBSourceRemote: 2_000,
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
				throttler:     tt.fields.throttler,
				easer:         tt.fields.easer,
			}
			if got := g.getLeafMBTargets(tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getLeafMBTargets() = %v, want %v", got, tt.want)
			}
		})
	}
}
