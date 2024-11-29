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

package finegrainedresource

import (
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

const (
	defaultCPUBurstPercent = 100
)

type CPUBurstConfiguration struct {
	// CPUBurstPolicy indicates which policy to enable the cpu burst feature
	CPUBurstPolicy string

	// CPUBurstPercent identifies the upper limit of the allowed burst percent
	CPUBurstPercent int64
}

func NewCPUBurstConfiguration() *CPUBurstConfiguration {
	return &CPUBurstConfiguration{
		CPUBurstPolicy:  consts.PodAnnotationCPUEnhancementCPUBurstPolicyNone,
		CPUBurstPercent: defaultCPUBurstPercent,
	}
}

func (c *CPUBurstConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.FineGrainedResourceConfig != nil && aqc.Spec.Config.FineGrainedResourceConfig.CPUBurstConfig != nil {
		config := aqc.Spec.Config.FineGrainedResourceConfig.CPUBurstConfig
		if config.CPUBurstPolicy != "" {
			c.CPUBurstPolicy = config.CPUBurstPolicy
		}

		if config.CPUBurstPercent != nil {
			c.CPUBurstPercent = *config.CPUBurstPercent
		}
	}
}
