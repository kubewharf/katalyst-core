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
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// clampReclaimedMaxRatio bounds a reclaimed-resource max ratio into [0, 1]. A
// value >1 would amplify rather than limit reclaim (contradicting the MaxRatio
// semantics), and a negative value is meaningless; both are corrected with a
// warning so a misconfigured CRD/flag cannot silently take effect.
func clampReclaimedMaxRatio(name string, ratio float64) float64 {
	if ratio < 0 {
		general.Warningf("%s=%.4f is out of range [0,1], clamping to 0", name, ratio)
		return 0
	}
	if ratio > 1 {
		general.Warningf("%s=%.4f is out of range [0,1], clamping to 1", name, ratio)
		return 1
	}
	return ratio
}

type CPUProvisionConfiguration struct {
	AllowSharedCoresOverlapReclaimedCores               bool
	DisableDedicatedCoresOverlapReclaimedCores          bool
	RequestedAllowSharedCoresOverlapReclaimedCores      bool
	RequestedDisableDedicatedCoresOverlapReclaimedCores bool
	RegionIndicatorTargetConfiguration                  map[v1alpha1.QoSRegionType][]v1alpha1.IndicatorTargetConfiguration
	IndicatorTargetGetters                              map[string]string
	IndicatorTargetDefaultGetter                        string
	IndicatorTargetMetricThresholdExpandFactors         map[string]float64
	// ReclaimedCPUMaxRatio is the ratio (in [0, 1]) of the maximum amount of CPUs
	// that can be reclaimed at any time. 0 means no limit.
	ReclaimedCPUMaxRatio float64
	// FillDefaultSharePoolWithNonReclaimCPUs makes the default non-NUMA-binding
	// share pool consume all residual non-reclaim CPUs.
	FillDefaultSharePoolWithNonReclaimCPUs bool
}

func NewCPUProvisionConfiguration() *CPUProvisionConfiguration {
	return &CPUProvisionConfiguration{
		AllowSharedCoresOverlapReclaimedCores:      false,
		DisableDedicatedCoresOverlapReclaimedCores: false,
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
	allowSharedCoresOverlapReclaimedCores := c.RequestedAllowSharedCoresOverlapReclaimedCores
	disableDedicatedCoresOverlapReclaimedCores := c.RequestedDisableDedicatedCoresOverlapReclaimedCores
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.AdvisorConfig != nil &&
		aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig != nil {
		if aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig != nil {
			for _, regionIndicator := range aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig.RegionIndicators {
				c.RegionIndicatorTargetConfiguration[utils.CompatibleLegacyRegionType(regionIndicator.RegionType)] = regionIndicator.Targets
			}
			if cfg := aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig; cfg.ReclaimedCPUMaxRatio != nil {
				c.ReclaimedCPUMaxRatio = clampReclaimedMaxRatio("ReclaimedCPUMaxRatio", *cfg.ReclaimedCPUMaxRatio)
			}
			if cfg := aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.CPUProvisionConfig; cfg.FillDefaultSharePoolWithNonReclaimCPUs != nil {
				c.FillDefaultSharePoolWithNonReclaimCPUs = *cfg.FillDefaultSharePoolWithNonReclaimCPUs
			}
		}
		if aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.AllowSharedCoresOverlapReclaimedCores != nil {
			allowSharedCoresOverlapReclaimedCores = *aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.AllowSharedCoresOverlapReclaimedCores
		}
		if aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.DisableDedicatedCoresOverlapReclaimedCores != nil {
			disableDedicatedCoresOverlapReclaimedCores = *aqc.Spec.Config.AdvisorConfig.CPUAdvisorConfig.DisableDedicatedCoresOverlapReclaimedCores
		}
	}
	c.SetRequestedOverlapPolicy(allowSharedCoresOverlapReclaimedCores, disableDedicatedCoresOverlapReclaimedCores)
}

// SetRequestedOverlapPolicy records the requested policy before deriving its effective values.
func (c *CPUProvisionConfiguration) SetRequestedOverlapPolicy(allowSharedCoresOverlapReclaimedCores, disableDedicatedCoresOverlapReclaimedCores bool) {
	c.RequestedAllowSharedCoresOverlapReclaimedCores = allowSharedCoresOverlapReclaimedCores
	c.RequestedDisableDedicatedCoresOverlapReclaimedCores = disableDedicatedCoresOverlapReclaimedCores
	c.NormalizeOverlapPolicy()
}

// NormalizeOverlapPolicy keeps CPU pool overlap flags in a supported policy combination.
func (c *CPUProvisionConfiguration) NormalizeOverlapPolicy() {
	c.AllowSharedCoresOverlapReclaimedCores = c.RequestedAllowSharedCoresOverlapReclaimedCores
	c.DisableDedicatedCoresOverlapReclaimedCores = c.RequestedDisableDedicatedCoresOverlapReclaimedCores
	if !c.FillDefaultSharePoolWithNonReclaimCPUs {
		return
	}
	if c.AllowSharedCoresOverlapReclaimedCores {
		general.Warningf("fill default share pool with non-reclaim cpus requires disallowing shared cores overlap reclaimed cores, overriding to false")
		c.AllowSharedCoresOverlapReclaimedCores = false
	}
	if !c.DisableDedicatedCoresOverlapReclaimedCores {
		general.Warningf("fill default share pool with non-reclaim cpus requires disabling dedicated cores overlap reclaimed cores, overriding to true")
		c.DisableDedicatedCoresOverlapReclaimedCores = true
	}
}
