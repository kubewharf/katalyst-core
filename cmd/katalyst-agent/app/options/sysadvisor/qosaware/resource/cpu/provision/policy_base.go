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

package provision

import (
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/errors"

	provisionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/provision"
)

type CPUProvisionPolicyOptions struct {
	PolicyRama *PolicyRamaOptions
}

func NewCPUProvisionPolicyOptions() *CPUProvisionPolicyOptions {
	return &CPUProvisionPolicyOptions{
		PolicyRama: NewPolicyRamaOptions(),
	}
}

// ApplyTo fills up config with options
func (o *CPUProvisionPolicyOptions) ApplyTo(c *provisionconfig.CPUProvisionPolicyConfiguration) error {
	var errList []error
	errList = append(errList, o.PolicyRama.ApplyTo(c.PolicyRama))
	return errors.NewAggregate(errList)
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUProvisionPolicyOptions) AddFlags(fs *pflag.FlagSet) {
	o.PolicyRama.AddFlags(fs)
}
