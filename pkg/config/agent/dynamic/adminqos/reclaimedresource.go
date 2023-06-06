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
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type ReclaimedResourceConfiguration struct {
	EnableReclaim                 bool
	ReservedResourceForReport     v1.ResourceList
	MinReclaimedResourceForReport v1.ResourceList
	ReservedResourceForAllocate   v1.ResourceList
}

func NewReclaimedResourceConfiguration() *ReclaimedResourceConfiguration {
	return &ReclaimedResourceConfiguration{}
}

func (c *ReclaimedResourceConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if ac := conf.AdminQoSConfiguration; ac != nil {
		if ac.Spec.Config.ReclaimedResourceConfig.EnableReclaim != nil {
			c.EnableReclaim = *ac.Spec.Config.ReclaimedResourceConfig.EnableReclaim
		}

		if reservedResourceForReport := ac.Spec.Config.ReclaimedResourceConfig.ReservedResourceForReport; reservedResourceForReport != nil {
			for resourceName, value := range *reservedResourceForReport {
				c.ReservedResourceForReport[resourceName] = value
			}
		}

		if minReclaimedResourceForReport := ac.Spec.Config.ReclaimedResourceConfig.MinReclaimedResourceForReport; minReclaimedResourceForReport != nil {
			for resourceName, value := range *minReclaimedResourceForReport {
				c.MinReclaimedResourceForReport[resourceName] = value
			}
		}

		if reservedResourceForAllocate := ac.Spec.Config.ReclaimedResourceConfig.ReservedResourceForAllocate; reservedResourceForAllocate != nil {
			for resourceName, value := range *reservedResourceForAllocate {
				c.ReservedResourceForAllocate[resourceName] = value
			}
		}
	}
}
