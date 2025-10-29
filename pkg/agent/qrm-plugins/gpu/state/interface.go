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

package state

import (
	v1 "k8s.io/api/core/v1"
)

// reader is used to get information from local states
type reader interface {
	GetMachineState() AllocationResourcesMap
	GetPodResourceEntries() PodResourceEntries
	GetPodEntries(resourceName v1.ResourceName) PodEntries
	GetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string) *AllocationInfo
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
type writer interface {
	SetMachineState(allocationResourcesMap AllocationResourcesMap, persist bool)
	SetResourceState(resourceName v1.ResourceName, allocationMap AllocationMap, persist bool)
	SetPodResourceEntries(podResourceEntries PodResourceEntries, persist bool)
	SetAllocationInfo(
		resourceName v1.ResourceName, podUID, containerName string, allocationInfo *AllocationInfo, persist bool,
	)

	Delete(resourceName v1.ResourceName, podUID, containerName string, persist bool)
	ClearState()
	StoreState() error
}

// DefaultResourceStateGenerator interface is used to generate default resource state for each resource
type DefaultResourceStateGenerator interface {
	GenerateDefaultResourceState() (AllocationMap, error)
}

// ReadonlyState interface only provides methods for tracking pod assignments
type ReadonlyState interface {
	reader
}

// State interface provides methods for tracking and setting pod assignments
type State interface {
	writer
	ReadonlyState
}
