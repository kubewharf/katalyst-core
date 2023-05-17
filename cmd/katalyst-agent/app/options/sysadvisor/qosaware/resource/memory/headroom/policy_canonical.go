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

package headroom

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory/headroom"
)

const (
	defaultEnabled             = false
	defaultFreeBasedRatio      = 0.6
	defaultStaticBasedCapacity = 20 << 30 // 20GB

	defaultCacheBaseRatio        = 0
	defaultCPUMemRatioLowerBound = 1. / 6.
	defaultCPUMemRatioUpperBound = 1. / 3.5
)

type MemoryPolicyCanonicalOptions struct {
	*MemoryUtilBasedOptions
}

type MemoryUtilBasedOptions struct {
	Enabled             bool
	FreeBasedRatio      float64
	CacheBasedRatio     float64
	StaticBasedCapacity float64

	CPUMemRatioLowerBound float64
	CPUMemRatioUpperBound float64
}

func NewMemoryPolicyCanonicalOptions() *MemoryPolicyCanonicalOptions {
	return &MemoryPolicyCanonicalOptions{
		MemoryUtilBasedOptions: &MemoryUtilBasedOptions{
			Enabled:               defaultEnabled,
			FreeBasedRatio:        defaultFreeBasedRatio,
			StaticBasedCapacity:   defaultStaticBasedCapacity,
			CacheBasedRatio:       defaultCacheBaseRatio,
			CPUMemRatioLowerBound: defaultCPUMemRatioLowerBound,
			CPUMemRatioUpperBound: defaultCPUMemRatioUpperBound,
		},
	}
}

func (o *MemoryPolicyCanonicalOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.Enabled, "memory-headroom-policy-canonical-util-based-enabled", o.Enabled,
		"the flag to enable memory buffer")
	fs.Float64Var(&o.FreeBasedRatio, "memory-headroom-policy-canonical-free-based-ratio", o.FreeBasedRatio,
		"the estimation of free memory utilization, which can be used as system buffer to oversold memory")
	fs.Float64Var(&o.StaticBasedCapacity, "memory-headroom-policy-canonical-static-based-capacity", o.StaticBasedCapacity,
		"the static oversold memory size by bytes")
	fs.Float64Var(&o.CacheBasedRatio, "memory-headroom-policy-canonical-cache-based-ratio", o.CacheBasedRatio,
		"the rate of cache oversold, 0 means disable cache oversold")
	fs.Float64Var(&o.CPUMemRatioLowerBound, "memory-headroom-policy-canonical-cpu-mem-ratio-lower-bound", o.CPUMemRatioLowerBound,
		"the upper bound of memory to cpu ratio for enabling cache oversold")
	fs.Float64Var(&o.CPUMemRatioUpperBound, "memory-headroom-policy-canonical-cpu-mem-ratio-upper-bound", o.CPUMemRatioUpperBound,
		"the lower bound of memory to cpu ratio for enabling cache oversold")
}

func (o *MemoryPolicyCanonicalOptions) ApplyTo(c *headroom.MemoryPolicyCanonicalConfiguration) error {
	c.Enabled = o.Enabled
	c.FreeBasedRatio = o.FreeBasedRatio
	c.StaticBasedCapacity = o.StaticBasedCapacity
	c.CacheBasedRatio = o.CacheBasedRatio
	c.CPUMemRatioLowerBound = o.CPUMemRatioLowerBound
	c.CPUMemRatioUpperBound = o.CPUMemRatioUpperBound
	return nil
}
