/*
Copyright 2026 The Katalyst Authors.

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

package advisor

import (
	"testing"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestCPUProvisionConfigurationApplyDisableDedicatedCoresOverlapReclaimedCores(t *testing.T) {
	t.Parallel()

	trueValue := true
	tests := []struct {
		name   string
		config *configv1alpha1.CPUAdvisorConfig
		want   bool
	}{
		{
			name:   "missing field defaults to false",
			config: &configv1alpha1.CPUAdvisorConfig{},
			want:   false,
		},
		{
			name: "explicit true takes effect",
			config: &configv1alpha1.CPUAdvisorConfig{
				DisableDedicatedCoresOverlapReclaimedCores: &trueValue,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := NewCPUProvisionConfiguration()
			c.ApplyConfiguration(&crd.DynamicConfigCRD{
				AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
					Spec: configv1alpha1.AdminQoSConfigurationSpec{
						Config: configv1alpha1.AdminQoSConfig{
							AdvisorConfig: &configv1alpha1.AdvisorConfig{
								CPUAdvisorConfig: tt.config,
							},
						},
					},
				},
			})

			if c.DisableDedicatedCoresOverlapReclaimedCores != tt.want {
				t.Fatalf("DisableDedicatedCoresOverlapReclaimedCores = %t, want %t",
					c.DisableDedicatedCoresOverlapReclaimedCores, tt.want)
			}
		})
	}
}
