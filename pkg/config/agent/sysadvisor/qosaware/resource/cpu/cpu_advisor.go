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

package cpu

import (
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/headroom"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/provision"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/region"
)

// CPUAdvisorConfiguration stores configurations of cpu advisors in qos aware plugin
type CPUAdvisorConfiguration struct {
	ProvisionPolicies  map[v1alpha1.QoSRegionType][]types.CPUProvisionPolicyName
	HeadroomPolicies   map[v1alpha1.QoSRegionType][]types.CPUHeadroomPolicyName
	ProvisionAssembler types.CPUProvisionAssemblerName
	HeadroomAssembler  types.CPUHeadroomAssemblerName

	*headroom.CPUHeadroomPolicyConfiguration
	*provision.CPUProvisionPolicyConfiguration
	*region.CPURegionConfiguration
	*CPUIsolationConfiguration
}

// NewCPUAdvisorConfiguration creates new cpu advisor configurations
func NewCPUAdvisorConfiguration() *CPUAdvisorConfiguration {
	return &CPUAdvisorConfiguration{
		ProvisionPolicies:               map[v1alpha1.QoSRegionType][]types.CPUProvisionPolicyName{},
		HeadroomPolicies:                map[v1alpha1.QoSRegionType][]types.CPUHeadroomPolicyName{},
		ProvisionAssembler:              types.CPUProvisionAssemblerCommon,
		HeadroomAssembler:               types.CPUHeadroomAssemblerCommon,
		CPUHeadroomPolicyConfiguration:  headroom.NewCPUHeadroomPolicyConfiguration(),
		CPUProvisionPolicyConfiguration: provision.NewCPUProvisionPolicyConfiguration(),
		CPURegionConfiguration:          region.NewCPURegionConfiguration(),
		CPUIsolationConfiguration:       NewCPUIsolationConfiguration(),
	}
}
