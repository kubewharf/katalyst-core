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

package test

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/rdt"
)

func TestGetRDTValue(t *testing.T) {
	t.Parallel()
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
