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
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/region"
)

type CPURegionOptions struct {
	CPUShare          *CPUShareOptions
	RestrictRefPolicy map[string]string
}

func NewCPURegionOptions() *CPURegionOptions {
	return &CPURegionOptions{
		CPUShare:          NewCPUShareOptions(),
		RestrictRefPolicy: map[string]string{string(types.CPUProvisionPolicyRama): string(types.CPUProvisionPolicyDynamicQuota)},
	}
}

// ApplyTo fills up config with options
func (o *CPURegionOptions) ApplyTo(c *region.CPURegionConfiguration) error {
	var errList []error
	errList = append(errList, o.CPUShare.ApplyTo(c.CPUShareConfiguration))

	restrictRefPolicy := make(map[types.CPUProvisionPolicyName]types.CPUProvisionPolicyName)
	for k, v := range o.RestrictRefPolicy {
		restrictRefPolicy[types.CPUProvisionPolicyName(k)] = types.CPUProvisionPolicyName(v)
	}
	c.RestrictRefPolicy = restrictRefPolicy
	return errors.NewAggregate(errList)
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPURegionOptions) AddFlags(fs *pflag.FlagSet) {
	o.CPUShare.AddFlags(fs)

	// AddFlags adds flags to the specified FlagSet.
	fs.StringToStringVar(&o.RestrictRefPolicy, "region-restrict-ref-policy", o.RestrictRefPolicy,
		"the provision policy map is used to restrict the control knob of base policy by the one of reference for CPU region")
}
