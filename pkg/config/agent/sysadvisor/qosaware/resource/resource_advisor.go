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

package resource

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

// ResourceAdvisorConfiguration stores configurations of resource advisors in qos aware plugin
type ResourceAdvisorConfiguration struct {
	ResourceAdvisors []string

	*cpu.CPUAdvisorConfiguration
	*memory.MemoryAdvisorConfiguration
}

// NewResourceAdvisorConfiguration creates new resource advisor configurations
func NewResourceAdvisorConfiguration() *ResourceAdvisorConfiguration {
	return &ResourceAdvisorConfiguration{
		ResourceAdvisors:           []string{},
		CPUAdvisorConfiguration:    cpu.NewCPUAdvisorConfiguration(),
		MemoryAdvisorConfiguration: memory.NewMemoryAdvisorConfiguration(),
	}
}

// ApplyConfiguration is used to set configuration based on conf.
func (c *ResourceAdvisorConfiguration) ApplyConfiguration(defaultConf *ResourceAdvisorConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.CPUAdvisorConfiguration.ApplyConfiguration(defaultConf.CPUAdvisorConfiguration, conf)
	c.MemoryAdvisorConfiguration.ApplyConfiguration(defaultConf.MemoryAdvisorConfiguration, conf)
}
