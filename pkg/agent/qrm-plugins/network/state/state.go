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
	"fmt"
	"strconv"

	info "github.com/google/cadvisor/info/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type AllocationInfo struct {
	commonstate.AllocationMeta `json:",inline"`

	Egress     uint32         `json:"egress"`
	Ingress    uint32         `json:"ingress"`
	Identifier string         `json:"identifier"`
	NSName     string         `json:"ns_name"`
	IfName     string         `json:"if_name"`   // we do not support cross-nic bandwidth
	NumaNodes  machine.CPUSet `json:"numa_node"` // associated numa nodes of the socket connecting to the selected NIC
	NetClassID string         `json:"net_class_id"`
}

type (
	ContainerEntries map[string]*AllocationInfo  // Keyed by container name
	PodEntries       map[string]ContainerEntries // Keyed by pod UID
)

// NICState indicates the status of a NIC, including the capacity/reservation/allocation (in Mbps)
type NICState struct {
	EgressState  BandwidthInfo `json:"egress_state"`
	IngressState BandwidthInfo `json:"ingress_state"`
	PodEntries   PodEntries    `json:"pod_entries"`
}

type BandwidthInfo struct {
	// Per K8s definition: allocatable = capacity - reserved, free = allocatable - allocated
	// All rates are in unit of Mbps

	// Actual line speed of a NIC. E.g. a 25Gbps NIC's max bandwidth is around 23.5Gbps
	// It's configurable. Its value = NIC line speed x configured CapacityRate
	Capacity uint32
	// Reserved bandwidth on this NIC (e.g. for system components or high priority tasks)
	// For the sake of safety, we generally keep an overflow buffer and do not allocate all bandwidth to tasks
	// Thus, both reservations should be set slightly larger than the actual required amount
	SysReservation uint32
	Reservation    uint32
	Allocatable    uint32
	Allocated      uint32
	Free           uint32
}

type NICMap map[string]*NICState // keyed by NIC name i.e. eth0

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
	if ai == nil {
		return nil
	}

	clone := &AllocationInfo{
		AllocationMeta: *ai.AllocationMeta.Clone(),
		Egress:         ai.Egress,
		Ingress:        ai.Ingress,
		Identifier:     ai.Identifier,
		NSName:         ai.NSName,
		IfName:         ai.IfName,
		NumaNodes:      ai.NumaNodes.Clone(),
		NetClassID:     ai.NetClassID,
	}

	return clone
}

func (pe PodEntries) Clone() PodEntries {
	clone := make(PodEntries)
	for podUID, containerEntries := range pe {
		clone[podUID] = make(ContainerEntries)
		for containerName, allocationInfo := range containerEntries {
			clone[podUID][containerName] = allocationInfo.Clone()
		}
	}
	return clone
}

func (ai *AllocationInfo) GetRequestedEgress() (uint32, error) {
	if ai == nil {
		return 0, fmt.Errorf("nil AllocationInfo")
	}

	if ai.Egress > 0 && ai.Annotations[NetBandwidthImplicitAnnotationKey] != "" {
		return 0, fmt.Errorf("ambiguous ai.Egress: %d, %s: %s",
			ai.Egress, NetBandwidthImplicitAnnotationKey, ai.Annotations[NetBandwidthImplicitAnnotationKey])
	} else if ai.Egress > 0 {
		return ai.Egress, nil
	} else if ai.Annotations[NetBandwidthImplicitAnnotationKey] != "" {
		ret, err := strconv.Atoi(ai.Annotations[NetBandwidthImplicitAnnotationKey])
		if err != nil {
			return 0, fmt.Errorf("parse %s: %s failed with error: %v",
				NetBandwidthImplicitAnnotationKey, ai.Annotations[NetBandwidthImplicitAnnotationKey], err)
		}

		return uint32(ret), nil
	}

	return 0, nil
}

