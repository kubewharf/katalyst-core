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
	"time"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/irqtuner"
)

type CPUQRMPluginConfig struct {
	// PolicyName is used to switch between several strategies
	PolicyName string
	// ReservedCPUCores indicates reserved cpus number for system agents
	ReservedCPUCores int
	// SkipCPUStateCorruption is set to skip cpu state corruption, and it will be used after updating state properties
	SkipCPUStateCorruption bool

	CPUDynamicPolicyConfig
	CPUNativePolicyConfig
}

type CPUDynamicPolicyConfig struct {
	// EnableCPUAdvisor indicates whether to enable sys-advisor module to calculate cpu resources
	EnableCPUAdvisor bool
	// Interval at which we get advice from sys-advisor
	GetAdviceInterval time.Duration
	// EnableCPUPressureEviction indicates whether to enable cpu-pressure eviction, such as cpu load eviction or cpu
	// suppress eviction
	EnableCPUPressureEviction bool
	// LoadPressureEvictionSkipPools indicates the ignored pools when check load pressure.
	LoadPressureEvictionSkipPools []string
	// EnableSyncingCPUIdle is set to sync specific cgroup path with EnableCPUIdle
	EnableSyncingCPUIdle bool
	// EnableCPUIdle indicates whether enabling cpu idle
	EnableCPUIdle bool
	// SharedCoresNUMABindingResultAnnotationKey is the annotation key for storing NUMA binding results of shared_cores QoS pods.
	// It enables schedulers to specify NUMA binding results, and the plugin will make best efforts to follow these results.
	// This key must be included in the pod-annotation-kept-keys configuration.
	SharedCoresNUMABindingResultAnnotationKey string
	// EnableReserveCPUReversely indicates whether to reserve cpu reversely
	EnableReserveCPUReversely bool

	*hintoptimizer.HintOptimizerConfiguration
	*irqtuner.IRQTunerConfiguration
}

type CPUNativePolicyConfig struct {
	// EnableFullPhysicalCPUsOnly is a flag to enable extra allocation restrictions to avoid
	// different containers to possibly end up on the same core.
	EnableFullPhysicalCPUsOnly bool
	// CPUAllocationOption is the allocation option of cpu (packed/distributed).
	CPUAllocationOption string
}

func NewCPUQRMPluginConfig() *CPUQRMPluginConfig {
	return &CPUQRMPluginConfig{
		CPUDynamicPolicyConfig: CPUDynamicPolicyConfig{
			HintOptimizerConfiguration: hintoptimizer.NewHintOptimizerConfiguration(),
			IRQTunerConfiguration:      irqtuner.NewIRQTunerConfiguration(),
		},
		CPUNativePolicyConfig: CPUNativePolicyConfig{},
	}
}
