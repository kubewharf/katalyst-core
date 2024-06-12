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

package region

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/region"
)

type CPUShareOptions struct {
	RestrictRefPolicy map[string]string
}

// NewCPUShareOptions creates a new Options with a default config
func NewCPUShareOptions() *CPUShareOptions {
	return &CPUShareOptions{
		RestrictRefPolicy: map[string]string{
			string(types.CPUProvisionPolicyRama): string(types.CPUProvisionPolicyCanonical),
		},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUShareOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringToStringVar(&o.RestrictRefPolicy, "share-restrict-ref-policy", o.RestrictRefPolicy,
		"the provision policy map is used to restrict the control knob of base policy by the one of reference for share region")
}

// ApplyTo fills up config with options
func (o *CPUShareOptions) ApplyTo(c *region.CPUShareConfiguration) error {
	restrictRefPolicy := make(map[types.CPUProvisionPolicyName]types.CPUProvisionPolicyName)
	for k, v := range o.RestrictRefPolicy {
		restrictRefPolicy[types.CPUProvisionPolicyName(k)] = types.CPUProvisionPolicyName(v)
	}
	c.RestrictRefPolicy = restrictRefPolicy

	return nil
}
