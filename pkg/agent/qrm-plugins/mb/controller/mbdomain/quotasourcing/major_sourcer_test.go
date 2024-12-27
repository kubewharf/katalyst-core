package quotasourcing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_majorfactorSourcer_AttributeMBToSources_matrix(t1 *testing.T) {
	t1.Parallel()
	type args struct {
		domainTargets []DomainMB
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
				domainTargets: []DomainMB{
					{
						Target:         70_198,
						MBSource:       27_700,
						MBSourceRemote: 7_700,
					},
					{
						Target:         59_000,
						MBSource:       14_121,
						MBSourceRemote: 5_180,
					},
				},
			},
			want: []int{67_248, 58_996},
		},
		{
			name: "both to throttle, major local",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         30_198,
						MBSource:       47_700,
						MBSourceRemote: 18_490,
					},
					{
						Target:         29_000,
						MBSource:       35_121,
						MBSourceRemote: 5_180,
					},
				},
			},
			want: []int{37_367, 17_027},
		},
		{
			name: "one to throttle, the other to ease, major local",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         30_198,
						MBSource:       47_700,
						MBSourceRemote: 18_490,
					},
					{
						Target:         60_000,
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
				domainTargets: []DomainMB{
					{
						Target:         30_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						Target:         20_000,
						MBSource:       44_121,
						MBSourceRemote: 40_180,
					},
				},
			},
			want: []int{20_429, 28_577},
		},
		{
			name: "both to ease, major remote",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         70_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						Target:         50_000,
						MBSource:       44_121,
						MBSourceRemote: 40_180,
					},
				},
			},
			want: []int{55_871, 62_023},
		},
		{
			name: "one to throttle, the other to ease, major remote",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         30_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						Target:         55_000,
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
				domainTargets: []DomainMB{
					{
						Target:         70_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						Target:         60_000,
						MBSource:       34_121,
						MBSourceRemote: 10_180,
					},
				},
			},
			want: []int{38_597, 41_738},
		},
		{
			name: "both to throttle, one major local, the other major remote",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         9_198,
						MBSource:       37_700,
						MBSourceRemote: 30_000,
					},
					{
						Target:         30_000,
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
				domainTargets: []DomainMB{
					{
						Target:         20_198,
						MBSource:       37_700,
						MBSourceRemote: 9_000,
					},
					{
						Target:         50_000,
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
				domainTargets: []DomainMB{
					{
						Target:         80_198,
						MBSource:       37_700,
						MBSourceRemote: 9_000,
					},
					{
						Target:         20_000,
						MBSource:       44_121,
						MBSourceRemote: 28_180,
					},
				},
			},
			want: []int{68_330, 10_206},
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := majorfactorSourcer{}
			assert.Equalf(t1, tt.want, t.AttributeMBToSources(tt.args.domainTargets), "AttributeMBToSources(%v)", tt.args.domainTargets)
		})
	}
}
