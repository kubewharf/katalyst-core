package mbsourcing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_majorfactorSourcer_AttributeMBToSources_matrix(t1 *testing.T) {
	t1.Parallel()
	type args struct {
		domainTargets []DomainMBTargetSource
	}
	tests := []struct {
		name string
		args args
		want []int
	}{
		// both major local traffic
		{
			name: "both to ease, major local",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 70_198,
						MBSource:       27_700,
						MBSourceRemote: 7_700,
					},
					{
						TargetIncoming: 59_000,
						MBSource:       14_121,
						MBSourceRemote: 5_180,
					},
				},
			},
			want: []int{67_235, 59_020},
		},
		{
			name: "both to throttle, major local",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 30_198,
						MBSource:       47_700,
						MBSourceRemote: 18_490,
					},
					{
						TargetIncoming: 29_000,
						MBSource:       35_121,
						MBSourceRemote: 5_180,
					},
				},
			},
			want: []int{37_671, 16_888},
		},
		{
			name: "one to throttle, the other to ease, major local",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 30_198,
						MBSource:       47_700,
						MBSourceRemote: 18_490,
					},
					{
						TargetIncoming: 60_000,
						MBSource:       35_121,
						MBSourceRemote: 5_180,
					},
				},
			},
			want: []int{37_585, 48_691},
		},
		// both major remote traffic
		{
			name: "both throttle, major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 30_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						TargetIncoming: 20_000,
						MBSource:       44_121,
						MBSourceRemote: 40_180,
					},
				},
			},
			want: []int{20_086, 28_655},
		},
		{
			name: "both to ease, major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 70_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						TargetIncoming: 50_000,
						MBSource:       44_121,
						MBSourceRemote: 40_180,
					},
				},
			},
			want: []int{55_882, 61_917},
		},
		{
			name: "one to throttle, the other to ease, major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 30_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						TargetIncoming: 55_000,
						MBSource:       44_121,
						MBSourceRemote: 40_180,
					},
				},
			},
			want: []int{64_164, 18_769},
		},
		// mixed traffic: one major local, the other major remote
		{
			name: "both to ease, one major local, the other major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 70_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						TargetIncoming: 60_000,
						MBSource:       34_121,
						MBSourceRemote: 10_180,
					},
				},
			},
			want: []int{38_592, 41_744},
		},
		{
			name: "both to throttle, one major local, the other major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 9_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						TargetIncoming: 30_000,
						MBSource:       44_121,
						MBSourceRemote: 10_180,
					},
				},
			},
			want: []int{16_012, 22_433},
		},
		{
			name: "one to throttle major local, one to ease major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 20_198,
						MBSource:       37_700,
						MBSourceRemote: 9_000,
					},
					{
						TargetIncoming: 50_000,
						MBSource:       44_121,
						MBSourceRemote: 28_180,
					},
				},
			},
			want: []int{0, 31_623},
		},
		{
			name: "one to ease major local, one to throttle major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 80_198,
						MBSource:       37_700,
						MBSourceRemote: 9_000,
					},
					{
						TargetIncoming: 20_000,
						MBSource:       44_121,
						MBSourceRemote: 28_180,
					},
				},
			},
			want: []int{68_330, 10_206},
		},
		// special cases for total remote, simulating cross-fire
		{
			name: "both major total remote, both to ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 80_198,
						MBSource:       17_700,
						MBSourceRemote: 17_700,
					},
					{
						TargetIncoming: 30_000,
						MBSource:       44_121,
						MBSourceRemote: 44_121,
					},
				},
			},
			want: []int{29_999, 58_560}, // the ideal {3_000, 80_198}
		},
		{
			name: "both major total remote, both to throttle",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 20_198,
						MBSource:       57_700,
						MBSourceRemote: 57_700,
					},
					{
						TargetIncoming: 35_000,
						MBSource:       44_121,
						MBSourceRemote: 44_121,
					},
				},
			},
			want: []int{35_000, 12_690}, // ideal {35_000, 20_198},
		},
		{
			name: "both major total remote, one to throttle and the other ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 40_198,
						MBSource:       57_700,
						MBSourceRemote: 57_700,
					},
					{
						TargetIncoming: 20_000,
						MBSource:       24_121,
						MBSourceRemote: 24_121,
					},
				},
			},
			want: []int{20_000, 40_198},
		},
		// another special case: both total local
		{
			name: "both major total remote, one to throttle and the other ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 40_198,
						MBSource:       57_700,
						MBSourceRemote: 0,
					},
					{
						TargetIncoming: 20_000,
						MBSource:       24_121,
						MBSourceRemote: 0,
					},
				},
			},
			want: []int{40_198, 14_203}, // ideal {40_198, 20_000}; major factor sourcer is not ideal but acceptable
		},
		// really conner case: one total local, the other total remote
		{
			name: "one major total local, the other total remote, one to throttle and the other ease - choose at conservative side",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 40_198,
						MBSource:       57_700,
						MBSourceRemote: 0,
					},
					{
						TargetIncoming: 20_000,
						MBSource:       24_121,
						MBSourceRemote: 24_121,
					},
				},
			},
			want: []int{0, 40198}, // inferior to what category getting sort of more balanced {16_077, 24_121}
		},
		{
			name: "one major total local, the other total remote, one to throttle and the other ease - impossible?",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 20_000,
						MBSource:       57_700,
						MBSourceRemote: 0,
					},
					{
						TargetIncoming: 40_198,
						MBSource:       24_121,
						MBSourceRemote: 24_121,
					},
				},
			},
			want: []int{0, 20_000},
		},
		{
			name: "one major total local, the other total remote, both to ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 40_198,
						MBSource:       24_121,
						MBSourceRemote: 0,
					},
					{
						TargetIncoming: 20_000,
						MBSource:       10_700,
						MBSourceRemote: 10_700,
					},
				},
			},
			want: []int{26_809, 133_88},
		},
		// test case based on integration half-cross
		{
			name: "one major total local, the other half local; one to throttle, one to ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 35_198,
						MBSource:       50_151,
						MBSourceRemote: 0,
					},
					{
						TargetIncoming: 81_641,
						MBSource:       51_517 + 20_485,
						MBSourceRemote: 20_485,
					},
				},
			},
			want: []int{2_734, 114_104},
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := majorfactorSourcer{}
			assert.Equalf(t1, tt.want, t.AttributeIncomingMBToSources(tt.args.domainTargets), "AttributeIncomingMBToSources(%v)", tt.args.domainTargets)
		})
	}
}
