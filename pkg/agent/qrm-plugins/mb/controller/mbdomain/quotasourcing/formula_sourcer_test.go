package quotasourcing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_attributeBasedOnSolution(t *testing.T) {
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
			name: "happy path",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         10_000,
						MBSource:       13_000,
						MBSourceRemote: 3_000,
					},
					{
						Target:         8_000,
						MBSource:       12_000,
						MBSourceRemote: 4_000,
					},
				},
			},
			want: []int{9176, 8823},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, attributeBasedOnSolution(tt.args.domainTargets), "attributeBasedOnSolution(%v)", tt.args.domainTargets)
		})
	}
}
