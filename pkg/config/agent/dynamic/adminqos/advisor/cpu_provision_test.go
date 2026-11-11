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

package advisor

import (
	"testing"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestCPUProvisionConfigurationFillDefaultSharePool(t *testing.T) {
	t.Parallel()

	t.Run("default disabled", func(t *testing.T) {
		t.Parallel()

		conf := NewCPUProvisionConfiguration()
		require.False(t, conf.FillDefaultSharePoolWithNonReclaimCPUs)
	})

	t.Run("apply dynamic configuration", func(t *testing.T) {
		t.Parallel()

		enabled := true
		conf := NewCPUProvisionConfiguration()
		conf.ApplyConfiguration(&crd.DynamicConfigCRD{
			AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
				Spec: configapi.AdminQoSConfigurationSpec{
					Config: configapi.AdminQoSConfig{
						AdvisorConfig: &configapi.AdvisorConfig{
							CPUAdvisorConfig: &configapi.CPUAdvisorConfig{
								CPUProvisionConfig: &configapi.CPUProvisionConfig{
									FillDefaultSharePoolWithNonReclaimCPUs: &enabled,
								},
							},
						},
					},
				},
			},
		})

		require.True(t, conf.FillDefaultSharePoolWithNonReclaimCPUs)
	})
}

func TestCPUProvisionConfigurationPreservesSharedReclaimOverlapWhenFillDefaultShareDisabled(t *testing.T) {
	t.Parallel()

	enabled := true
	disabled := false
	conf := NewCPUProvisionConfiguration()
	conf.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
			Spec: configapi.AdminQoSConfigurationSpec{
				Config: configapi.AdminQoSConfig{
					AdvisorConfig: &configapi.AdvisorConfig{
						CPUAdvisorConfig: &configapi.CPUAdvisorConfig{
							AllowSharedCoresOverlapReclaimedCores: &enabled,
							CPUProvisionConfig: &configapi.CPUProvisionConfig{
								FillDefaultSharePoolWithNonReclaimCPUs: &disabled,
							},
						},
					},
				},
			},
		},
	})

	require.False(t, conf.FillDefaultSharePoolWithNonReclaimCPUs)
	require.True(t, conf.AllowSharedCoresOverlapReclaimedCores)
}

func TestCPUProvisionConfigurationFillDefaultShareDisallowsSharedReclaimOverlap(t *testing.T) {
	t.Parallel()

	enabled := true
	conf := NewCPUProvisionConfiguration()
	conf.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
			Spec: configapi.AdminQoSConfigurationSpec{
				Config: configapi.AdminQoSConfig{
					AdvisorConfig: &configapi.AdvisorConfig{
						CPUAdvisorConfig: &configapi.CPUAdvisorConfig{
							AllowSharedCoresOverlapReclaimedCores: &enabled,
							CPUProvisionConfig: &configapi.CPUProvisionConfig{
								FillDefaultSharePoolWithNonReclaimCPUs: &enabled,
							},
						},
					},
				},
			},
		},
	})

	require.True(t, conf.FillDefaultSharePoolWithNonReclaimCPUs)
	require.False(t, conf.AllowSharedCoresOverlapReclaimedCores)
	require.True(t, conf.DisableDedicatedCoresOverlapReclaimedCores)
}
