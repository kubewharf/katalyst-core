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
	"encoding/json"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type AllocationInfo struct {
	commonstate.AllocationMeta `json:",inline"`

	VFInfo VFInfo `json:"vfInfo"`
}

func (ai *AllocationInfo) String() string {
	if ai == nil {
		return ""
	}

	contentBytes, err := json.Marshal(ai)
	if err != nil {
		general.LoggerWithPrefix("AllocationInfo.String", general.LoggingPKGFull).
			Errorf("marshal AllocationInfo failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (ai *AllocationInfo) Clone() *AllocationInfo {
	clone := &AllocationInfo{
		AllocationMeta: *ai.AllocationMeta.Clone(),
		VFInfo:         VFInfo{},
	}

	return clone
}

type (
	ContainerEntries map[string]*AllocationInfo  // keyed by container name
	PodEntries       map[string]ContainerEntries // Keyed by pod UID
)

func (pe *PodEntries) Clone() PodEntries {
	clone := make(PodEntries)
	for podUID, containerEntries := range *pe {
		clone[podUID] = make(ContainerEntries)
		for containerName, allocationInfo := range containerEntries {
			clone[podUID][containerName] = allocationInfo.Clone()
		}
	}
	return clone
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() VFState
	GetPodEntries() PodEntries
	GetAllocationInfo(podUID, containerName string) *AllocationInfo
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
// todo: optimize me according to actual needs
type writer interface {
	SetMachineState(state VFState, persist bool)
	SetPodEntries(podEntries PodEntries, persist bool)
	SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo, persist bool)
	Delete(podUID string, persist bool)

	ClearState()
	StoreState() error
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
