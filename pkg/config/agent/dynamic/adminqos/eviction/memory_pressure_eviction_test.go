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
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func boolPtr(b bool) *bool          { return &b }
func float64Ptr(f float64) *float64 { return &f }

func newMemoryPressureSafeZoneGateDynamicConfig(
	enable *bool, ratio *float64,
) *crd.DynamicConfigCRD {
	return &crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
			Spec: configapi.AdminQoSConfigurationSpec{
				Config: configapi.AdminQoSConfig{
					EvictionConfig: &configapi.EvictionConfig{
						MemoryPressureEvictionConfig: &configapi.MemoryPressureEvictionConfig{
							EnableReclaimedMemorySafeZoneGate:  enable,
							ReclaimedMemoryUsageRatioThreshold: ratio,
						},
					},
				},
			},
		},
	}
}

func TestMemoryPressureEvictionConfigurationSafeZoneGateDefaults(t *testing.T) {
	t.Parallel()

	conf := NewMemoryPressureEvictionPluginConfiguration()
	// The safe-zone gate ships disabled (false) and fully closed (ratio 0.0): an
	// operator must both opt in with enable=true and set an explicit positive
	// threshold within [0,1].
	require.False(t, conf.EnableReclaimedMemorySafeZoneGate)
	require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
}

func TestMemoryPressureEvictionConfigurationSafeZoneGateApplyConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("explicit enable=true and positive ratio are mapped", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()

		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(0.42),
		))

		require.True(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.42, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("explicit enable=false is mapped to false", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		// Pre-arm to true first, then apply explicit false to prove false is not
		// treated as an omitted/nil field.
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(0.42),
		))
		require.True(t, conf.EnableReclaimedMemorySafeZoneGate)

		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(false), float64Ptr(0.42),
		))
		require.False(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.42, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("explicit zero ratio is mapped to zero (gate closed)", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		// Pre-arm the gate with a positive value first, then apply an explicit
		// zero to prove the operator can deliberately close it again.
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(0.42),
		))
		require.Equal(t, 0.42, conf.ReclaimedMemoryUsageRatioThreshold)

		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(0),
		))
		require.True(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("upper bound 1.0 is accepted", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(1.0),
		))
		require.True(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 1.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("nil pointer fields keep construction defaults", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		// Pre-arm the applied values first to prove a subsequent nil-field CR does
		// not reset them, and to exercise the old-CR shape (config present but the
		// new safe-zone gate fields absent).
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(0.7),
		))
		require.True(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.7, conf.ReclaimedMemoryUsageRatioThreshold)

		// A nil enable field must NOT flip an already-armed true back to false;
		// a nil ratio must NOT reset the ratio.
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(nil, nil))
		require.True(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.7, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("nil enable on a fresh conf defaults to false (auto-shadow arm)", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		// Old-CR / omitted enable resolves to the default false, i.e. the
		// auto-shadow (observe-only) arm once the ratio is positive.
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(nil, float64Ptr(0.3)))
		require.False(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.3, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("missing CR sections keep construction defaults", func(t *testing.T) {
		t.Parallel()

		// No AdminQoSConfiguration at all.
		conf := NewMemoryPressureEvictionPluginConfiguration()
		conf.ApplyConfiguration(&crd.DynamicConfigCRD{})
		require.False(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)

		// EvictionConfig present but the memory-pressure block nil.
		conf.ApplyConfiguration(&crd.DynamicConfigCRD{
			AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
				Spec: configapi.AdminQoSConfigurationSpec{
					Config: configapi.AdminQoSConfig{
						EvictionConfig: &configapi.EvictionConfig{},
					},
				},
			},
		})
		require.False(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("invalid NaN ratio fails closed to zero", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(math.NaN()),
		))
		require.True(t, conf.EnableReclaimedMemorySafeZoneGate)
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("invalid +Inf ratio fails closed to zero", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(math.Inf(1)),
		))
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("invalid -Inf ratio fails closed to zero", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(math.Inf(-1)),
		))
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("ratio above 1 fails closed to zero", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(1.5),
		))
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("negative ratio fails closed to zero", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(-0.2),
		))
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})

	t.Run("invalid ratio replaces an armed positive and closes the gate", func(t *testing.T) {
		t.Parallel()
		conf := NewMemoryPressureEvictionPluginConfiguration()
		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(0.4),
		))
		require.Equal(t, 0.4, conf.ReclaimedMemoryUsageRatioThreshold)

		conf.ApplyConfiguration(newMemoryPressureSafeZoneGateDynamicConfig(
			boolPtr(true), float64Ptr(math.NaN()),
		))
		require.Equal(t, 0.0, conf.ReclaimedMemoryUsageRatioThreshold)
	})
}
