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
	PolicyName                    string
	GPUDeviceNames                []string
	GPUMemoryAllocatablePerGPU    string
	MilliGPUAllocatablePerGPU     string
	SkipGPUStateCorruption        bool
	RDMADeviceNames               []string
	RequiredDeviceAffinity        bool
	EnableKubeletCheckpointFallback bool
	FractionalGPUPrefersSpreading bool
	VMemWeightEnvKey              string
	MilliGPUComputeWeightEnvKey   string
	MilliGPUTimesliceEnvKey       string
	MilliGPUComputePolicyEnvKey   string
	GPUSelectionResultKey         string
	MilliGPUTimesliceEnvValue     int
	MilliGPUComputePolicyEnvValue int

	GPUStrategyOptions *gpustrategy.GPUStrategyOptions
}

func NewGPUOptions() *GPUOptions {
	return &GPUOptions{
		PolicyName:                    "static",
		GPUDeviceNames:                []string{"nvidia.com/gpu"},
		GPUMemoryAllocatablePerGPU:    "100",
		MilliGPUAllocatablePerGPU:     "1000",
		RDMADeviceNames:               []string{},
		GPUStrategyOptions:            gpustrategy.NewGPUStrategyOptions(),
		RequiredDeviceAffinity:        true,
		EnableKubeletCheckpointFallback: true,
		FractionalGPUPrefersSpreading: false,
		GPUSelectionResultKey:         consts.PodAnnotationGPUSelectionResultKey,
		MilliGPUTimesliceEnvValue:     300,
		MilliGPUComputePolicyEnvValue: 0,
	}
}

func (o *GPUOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("gpu_resource_plugin")

	fs.StringVar(&o.PolicyName, "gpu-resource-plugin-policy",
		o.PolicyName, "The policy gpu resource plugin should use")
	fs.StringSliceVar(&o.GPUDeviceNames, "gpu-resource-names", o.GPUDeviceNames, "The name of the GPU resource")
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
	fs.BoolVar(&o.FractionalGPUPrefersSpreading, "fractional-gpu-prefers-spreading",
		o.FractionalGPUPrefersSpreading, "whether fractional GPU prefers spreading across devices")
	fs.StringVar(&o.VMemWeightEnvKey, "gpu-vmem-weight-env-key",
		o.VMemWeightEnvKey, "The environment variable key for GPU memory weight")
	fs.StringVar(&o.MilliGPUComputeWeightEnvKey, "gpu-milligpu-compute-weight-env-key",
		o.MilliGPUComputeWeightEnvKey, "The environment variable key for MilliGPU compute weight")
	fs.StringVar(&o.MilliGPUTimesliceEnvKey, "gpu-milligpu-timeslice-env-key",
		o.MilliGPUTimesliceEnvKey, "The environment variable key for MilliGPU timeslice")
	fs.StringVar(&o.MilliGPUComputePolicyEnvKey, "gpu-milligpu-compute-policy-env-key",
		o.MilliGPUComputePolicyEnvKey, "The environment variable key for MilliGPU compute policy")
	fs.StringVar(&o.GPUSelectionResultKey, "gpu-selection-result-key",
		o.GPUSelectionResultKey, "The annotation key for GPU selection result")
	fs.IntVar(&o.MilliGPUTimesliceEnvValue, "gpu-milligpu-timeslice-env-value",
		o.MilliGPUTimesliceEnvValue, "The environment variable value for MilliGPU timeslice")
	fs.IntVar(&o.MilliGPUComputePolicyEnvValue, "gpu-milligpu-compute-policy-env-value",
		o.MilliGPUComputePolicyEnvValue, "The environment variable value for MilliGPU compute policy")
	o.GPUStrategyOptions.AddFlags(fss)
}

func (o *GPUOptions) ApplyTo(conf *qrmconfig.GPUQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.GPUDeviceNames = o.GPUDeviceNames
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
	conf.FractionalGPUPrefersSpreading = o.FractionalGPUPrefersSpreading
	conf.VMemWeightEnvKey = o.VMemWeightEnvKey
	conf.MilliGPUComputeWeightEnvKey = o.MilliGPUComputeWeightEnvKey
	conf.MilliGPUTimesliceEnvKey = o.MilliGPUTimesliceEnvKey
	conf.MilliGPUComputePolicyEnvKey = o.MilliGPUComputePolicyEnvKey
	conf.GPUSelectionResultKey = o.GPUSelectionResultKey
	conf.MilliGPUTimesliceEnvValue = o.MilliGPUTimesliceEnvValue
	conf.MilliGPUComputePolicyEnvValue = o.MilliGPUComputePolicyEnvValue
	if err := o.GPUStrategyOptions.ApplyTo(conf.GPUStrategyConfig); err != nil {
		return err
	}
	return nil
}
