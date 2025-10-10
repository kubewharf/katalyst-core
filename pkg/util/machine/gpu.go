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
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type GPUTopologyProvider interface {
	GetGPUTopology() (*GPUTopology, bool, error)
	SetGPUTopology(*GPUTopology) error
}

type GPUTopology struct {
	GPUs map[string]GPUInfo
}

type GPUInfo struct {
	Health   string
	NUMANode []int
}

func (i GPUInfo) GetNUMANode() []int {
	if i.NUMANode == nil {
		return []int{}
	}
	return i.NUMANode
}

type gpuTopologyProviderImpl struct {
	mutex         sync.RWMutex
	resourceNames []string

	gpuTopology       *GPUTopology
	numaTopologyReady bool
}

func NewGPUTopologyProvider(resourceNames []string) GPUTopologyProvider {
	gpuTopology, err := initGPUTopology(resourceNames)
	if err != nil {
		gpuTopology = getEmptyGPUTopology()
		general.Warningf("initGPUTopology failed with error: %v", err)
	} else {
		general.Infof("initGPUTopology success: %v", gpuTopology)
	}

	return &gpuTopologyProviderImpl{
		resourceNames: resourceNames,
		gpuTopology:   gpuTopology,
	}
}

func getEmptyGPUTopology() *GPUTopology {
	return &GPUTopology{
		GPUs: make(map[string]GPUInfo),
	}
}

func (p *gpuTopologyProviderImpl) SetGPUTopology(gpuTopology *GPUTopology) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if gpuTopology == nil {
		return fmt.Errorf("gpuTopology is nil")
	}

	p.gpuTopology = gpuTopology
	p.numaTopologyReady = checkNUMATopologyReady(gpuTopology)
	return nil
}

func (p *gpuTopologyProviderImpl) GetGPUTopology() (*GPUTopology, bool, error) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return p.gpuTopology, p.numaTopologyReady, nil
}

func initGPUTopology(resourceNames []string) (*GPUTopology, error) {
	gpuTopology := getEmptyGPUTopology()

	kubeletCheckpoint, err := native.GetKubeletCheckpoint()
	if err != nil {
		general.Errorf("Failed to get kubelet checkpoint: %v", err)
		return gpuTopology, nil
	}

	_, registeredDevs := kubeletCheckpoint.GetDataInLatestFormat()
	for _, resourceName := range resourceNames {
		gpuDevice, ok := registeredDevs[resourceName]
		if !ok {
			continue
		}

		for _, id := range gpuDevice {
			// get NUMA node from UpdateAllocatableAssociatedDevices
			gpuTopology.GPUs[id] = GPUInfo{}
		}
	}

	return gpuTopology, nil
}

func checkNUMATopologyReady(topology *GPUTopology) bool {
	if topology == nil {
		return false
	}

	for _, gpu := range topology.GPUs {
		if gpu.NUMANode == nil {
			return false
		}
	}
	return true
}
