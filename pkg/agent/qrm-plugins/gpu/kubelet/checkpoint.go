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

package kubelet

import (
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	cpmerrors "k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// Allocation represents a GPU allocation in the kubelet device manager.
type Allocation struct {
	PodUID        string
	ContainerName string
	ResourceName  string
	DeviceIDs     []string
}

// ReadAllocations reads allocations from the kubelet device manager checkpoint file.
// This is to make sure that we are aware of the existing allocations in the kubelet device manager.
func ReadAllocations(
	manager checkpointmanager.CheckpointManager,
	gpuDeviceNames sets.String,
) ([]Allocation, error) {
	checkpointData, err := native.GetKubeletCheckpoint(manager)
	if err != nil {
		if errors.Is(err, cpmerrors.ErrCheckpointNotFound) {
			return []Allocation{}, nil
		}
		return nil, fmt.Errorf("failed to get kubelet GPU checkpoint: %w", err)
	}

	entries, _ := checkpointData.GetDataInLatestFormat()
	allocations := make([]Allocation, 0, len(entries))
	for _, entry := range entries {
		if !gpuDeviceNames.Has(entry.ResourceName) {
			continue
		}

		deviceSet := sets.NewString()
		for _, deviceIDs := range entry.DeviceIDs {
			deviceSet.Insert(deviceIDs...)
		}
		allocations = append(allocations, Allocation{
			PodUID:        entry.PodUID,
			ContainerName: entry.ContainerName,
			ResourceName:  entry.ResourceName,
			DeviceIDs:     deviceSet.List(),
		})
	}

	return allocations, nil
}
