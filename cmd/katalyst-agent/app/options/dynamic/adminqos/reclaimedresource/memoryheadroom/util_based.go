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

package memoryheadroom

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/memoryheadroom"
)

const (
	defaultEnabled             = false
	defaultFreeBasedRatio      = 0.6
	defaultStaticBasedCapacity = 20 << 30 // 20GB
	defaultCacheBasedRatio     = 0
	defaultMaxOversoldRate     = 2.0
)

type UtilBasedOptions struct {
	Enable              bool
	FreeBasedRatio      float64
	StaticBasedCapacity float64
	CacheBasedRatio     float64
	MaxOversoldRate     float64
}

func NewUtilBasedOptions() *UtilBasedOptions {
	return &UtilBasedOptions{
		Enable:              defaultEnabled,
		FreeBasedRatio:      defaultFreeBasedRatio,
		StaticBasedCapacity: defaultStaticBasedCapacity,
		CacheBasedRatio:     defaultCacheBasedRatio,
		MaxOversoldRate:     defaultMaxOversoldRate,
	}
}

func (o *UtilBasedOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.Enable, "memory-headroom-util-based-enable", o.Enable,
		"show whether enable utilization based memory headroom policy")
	fs.Float64Var(&o.FreeBasedRatio, "memory-headroom-free-based-ratio", o.FreeBasedRatio,
		"the estimation of free memory utilization, which can")
	fs.Float64Var(&o.StaticBasedCapacity, "memory-headroom-static-based-capacity", o.StaticBasedCapacity,
		"the static oversold memory size by bytes")
	fs.Float64Var(&o.CacheBasedRatio, "memory-headroom-cache-based-ratio", o.CacheBasedRatio,
		"the rate of cache oversold, 0 means disable cache oversold")
	fs.Float64Var(&o.MaxOversoldRate, "memory-headroom-max-oversold-rate", o.MaxOversoldRate,
		"the max oversold rate of memory headroom to the memory limit of reclaimed_cores cgroup")
}

func (o *UtilBasedOptions) ApplyTo(c *memoryheadroom.MemoryUtilBasedConfiguration) error {
	c.Enable = o.Enable
	c.FreeBasedRatio = o.FreeBasedRatio
	c.StaticBasedCapacity = o.StaticBasedCapacity
	c.CacheBasedRatio = o.CacheBasedRatio
	c.MaxOversoldRate = o.MaxOversoldRate
	return nil
}
