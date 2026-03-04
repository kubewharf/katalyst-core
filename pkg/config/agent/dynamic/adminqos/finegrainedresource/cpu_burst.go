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

	// EnableSharedCoresDefaultCPUBurst indicates whether cpu burst should be enabled by default for pods with shared cores.
	// To avoid cpu contention with other pods, CPU burst should only be enabled for shared cores pods when they are the sole shared cores pod running on the node.
	// If set to true, it means that cpu burst should be enabled by default for pods with shared cores (cpu burst value is calculated and set).
	// If set to false, it means that cpu burst should be disabled for pods with shared cores (cpu burst value is set to 0).
	// If set to nil, it means that no operation is done on the cpu burst value.
	// +optional
	EnableSharedCoresDefaultCPUBurst *bool

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
		c.EnableSharedCoresDefaultCPUBurst = config.EnableSharedCoresDefaultCPUBurst

		if config.DefaultCPUBurstPercent != nil {
			c.DefaultCPUBurstPercent = *config.DefaultCPUBurstPercent
		}
	}
}
