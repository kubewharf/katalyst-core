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

package qrm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestNumaMemCompactConfigurationApplyConfiguration(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	enableNumaCompact := true
	enablePeriodicNumaCompact := false
	intervalSeconds := int64(900)
	degradedThreshold := 12.5

	// A freshly constructed configuration applies the static defaults.
	defaultConf := NewNumaMemCompactConfiguration()
	as.True(defaultConf.EnablePeriodicNumaMemCompact)
	as.Equal(defaultNumaMemCompactInterval, defaultConf.NumaMemCompactInterval)
	as.Equal(defaultTHPUnusableIndexDegradedThreshold, defaultConf.THPUnusableIndexDegradedThreshold)

	conf := NewNumaMemCompactConfiguration()
	conf.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &apiconfig.AdminQoSConfiguration{
			Spec: apiconfig.AdminQoSConfigurationSpec{
				Config: apiconfig.AdminQoSConfig{
					QRMPluginConfig: &apiconfig.QRMPluginConfig{
						MemoryPluginConfig: &apiconfig.MemoryPluginConfig{
							NumaMemCompactConfig: &apiconfig.NumaMemCompactConfig{
								EnableNumaMemCompact:              &enableNumaCompact,
								EnablePeriodicNumaMemCompact:      &enablePeriodicNumaCompact,
								NumaMemCompactIntervalSeconds:     &intervalSeconds,
								THPUnusableIndexDegradedThreshold: &degradedThreshold,
							},
						},
					},
				},
			},
		},
	})

	as.True(conf.EnableNumaMemCompact)
	as.False(conf.EnablePeriodicNumaMemCompact)
	as.Equal(900*time.Second, conf.NumaMemCompactInterval)
	as.Equal(12.5, conf.THPUnusableIndexDegradedThreshold)
}

func TestNumaMemCompactConfigurationPeriodicDefaultsToEnabled(t *testing.T) {
	t.Parallel()

	enableNumaCompact := true
	conf := NewNumaMemCompactConfiguration()
	conf.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &apiconfig.AdminQoSConfiguration{
			Spec: apiconfig.AdminQoSConfigurationSpec{
				Config: apiconfig.AdminQoSConfig{
					QRMPluginConfig: &apiconfig.QRMPluginConfig{
						MemoryPluginConfig: &apiconfig.MemoryPluginConfig{
							NumaMemCompactConfig: &apiconfig.NumaMemCompactConfig{
								EnableNumaMemCompact: &enableNumaCompact,
							},
						},
					},
				},
			},
		},
	})

	as := require.New(t)
	as.True(conf.EnableNumaMemCompact)
	as.True(conf.EnablePeriodicNumaMemCompact)
	as.Equal(defaultNumaMemCompactInterval, conf.NumaMemCompactInterval)
}

func TestNumaMemCompactConfigurationIntervalClamp(t *testing.T) {
	t.Parallel()

	applyInterval := func(seconds int64) time.Duration {
		conf := NewNumaMemCompactConfiguration()
		conf.ApplyConfiguration(&crd.DynamicConfigCRD{
			AdminQoSConfiguration: &apiconfig.AdminQoSConfiguration{
				Spec: apiconfig.AdminQoSConfigurationSpec{
					Config: apiconfig.AdminQoSConfig{
						QRMPluginConfig: &apiconfig.QRMPluginConfig{
							MemoryPluginConfig: &apiconfig.MemoryPluginConfig{
								NumaMemCompactConfig: &apiconfig.NumaMemCompactConfig{
									NumaMemCompactIntervalSeconds: &seconds,
								},
							},
						},
					},
				},
			},
		})
		return conf.NumaMemCompactInterval
	}

	as := require.New(t)
	// A positive interval below the minimum is clamped up to the minimum.
	as.Equal(minNumaMemCompactInterval, applyInterval(1))
	as.Equal(minNumaMemCompactInterval, applyInterval(599))
	// At or above the minimum is kept as configured.
	as.Equal(minNumaMemCompactInterval, applyInterval(600))
	as.Equal(1200*time.Second, applyInterval(1200))
	// A non-positive interval falls back to the default; the periodic switch controls enablement.
	as.Equal(defaultNumaMemCompactInterval, applyInterval(0))
	as.Equal(defaultNumaMemCompactInterval, applyInterval(-5))
}

func TestClampTHPUnusableIndexDegradedThreshold(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal(0.0, ClampTHPUnusableIndexDegradedThreshold(-1))
	as.Equal(0.0, ClampTHPUnusableIndexDegradedThreshold(0))
	as.Equal(10.0, ClampTHPUnusableIndexDegradedThreshold(10))
	as.Equal(100.0, ClampTHPUnusableIndexDegradedThreshold(100))
	as.Equal(100.0, ClampTHPUnusableIndexDegradedThreshold(101))
}
