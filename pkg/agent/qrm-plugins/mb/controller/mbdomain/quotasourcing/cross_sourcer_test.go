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
			want: []int{7119, 8512},
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
			want: []int{8512, 7119},
		},
		{
			name: "both domains have constraint",
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
			want: []int{8512, 7119},
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

func Test_orthogonalPoint(t *testing.T) {
	t.Parallel()
	type args struct {
		originX float64
		originY float64
		a       float64
		b       float64
	}
	tests := []struct {
		name  string
		args  args
		wantX int
		wantY int
	}{
		{
			name: "horizontal line",
			args: args{
				originX: 10,
				originY: 10,
				a:       0,
				b:       4,
			},
			wantX: 10,
			wantY: 4,
		},
		{
			name: "diagonal line",
			args: args{
				originX: 10,
				originY: 10,
				a:       -1,
				b:       4,
			},
			wantX: 2,
			wantY: 2,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotX, gotY := orthogonalPoint(tt.args.originX, tt.args.originY, tt.args.a, tt.args.b)
			if gotX != tt.wantX {
				t.Errorf("orthogonalPoint() gotX = %v, want %v", gotX, tt.wantX)
			}
			if gotY != tt.wantY {
				t.Errorf("orthogonalPoint() gotY = %v, want %v", gotY, tt.wantY)
			}
		})
	}
}

func Test_locateMeetingPoint(t *testing.T) {
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
			name: "yes meet",
			args: args{
				domainTargets: []DomainMB{
					{
						Target:         10_000,
						MBSource:       10_000,
						MBSourceRemote: 5_000,
					},
					{
						Target:         12_000,
						MBSource:       12_000,
						MBSourceRemote: 4_000,
					},
				},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := locateMeetingPoint(tt.args.domainTargets); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("locateMeetingPoint() = %v, want %v", got, tt.want)
			}
		})
	}
}
