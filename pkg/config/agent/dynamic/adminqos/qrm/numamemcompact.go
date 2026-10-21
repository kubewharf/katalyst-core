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
	"math"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type NumaMemCompactConfiguration struct {
	// EnableNumaMemCompact gates proactive compaction so that only NUMA nodes without online
	// business (shared_cores/dedicated_cores) pods are compacted. The idle-NUMA path is
	// edge-triggered: a NUMA node is compacted once right after its business pods leave. It is
	// dynamically configurable (per machine-type via AdminQoSConfiguration nodeLabelSelector) and
	// is independent of the fragmem feature, so both may take effect simultaneously.
	EnableNumaMemCompact bool

	// EnablePeriodicNumaMemCompact enables readiness-based repeated compaction while a NUMA node
	// stays idle. When disabled, each NUMA node is compacted only once per idle period. Defaults to
	// true.
	EnablePeriodicNumaMemCompact bool

	// NumaMemCompactInterval is the minimum interval between two actual compactions while a NUMA
	// node stays idle and EnablePeriodicNumaMemCompact is enabled. Once the interval has elapsed,
	// THP-order readiness is checked on each handler cycle and the node is compacted only when its
	// unusable-index increase from the post-compaction baseline exceeds the threshold. Defaults to
	// defaultNumaMemCompactInterval.
	NumaMemCompactInterval time.Duration

	// THPUnusableIndexDegradedThreshold is the minimum increase from the post-compaction THP-order
	// unusable-index baseline that triggers another compaction after NumaMemCompactInterval.
	// Defaults to defaultTHPUnusableIndexDegradedThreshold.
	THPUnusableIndexDegradedThreshold float64
}

// defaultNumaMemCompactInterval is the default minimum interval between two actual compactions for
// a NUMA node that stays idle. It is used unless overridden by AdminQoSConfiguration.
const defaultNumaMemCompactInterval = 1800 * time.Second

// minNumaMemCompactInterval is the lower bound for a periodic compaction interval.
const minNumaMemCompactInterval = 600 * time.Second

const (
	defaultTHPUnusableIndexDegradedThreshold = 5.0
	minTHPUnusableIndexDegradedThreshold     = 0.0
	maxTHPUnusableIndexDegradedThreshold     = 100.0
)

// ClampNumaMemCompactInterval guards against a misconfigured periodic compaction interval. A
// non-positive value falls back to the default; a positive value below the minimum is clamped up.
func ClampNumaMemCompactInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return defaultNumaMemCompactInterval
	}
	if interval < minNumaMemCompactInterval {
		return minNumaMemCompactInterval
	}
	return interval
}

// ClampTHPUnusableIndexDegradedThreshold keeps the threshold within the valid unusable-index
// range. NaN falls back to the default value.
func ClampTHPUnusableIndexDegradedThreshold(threshold float64) float64 {
	if math.IsNaN(threshold) {
		return defaultTHPUnusableIndexDegradedThreshold
	}
	if threshold < minTHPUnusableIndexDegradedThreshold {
		return minTHPUnusableIndexDegradedThreshold
	}
	if threshold > maxTHPUnusableIndexDegradedThreshold {
		return maxTHPUnusableIndexDegradedThreshold
	}
	return threshold
}

func NewNumaMemCompactConfiguration() *NumaMemCompactConfiguration {
	return &NumaMemCompactConfiguration{
		EnablePeriodicNumaMemCompact:      true,
		NumaMemCompactInterval:            defaultNumaMemCompactInterval,
		THPUnusableIndexDegradedThreshold: defaultTHPUnusableIndexDegradedThreshold,
	}
}

func (c *NumaMemCompactConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.QRMPluginConfig != nil &&
		aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig != nil &&
		aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig.NumaMemCompactConfig != nil {
		config := aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig.NumaMemCompactConfig
		if config.EnableNumaMemCompact != nil {
			c.EnableNumaMemCompact = *config.EnableNumaMemCompact
		}
		if config.EnablePeriodicNumaMemCompact != nil {
			c.EnablePeriodicNumaMemCompact = *config.EnablePeriodicNumaMemCompact
		}
		if config.NumaMemCompactIntervalSeconds != nil {
			c.NumaMemCompactInterval = ClampNumaMemCompactInterval(
				time.Duration(*config.NumaMemCompactIntervalSeconds) * time.Second)
		}
		if config.THPUnusableIndexDegradedThreshold != nil {
			c.THPUnusableIndexDegradedThreshold = ClampTHPUnusableIndexDegradedThreshold(
				*config.THPUnusableIndexDegradedThreshold)
		}
	}
}
