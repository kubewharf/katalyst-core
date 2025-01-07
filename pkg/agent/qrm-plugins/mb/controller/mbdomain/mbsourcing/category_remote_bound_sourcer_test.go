package mbsourcing

import (
	"testing"

	policyconfig "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"
	"github.com/stretchr/testify/assert"
)

func Test_categoryRemoteBoundSourcer_AttributeMBToSources_matrix(t *testing.T) {
	t.Parallel()
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
						TargetIncoming:      70_198,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            27_700,
						MBSourceRemote:      7_700,
					},
					{
						TargetIncoming:      59_000,
						MBSourceRemoteLimit: 20_000,
						MBSource:            14_121,
						MBSourceRemote:      5_180,
					},
				},
			},
			want: []int{63952, 54054}, // {63_952, 65_245} if no remote upper bound
		},
		{
			name: "both to throttle, major local",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      30_198,
						MBSourceRemoteLimit: 20_000,
						MBSource:            47_700,
						MBSourceRemote:      18_490,
					},
					{
						TargetIncoming:      29_000,
						MBSourceRemoteLimit: 20_000,
						MBSource:            35_121,
						MBSourceRemote:      5_180,
					},
				},
			},
			want: []int{46_291, 12_908},
		},
		{
			name: "one to throttle, the other to ease, major local",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      30_198,
						MBSourceRemoteLimit: 10_000,
						MBSource:            47_700,
						MBSourceRemote:      18_490,
					},
					{
						TargetIncoming:      60_000,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            35_121,
						MBSourceRemote:      5_180,
					},
				},
			},
			want: []int{25_641, 54_016}, // {36_182, 54_016},
		},
		// both major remote traffic
		{
			name: "both throttle, major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      30_198,
						MBSourceRemoteLimit: 5_000,
						MBSource:            37_700,
						MBSourceRemote:      30_000,
					},
					{
						TargetIncoming:      20_000,
						MBSourceRemoteLimit: 5_000,
						MBSource:            44_121,
						MBSourceRemote:      40_180,
					},
				},
			},
			want: []int{6_250, 5_434}, // {21_852, 28_347} if no remote limit
		},
		{
			name: "both to ease, major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      70_198,
						MBSourceRemoteLimit: 25_000,
						MBSource:            37_700,
						MBSourceRemote:      30_000,
					},
					{
						TargetIncoming:      50_000,
						MBSourceRemoteLimit: 25_000,
						MBSource:            44_121,
						MBSourceRemote:      40_180,
					},
				},
			},
			want: []int{31_250, 27_173}, // {55_740, 64_457},
		},
		{
			name: "one to throttle, the other to ease, major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      30_198,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            37_700,
						MBSourceRemote:      30_000,
					},
					{
						TargetIncoming:      55_000,
						MBSourceRemoteLimit: 22_000,
						MBSource:            44_121,
						MBSourceRemote:      40_180,
					},
				},
			},
			want: []int{66_573, 18_625},
		},
		// mixed traffic: one major local, the other major remote
		{
			name: "both to ease, one major local, the other major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      70_198,
						MBSourceRemoteLimit: 20_000,
						MBSource:            37_700,
						MBSourceRemote:      30_000,
					},
					{
						TargetIncoming:      60_000,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            34_121,
						MBSourceRemote:      10_180,
					},
				},
			},
			want: []int{25_000, 38448}, // {41_486, 38_448} if no remote limit
		},
		{
			name: "both to throttle, one major local, the other major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      9_198,
						MBSourceRemoteLimit: 20_000,
						MBSource:            37_700,
						MBSourceRemote:      30_000,
					},
					{
						TargetIncoming:      30_000,
						MBSourceRemoteLimit: 20_000,
						MBSource:            44_121,
						MBSourceRemote:      10_180,
					},
				},
			},
			want: []int{0, 38325},
		},
		{
			name: "one to throttle major local, one to ease major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      20_198,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            37_700,
						MBSourceRemote:      9_000,
					},
					{
						TargetIncoming:      50_000,
						MBSourceRemoteLimit: 15_000,
						MBSource:            44_121,
						MBSourceRemote:      28_180,
					},
				},
			},
			want: []int{0, 23_437}, // {0, 31_559} if no remote limit
		},
		{
			name: "one to ease major local, one to throttle major remote",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      80_198,
						MBSourceRemoteLimit: 20_000,
						MBSource:            37_700,
						MBSourceRemote:      9_000,
					},
					{
						TargetIncoming:      20_000,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            44_121,
						MBSourceRemote:      28_180,
					},
				},
			},
			want: []int{83_333, 0},
		},
		// special cases for total remote, simulating cross-fire
		{
			name: "both major total remote, both to ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      80_198,
						MBSourceRemoteLimit: 20_000,
						MBSource:            17_700,
						MBSourceRemote:      17_700,
					},
					{
						TargetIncoming:      30_000,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            44_121,
						MBSourceRemote:      44_121,
					},
				},
			},
			want: []int{20_000, 80_198}, // {30_000, 80_198} if no remote limit
		},
		{
			name: "both major total remote, both to throttle",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      20_198,
						MBSourceRemoteLimit: 20_000,
						MBSource:            57_700,
						MBSourceRemote:      57_700,
					},
					{
						TargetIncoming:      35_000,
						MBSourceRemoteLimit: 20_000,
						MBSource:            44_121,
						MBSourceRemote:      44_121,
					},
				},
			},
			want: []int{20_000, 20_000}, // {35_000, 20_198} if no remote limit
		},
		{
			name: "both major total remote, one to throttle and the other ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      40_198,
						MBSourceRemoteLimit: 15_000,
						MBSource:            57_700,
						MBSourceRemote:      57_700,
					},
					{
						TargetIncoming:      20_000,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            24_121,
						MBSourceRemote:      24_121,
					},
				},
			},
			want: []int{15_000, 40_198}, // {20_000, 40_198} if no remote limit
		},
		// another special case: both total local
		{
			name: "both major total remote, one to throttle and the other ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      40_198,
						MBSourceRemoteLimit: 5_000,
						MBSource:            57_700,
						MBSourceRemote:      0,
					},
					{
						TargetIncoming:      20_000,
						MBSourceRemoteLimit: 5_000,
						MBSource:            24_121,
						MBSourceRemote:      0,
					},
				},
			},
			want: []int{40_198, 20_000},
		},
		// really conner case: one total local, the other total remote
		{
			name: "one major total local, the other total remote, one to throttle and the other ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      40_198,
						MBSourceRemoteLimit: 20_000,
						MBSource:            57_700,
						MBSourceRemote:      0,
					},
					{
						TargetIncoming:      20_000,
						MBSourceRemoteLimit: 20_000,
						MBSource:            24_121,
						MBSourceRemote:      24_121,
					},
				},
			},
			want: []int{16_077, 20_000}, //{16_077, 24_121},
		},
		{
			name: "one major total local, the other total remote, one to throttle and the other ease - impossible?",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      20_000,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            57_700,
						MBSourceRemote:      0,
					},
					{
						TargetIncoming:      40_198,
						MBSourceRemoteLimit: 12_345,
						MBSource:            24_121,
						MBSourceRemote:      24_121,
					},
				},
			},
			want: []int{0, 12_345}, // {0, 20_000},
		},
		{
			name: "one major total local, the other total remote, both to ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      40_198,
						MBSourceRemoteLimit: 20_000,
						MBSource:            24_121,
						MBSourceRemote:      0,
					},
					{
						TargetIncoming:      20_000,
						MBSourceRemoteLimit: 20_000,
						MBSource:            10_700,
						MBSourceRemote:      10_700,
					},
				},
			},
			want: []int{26_809, 13_388},
		},
		// test case based on integration half-cross
		{
			name: "one major total local, the other half local; one to throttle, one to ease",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming:      35_198,
						MBSourceRemoteLimit: policyconfig.PolicyConfig.DomainMBMax,
						MBSource:            50_151,
						MBSourceRemote:      0,
					},
					{
						TargetIncoming:      81_641,
						MBSourceRemoteLimit: 20_000,
						MBSource:            51_517 + 20_485,
						MBSourceRemote:      20_485,
					},
				},
			},
			want: []int{2_409, 68_965}, // {2_409, 114_430} if no remote limit
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := categoryRemoteBoundSourcer{}
			assert.Equalf(t, tt.want, c.AttributeIncomingMBToSources(tt.args.domainTargets), "AttributeIncomingMBToSources(%v)", tt.args.domainTargets)
		})
	}
}
