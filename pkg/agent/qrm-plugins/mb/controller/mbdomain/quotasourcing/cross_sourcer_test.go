package quotasourcing

import (
	"reflect"
	"testing"
)

func TestCrossSourcer_AttributeMBToSources(t *testing.T) {
	t.Parallel()
	type args struct {
		domainTargets []DomainMB
	}
	tests := []struct {
		name string
		args args
		want []int
	}{
		{
			name: "none constraint",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         -1,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
					{
						Target:         -1,
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
				domainTargets: []DomainMB{
					{
						Target:         -1,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
					{
						Target:         7_000,
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
				domainTargets: []DomainMB{
					{
						Target:         7_000,
						MBSource:       14_000,
						MBSourceRemote: 6_000,
					},
					{
						Target:         -1,
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
				domainTargets: []DomainMB{
					{
						Target:         7_000,
						MBSource:       14_000,
						MBSourceRemote: 6_000,
					},
					{
						Target:         12_000,
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
				domainTargets: []DomainMB{
					{
						Target:         12_000,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
					{
						Target:         12_000,
						MBSource:       10_000,
						MBSourceRemote: 3_000,
					},
				},
			},
			want: []int{12_000, 12_000},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := CrossSourcer{}
			if got := c.AttributeMBToSources(tt.args.domainTargets); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AttributeMBToSources() = %v, want %v", got, tt.want)
			}
		})
	}
}
