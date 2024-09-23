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

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

func Test_getMins(t *testing.T) {
	t.Parallel()
	type args struct {
		qos []task.QoSLevel
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "happy path",
			args: args{
				qos: []task.QoSLevel{task.QoSLevelSharedCores, task.QoSLevelSystemCores, task.QoSLevelReclaimedCores},
			},
			want: 5000,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, GetMins(tt.args.qos...), "getMins(%v)", tt.args.qos)
		})
	}
}
