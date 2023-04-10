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
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
)

// CPUAdvisorOptions holds the configurations for cpu advisor in qos aware plugin
type CPUAdvisorOptions struct {
	CPUProvisionAdditionalPolicy string
	CPUHeadroomAdditionalPolicy  string
}

// NewCPUAdvisorOptions creates a new Options with a default config
func NewCPUAdvisorOptions() *CPUAdvisorOptions {
	return &CPUAdvisorOptions{}
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUAdvisorOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.CPUProvisionAdditionalPolicy, "cpu-provision-additional-policy", o.CPUProvisionAdditionalPolicy,
		"additional policy of each region type for cpu advisor to update resource provision")
	fs.StringVar(&o.CPUHeadroomAdditionalPolicy, "cpu-headroom-additional-policy", o.CPUHeadroomAdditionalPolicy,
		"additional policy of each region type for cpu advisor to estimate resource headroom")
}

// ApplyTo fills up config with options
func (o *CPUAdvisorOptions) ApplyTo(c *cpu.CPUAdvisorConfiguration) error {
	c.ProvisionAdditionalPolicy = make(map[types.QoSRegionType]types.CPUProvisionPolicyName)

	if len(o.CPUProvisionAdditionalPolicy) > 0 {
		provisionPolicies := strings.Split(o.CPUProvisionAdditionalPolicy, ",")
		for _, policy := range provisionPolicies {
			kvs := strings.Split(policy, "=")
			if len(kvs) != 2 {
				return fmt.Errorf("invalid provision policies: %v", o.CPUProvisionAdditionalPolicy)
			}
			c.ProvisionAdditionalPolicy[types.QoSRegionType(kvs[0])] = types.CPUProvisionPolicyName(kvs[1])
		}
	}

	if len(o.CPUHeadroomAdditionalPolicy) > 0 {
		c.HeadroomAdditionalPolicy = make(map[types.QoSRegionType]types.CPUHeadroomPolicyName)
		headroomPolicies := strings.Split(o.CPUHeadroomAdditionalPolicy, ",")
		for _, policy := range headroomPolicies {
			kvs := strings.Split(policy, "=")
			if len(kvs) != 2 {
				return fmt.Errorf("invalid headroom policies: %v", o.CPUHeadroomAdditionalPolicy)
			}
			c.HeadroomAdditionalPolicy[types.QoSRegionType(kvs[0])] = types.CPUHeadroomPolicyName(kvs[1])
		}
	}

	return nil
}
