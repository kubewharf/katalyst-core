package quotasourcing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_trendSourcer_AttributeMBToSources(t1 *testing.T) {
	t1.Parallel()
	type args struct {
		domainTargets []DomainMB
	}
	tests := []struct {
		name string
		args args
		want []int
	}{
		{
			name: "little socket traffic",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         70_198,
						MBSource:       27_700,
						MBSourceRemote: 27_700 - 18_490,
					},
					{
						Target:         59_000,
						MBSource:       14_121,
						MBSourceRemote: 14_121 - 5_180,
					},
				},
			},
			want: []int{85078, 44119}, // {-337_997, 467_195} no sense!
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := trendSourcer{}
			assert.Equalf(t1, tt.want, t.AttributeMBToSources(tt.args.domainTargets), "AttributeMBToSources(%v)", tt.args.domainTargets)
		})
	}
}
