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

func TestMemoryGuardConfigurationApplyConfiguration(t *testing.T) {
	t.Parallel()

	high := configapi.CriticalWatermarkSourceHigh
	conf := NewMemoryGuardConfiguration()
	require.Equal(t, configapi.CriticalWatermarkSourceLow, conf.CriticalWatermarkSource)

	conf.ApplyConfiguration(newMemoryGuardDynamicConfig(&high))
	require.Equal(t, configapi.CriticalWatermarkSourceHigh, conf.CriticalWatermarkSource)

	conf.ApplyConfiguration(newMemoryGuardDynamicConfig(nil))
	require.Equal(t, configapi.CriticalWatermarkSourceHigh, conf.CriticalWatermarkSource)
}

func newMemoryGuardDynamicConfig(source *configapi.CriticalWatermarkSource) *crd.DynamicConfigCRD {
	return &crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
			Spec: configapi.AdminQoSConfigurationSpec{
				Config: configapi.AdminQoSConfig{
					AdvisorConfig: &configapi.AdvisorConfig{
						MemoryAdvisorConfig: &configapi.MemoryAdvisorConfig{
							MemoryGuardConfig: &configapi.MemoryGuardConfig{
								CriticalWatermarkSource: source,
							},
						},
					},
				},
			},
		},
	}
}
