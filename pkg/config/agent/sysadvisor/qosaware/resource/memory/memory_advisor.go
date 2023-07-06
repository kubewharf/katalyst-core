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

package memory

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory/headroom"
)

// MemoryAdvisorConfiguration stores configurations of memory advisors in qos aware plugin
type MemoryAdvisorConfiguration struct {
	MemoryHeadroomPolicies []types.MemoryHeadroomPolicyName
	*headroom.MemoryHeadroomPolicyConfiguration
	MemoryAdvisorPlugins []types.MemoryAdvisorPluginName
}

// NewMemoryAdvisorConfiguration creates new memory advisor configurations
func NewMemoryAdvisorConfiguration() *MemoryAdvisorConfiguration {
	return &MemoryAdvisorConfiguration{
		MemoryHeadroomPolicies:            make([]types.MemoryHeadroomPolicyName, 0),
		MemoryHeadroomPolicyConfiguration: headroom.NewMemoryHeadroomPolicyConfiguration(),
		MemoryAdvisorPlugins:              make([]types.MemoryAdvisorPluginName, 0),
	}
}
