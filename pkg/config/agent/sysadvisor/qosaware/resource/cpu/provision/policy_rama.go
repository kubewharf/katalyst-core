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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type PolicyRamaConfiguration struct {
	IndicatorMetrics map[types.QoSRegionType][]string
	PIDParameters    map[string]types.FirstOrderPIDParams
}

// todo: support update pid parameters by kcc
func NewPolicyRamaConfiguration() *PolicyRamaConfiguration {
	return &PolicyRamaConfiguration{
		IndicatorMetrics: map[types.QoSRegionType][]string{
			types.QoSRegionTypeShare: {
				consts.MetricCPUSchedwait,
			},
			types.QoSRegionTypeDedicatedNumaExclusive: {
				consts.MetricCPUCPIContainer,
			},
		},
		PIDParameters: map[string]types.FirstOrderPIDParams{
			consts.MetricCPUSchedwait: {
				Kpp:                  10.0,
				Kpn:                  1.0,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandLowerPct:     0.8,
				DeadbandUpperPct:     0.05,
			},
			consts.MetricCPUCPIContainer: {
				Kpp:                  10.0,
				Kpn:                  1.0,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandLowerPct:     0.95,
				DeadbandUpperPct:     0.02,
			},
			consts.MetricMemBandwidthNuma: {
				Kpp:                  10.0,
				Kpn:                  1.0,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandLowerPct:     0.95,
				DeadbandUpperPct:     0.02,
			},
		},
	}
}
