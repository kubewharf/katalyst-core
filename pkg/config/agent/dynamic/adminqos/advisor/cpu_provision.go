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

package advisor

import (
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func NewPolicyRamaConfiguration() *v1alpha1.PolicyRamaConfiguration {
	return &v1alpha1.PolicyRamaConfiguration{
		PIDParameters: map[string]v1alpha1.FirstOrderPIDParams{
			string(workloadv1alpha1.ServiceSystemIndicatorNameCPUSchedWait): {
				Kpp:                  5.0,
				Kpn:                  0.9,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandUpperPct:     0.05,
				DeadbandLowerPct:     0.2,
			},
			string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {
				Kpp:                  10.0,
				Kpn:                  2.0,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandUpperPct:     0.01,
				DeadbandLowerPct:     0.06,
			},
			string(workloadv1alpha1.ServiceSystemIndicatorNameCPI): {
				Kpp:                  10.0,
				Kpn:                  2.0,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandUpperPct:     0.0,
				DeadbandLowerPct:     0.02,
			},
			string(workloadv1alpha1.ServiceSystemIndicatorNameMemoryAccessReadLatency): {
				Kpp:                  5.0,
				Kpn:                  0.9,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandUpperPct:     0.05,
				DeadbandLowerPct:     0.2,
			},
			string(workloadv1alpha1.ServiceSystemIndicatorNameMemoryAccessWriteLatency): {
				Kpp:                  5.0,
				Kpn:                  0.9,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandUpperPct:     0.05,
				DeadbandLowerPct:     0.2,
			},
			string(workloadv1alpha1.ServiceSystemIndicatorNameMemoryL3MissLatency): {
				Kpp:                  5.0,
				Kpn:                  0.9,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandUpperPct:     0.05,
				DeadbandLowerPct:     0.2,
			},
		},
	}
}

type CPUProvisionConfiguration struct {
	RegionIndicatorTargetConfiguration map[string][]v1alpha1.IndicatorTargetConfiguration
	PolicyRama                         *v1alpha1.PolicyRamaConfiguration
}

func NewCPUProvisionConfiguration() *CPUProvisionConfiguration {
	return &CPUProvisionConfiguration{
		RegionIndicatorTargetConfiguration: map[string][]v1alpha1.IndicatorTargetConfiguration{
			string(types.QoSRegionTypeShare): {
				{
					Name:   string(workloadv1alpha1.ServiceSystemIndicatorNameCPUSchedWait),
					Target: 460,
				},
				{
					Name:   string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio),
					Target: 0.8,
				},
			},
			string(types.QoSRegionTypeDedicatedNumaExclusive): {
				{
					Name:   string(workloadv1alpha1.ServiceSystemIndicatorNameCPI),
					Target: 1.4,
				},
			},
		},
		PolicyRama: NewPolicyRamaConfiguration(),
	}
}

func (c *CPUProvisionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.AdvisorConfig != nil &&
		aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig != nil &&
		aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig != nil {
		if len(aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig.IndicatorTargets) != 0 {
			c.RegionIndicatorTargetConfiguration = aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig.IndicatorTargets
		}
		if aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig.PolicyRama != nil {
			c.PolicyRama = aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig.PolicyRama
		}
	}
}
