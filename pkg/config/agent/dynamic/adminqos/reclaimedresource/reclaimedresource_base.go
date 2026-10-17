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

package reclaimedresource

import (
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/cpuheadroom"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/memoryheadroom"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type ReclaimedResourceConfiguration struct {
	EnableReclaim                                     bool
	DisableReclaimSharePools                          []string
	DisableReclaimPinnedCPUSetResourcePackageSelector string
	ReservedResourceForReport                         v1.ResourceList
	MinReclaimedResourceForReport                     v1.ResourceList
	MinIgnoredReclaimedResourceForReport              v1.ResourceList
	ReservedResourceForAllocate                       v1.ResourceList
	MinReclaimedResourceForAllocate                   v1.ResourceList
	NumaMinReclaimedResourceRatioForAllocate          v1.ResourceList
	NumaMinReclaimedResourceForAllocate               v1.ResourceList

	// ReclaimedPercentageByConsumer maps a reclaim-consumer name to the
	// whole-percent share of the total reclaimed resource that
	// a reclaimed consumer owns. Missing keys default to 0.
	ReclaimedPercentageByConsumer map[string]int

	*cpuheadroom.CPUHeadroomConfiguration
	*memoryheadroom.MemoryHeadroomConfiguration
}

func NewReclaimedResourceConfiguration() *ReclaimedResourceConfiguration {
	return &ReclaimedResourceConfiguration{
		CPUHeadroomConfiguration:      cpuheadroom.NewCPUHeadroomConfiguration(),
		MemoryHeadroomConfiguration:   memoryheadroom.NewMemoryHeadroomConfiguration(),
		ReclaimedPercentageByConsumer: map[string]int{},
	}
}

func (c *ReclaimedResourceConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.ReclaimedResourceConfig != nil {
		config := aqc.Spec.Config.ReclaimedResourceConfig
		if config.EnableReclaim != nil {
			c.EnableReclaim = *config.EnableReclaim
		}

		if config.DisableReclaimSharePools != nil {
			c.DisableReclaimSharePools = config.DisableReclaimSharePools
		}

		if config.ReservedResourceForReport != nil {
			for resourceName, value := range *config.ReservedResourceForReport {
				c.ReservedResourceForReport[resourceName] = value
			}
		}

		if config.MinReclaimedResourceForReport != nil {
			for resourceName, value := range *config.MinReclaimedResourceForReport {
				c.MinReclaimedResourceForReport[resourceName] = value
			}
		}

		if config.MinIgnoredReclaimedResourceForReport != nil {
			for resourceName, value := range *config.MinIgnoredReclaimedResourceForReport {
				c.MinIgnoredReclaimedResourceForReport[resourceName] = value
			}
		}

		if config.ReservedResourceForAllocate != nil {
			for resourceName, value := range *config.ReservedResourceForAllocate {
				c.ReservedResourceForAllocate[resourceName] = value
			}
		}

		if config.MinReclaimedResourceForAllocate != nil {
			for resourceName, value := range *config.MinReclaimedResourceForAllocate {
				c.MinReclaimedResourceForAllocate[resourceName] = value
			}
		}

		if config.NumaMinReclaimedResourceRatioForAllocate != nil {
			for resourceName, value := range *config.NumaMinReclaimedResourceRatioForAllocate {
				c.NumaMinReclaimedResourceRatioForAllocate[resourceName] = value
			}
		}
		if config.NumaMinReclaimedResourceForAllocate != nil {
			for resourceName, value := range *config.NumaMinReclaimedResourceForAllocate {
				c.NumaMinReclaimedResourceForAllocate[resourceName] = value
			}
		}

		if config.ReclaimedConsumerToReclaimedResourcePercentage != nil {
			percentages := make(map[string]int, len(*config.ReclaimedConsumerToReclaimedResourcePercentage))
			for consumer, percentage := range *config.ReclaimedConsumerToReclaimedResourcePercentage {
				percentages[consumer] = percentage
			}
			c.ReclaimedPercentageByConsumer = percentages
		}
	}

	c.CPUHeadroomConfiguration.ApplyConfiguration(conf)
	c.MemoryHeadroomConfiguration.ApplyConfiguration(conf)
}
