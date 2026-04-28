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
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/gpustrategy"
)

type GPUQRMPluginConfig struct {
	// PolicyName is used to switch between several strategies
	PolicyName string
	// GPUDeviceNames is the names of the GPU device
	GPUDeviceNames []string
	// RDMADeviceNames is the names of the RDMA device
	RDMADeviceNames []string
	// GPUMemoryAllocatablePerGPU is the total memory allocatable for each GPU
	GPUMemoryAllocatablePerGPU resource.Quantity
	// MilliGPUAllocatablePerGPU is the total milliGPU allocatable for each GPU
	MilliGPUAllocatablePerGPU resource.Quantity
	// SkipGPUStateCorruption skip gpu state corruption, and it will be used after updating state properties
	SkipGPUStateCorruption bool
	// RequiredDeviceAffinity specifies whether it is required for pods to follow device affinity strictly.
	// If true, pods will fail to admit if they are not able to satisfy device affinity constraints. Set to true by default.
	RequiredDeviceAffinity bool
	// EnableKubeletCheckpointFallback specifies whether to fallback to kubelet device plugin checkpoint for allocation.
	EnableKubeletCheckpointFallback bool
	// VirtualGPUPrefersSpreading indicates whether virtual GPU allocation prefers spreading across devices.
	// If true, it sorts devices by available GPU memory and compute in descending order (spreading).
	// If false, it sorts in ascending order (packing).
	VirtualGPUPrefersSpreading bool
	// VirtualGPUMemoryWeightEnvName is the environment variable name injected into the pod to specify
	// the allocated GPU memory percentage. The format will be "<deviceID>:<percentage>",
	// where the percentage is an integer from 1 to 100.
	VirtualGPUMemoryWeightEnvName string
	// VirtualGPUComputeWeightEnvName is the environment variable name injected into the pod to specify
	// the allocated Virtual GPU compute percentage. The format will be "<deviceID>:<percentage>",
	// where the percentage is an integer from 1 to 100.
	VirtualGPUComputeWeightEnvName string
	// VirtualGPUTimesliceEnvName is the environment variable name injected into the pod to specify
	// the timeslice configuration for Virtual GPU isolation.
	VirtualGPUTimesliceEnvName string
	// VirtualGPUComputePolicyEnvName is the environment variable name injected into the pod to specify
	// the compute policy for Virtual GPU isolation.
	VirtualGPUComputePolicyEnvName string
	// VirtualGPUTimesliceEnvValue is the default value of the VirtualGPUTimesliceEnvName environment variable.
	VirtualGPUTimesliceEnvValue int
	// VirtualGPUComputePolicyEnvValue is the default value of the VirtualGPUComputePolicyEnvName environment variable.
	VirtualGPUComputePolicyEnvValue int
	// GPUSelectionResultAnnotationKey is the pod annotation key used to retrieve the GPU selection result
	// (e.g., a comma-separated list of device IDs) scheduled by the control plane scheduler.
	GPUSelectionResultAnnotationKey string

	*gpustrategy.GPUStrategyConfig
}

func NewGPUQRMPluginConfig() *GPUQRMPluginConfig {
	return &GPUQRMPluginConfig{
		GPUStrategyConfig: gpustrategy.NewGPUStrategyConfig(),
	}
}
