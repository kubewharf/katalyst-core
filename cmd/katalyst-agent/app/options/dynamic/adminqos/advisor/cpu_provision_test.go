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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	advisorconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/util/syntax"
)

func TestCPUProvisionOptionsApplyFillDefaultSharePool(t *testing.T) {
	t.Parallel()

	options := NewCPUProvisionOptions()
	options.FillDefaultSharePoolWithNonReclaimCPUs = true

	conf := advisorconfig.NewCPUProvisionConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.True(t, conf.FillDefaultSharePoolWithNonReclaimCPUs)
}

func TestCPUProvisionOptionsPreserveSharedReclaimOverlapWhenFillDefaultShareDisabled(t *testing.T) {
	t.Parallel()

	options := NewCPUProvisionOptions()
	options.AllowSharedCoresOverlapReclaimedCores = true
	options.FillDefaultSharePoolWithNonReclaimCPUs = false

	conf := advisorconfig.NewCPUProvisionConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.False(t, conf.FillDefaultSharePoolWithNonReclaimCPUs)
	require.True(t, conf.AllowSharedCoresOverlapReclaimedCores)
}

func TestCPUProvisionOptionsFillDefaultShareDisallowsSharedReclaimOverlap(t *testing.T) {
	t.Parallel()

	options := NewCPUProvisionOptions()
	options.AllowSharedCoresOverlapReclaimedCores = true
	options.FillDefaultSharePoolWithNonReclaimCPUs = true

	conf := advisorconfig.NewCPUProvisionConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.True(t, conf.FillDefaultSharePoolWithNonReclaimCPUs)
	require.False(t, conf.AllowSharedCoresOverlapReclaimedCores)
	require.True(t, conf.DisableDedicatedCoresOverlapReclaimedCores)
}

func TestCPUProvisionOptionsDynamicConfigurationUsesRequestedOverlapPolicy(t *testing.T) {
	t.Parallel()

	boolPtr := func(value bool) *bool {
		return &value
	}
	tests := []struct {
		name            string
		dynamicAllow    *bool
		dynamicDisable  *bool
		dynamicFill     bool
		expectedAllow   bool
		expectedDisable bool
	}{
		{
			name:            "fill disabled with both overlap fields omitted",
			expectedAllow:   true,
			expectedDisable: false,
		},
		{
			name:            "fill disabled with only allow explicit",
			dynamicAllow:    boolPtr(false),
			expectedAllow:   false,
			expectedDisable: false,
		},
		{
			name:            "fill disabled with only disable explicit",
			dynamicDisable:  boolPtr(true),
			expectedAllow:   true,
			expectedDisable: true,
		},
		{
			name:            "fill disabled with both overlap fields explicit",
			dynamicAllow:    boolPtr(false),
			dynamicDisable:  boolPtr(true),
			expectedAllow:   false,
			expectedDisable: true,
		},
		{
			name:            "fill enabled forces overlap policy",
			dynamicAllow:    boolPtr(true),
			dynamicDisable:  boolPtr(false),
			dynamicFill:     true,
			expectedAllow:   false,
			expectedDisable: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := NewCPUProvisionOptions()
			options.AllowSharedCoresOverlapReclaimedCores = true
			options.DisableDedicatedCoresOverlapReclaimedCores = false
			options.FillDefaultSharePoolWithNonReclaimCPUs = true

			conf := dynamic.NewConfiguration()
			require.NoError(t, options.ApplyTo(conf.CPUProvisionConfiguration))
			require.False(t, conf.AllowSharedCoresOverlapReclaimedCores)
			require.True(t, conf.DisableDedicatedCoresOverlapReclaimedCores)

			conf = syntax.DeepCopy(conf).(*dynamic.Configuration)
			conf.ApplyConfiguration(&crd.DynamicConfigCRD{
				AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
					Spec: configapi.AdminQoSConfigurationSpec{
						Config: configapi.AdminQoSConfig{
							AdvisorConfig: &configapi.AdvisorConfig{
								CPUAdvisorConfig: &configapi.CPUAdvisorConfig{
									AllowSharedCoresOverlapReclaimedCores:      tt.dynamicAllow,
									DisableDedicatedCoresOverlapReclaimedCores: tt.dynamicDisable,
									CPUProvisionConfig: &configapi.CPUProvisionConfig{
										FillDefaultSharePoolWithNonReclaimCPUs: boolPtr(tt.dynamicFill),
									},
								},
							},
						},
					},
				},
			})

			require.Equal(t, tt.dynamicFill, conf.FillDefaultSharePoolWithNonReclaimCPUs)
			require.Equal(t, tt.expectedAllow, conf.AllowSharedCoresOverlapReclaimedCores)
			require.Equal(t, tt.expectedDisable, conf.DisableDedicatedCoresOverlapReclaimedCores)
		})
	}
}
