package quotasourcing

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func Test_deltaSourcer_AttributeMBToSources(t *testing.T) {
	t.Parallel()
	type args struct {
		domainTargets []DomainMB
	}
	tests := []struct {
		name string
		args args
		want []int
	}{
		//{
		//	name: "little socket traffic",
		//	args: args{
		//		domainTargets: []DomainMB{
		//			{
		//				Target:         70_198,
		//				MBSource:       27_700,
		//				MBSourceRemote: 27_700 - 18_490,
		//			},
		//			{
		//				Target:         59_000,
		//				MBSource:       14_121,
		//				MBSourceRemote: 14_121 - 5_180,
		//			},
		//		},
		//	},
		//	want: []int{0, 0}, // {-155_148, 240_658} making no sense!
		//},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := deltaSourcer{}
			assert.Equalf(t, tt.want, d.AttributeMBToSources(tt.args.domainTargets), "AttributeMBToSources(%v)", tt.args.domainTargets)
		})
	}
}
