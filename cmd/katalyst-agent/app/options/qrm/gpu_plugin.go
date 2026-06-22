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
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/qrm/gpustrategy"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

type GPUOptions struct {
	PolicyName                                  string
	GPUDeviceNames                              []string
	ShareGPUResourceNames                       []string
	GPUMemoryAllocatablePerGPU                  string
	MilliGPUAllocatablePerGPU                   string
	SkipGPUStateCorruption                      bool
	RDMADeviceNames                             []string
	RequiredDeviceAffinity                      bool
	EnableKubeletCheckpointFallback             bool
	VirtualGPUPrefersSpreading                  bool
	VirtualGPUVisibleDevicesEnvNames            []string
	VirtualGPUUUIDEnvName                       string
	VirtualGPUMemoryWeightEnvName               string
	VirtualGPUComputeWeightEnvName              string
	VirtualGPUTimesliceEnvName                  string
	VirtualGPUComputePolicyEnvName              string
	GPUSelectionResultAnnotationKey             string
	VirtualGPUDisableEnvsInjectionAnnotationKey string
	VirtualGPUTimesliceEnvValue                 int
	VirtualGPUComputePolicyEnvValue             int

	GPUStrategyOptions *gpustrategy.GPUStrategyOptions
}

func NewGPUOptions() *GPUOptions {
	return &GPUOptions{
		PolicyName:                                  "static",
		GPUDeviceNames:                              []string{"nvidia.com/gpu"},
		ShareGPUResourceNames:                       nil,
		GPUMemoryAllocatablePerGPU:                  "100",
		MilliGPUAllocatablePerGPU:                   "1000",
		RDMADeviceNames:                             []string{},
		GPUStrategyOptions:                          gpustrategy.NewGPUStrategyOptions(),
		RequiredDeviceAffinity:                      true,
		EnableKubeletCheckpointFallback:             true,
		VirtualGPUPrefersSpreading:                  false,
		VirtualGPUVisibleDevicesEnvNames:            []string{"NVIDIA_VISIBLE_DEVICES"},
		GPUSelectionResultAnnotationKey:             consts.PodAnnotationGPUSelectionResultKey,
		VirtualGPUDisableEnvsInjectionAnnotationKey: "",
		VirtualGPUTimesliceEnvValue:                 300,
		VirtualGPUComputePolicyEnvValue:             0,
	}
}

