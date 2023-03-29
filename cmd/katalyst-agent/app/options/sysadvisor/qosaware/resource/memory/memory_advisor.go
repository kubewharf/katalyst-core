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

package memory

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory"
)

// MemoryAdvisorOptions holds the configurations for memory advisor in qos aware plugin
type MemoryAdvisorOptions struct {
	MemoryHeadroomPolicy string
}

// NewMemoryAdvisorOptions creates a new Options with a default config
func NewMemoryAdvisorOptions() *MemoryAdvisorOptions {
	return &MemoryAdvisorOptions{
		MemoryHeadroomPolicy: string(types.MemoryHeadroomPolicyCanonical),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MemoryAdvisorOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.MemoryHeadroomPolicy, "memory-headroom-policy", o.MemoryHeadroomPolicy,
		"policy memory advisor to estimate resource headroom")
}

// ApplyTo fills up config with options
func (o *MemoryAdvisorOptions) ApplyTo(c *memory.MemoryAdvisorConfiguration) error {
	c.MemoryHeadroomPolicy = o.MemoryHeadroomPolicy
	return nil
}
