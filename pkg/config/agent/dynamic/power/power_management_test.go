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

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func boolPtr(b bool) *bool {
	return &b
}

func int32Ptr(i int32) *int32 {
	return &i
}

func TestPowerManagementConfiguration_ApplyConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		conf *crd.DynamicConfigCRD
		want *PowerManagementConfiguration
	}{
		{
			name: "nil conf",
			conf: nil,
			want: &PowerManagementConfiguration{
				DisablePowerAdvisor: DefaultDisablePowerAdvisor,
				DisablePowerCapping: DefaultDisablePowerCapping,
				PowerReductionRatio: DefaultPowerReductionRatio,
			},
		},
		{
			name: "nil PowerManagementConfiguration in conf",
			conf: &crd.DynamicConfigCRD{},
			want: &PowerManagementConfiguration{
				DisablePowerAdvisor: DefaultDisablePowerAdvisor,
				DisablePowerCapping: DefaultDisablePowerCapping,
				PowerReductionRatio: DefaultPowerReductionRatio,
			},
		},
		{
			name: "all fields set",
			conf: &crd.DynamicConfigCRD{
				PowerManagementConfiguration: &v1alpha1.PowerManagementConfiguration{
					Spec: v1alpha1.PowerManagementConfigurationSpec{
						Config: v1alpha1.PowerManagementConfig{
							DisablePowerAdvisor: boolPtr(false),
							DisablePowerCapping: boolPtr(false),
							PowerReductionRatio: int32Ptr(50),
						},
					},
				},
			},
			want: &PowerManagementConfiguration{
				DisablePowerAdvisor: false,
				DisablePowerCapping: false,
				PowerReductionRatio: 50,
			},
		},
		{
			name: "partial fields set",
			conf: &crd.DynamicConfigCRD{
				PowerManagementConfiguration: &v1alpha1.PowerManagementConfiguration{
					Spec: v1alpha1.PowerManagementConfigurationSpec{
						Config: v1alpha1.PowerManagementConfig{
							DisablePowerAdvisor: boolPtr(false),
						},
					},
				},
			},
			want: &PowerManagementConfiguration{
				DisablePowerAdvisor: false,
				DisablePowerCapping: DefaultDisablePowerCapping,
				PowerReductionRatio: DefaultPowerReductionRatio,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := NewPowerManagementConfiguration()
			c.ApplyConfiguration(tt.conf)

			if c.DisablePowerAdvisor != tt.want.DisablePowerAdvisor {
				t.Errorf("DisablePowerAdvisor = %v, want %v", c.DisablePowerAdvisor, tt.want.DisablePowerAdvisor)
			}
			if c.DisablePowerCapping != tt.want.DisablePowerCapping {
				t.Errorf("DisablePowerCapping = %v, want %v", c.DisablePowerCapping, tt.want.DisablePowerCapping)
			}
			if c.PowerReductionRatio != tt.want.PowerReductionRatio {
				t.Errorf("PowerReductionRatio = %v, want %v", c.PowerReductionRatio, tt.want.PowerReductionRatio)
			}
		})
	}
}
