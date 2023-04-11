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

package qrm

import (
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

type CPUQRMPluginConfig struct {
	// PolicyName is used to switch between several strategies
	PolicyName string
	// EnableSysAdvisor indicates whether to enable sys-advisor module to calculate cpu resources
	EnableSysAdvisor bool
	// ReservedCPUCores indicates reserved cpus number for system agents
	ReservedCPUCores int
	// SkipCPUStateCorruption is set to skip cpu state corruption and it will be used after updating state properties
	SkipCPUStateCorruption bool
	// EnableSyncingCPUIdle is set to sync specific cgroup path with EnableCPUIdle
	EnableSyncingCPUIdle bool
	// EnableCPUIdle indicateds whether enabling cpu idle
	EnableCPUIdle bool
}

func NewCPUQRMPluginConfig() *CPUQRMPluginConfig {
	return &CPUQRMPluginConfig{}
}

func (c *CPUQRMPluginConfig) ApplyConfiguration(*CPUQRMPluginConfig, *dynamic.DynamicConfigCRD) {}
