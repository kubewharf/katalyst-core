package mbsourcing

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_hasValidMeetingPoint(t *testing.T) {
	t.Parallel()
	type args struct {
		a0 float64
		b0 float64
		c0 float64
		a1 float64
		b1 float64
		c1 float64
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "usually yes",
			args: args{
				a0: 0.7,
				b0: 0.3,
				c0: 10,
				a1: 0.3,
				b1: 0.7,
				c1: 12,
			},
			want: true,
		},
		{
			name: "parallels no",
			args: args{
				a0: 0.7,
				b0: 0.7,
				c0: 10,
				a1: 0.3,
				b1: 0.3,
				c1: 12,
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, hasValidMeetingPoint(tt.args.a0, tt.args.b0, tt.args.c0, tt.args.a1, tt.args.b1, tt.args.c1), "hasValidMeetingPoint(%v, %v, %v, %v, %v, %v)", tt.args.a0, tt.args.b0, tt.args.c0, tt.args.a1, tt.args.b1, tt.args.c1)
		})
	}
}

func Test_getMeetingPoint(t *testing.T) {
	t.Parallel()
	type args struct {
		r0 float64
		r1 float64
		t0 float64
		t1 float64
	}
	tests := []struct {
		name    string
		args    args
		wantX   float64
		wantY   float64
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "crossing line2",
			args: args{
				r0: 0.6,
				r1: 0.7,
				t0: 80,
				t1: 100,
			},
			wantX:   86.66666,
			wantY:   93.33333,
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotX, gotY, err := getMeetingPoint(tt.args.r0, tt.args.r1, tt.args.t0, tt.args.t1)
			if !tt.wantErr(t, err, fmt.Sprintf("getMeetingPoint(%v, %v, %v, %v)", tt.args.r0, tt.args.r1, tt.args.t0, tt.args.t1)) {
				return
			}
			assert.Equalf(t, int(tt.wantX), int(gotX), "getMeetingPoint(%v, %v, %v, %v)", tt.args.r0, tt.args.r1, tt.args.t0, tt.args.t1)
			assert.Equalf(t, int(tt.wantY), int(gotY), "getMeetingPoint(%v, %v, %v, %v)", tt.args.r0, tt.args.r1, tt.args.t0, tt.args.t1)
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
