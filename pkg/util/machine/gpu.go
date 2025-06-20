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

package machine

import (
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type GPUTopologyProvider interface {
	GetGPUTopology() (*GPUTopology, error)
}

type GPUTopology struct {
	GPUs map[string]GPUInfo
}

type GPUInfo struct {
	NUMANode int
}

type kubeletCheckpointGPUTopologyProvider struct {
	resourceNames []string
}

func NewKubeletCheckpointGPUTopologyProvider(resourceNames []string) GPUTopologyProvider {
	return &kubeletCheckpointGPUTopologyProvider{
		resourceNames: resourceNames,
	}
}

func (p *kubeletCheckpointGPUTopologyProvider) GetGPUTopology() (*GPUTopology, error) {
	gpuTopology := &GPUTopology{
		GPUs: make(map[string]GPUInfo),
	}

	kubeletCheckpoint, err := native.GetKubeletCheckpoint()
	if err != nil {
		general.Errorf("Failed to get kubelet checkpoint: %v", err)
		return gpuTopology, nil
	}

	_, registeredDevs := kubeletCheckpoint.GetDataInLatestFormat()
	for _, resourceName := range p.resourceNames {
		gpuDevice, ok := registeredDevs[resourceName]
		if !ok {
			continue
		}

		for _, id := range gpuDevice {
			gpuTopology.GPUs[id] = GPUInfo{
				// TODO: get NUMA node from Kubelet
				NUMANode: 0,
			}
		}
	}

	return gpuTopology, nil
}
