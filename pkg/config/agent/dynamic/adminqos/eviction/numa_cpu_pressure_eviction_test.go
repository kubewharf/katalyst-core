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

	"github.com/stretchr/testify/assert"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
)

func TestNumaCPUPressureEvictionConfiguration_ApplyConfiguration_CRDNil(t *testing.T) {
	t.Parallel()

	c := NumaCPUPressureEvictionConfiguration{
		ThresholdExpandFactor:  0.7 / 0.55,
		CpuUsageRatioThreshold: 0.55,
	}

	c.ApplyConfiguration(&crd.DynamicConfigCRD{})

	assert.Equal(t, 0.55, c.CpuUsageRatioThreshold,
		"when CRD is nil, CpuUsageRatioThreshold should remain unchanged (Options default 0.55)")
	assert.Equal(t, 0.7/0.55, c.ThresholdExpandFactor,
		"when CRD is nil, ThresholdExpandFactor should remain unchanged (Options default 0.7/0.55)")
}

func TestNumaCPUPressureEvictionConfiguration_ApplyConfiguration_CRDEmptyField(t *testing.T) {
	t.Parallel()

	c := NumaCPUPressureEvictionConfiguration{
		ThresholdExpandFactor:  0.7 / 0.55,
		CpuUsageRatioThreshold: 0.55,
	}

	conf := &crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					EvictionConfig: &configv1alpha1.EvictionConfig{
						CPUPressureEvictionConfig: &configv1alpha1.CPUPressureEvictionConfig{
							NumaCPUPressureEvictionConfig: configv1alpha1.NumaCPUPressureEvictionConfig{},
						},
					},
				},
			},
		},
	}

	c.ApplyConfiguration(conf)

	assert.Equal(t, 0.55, c.CpuUsageRatioThreshold,
		"when CRD NumaCPUPressureEvictionConfig exists but CpuUsageRatioThreshold field is nil "+
			"(not yet in katalyst-api), CpuUsageRatioThreshold should remain 0.55")
	assert.Equal(t, 0.7/0.55, c.ThresholdExpandFactor,
		"when CRD ThresholdExpandFactor is nil, it should remain 0.7/0.55")
}

func TestNumaCPUPressureEvictionConfiguration_ApplyConfiguration_CRDThresholdExpandFactorOverride(t *testing.T) {
	t.Parallel()

	c := NumaCPUPressureEvictionConfiguration{
		ThresholdExpandFactor:  0.7 / 0.55,
		CpuUsageRatioThreshold: 0.55,
	}

	expandFactorOverride := 1.3
	conf := &crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
			Spec: configv1alpha1.AdminQoSConfigurationSpec{
				Config: configv1alpha1.AdminQoSConfig{
					EvictionConfig: &configv1alpha1.EvictionConfig{
						CPUPressureEvictionConfig: &configv1alpha1.CPUPressureEvictionConfig{
							NumaCPUPressureEvictionConfig: configv1alpha1.NumaCPUPressureEvictionConfig{
								ThresholdExpandFactor: &expandFactorOverride,
							},
						},
					},
				},
			},
		},
	}

	c.ApplyConfiguration(conf)

	assert.Equal(t, 1.3, c.ThresholdExpandFactor,
		"when CRD ThresholdExpandFactor is set, it should override the Options default value")
	assert.Equal(t, 0.55, c.CpuUsageRatioThreshold,
		"when CRD CpuUsageRatioThreshold is nil (not yet in katalyst-api), it should remain 0.55")
}

func TestNumaCPUPressureEvictionConfiguration_EvictionTriggerLine(t *testing.T) {
	t.Parallel()

	c := NumaCPUPressureEvictionConfiguration{
		ThresholdExpandFactor:  0.7 / 0.55,
		CpuUsageRatioThreshold: 0.55,
	}

	evictionTriggerLine := c.CpuUsageRatioThreshold * c.ThresholdExpandFactor
	safetyLine := evictionTriggerLine / c.ThresholdExpandFactor
	buffer := evictionTriggerLine - safetyLine

	assert.InDelta(t, 0.7, evictionTriggerLine, 1e-9,
		"eviction trigger line = CpuUsageRatioThreshold * ThresholdExpandFactor = 0.55 * (0.7/0.55) = 0.7")
	assert.InDelta(t, 0.55, safetyLine, 1e-9,
		"safety line = evictionTriggerLine / ThresholdExpandFactor = 0.55")
	assert.InDelta(t, 0.15, buffer, 1e-9,
		"buffer = evictionTriggerLine - safetyLine = 0.7 - 0.55 = 0.15")
}

