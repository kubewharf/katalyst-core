package domaintarget

import (
	"testing"
)

func Test_halfEasePlanner_GetQuota(t *testing.T) {
	t.Parallel()
	type fields struct {
		innerPlanner fullEasePlanner
	}
	type args struct {
		capacity     int
		currentUsage int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name:   "little socket traffic, small shared-30",
			fields: fields{},
			args: args{
				capacity:     122_000 - 35,
				currentUsage: 18490 + (14121 - 5180),
			},
			want: 70198,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := halfEasePlanner{
				innerPlanner: tt.fields.innerPlanner,
			}
			if got := s.GetQuota(tt.args.capacity, tt.args.currentUsage); got != tt.want {
				t.Errorf("GetQuota() = %v, want %v", got, tt.want)
			}
		})
	}
}