func (pe PodEntries) String() string {
	if pe == nil {
		return ""
	}

	contentBytes, err := json.Marshal(pe)
	if err != nil {
		general.LoggerWithPrefix("PodEntries.String", general.LoggingPKGFull).Errorf("marshal PodEntries failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

// GetMainContainerAllocation returns AllocationInfo that belongs
// the main container for this pod
func (pe PodEntries) GetMainContainerAllocation(podUID string) (*AllocationInfo, bool) {
	for _, allocationInfo := range pe[podUID] {
		if allocationInfo.CheckMainContainer() {
			return allocationInfo, true
		}
	}
	return nil, false
}

func (ns *NICState) String() string {
	if ns == nil {
		return ""
	}

	contentBytes, err := json.Marshal(ns)
	if err != nil {
		general.LoggerWithPrefix("NICState.String", general.LoggingPKGFull).Errorf("marshal NICState failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (ns *NICState) Clone() *NICState {
	if ns == nil {
		return nil
	}

	return &NICState{
		EgressState: BandwidthInfo{
			Capacity:       ns.EgressState.Capacity,
			SysReservation: ns.EgressState.SysReservation,
			Reservation:    ns.EgressState.Reservation,
			Allocatable:    ns.EgressState.Allocatable,
			Allocated:      ns.EgressState.Allocated,
			Free:           ns.EgressState.Free,
		},
		IngressState: BandwidthInfo{
			Capacity:       ns.IngressState.Capacity,
			SysReservation: ns.IngressState.SysReservation,
			Reservation:    ns.IngressState.Reservation,
			Allocatable:    ns.IngressState.Allocatable,
			Allocated:      ns.IngressState.Allocated,
			Free:           ns.IngressState.Free,
		},
		PodEntries: ns.PodEntries.Clone(),
	}
}

// SetAllocationInfo adds a new AllocationInfo (for pod/container pairs) into the given NICState
func (ns *NICState) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	if ns == nil {
		return
	}

	if allocationInfo == nil {
		general.LoggerWithPrefix("NICState.SetAllocationInfo", general.LoggingPKGFull).Errorf("passed allocationInfo is nil")
		return
	}

	if ns.PodEntries == nil {
		ns.PodEntries = make(PodEntries)
	}

	if _, ok := ns.PodEntries[podUID]; !ok {
		ns.PodEntries[podUID] = make(ContainerEntries)
	}

	ns.PodEntries[podUID][containerName] = allocationInfo.Clone()
}

func (nm NICMap) Clone() NICMap {
	clone := make(NICMap)
	for ifname, ns := range nm {
		clone[ifname] = ns.Clone()
	}
	return clone
}

// EgressBandwidthPerNIC is a helper function to parse egress bandwidth per NIC
func (nm NICMap) EgressBandwidthPerNIC() (uint32, error) {
	if len(nm) == 0 {
		return 0, fmt.Errorf("getEgressBandwidthPerNICFromMachineState got nil nicMap")
	}

	for _, nicState := range nm {
		if nicState != nil {
			return nicState.EgressState.Allocatable, nil
		}
	}

	return 0, fmt.Errorf("getEgressBandwidthPerNICFromMachineState doesn't get valid nicState")
}

// IngressBandwidthPerNIC is a helper function to parse egress bandwidth per NIC
func (nm NICMap) IngressBandwidthPerNIC() (uint32, error) {
	if len(nm) == 0 {
		return 0, fmt.Errorf("getIngressBandwidthPerNICFromMachineState got nil nicMap")
	}

	for _, nicState := range nm {
		if nicState != nil {
			return nicState.IngressState.Allocatable, nil
		}
	}

	return 0, fmt.Errorf("getIngressBandwidthPerNICFromMachineState doesn't get valid nicState")
}

func (nm NICMap) String() string {
	if nm == nil {
		return ""
	}

	contentBytes, err := json.Marshal(nm)
	if err != nil {
		general.LoggerWithPrefix("NICMap.String", general.LoggingPKGFull).Errorf("marshal NICMap failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() NICMap
	GetPodEntries() PodEntries
	GetAllocationInfo(podUID, containerName string) *AllocationInfo
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
type writer interface {
	SetMachineState(nicMap NICMap, persist bool)
	SetPodEntries(podEntries PodEntries, persist bool)
	SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo, persist bool)

	Delete(podUID, containerName string, persist bool)
	ClearState()
	StoreState() error
}

// ReadonlyState interface only provides methods for tracking pod assignments
type ReadonlyState interface {
	reader

	GetMachineInfo() *info.MachineInfo
	GetEnabledNICs() []machine.InterfaceInfo
	GetReservedBandwidth() map[string]uint32
}

// State interface provides methods for tracking and setting pod assignments
type State interface {
	writer
	ReadonlyState
}
