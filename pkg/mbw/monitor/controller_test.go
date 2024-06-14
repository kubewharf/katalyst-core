/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package monitor

import (
	"testing"

	v1 "github.com/google/cadvisor/info/v1"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func Test_checkMSR(t *testing.T) {
	t.Parallel()
	type args struct {
		core   uint32
		msr    int64
		target uint64
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "happy path no error",
			args: args{
				core:   2,
				msr:    168,
				target: 210,
			},
			wantErr: false,
		},
		{
			name: "inequality leads to error",
			args: args{
				core:   2,
				msr:    168,
				target: 211,
			},
			wantErr: true,
		},
		{
			name: "negative path of msr method error",
			args: args{
				core:   99999,
				msr:    16,
				target: 210,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := checkMSR(tt.args.core, tt.args.msr, tt.args.target); (err != nil) != tt.wantErr {
				t.Errorf("checkMSR() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_writeMBAMSR(t *testing.T) {
	t.Parallel()
	type args struct {
		core uint32
		msr  int64
		val  uint64
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "happy path no error",
			args: args{
				core: 2,
				msr:  168,
				val:  210,
			},
			wantErr: false,
		},
		{
			name: "negative path of write error",
			args: args{
				core: 77777,
				msr:  168,
				val:  210,
			},
			wantErr: true,
		},
		{
			name: "negative path of check error",
			args: args{
				core: 99999,
				msr:  168,
				val:  210,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := writeMBAMSR(tt.args.core, tt.args.msr, tt.args.val); (err != nil) != tt.wantErr {
				t.Errorf("writeMBAMSR() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_bindCorePQRAssoc(t *testing.T) {
	t.Parallel()
	if err := bindCorePQRAssoc(0, 210, 0); err != nil {
		t.Errorf("unexpected error %v", err)
	}
}

func Test_ConfigCCDMBACos(t *testing.T) {
	t.Parallel()
	m := MBMonitor{
		SysInfo: &SysInfo{
			CCDMap: map[int][]int{
				0: {0, 1},
			},
		},
	}

	// no verification here as this test is mere for coverage data purpose
	_ = m.ConfigCCDMBACos(0, 210, 0, 8)
}

func Test_ResetMBACos(t *testing.T) {
	t.Parallel()
	m := MBMonitor{
		SysInfo: &SysInfo{
			NumCCDs: 3,
			KatalystMachineInfo: machine.KatalystMachineInfo{
				MachineInfo: &v1.MachineInfo{
					NumCores: 1,
				},
			},
			CCDMap: map[int][]int{
				0: {0, 1},
			},
		},
	}

	// no verification here as this test is mere for coverage data purpose
	_ = m.ResetMBACos()
}
