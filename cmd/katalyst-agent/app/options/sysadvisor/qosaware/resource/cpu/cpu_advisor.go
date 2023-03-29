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
	CPUProvisionPolicy string
	CPUHeadroomPolicy  string
}

// NewCPUAdvisorOptions creates a new Options with a default config
func NewCPUAdvisorOptions() *CPUAdvisorOptions {
	return &CPUAdvisorOptions{
		CPUProvisionPolicy: string(types.CPUProvisionPolicyCanonical),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUAdvisorOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.CPUProvisionPolicy, "cpu-provision-policy", o.CPUProvisionPolicy,
		"policy of each region type for cpu advisor to update resource provision")
	fs.StringVar(&o.CPUHeadroomPolicy, "cpu-headroom-policy", o.CPUHeadroomPolicy,
		"policy of each region type for cpu advisor to estimate resource headroom")
}

// ApplyTo fills up config with options
func (o *CPUAdvisorOptions) ApplyTo(c *cpu.CPUAdvisorConfiguration) error {
	c.CPUProvisionPolicy = make(map[types.QoSRegionType]types.CPUProvisionPolicyName)
	provisionPolicies := strings.Split(o.CPUProvisionPolicy, ",")
	for _, policy := range provisionPolicies {
		kvs := strings.Split(policy, "=")
		if len(kvs) != 2 {
			continue
		}
		c.CPUProvisionPolicy[types.QoSRegionType(kvs[0])] = types.CPUProvisionPolicyName(kvs[1])
	}

	c.CPUHeadroomPolicy = make(map[types.QoSRegionType]types.CPUHeadroomPolicyName)
	headroomPolicies := strings.Split(o.CPUHeadroomPolicy, ",")
	for _, policy := range headroomPolicies {
		kvs := strings.Split(policy, "=")
		if len(kvs) != 2 {
			continue
		}
		c.CPUHeadroomPolicy[types.QoSRegionType(kvs[0])] = types.CPUHeadroomPolicyName(kvs[1])
	}

	return nil
}
