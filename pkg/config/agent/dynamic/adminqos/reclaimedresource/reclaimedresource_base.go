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
	EnableReclaim                        bool
	DisableReclaimSharePools             []string
	ReservedResourceForReport            v1.ResourceList
	MinReclaimedResourceForReport        v1.ResourceList
	MinIgnoredReclaimedResourceForReport v1.ResourceList
	ReservedResourceForAllocate          v1.ResourceList
	MinReclaimedResourceForAllocate      v1.ResourceList

	*cpuheadroom.CPUHeadroomConfiguration
	*memoryheadroom.MemoryHeadroomConfiguration
}

func NewReclaimedResourceConfiguration() *ReclaimedResourceConfiguration {
	return &ReclaimedResourceConfiguration{
		CPUHeadroomConfiguration:    cpuheadroom.NewCPUHeadroomConfiguration(),
		MemoryHeadroomConfiguration: memoryheadroom.NewMemoryHeadroomConfiguration(),
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
	}

	c.CPUHeadroomConfiguration.ApplyConfiguration(conf)
	c.MemoryHeadroomConfiguration.ApplyConfiguration(conf)
}
