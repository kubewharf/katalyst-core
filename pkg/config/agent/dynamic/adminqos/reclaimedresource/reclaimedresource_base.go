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
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/cpuheadroom"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/memoryheadroom"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type ReclaimedResourceConfiguration struct {
	EnableReclaim                   bool
	ReservedResourceForReport       v1.ResourceList
	MinReclaimedResourceForReport   v1.ResourceList
	ReservedResourceForAllocate     v1.ResourceList
	MinReclaimedResourceForAllocate v1.ResourceList
	MaxNodeUtilizationPercent       map[v1.ResourceName]int64

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

		if config.MaxNodeUtilizationPercent != nil {
			for resourceName, value := range config.MaxNodeUtilizationPercent {
				c.MaxNodeUtilizationPercent[resourceName] = value
			}
		}
	}

	c.CPUHeadroomConfiguration.ApplyConfiguration(conf)
	c.MemoryHeadroomConfiguration.ApplyConfiguration(conf)
}

func (c *ReclaimedResourceConfiguration) GetReservedResourceForAllocate(nodeResourceList v1.ResourceList) v1.ResourceList {
	if len(c.MaxNodeUtilizationPercent) == 0 {
		return c.ReservedResourceForAllocate
	}

	res := v1.ResourceList{}
	for resource, quantity := range c.ReservedResourceForAllocate {
		nodeAllocatable, ok := nodeResourceList[resource]
		if !ok {
			res[resource] = quantity
			continue
		}

		maxUtil, ok := c.MaxNodeUtilizationPercent[resource]
		if !ok {
			res[resource] = quantity
			continue
		}
		if maxUtil <= 0 || maxUtil > 100 {
			klog.Warningf("unsupported MaxNodeUtilizationPercent, resourceName: %v, value: %v", resource, maxUtil)
			res[resource] = quantity
			continue
		}

		nodeAllocatableCopy := nodeAllocatable.DeepCopy()
		nodeAllocatableCopy.Sub(native.MultiplyResourceQuantity(resource, nodeAllocatableCopy, float64(maxUtil)/100.0))
		res[resource] = nodeAllocatableCopy

		klog.V(6).Infof("GetReservedResourceForAllocate resource: %v, nodeResource: %v, "+
			"MaxNodeUtilizationPercent: %v, ReservedResourceForAllocate: %v, res: %v",
			resource, nodeAllocatable.String(), maxUtil, quantity.String(), nodeAllocatableCopy.String())
	}

	return res
}