func TestPullThresholds_CpuUsageRatioThresholdPriority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		cpuUsageRatioThreshold float64
		expandFactor           float64
		wantUseAQC             bool
		wantThresholdValue     float64
		wantEvictionLine       float64
	}{
		{
			name:                   "CpuUsageRatioThreshold > 0: use AQC value",
			cpuUsageRatioThreshold: 0.55,
			expandFactor:           0.7 / 0.55,
			wantUseAQC:             true,
			wantThresholdValue:     0.55,
			wantEvictionLine:       0.7,
		},
		{
			name:                   "CpuUsageRatioThreshold = 0: fallback to NPD/MetricThresholdConfiguration",
			cpuUsageRatioThreshold: 0,
			expandFactor:           0.7 / 0.55,
			wantUseAQC:             false,
			wantThresholdValue:     0,
			wantEvictionLine:       0,
		},
		{
			name:                   "CpuUsageRatioThreshold = 0.65 with custom expand factor",
			cpuUsageRatioThreshold: 0.65,
			expandFactor:           1.2,
			wantUseAQC:             true,
			wantThresholdValue:     0.65,
			wantEvictionLine:       0.78,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := NumaCPUPressureEvictionConfiguration{
				CpuUsageRatioThreshold: tt.cpuUsageRatioThreshold,
				ThresholdExpandFactor:  tt.expandFactor,
			}

			if tt.wantUseAQC {
				assert.True(t, conf.CpuUsageRatioThreshold > 0,
					"CpuUsageRatioThreshold > 0 should trigger AQC priority path")

				thresholds := map[string]float64{
					metricthreshold.NUMACPUUsageRatioThreshold: conf.CpuUsageRatioThreshold,
				}
				assert.Equal(t, tt.wantThresholdValue, thresholds[metricthreshold.NUMACPUUsageRatioThreshold])

				expandedThresholds := make(map[string]float64)
				for k, v := range thresholds {
					expandedThresholds[k] = v * conf.ThresholdExpandFactor
				}
				assert.InDelta(t, tt.wantEvictionLine,
					expandedThresholds[metricthreshold.NUMACPUUsageRatioThreshold], 1e-9)
			} else {
				assert.Equal(t, float64(0), conf.CpuUsageRatioThreshold,
					"CpuUsageRatioThreshold == 0 should fallback to NPD/MetricThresholdConfiguration")
			}
		})
	}
}

func TestNumaCPUPressureEvictionConfiguration_FullDataFlow(t *testing.T) {
	t.Parallel()

	c := NumaCPUPressureEvictionConfiguration{
		ThresholdExpandFactor:  0.7 / 0.55,
		CpuUsageRatioThreshold: 0.55,
	}

	c.ApplyConfiguration(&crd.DynamicConfigCRD{})

	assert.True(t, c.CpuUsageRatioThreshold > 0,
		"CpuUsageRatioThreshold > 0 should trigger AQC priority path in pullThresholds")

	thresholds := map[string]float64{
		metricthreshold.NUMACPUUsageRatioThreshold: c.CpuUsageRatioThreshold,
	}

	convertedThresholds := make(map[string]float64)
	for k, v := range thresholds {
		newKey, ok := metricthreshold.ThresholdNameToResourceName[k]
		assert.True(t, ok, "threshold name %s should have a mapping", k)
		convertedThresholds[newKey] = v
	}

	expandedThresholds := make(map[string]float64)
	for k, v := range convertedThresholds {
		expandedThresholds[k] = v * c.ThresholdExpandFactor
	}

	evictionTriggerLine := expandedThresholds["cpu.usage.container"]
	safetyLine := evictionTriggerLine / c.ThresholdExpandFactor

	assert.InDelta(t, 0.7, evictionTriggerLine, 1e-9,
		"full data flow: eviction trigger line should be 0.7 (70%%)")
	assert.InDelta(t, 0.55, safetyLine, 1e-9,
		"full data flow: safety line should be 0.55 (55%%)")
}
