// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package eviction

import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

// CPUPressureEvictionPluginConfiguration is the config of CPUPressureEvictionPlugin
type CPUPressureEvictionPluginConfiguration struct {
	EnableCPUPressureEviction                bool
	LoadUpperBoundRatio                      float64
	LoadThresholdMetPercentage               float64
	CPUPressureEvictionPodGracePeriodSeconds int64
	MetricRingSize                           int
	CPUPressureEvictionSyncPeriod            time.Duration
	CPUPressureEvictionColdPeriod            time.Duration
	MaxCPUSuppressionToleranceRate           float64
	MinCPUSuppressionToleranceDuration       time.Duration
}

// NewCPUPressureEvictionPluginConfiguration returns a new CPUPressureEvictionPluginConfiguration
func NewCPUPressureEvictionPluginConfiguration() *CPUPressureEvictionPluginConfiguration {
	return &CPUPressureEvictionPluginConfiguration{}
}

// ApplyConfiguration applies dynamic.DynamicConfigCRD to CPUPressureEvictionPluginConfiguration
func (c *CPUPressureEvictionPluginConfiguration) ApplyConfiguration(*CPUPressureEvictionPluginConfiguration,
	*dynamic.DynamicConfigCRD) {
}
