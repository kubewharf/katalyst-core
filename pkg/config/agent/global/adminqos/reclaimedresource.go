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

package adminqos

import (
	"sync"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

type ReclaimedResourceConfiguration struct {
	mutex                         sync.RWMutex
	enableReclaim                 bool
	reservedResourceForReport     v1.ResourceList
	minReclaimedResourceForReport v1.ResourceList
	reservedResourceForAllocate   v1.ResourceList
}

func NewDynamicReclaimedResourceConfiguration() *ReclaimedResourceConfiguration {
	return &ReclaimedResourceConfiguration{}
}

func (c *ReclaimedResourceConfiguration) DeepCopy() interface{} {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	nc := NewDynamicReclaimedResourceConfiguration()
	nc.applyDefault(c)
	return nc
}

func (c *ReclaimedResourceConfiguration) EnableReclaim() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.enableReclaim
}

func (c *ReclaimedResourceConfiguration) SetEnableReclaim(enableReclaim bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.enableReclaim = enableReclaim
}

func (c *ReclaimedResourceConfiguration) ReservedResourceForReport() v1.ResourceList {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.reservedResourceForReport
}

func (c *ReclaimedResourceConfiguration) SetReservedResourceForReport(reservedResourceForReport v1.ResourceList) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.reservedResourceForReport = reservedResourceForReport
}

func (c *ReclaimedResourceConfiguration) MinReclaimedResourceForReport() v1.ResourceList {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.minReclaimedResourceForReport
}

func (c *ReclaimedResourceConfiguration) SetMinReclaimedResourceForReport(minReclaimedResourceForReport v1.ResourceList) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.minReclaimedResourceForReport = minReclaimedResourceForReport
}

func (c *ReclaimedResourceConfiguration) ReservedResourceForAllocate() v1.ResourceList {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.reservedResourceForAllocate
}

func (c *ReclaimedResourceConfiguration) SetReservedResourceForAllocate(reservedResourceForAllocate v1.ResourceList) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.reservedResourceForAllocate = reservedResourceForAllocate
}

func (c *ReclaimedResourceConfiguration) ApplyConfiguration(defaultConf *ReclaimedResourceConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.applyDefault(defaultConf)
	if ac := conf.AdminQoSConfiguration; ac != nil {
		if ac.Spec.Config.ReclaimedResourceConfig.EnableReclaim != nil {
			c.enableReclaim = *ac.Spec.Config.ReclaimedResourceConfig.EnableReclaim
		}

		if reservedResourceForReport := ac.Spec.Config.ReclaimedResourceConfig.ReservedResourceForReport; reservedResourceForReport != nil {
			for resourceName, value := range *reservedResourceForReport {
				c.reservedResourceForReport[resourceName] = value
			}
		}

		if minReclaimedResourceForReport := ac.Spec.Config.ReclaimedResourceConfig.MinReclaimedResourceForReport; minReclaimedResourceForReport != nil {
			for resourceName, value := range *minReclaimedResourceForReport {
				c.minReclaimedResourceForReport[resourceName] = value
			}
		}

		if reservedResourceForAllocate := ac.Spec.Config.ReclaimedResourceConfig.ReservedResourceForAllocate; reservedResourceForAllocate != nil {
			for resourceName, value := range *reservedResourceForAllocate {
				c.reservedResourceForAllocate[resourceName] = value
			}
		}
	}
}

func (c *ReclaimedResourceConfiguration) applyDefault(defaultConf *ReclaimedResourceConfiguration) {
	c.enableReclaim = defaultConf.enableReclaim
	c.reservedResourceForReport = defaultConf.reservedResourceForReport.DeepCopy()
	c.minReclaimedResourceForReport = defaultConf.minReclaimedResourceForReport.DeepCopy()
	c.reservedResourceForAllocate = defaultConf.reservedResourceForAllocate.DeepCopy()
}
