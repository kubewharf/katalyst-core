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
	defaultCPUMemRatioLowerBound = 1. / 6.
	defaultCPUMemRatioUpperBound = 1. / 3.5
)

type MemoryPolicyCanonicalOptions struct {
	*MemoryUtilBasedOptions
}

type MemoryUtilBasedOptions struct {
	CPUMemRatioLowerBound float64
	CPUMemRatioUpperBound float64
}

func NewMemoryPolicyCanonicalOptions() *MemoryPolicyCanonicalOptions {
	return &MemoryPolicyCanonicalOptions{
		MemoryUtilBasedOptions: &MemoryUtilBasedOptions{
			CPUMemRatioLowerBound: defaultCPUMemRatioLowerBound,
			CPUMemRatioUpperBound: defaultCPUMemRatioUpperBound,
		},
	}
}

func (o *MemoryPolicyCanonicalOptions) AddFlags(fs *pflag.FlagSet) {
	fs.Float64Var(&o.CPUMemRatioLowerBound, "memory-headroom-cpu-mem-ratio-lower-bound", o.CPUMemRatioLowerBound,
		"the upper bound of memory to cpu ratio for enabling cache oversold")
	fs.Float64Var(&o.CPUMemRatioUpperBound, "memory-headroom-cpu-mem-ratio-upper-bound", o.CPUMemRatioUpperBound,
		"the lower bound of memory to cpu ratio for enabling cache oversold")
}

func (o *MemoryPolicyCanonicalOptions) ApplyTo(c *headroom.MemoryPolicyCanonicalConfiguration) error {
	c.CPUMemRatioLowerBound = o.CPUMemRatioLowerBound
	c.CPUMemRatioUpperBound = o.CPUMemRatioUpperBound
	return nil
}
