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

import "time"

type CPUProvisionPolicyConfiguration struct {
	CPURegulatorConfiguration
	// PolicyRama is the configuration for policy rama
	PolicyRama *PolicyRamaConfiguration
	// enable to use control knob cpu quota when cgroup2 available
	EnableControlKnobCPUQuota bool
	// enable to use control knob cpu quota when cgroup1 available
	EnableControlKnobCPUQuotaForV1 bool
}

func NewCPUProvisionPolicyConfiguration() *CPUProvisionPolicyConfiguration {
	return &CPUProvisionPolicyConfiguration{
		PolicyRama: NewPolicyRamaConfiguration(),
	}
}

type CPURegulatorConfiguration struct {
	// MaxRampUpStep is the max cpu cores can be increased during each cpu requirement update
	MaxRampUpStep int

	// MaxRampDownStep is the max cpu cores can be decreased during each cpu requirement update
	MaxRampDownStep int

	// MinRampDownPeriod is the min time gap between two consecutive cpu requirement ramp down
	MinRampDownPeriod time.Duration
}
