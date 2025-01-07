package mbsourcing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_attributeBasedOnSolution(t *testing.T) {
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
			name: "happy path",
			args: args{
				domainTargets: []DomainMBTargetSource{
					{
						TargetIncoming: 10_000,
						MBSource:       13_000,
						MBSourceRemote: 3_000,
					},
					{
						TargetIncoming: 8_000,
						MBSource:       12_000,
						MBSourceRemote: 4_000,
					},
				},
			},
			want: []int{9176, 8823},
		},
		//{
		//	name: "little socket traffic",
		//	args: args{
		//		domainTargets: []DomainMBTargetSource{
		//			{
		//				TargetIncoming:         70_198,
		//				MBSource:       27_700,
		//				MBSourceRemote: 27_700 - 18_490,
		//			},
		//			{
		//				TargetIncoming:         59_000,
		//				MBSource:       14_121,
		//				MBSourceRemote: 14_121 - 5_180,
		//			},
		//		},
		//	},
		//	want: []int{0, 0}, // {-337_997, 467_195} no sense!
		//},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, attributeBasedOnSolution(tt.args.domainTargets), "attributeBasedOnSolution(%v)", tt.args.domainTargets)
		})
	}
}
