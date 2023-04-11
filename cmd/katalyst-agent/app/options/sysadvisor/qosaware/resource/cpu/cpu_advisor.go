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

package cpu

import (
	"strings"

	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
)

// CPUAdvisorOptions holds the configurations for cpu advisor in qos aware plugin
type CPUAdvisorOptions struct {
	CPUProvisionPolicyPriority map[string]string
	CPUHeadroomPolicyPriority  map[string]string
}

// NewCPUAdvisorOptions creates a new Options with a default config
func NewCPUAdvisorOptions() *CPUAdvisorOptions {
	return &CPUAdvisorOptions{
		CPUProvisionPolicyPriority: map[string]string{
			string(types.QoSRegionTypeShare):                  string(types.CPUProvisionPolicyCanonical),
			string(types.QoSRegionTypeDedicatedNumaExclusive): string(types.CPUProvisionPolicyCanonical),
		},
		CPUHeadroomPolicyPriority: map[string]string{
			string(types.QoSRegionTypeShare):                  string(types.CPUHeadroomPolicyCanonical),
			string(types.QoSRegionTypeDedicatedNumaExclusive): string(types.CPUHeadroomPolicyCanonical),
		},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUAdvisorOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringToStringVar(&o.CPUProvisionPolicyPriority, "cpu-provision-policy-priority", o.CPUProvisionPolicyPriority,
		"policies of each region type for cpu advisor to update resource provision, sorted by priority descending order, "+
			"should be formatted as 'share=rama/canonical,dedicated-numa-exclusive=rama/canonical'")
	fs.StringToStringVar(&o.CPUHeadroomPolicyPriority, "cpu-headroom-policy-priority", o.CPUHeadroomPolicyPriority,
		"policies of each region type for cpu advisor to estimate resource headroom, sorted by priority descending order, "+
			"should be formatted as 'share=rama/canonical,dedicated-numa-exclusive=rama/canonical'")
}

// ApplyTo fills up config with options
func (o *CPUAdvisorOptions) ApplyTo(c *cpu.CPUAdvisorConfiguration) error {
	for regionType, policies := range o.CPUProvisionPolicyPriority {
		provisionPolicies := strings.Split(policies, "/")
		for _, policyName := range provisionPolicies {
			c.ProvisionPolicies[types.QoSRegionType(regionType)] =
				append(c.ProvisionPolicies[types.QoSRegionType(regionType)], types.CPUProvisionPolicyName(policyName))
		}
	}

	for regionType, policies := range o.CPUHeadroomPolicyPriority {
		headroomPolicies := strings.Split(policies, "/")
		for _, policyName := range headroomPolicies {
			c.HeadroomPolicies[types.QoSRegionType(regionType)] =
				append(c.HeadroomPolicies[types.QoSRegionType(regionType)], types.CPUHeadroomPolicyName(policyName))
		}
	}

	return nil
}
