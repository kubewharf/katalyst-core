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
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

type NumaMemCompactOptions struct {
	EnableNumaMemCompact              bool
	EnablePeriodicNumaMemCompact      bool
	NumaMemCompactInterval            time.Duration
	THPUnusableIndexDegradedThreshold float64
}

func NewNumaMemCompactOptions() *NumaMemCompactOptions {
	return &NumaMemCompactOptions{
		EnableNumaMemCompact:              false,
		EnablePeriodicNumaMemCompact:      true,
		NumaMemCompactInterval:            1800 * time.Second,
		THPUnusableIndexDegradedThreshold: 5.0,
	}
}

func (o *NumaMemCompactOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory_resource_plugin")
	fs.BoolVar(&o.EnableNumaMemCompact, "qrm-memory-enable-numa-mem-compact",
		o.EnableNumaMemCompact, "if set true, we will proactively compact idle NUMA nodes (those without shared_cores/dedicated_cores pods)")
	fs.BoolVar(&o.EnablePeriodicNumaMemCompact, "qrm-memory-enable-periodic-numa-mem-compact",
		o.EnablePeriodicNumaMemCompact, "if set true, we will periodically compact idle NUMA nodes when their THP-order unusable index degrades")
	fs.DurationVar(&o.NumaMemCompactInterval, "qrm-memory-numa-mem-compact-interval",
		o.NumaMemCompactInterval, "the minimum interval between actual compactions while periodic idle-NUMA compaction is enabled")
	fs.Float64Var(&o.THPUnusableIndexDegradedThreshold,
		"qrm-memory-numa-mem-compact-thp-unusable-index-degraded-threshold",
		o.THPUnusableIndexDegradedThreshold,
		"the minimum increase from the post-compaction THP-order unusable-index baseline that triggers another compaction")
}

func (o *NumaMemCompactOptions) ApplyTo(c *dynamicqrm.NumaMemCompactConfiguration) error {
	c.EnableNumaMemCompact = o.EnableNumaMemCompact
	c.EnablePeriodicNumaMemCompact = o.EnablePeriodicNumaMemCompact
	// Apply the same guard as the AdminQoSConfiguration path so a misconfigured static flag cannot
	// cause an idle NUMA node to be compacted too frequently.
	c.NumaMemCompactInterval = dynamicqrm.ClampNumaMemCompactInterval(o.NumaMemCompactInterval)
	c.THPUnusableIndexDegradedThreshold = dynamicqrm.ClampTHPUnusableIndexDegradedThreshold(
		o.THPUnusableIndexDegradedThreshold)
	return nil
}
