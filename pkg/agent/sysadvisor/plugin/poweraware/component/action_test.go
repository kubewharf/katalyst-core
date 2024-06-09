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

package component

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

func TestPowerAction_String(t *testing.T) {
	t.Parallel()
	type fields struct {
		op  types.InternalOp
		arg int
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "happy path normal result",
			fields: fields{
				op:  types.InternalOpFreqCap,
				arg: 255,
			},
			want: "op: FreqCap, arg: 255",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pa := PowerAction{
				op:  tt.fields.op,
				arg: tt.fields.arg,
			}
			if got := pa.String(); got != tt.want {
				t.Errorf("expected %s, got %s", tt.want, got)
			}
		})
	}
}
