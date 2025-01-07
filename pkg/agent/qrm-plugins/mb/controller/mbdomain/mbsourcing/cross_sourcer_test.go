package mbsourcing

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCrossSourcer_AttributeMBToSources(t *testing.T) {
	t.Parallel()
	type args struct {
		domainTargets []DomainMBTargetSource
	}
	tests := []struct {
		name string
		args args
		want []int
	}{
		{
			name: "none constraint",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: -1,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
					{
						TargetIncoming: -1,
						MBSource:       5_000,
						MBSourceRemote: 2_000,
					},
				},
			},
			want: []int{-1, -1},
		},
		{
			name: "one side of constraint",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: -1,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
					{
						TargetIncoming: 7_000,
						MBSource:       14_000,
						MBSourceRemote: 6_000,
					},
				},
			},
			want: []int{8765, 7648},
		},
		{
			name: "flipping side of constraint",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 7_000,
						MBSource:       14_000,
						MBSourceRemote: 6_000,
					},
					{
						TargetIncoming: -1,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
				},
			},
			want: []int{7648, 8765},
		},
		{
			name: "both domains have constraints",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 7_000,
						MBSource:       14_000,
						MBSourceRemote: 6_000,
					},
					{
						TargetIncoming: 12_000,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
				},
			},
			want: []int{7648, 8765},
		},
		{
			name: "cross point is the best",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 12_000,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
					{
						TargetIncoming: 12_000,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
				},
			},
			want: []int{12_000, 12_000},
		},
		{
			name: "little socket traffic",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 70_198,
						MBSource:       27_700,
						MBSourceRemote: 27_700 - 18_490,
					},
					{
						TargetIncoming: 59_000,
						MBSource:       14_121,
						MBSourceRemote: 14_121 - 5_180,
					},
				},
			},
			want: []int{54993, 52891},
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
			want: []int{34372, 18388},
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
			want: []int{40_540, 36_426},
		},
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
			want: []int{21_964, 28_233},
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
			want: []int{39_667, 24_263},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := CrossSourcer{}
			if got := c.AttributeIncomingMBToSources(tt.args.domainTargets); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AttributeIncomingMBToSources() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getLeftEndpoint(t *testing.T) {
	t.Parallel()
	type args struct {
		hostDomain   DomainMBTargetSource
		remoteDomain DomainMBTargetSource
	}
	tests := []struct {
		name            string
		args            args
		wantHostQuota   int
		wantRemoteQuota int
		wantErr         assert.ErrorAssertionFunc
	}{
		{
			name: "trivial diagonal line",
			args: args{
				hostDomain: DomainMBTargetSource{
					TargetIncoming: 8_000,
					MBSource:       6_000,
					MBSourceRemote: 2_000,
				},
				remoteDomain: DomainMBTargetSource{
					TargetIncoming: 111_111_111,
					MBSource:       5_000,
					MBSourceRemote: 2_000,
				},
			},
			wantHostQuota:   4_000,
			wantRemoteQuota: 13_333,
			wantErr:         assert.NoError,
		},
		{
			name: "no local",
			args: args{
				hostDomain: DomainMBTargetSource{
					TargetIncoming: 8_000,
					MBSource:       6_000,
					MBSourceRemote: 6_000,
				},
				remoteDomain: DomainMBTargetSource{
					TargetIncoming: 111_111_111,
					MBSource:       5_000,
					MBSourceRemote: 2_000,
				},
			},
			wantHostQuota:   4_000,
			wantRemoteQuota: 20_000,
			wantErr:         assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotHostQuota, gotRemoteQuota, err := getLeftEndpoint(tt.args.hostDomain, tt.args.remoteDomain)
			if !tt.wantErr(t, err, fmt.Sprintf("getLeftEndpoint(%v, %v)", tt.args.hostDomain, tt.args.remoteDomain)) {
				return
			}
			assert.Equalf(t, tt.wantHostQuota, gotHostQuota, "getLeftEndpoint(%v, %v)", tt.args.hostDomain, tt.args.remoteDomain)
			assert.Equalf(t, tt.wantRemoteQuota, gotRemoteQuota, "getLeftEndpoint(%v, %v)", tt.args.hostDomain, tt.args.remoteDomain)
		})
	}
}

func Test_crossSourcer_AttributeMBToSources_matrix(t1 *testing.T) {
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
			want: []int{64_202, 64_995},
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
			want: []int{34_372, 18_388},
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
			want: []int{40_540, 36_426},
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
			want: []int{21_964, 28_233},
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
			want: []int{55_580, 64_617},
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
			want: []int{39_667, 24_263}, // other sourcers getting {64_xxx, 18_xxx} which seems inferior :)
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
			want: []int{40_208, 39_911}, // other sourcers getting {39_xxx, 41_xxx}
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
			want: []int{18_860, 19_487},
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
			want: []int{12_073, 17_233}, // other sourcers getting {0, 31_623}, - neither is ideal in terms of trend
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
			want: []int{34_363, 32_650}, // others geeting {68_330, 10_206}, which seems inferior
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
			want: []int{30_000, 44_121}, // the ideal {3_000, 80_198}; this one got {30_000, 44_121} chooses the "conservative" candidate
		},
		{
			name: "both major total remote, continuing with the previous case - unable to dissolve itself", // this sourcer has flaw in conner case of total remotes!
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 80_198,
						MBSource:       30_000,
						MBSourceRemote: 30_300,
					},
					{
						TargetIncoming: 30_000,
						MBSource:       44_121,
						MBSourceRemote: 44_121,
					},
				},
			},
			want: []int{30_000, 44_121}, // the ideal {3_000, 80_198}; this one got {30_000, 44_121} no change in inferior trap
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
			want: []int{35_000, 20_198}, // ideal {35_000, 20_198},
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
			want: []int{20_000, 24_121}, //this one got {20000, 24121}, not the best - the most "conservative" candidate
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := CrossSourcer{}
			assert.Equalf(t1, tt.want, t.AttributeIncomingMBToSources(tt.args.domainTargets), "AttributeIncomingMBToSources(%v)", tt.args.domainTargets)
		})
	}
}
