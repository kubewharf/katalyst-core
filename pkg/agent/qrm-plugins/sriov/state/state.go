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

	info "github.com/google/cadvisor/info/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type PCIDevice struct {
	Address string `json:"address"`
	RepName string `json:"repName"`
	VfName  string `json:"vfName"`
}

type AllocationInfo struct {
	commonstate.AllocationMeta `json:",inline"`

	PCIDevice    PCIDevice               `json:"pciDevice"`
	MountDevices []*pluginapi.DeviceSpec `json:"mountDevices"`
}

func (ai *AllocationInfo) String() string {
	if ai == nil {
		return ""
	}

	contentBytes, err := json.Marshal(ai)
	if err != nil {
		general.LoggerWithPrefix("AllocationInfo.String", general.LoggingPKGFull).Errorf("marshal AllocationInfo failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (ai *AllocationInfo) Clone() *AllocationInfo {
	clone := &AllocationInfo{
		AllocationMeta: *ai.AllocationMeta.Clone(),
		PCIDevice: PCIDevice{
			Address: ai.PCIDevice.Address,
			RepName: ai.PCIDevice.RepName,
			VfName:  ai.PCIDevice.VfName,
		},
		MountDevices: make([]*pluginapi.DeviceSpec, 0, len(ai.MountDevices)),
	}

	for _, mountDevice := range ai.MountDevices {
		clone.MountDevices = append(clone.MountDevices, &pluginapi.DeviceSpec{
			ContainerPath: mountDevice.ContainerPath,
			HostPath:      mountDevice.HostPath,
			Permissions:   mountDevice.Permissions,
		})
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

func (pe *PodEntries) FilterByAllocated(expected bool) VfFilter {
	return func(info machine.VFInterfaceInfo) bool {
		allocated := false
		for _, containerEntries := range *pe {
			for _, allocationInfo := range containerEntries {
				if info.Name == allocationInfo.PCIDevice.VfName {
					allocated = true
					break
				}
			}
		}

		return allocated == expected
	}
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() VfState
	GetPodEntries() PodEntries
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
// todo: optimize me according to actual needs
type writer interface {
	SetMachineState(state VfState, persist bool)
	SetPodEntries(podEntries PodEntries, persist bool)

	ClearState()
	StoreState() error
}

// ReadonlyState interface only provides methods for tracking pod assignments
type ReadonlyState interface {
	reader

	GetMachineInfo() *info.MachineInfo
}

// State interface provides methods for tracking and setting pod assignments
type State interface {
	writer
	ReadonlyState
}
