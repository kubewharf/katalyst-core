package test

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/rdt"
)

func TestGetRDTValue(t *testing.T) {
	t.Parallel()

	// set up test stub
	setupTestSyscaller()

	type args struct {
		core  uint32
		event rdt.PQOS_EVENT_TYPE
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{
		{
			name: "negative path returns error",
			args: args{
				core:  2,
				event: 11,
			},
			want:    0x3B00000000000000,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := rdt.GetRDTValue(tt.args.core, tt.args.event)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetRDTValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GetRDTValue() got = %v, want %v", got, tt.want)
			}
		})
	}
}
