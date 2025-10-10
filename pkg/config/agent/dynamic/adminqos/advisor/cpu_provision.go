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
	"github.com/kubewharf/katalyst-api/pkg/utils"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type CPUProvisionConfiguration struct {
	AllowSharedCoresOverlapReclaimedCores       bool
	RegionIndicatorTargetConfiguration          map[v1alpha1.QoSRegionType][]v1alpha1.IndicatorTargetConfiguration
	IndicatorTargetGetters                      map[string]string
	IndicatorTargetDefaultGetter                string
	IndicatorTargetMetricThresholdExpandFactors map[string]float64
}

func NewCPUProvisionConfiguration() *CPUProvisionConfiguration {
	return &CPUProvisionConfiguration{
		AllowSharedCoresOverlapReclaimedCores: false,
		RegionIndicatorTargetConfiguration: map[v1alpha1.QoSRegionType][]v1alpha1.IndicatorTargetConfiguration{
			v1alpha1.QoSRegionTypeShare: {
				{
					Name:   workloadv1alpha1.ServiceSystemIndicatorNameCPUSchedWait,
					Target: 460,
				},
				{
					Name:   workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio,
					Target: 0.8,
				},
			},
			v1alpha1.QoSRegionTypeDedicated: {
				{
					Name:   workloadv1alpha1.ServiceSystemIndicatorNameCPI,
					Target: 1.4,
				},
				{
					Name:   workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio,
					Target: 0.55,
				},
			},
		},
		IndicatorTargetGetters: map[string]string{
			string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): string(consts.IndicatorTargetGetterSPDAvg),
		},
		IndicatorTargetDefaultGetter: string(consts.IndicatorTargetGetterSPDMin),
		IndicatorTargetMetricThresholdExpandFactors: map[string]float64{
			string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): 1,
		},
	}
}

func (c *CPUProvisionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.AdvisorConfig != nil &&
		aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig != nil {
		if aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig != nil {
			for _, regionIndicator := range aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig.RegionIndicators {
				c.RegionIndicatorTargetConfiguration[utils.CompatibleLegacyRegionType(regionIndicator.RegionType)] = regionIndicator.Targets
			}
		}
		if aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.AllowSharedCoresOverlapReclaimedCores != nil {
			c.AllowSharedCoresOverlapReclaimedCores = *aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.AllowSharedCoresOverlapReclaimedCores
		}
	}
}
