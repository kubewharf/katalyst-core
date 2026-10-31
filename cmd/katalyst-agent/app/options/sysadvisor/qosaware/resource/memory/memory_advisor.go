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
	"fmt"
	"math"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/resource/memory/headroom"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/resource/memory/plugins"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory"
)

// MemoryAdvisorOptions holds the configurations for memory advisor in qos aware plugin
type MemoryAdvisorOptions struct {
	MemoryHeadroomPolicyPriority []string
	*headroom.MemoryHeadroomPolicyOptions
	MemoryAdvisorPlugins       []string
	MinCriticalWatermark       resource.QuantityValue
	ReclaimedMemoryLimitMargin resource.QuantityValue
	*plugins.MemoryAdvisorPluginsOptions
}

// NewMemoryAdvisorOptions creates a new Options with a default config
func NewMemoryAdvisorOptions() *MemoryAdvisorOptions {
	return &MemoryAdvisorOptions{
		MemoryHeadroomPolicyPriority: []string{string(types.MemoryHeadroomPolicyCanonical)},
		MemoryHeadroomPolicyOptions:  headroom.NewMemoryHeadroomPolicyOptions(),
		MemoryAdvisorPlugins:         []string{},
		MinCriticalWatermark:         resource.QuantityValue{Quantity: resource.MustParse("4Gi")},
		ReclaimedMemoryLimitMargin:   resource.QuantityValue{Quantity: resource.MustParse("0")},
		MemoryAdvisorPluginsOptions:  plugins.NewMemoryAdvisorPluginsOptions(),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MemoryAdvisorOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringSliceVar(&o.MemoryHeadroomPolicyPriority, "memory-headroom-policy-priority", o.MemoryHeadroomPolicyPriority,
		"policy memory advisor to estimate resource headroom, sorted by priority descending order, should be formatted as 'policy1,policy2'")
	o.MemoryHeadroomPolicyOptions.AddFlags(fs)
	fs.StringSliceVar(&o.MemoryAdvisorPlugins, "memory-advisor-plugins", o.MemoryAdvisorPlugins,
		"memory advisor plugins to use.")
	fs.Var(&o.MinCriticalWatermark, "memory-advisor-min-critical-watermark", "min watermark to trigger reclaim")
	fs.Var(&o.ReclaimedMemoryLimitMargin, "memory-advisor-reclaimed-memory-limit-margin",
		"growth margin above active per-NUMA reclaimed memory usage; overrides the max ratio cap, bounded by NUMA total; 0 preserves existing behavior")
	o.MemoryAdvisorPluginsOptions.AddFlags(fs)
}

// ApplyTo fills up config with options
func (o *MemoryAdvisorOptions) ApplyTo(c *memory.MemoryAdvisorConfiguration) error {
	if o.ReclaimedMemoryLimitMargin.Sign() < 0 || o.ReclaimedMemoryLimitMargin.CmpInt64(math.MaxInt64) > 0 {
		return fmt.Errorf("memory-advisor-reclaimed-memory-limit-margin must be between 0 and %d bytes", int64(math.MaxInt64))
	}
	c.ReclaimedMemoryLimitMargin = o.ReclaimedMemoryLimitMargin.Value()

	for _, policy := range o.MemoryHeadroomPolicyPriority {
		c.MemoryHeadroomPolicies = append(c.MemoryHeadroomPolicies, types.MemoryHeadroomPolicyName(policy))
	}
	for _, plugin := range o.MemoryAdvisorPlugins {
		c.MemoryAdvisorPlugins = append(c.MemoryAdvisorPlugins, types.MemoryAdvisorPluginName(plugin))
	}
	c.MinCriticalWatermark = o.MinCriticalWatermark.Value()

	var errList []error
	errList = append(errList, o.MemoryHeadroomPolicyOptions.ApplyTo(c.MemoryHeadroomPolicyConfiguration))
	errList = append(errList, o.MemoryAdvisorPluginsOptions.ApplyTo(c.MemoryAdvisorPluginsConfiguration))
	return errors.NewAggregate(errList)
}
