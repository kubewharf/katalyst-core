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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

const (
	defaultCPUBurstPercent = 100
)

type CPUBurstConfiguration struct {
	// EnableDedicatedCoresDefaultCPUBurst indicates whether to enable cpu burst for dedicated cores by default.
	// If set to true, we enable cpu burst for dedicated cores by default (cpu burst value is calculated and set).
	// If set to false, we disable cpu burst for dedicated cores by default (cpu burst value is set to 0).
	// If set to nil, there will be no operation on cpu burst value.
	// It is set to nil by default
	EnableDedicatedCoresDefaultCPUBurst *bool

	// DefaultCPUBurstPercent indicates the default cpu burst percent for dedicated cores
	DefaultCPUBurstPercent int64
}

func NewCPUBurstConfiguration() *CPUBurstConfiguration {
	return &CPUBurstConfiguration{
		DefaultCPUBurstPercent: defaultCPUBurstPercent,
	}
}

func (c *CPUBurstConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.FineGrainedResourceConfig != nil && aqc.Spec.Config.FineGrainedResourceConfig.CPUBurstConfig != nil {
		config := aqc.Spec.Config.FineGrainedResourceConfig.CPUBurstConfig
		c.EnableDedicatedCoresDefaultCPUBurst = config.EnableDedicatedCoresDefaultCPUBurst

		if config.DefaultCPUBurstPercent != nil {
			c.DefaultCPUBurstPercent = *config.DefaultCPUBurstPercent
		}
	}
}
