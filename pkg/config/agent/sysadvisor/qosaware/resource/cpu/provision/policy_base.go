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
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

type IndicatorTargetConfiguration struct {
	Name   string
	Target float64
}

type CPUProvisionPolicyConfiguration struct {
	RegionIndicatorTargetConfiguration map[types.QoSRegionType][]IndicatorTargetConfiguration
	PolicyRama                         *PolicyRamaConfiguration
}

func NewCPUProvisionPolicyConfiguration() *CPUProvisionPolicyConfiguration {
	return &CPUProvisionPolicyConfiguration{
		RegionIndicatorTargetConfiguration: map[types.QoSRegionType][]IndicatorTargetConfiguration{
			types.QoSRegionTypeShare: {
				{
					Name:   string(v1alpha1.ServiceSystemIndicatorNameCPUSchedWait),
					Target: 460,
				},
				{
					Name:   string(v1alpha1.ServiceSystemIndicatorNameCPUUsageRatio),
					Target: 0.8,
				},
			},
			types.QoSRegionTypeDedicatedNumaExclusive: {
				{
					Name:   string(v1alpha1.ServiceSystemIndicatorNameCPI),
					Target: 1.4,
				},
			},
		},
		PolicyRama: NewPolicyRamaConfiguration(),
	}
}