func (o *GPUOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("gpu_resource_plugin")

	fs.StringVar(&o.PolicyName, "gpu-resource-plugin-policy",
		o.PolicyName, "The policy gpu resource plugin should use")
	fs.StringSliceVar(&o.GPUDeviceNames, "gpu-resource-names", o.GPUDeviceNames, "The name of the GPU resource")
	fs.StringSliceVar(&o.ShareGPUResourceNames, "share-gpu-resource-names", o.ShareGPUResourceNames,
		"Resource names of GPU devices that participate in ShareGPU decision. "+
			"Only EnableShareGPU calls with resourceName in this list return a non-nil result.")
	fs.StringVar(&o.GPUMemoryAllocatablePerGPU, "gpu-memory-allocatable-per-gpu",
		o.GPUMemoryAllocatablePerGPU, "The total memory allocatable for each GPU, e.g. 100")
	fs.StringVar(&o.MilliGPUAllocatablePerGPU, "gpu-milligpu-allocatable-per-gpu",
		o.MilliGPUAllocatablePerGPU, "The total milliGPU allocatable for each GPU, e.g. 1000")
	fs.BoolVar(&o.SkipGPUStateCorruption, "skip-gpu-state-corruption",
		o.SkipGPUStateCorruption, "skip gpu state corruption, and it will be used after updating state properties")
	fs.StringSliceVar(&o.RDMADeviceNames, "rdma-resource-names", o.RDMADeviceNames, "The name of the RDMA resource")
	fs.BoolVar(&o.RequiredDeviceAffinity, "gpu-required-device-affinity", o.RequiredDeviceAffinity,
		"required device affinity, and when true it will cause pods to admit fail if unable to meet device affinity")
	fs.BoolVar(&o.EnableKubeletCheckpointFallback, "enable-kubelet-checkpoint-fallback", o.EnableKubeletCheckpointFallback,
		"enable fallback to kubelet device plugin checkpoint for device allocation.")
	fs.BoolVar(&o.VirtualGPUPrefersSpreading, "virtual-gpu-prefers-spreading",
		o.VirtualGPUPrefersSpreading, "whether virtual GPU prefers spreading across devices")
	fs.StringSliceVar(&o.VirtualGPUVisibleDevicesEnvNames, "virtual-gpu-visible-devices-env-names",
		o.VirtualGPUVisibleDevicesEnvNames, "The environment variable names for Virtual GPU visible devices")
	fs.StringVar(&o.VirtualGPUUUIDEnvName, "virtual-gpu-uuid-env-name",
		o.VirtualGPUUUIDEnvName, "The environment variable name for the Virtual GPU pod UID")
	fs.StringVar(&o.VirtualGPUMemoryWeightEnvName, "virtual-gpu-memory-weight-env-name",
		o.VirtualGPUMemoryWeightEnvName, "The environment variable name for Virtual GPU memory weight")
	fs.StringVar(&o.VirtualGPUComputeWeightEnvName, "virtual-gpu-compute-weight-env-name",
		o.VirtualGPUComputeWeightEnvName, "The environment variable name for Virtual GPU compute weight")
	fs.StringVar(&o.VirtualGPUTimesliceEnvName, "virtual-gpu-timeslice-env-name",
		o.VirtualGPUTimesliceEnvName, "The environment variable name for Virtual GPU timeslice")
	fs.StringVar(&o.VirtualGPUComputePolicyEnvName, "virtual-gpu-compute-policy-env-name",
		o.VirtualGPUComputePolicyEnvName, "The environment variable name for Virtual GPU compute policy")
	fs.StringVar(&o.GPUSelectionResultAnnotationKey, "gpu-selection-result-annotation-key",
		o.GPUSelectionResultAnnotationKey, "The annotation key for GPU selection result")
	fs.StringVar(&o.VirtualGPUDisableEnvsInjectionAnnotationKey, "virtual-gpu-disable-envs-injection-annotation-key",
		o.VirtualGPUDisableEnvsInjectionAnnotationKey,
		"If this annotation key exists on the resource request and its value is true, skip Virtual GPU env injection")
	fs.IntVar(&o.VirtualGPUTimesliceEnvValue, "virtual-gpu-timeslice-env-value",
		o.VirtualGPUTimesliceEnvValue, "The environment variable value for Virtual GPU timeslice")
	fs.IntVar(&o.VirtualGPUComputePolicyEnvValue, "virtual-gpu-compute-policy-env-value",
		o.VirtualGPUComputePolicyEnvValue, "The environment variable value for Virtual GPU compute policy")
	o.GPUStrategyOptions.AddFlags(fss)
}

func (o *GPUOptions) ApplyTo(conf *qrmconfig.GPUQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.GPUDeviceNames = o.GPUDeviceNames
	conf.ShareGPUResourceNames = o.ShareGPUResourceNames
	gpuMemory, err := resource.ParseQuantity(o.GPUMemoryAllocatablePerGPU)
	if err != nil {
		return err
	}
	conf.GPUMemoryAllocatablePerGPU = gpuMemory
	milliGPU, err := resource.ParseQuantity(o.MilliGPUAllocatablePerGPU)
	if err != nil {
		return err
	}
	conf.MilliGPUAllocatablePerGPU = milliGPU
	conf.SkipGPUStateCorruption = o.SkipGPUStateCorruption
	conf.RDMADeviceNames = o.RDMADeviceNames
	conf.RequiredDeviceAffinity = o.RequiredDeviceAffinity
	conf.EnableKubeletCheckpointFallback = o.EnableKubeletCheckpointFallback
	conf.VirtualGPUPrefersSpreading = o.VirtualGPUPrefersSpreading
	conf.VirtualGPUVisibleDevicesEnvNames = o.VirtualGPUVisibleDevicesEnvNames
	conf.VirtualGPUUUIDEnvName = o.VirtualGPUUUIDEnvName
	conf.VirtualGPUMemoryWeightEnvName = o.VirtualGPUMemoryWeightEnvName
	conf.VirtualGPUComputeWeightEnvName = o.VirtualGPUComputeWeightEnvName
	conf.VirtualGPUTimesliceEnvName = o.VirtualGPUTimesliceEnvName
	conf.VirtualGPUComputePolicyEnvName = o.VirtualGPUComputePolicyEnvName
	conf.VirtualGPUTimesliceEnvValue = o.VirtualGPUTimesliceEnvValue
	conf.VirtualGPUComputePolicyEnvValue = o.VirtualGPUComputePolicyEnvValue
	conf.GPUSelectionResultAnnotationKey = o.GPUSelectionResultAnnotationKey
	conf.VirtualGPUDisableEnvsInjectionAnnotationKey = o.VirtualGPUDisableEnvsInjectionAnnotationKey
	if err := o.GPUStrategyOptions.ApplyTo(conf.GPUStrategyConfig); err != nil {
		return err
	}
	return nil
}
