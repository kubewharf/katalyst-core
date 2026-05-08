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

	pkgeviction "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

func TestNumaCPUPressureEvictionOptions_Defaults(t *testing.T) {
	t.Parallel()

	o := NewNumaCPUPressureEvictionOptions()
	assert.Equal(t, 7.0/6.0, o.ThresholdExpandFactor, "ThresholdExpandFactor default should be 7/6")
	assert.Equal(t, 0.6, o.CpuUsageRatioThreshold, "CpuUsageRatioThreshold default should be 0.6")
}

func TestNumaCPUPressureEvictionOptions_ApplyTo(t *testing.T) {
	t.Parallel()

	o := NewNumaCPUPressureEvictionOptions()
	c := pkgeviction.NumaCPUPressureEvictionConfiguration{}

	err := o.ApplyTo(&c)
	assert.NoError(t, err)
	assert.Equal(t, 7.0/6.0, c.ThresholdExpandFactor, "ApplyTo should propagate ThresholdExpandFactor from Options")
	assert.Equal(t, 0.6, c.CpuUsageRatioThreshold, "ApplyTo should propagate CpuUsageRatioThreshold from Options")
}

func TestNumaCPUPressureEvictionOptions_ApplyTo_CustomValues(t *testing.T) {
	t.Parallel()

	o := NumaCPUPressureEvictionOptions{
		ThresholdExpandFactor:  1.3,
		CpuUsageRatioThreshold: 0.65,
	}
	c := pkgeviction.NumaCPUPressureEvictionConfiguration{}

	err := o.ApplyTo(&c)
	assert.NoError(t, err)
	assert.Equal(t, 1.3, c.ThresholdExpandFactor, "ApplyTo should propagate custom ThresholdExpandFactor")
	assert.Equal(t, 0.65, c.CpuUsageRatioThreshold, "ApplyTo should propagate custom CpuUsageRatioThreshold")
}

func TestNumaCPUPressureEvictionOptions_ApplyTo_ZeroCpuUsageRatioThreshold(t *testing.T) {
	t.Parallel()

	o := NumaCPUPressureEvictionOptions{
		ThresholdExpandFactor:  7.0 / 6.0,
		CpuUsageRatioThreshold: 0,
	}
	c := pkgeviction.NumaCPUPressureEvictionConfiguration{}

	err := o.ApplyTo(&c)
	assert.NoError(t, err)
	assert.Equal(t, float64(0), c.CpuUsageRatioThreshold,
		"ApplyTo should propagate CpuUsageRatioThreshold=0, which means fallback to NPD")
}

func TestNumaCPUPressureEvictionOptions_EvictionTriggerLine(t *testing.T) {
	t.Parallel()

	o := NewNumaCPUPressureEvictionOptions()
	c := pkgeviction.NumaCPUPressureEvictionConfiguration{}
	_ = o.ApplyTo(&c)

	evictionTriggerLine := c.CpuUsageRatioThreshold * c.ThresholdExpandFactor
	safetyLine := evictionTriggerLine / c.ThresholdExpandFactor

	assert.InDelta(t, 0.7, evictionTriggerLine, 1e-9,
		"eviction trigger line = 0.6 * 7/6 = 0.7")
	assert.InDelta(t, 0.6, safetyLine, 1e-9,
		"safety line = 0.7 / (7/6) = 0.6")
}
