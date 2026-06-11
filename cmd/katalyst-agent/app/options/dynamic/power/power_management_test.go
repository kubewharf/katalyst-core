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

package power

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/power"
)

func TestPowerOptions_ApplyTo(t *testing.T) {
	t.Parallel()
	type fields struct {
		DisablePowerAdvisor bool
		DisablePowerCapping bool
		PowerReductionRatio int
	}
	type args struct {
		c *power.PowerManagementConfiguration
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				DisablePowerAdvisor: false,
				DisablePowerCapping: false,
				PowerReductionRatio: 11,
			},
			args: args{
				c: &power.PowerManagementConfiguration{
					DisablePowerAdvisor: true,
					PowerReductionRatio: 10,
					DisablePowerCapping: true,
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &PowerOptions{
				DisablePowerAdvisor: tt.fields.DisablePowerAdvisor,
				DisablePowerCapping: tt.fields.DisablePowerCapping,
				PowerReductionRatio: tt.fields.PowerReductionRatio,
			}
			if err := o.ApplyTo(tt.args.c); (err != nil) != tt.wantErr {
				t.Errorf("ApplyTo() error = %v, wantErr %v", err, tt.wantErr)
			}
			assert.Equal(t, tt.fields.DisablePowerCapping, tt.args.c.DisablePowerCapping)
			assert.Equal(t, tt.fields.DisablePowerAdvisor, tt.args.c.DisablePowerAdvisor)
			assert.Equal(t, tt.fields.PowerReductionRatio, tt.args.c.PowerReductionRatio)
		})
	}
}
