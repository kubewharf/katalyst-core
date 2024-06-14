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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

// not to verify much other than expecting no error
// most of this test purpose is more test coverage
func Test_newSysInfo(t *testing.T) {
	t.Parallel()
	type args struct {
		machineInfoConfig *global.MachineInfoConfiguration
	}
	tests := []struct {
		name    string
		args    args
		want    *SysInfo
		wantErr bool
	}{
		{
			name: "happy path en error",
			args: args{
				machineInfoConfig: &global.MachineInfoConfiguration{},
			},
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := newSysInfo(tt.args.machineInfoConfig)
			if (err != nil) != tt.wantErr {
				t.Errorf("newSysInfo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}
