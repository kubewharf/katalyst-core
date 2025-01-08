package domaintarget

import "testing"

func TestIsResourceUnderPressure(t *testing.T) {
	t.Parallel()
	type args struct {
		capacity int
		used     int
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "not under pressure",
			args: args{
				capacity: 122_000,
				used:     55370 + 20_323 + 36_175,
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsResourceUnderPressure(tt.args.capacity, tt.args.used); got != tt.want {
				t.Errorf("IsResourceUnderPressure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsResourceAtEase(t *testing.T) {
	t.Parallel()
	type args struct {
		capacity int
		used     int
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "at ease",
			args: args{
				capacity: 122_000,
				used:     20_323 + 36_175,
			},
			want: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsResourceAtEase(tt.args.capacity, tt.args.used); got != tt.want {
				t.Errorf("IsResourceAtEase() = %v, want %v", got, tt.want)
			}
		})
	}
}
