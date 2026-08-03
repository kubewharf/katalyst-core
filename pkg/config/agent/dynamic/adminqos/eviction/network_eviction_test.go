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

package eviction

import (
	"testing"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestNetworkEvictionConfigurationApplyBandwidthConfiguration(t *testing.T) {
	t.Parallel()

	enableBandwidthEviction := true
	utilizationThreshold := 0.75
	continuousMetThreshold := int64(3)
	ringSize := int64(6)
	ringMetThreshold := int64(4)
	gracePeriod := int64(12)

	conf := NewNetworkEvictionConfiguration()
	conf.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
			Spec: configapi.AdminQoSConfigurationSpec{
				Config: configapi.AdminQoSConfig{
					EvictionConfig: &configapi.EvictionConfig{
						NetworkEvictionConfig: &configapi.NetworkEvictionConfig{
							EnableNICBandwidthEviction:         &enableBandwidthEviction,
							NICBandwidthUtilizationThreshold:   &utilizationThreshold,
							NICBandwidthContinuousMetThreshold: &continuousMetThreshold,
							NICBandwidthRingSize:               &ringSize,
							NICBandwidthRingMetThreshold:       &ringMetThreshold,
							NICBandwidthGracePeriod:            &gracePeriod,
						},
					},
				},
			},
		},
	})

	require.True(t, conf.EnableNICBandwidthEviction)
	require.Equal(t, 0.75, conf.NICBandwidthUtilizationThreshold)
	require.Equal(t, 3, conf.NICBandwidthContinuousMetThreshold)
	require.Equal(t, 6, conf.NICBandwidthRingSize)
	require.Equal(t, 4, conf.NICBandwidthRingMetThreshold)
	require.EqualValues(t, 12, conf.NICBandwidthGracePeriod)
}
