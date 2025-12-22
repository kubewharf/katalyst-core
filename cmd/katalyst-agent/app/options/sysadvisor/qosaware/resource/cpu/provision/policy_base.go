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
	"time"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/errors"

	provisionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/provision"
)

type CPURegulatorOptions struct {
	MaxRampUpStep     int
	MaxRampDownStep   int
	MinRampDownPeriod time.Duration
}

type CPUProvisionPolicyOptions struct {
	// CPURegulatorOptions is the options for cpu regulator
	CPURegulatorOptions

	// PolicyRamaOptions is the options for policy rama
	PolicyRama *PolicyRamaOptions

	// enable to use control knob cpu quota when cgroup2 available
	EnableControlKnobCPUQuota bool

	// enable to use control knob cpu quota when cgroup1 available
	EnableControlKnobCPUQuotaForV1 bool
}

func NewCPUProvisionPolicyOptions() *CPUProvisionPolicyOptions {
	return &CPUProvisionPolicyOptions{
		CPURegulatorOptions: CPURegulatorOptions{
			MaxRampUpStep:     10,
			MaxRampDownStep:   2,
			MinRampDownPeriod: 30 * time.Second,
		},
		PolicyRama: NewPolicyRamaOptions(),
	}
}

// ApplyTo fills up config with options
func (o *CPUProvisionPolicyOptions) ApplyTo(c *provisionconfig.CPUProvisionPolicyConfiguration) error {
	c.MaxRampUpStep = o.MaxRampUpStep
	c.MaxRampDownStep = o.MaxRampDownStep
	c.MinRampDownPeriod = o.MinRampDownPeriod

	c.EnableControlKnobCPUQuota = o.EnableControlKnobCPUQuota
	c.EnableControlKnobCPUQuotaForV1 = o.EnableControlKnobCPUQuotaForV1

	var errList []error
	errList = append(errList, o.PolicyRama.ApplyTo(c.PolicyRama))

	return errors.NewAggregate(errList)
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUProvisionPolicyOptions) AddFlags(fs *pflag.FlagSet) {
	fs.IntVar(&o.MaxRampUpStep, "cpu-regulator-max-ramp-up-step", o.MaxRampUpStep, "max ramp up step for cpu provision policy")
	fs.IntVar(&o.MaxRampDownStep, "cpu-regulator-max-ramp-down-step", o.MaxRampDownStep, "max ramp down step for cpu provision policy")
	fs.DurationVar(&o.MinRampDownPeriod, "cpu-regulator-min-ramp-down-period", o.MinRampDownPeriod, "min ramp down period for cpu provision policy")
	o.PolicyRama.AddFlags(fs)
	fs.BoolVar(&o.EnableControlKnobCPUQuota, "cpu-provision-enable-control-knob-cpu-quota", o.EnableControlKnobCPUQuota, "enable control knob cpu quota for cpu provision policy")
	fs.BoolVar(&o.EnableControlKnobCPUQuotaForV1, "cpu-provision-enable-control-knob-cpu-quota-v1", o.EnableControlKnobCPUQuotaForV1, "enable control knob cpu quota for cpu provision policy for cgroup1")
}
