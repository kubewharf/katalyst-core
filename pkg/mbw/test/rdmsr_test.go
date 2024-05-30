package test

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/msr"
)

func TestMSRDev_Read(t *testing.T) {
	t.Parallel()

	// set up test stub
	setupTestSyscaller()

	type args struct {
		msr int64
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				msr: 22,
			},
			want:    0x3B00000000000000,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := msr.MSRDev{}
			got, err := d.Read(tt.args.msr)
			if (err != nil) != tt.wantErr {
				t.Errorf("Read() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Read() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadMSR(t *testing.T) {
	t.Parallel()

	// set up test stub
	setupTestSyscaller()

	type args struct {
		cpu uint32
		msr int64
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				cpu: 1,
				msr: 5,
			},
			want:    0x3B00000000000000,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := msr.ReadMSR(tt.args.cpu, tt.args.msr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadMSR() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadMSR() got = %v, want %v", got, tt.want)
			}
		})
	}
}
