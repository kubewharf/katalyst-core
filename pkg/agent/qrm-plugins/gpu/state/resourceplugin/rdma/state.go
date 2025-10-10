package rdma

import (
	"encoding/json"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type AllocationInfo struct {
	commonstate.AllocationMeta `json:",inline"`

	AllocatedAllocation      RDMAAllocation            `json:"allocatedAllocation"`
	TopologyAwareAllocations map[string]RDMAAllocation `json:"topologyAwareAllocations"`
}

type RDMAAllocation struct {
	NUMANodes []int `json:"numa_nodes"`
}

func (a *RDMAAllocation) Clone() RDMAAllocation {
	if a == nil {
		return RDMAAllocation{}
	}

	numaNodes := make([]int, len(a.NUMANodes))
	copy(numaNodes, a.NUMANodes)
	return RDMAAllocation{
		NUMANodes: numaNodes,
	}
}

type (
	ContainerEntries map[string]*AllocationInfo  // Keyed by container name
	PodEntries       map[string]ContainerEntries // Keyed by pod name
)

type RDMAState struct {
	PodEntries PodEntries `json:"pod_entries"`
}

type RDMAMap map[string]*RDMAState // RDMAMap keyed by RDMA NIC card ID

func (i *AllocationInfo) String() string {
	if i == nil {
		return ""
	}

	contentBytes, err := json.Marshal(i)

	if err != nil {
		general.LoggerWithPrefix("AllocationInfo.String", general.LoggingPKGFull).Errorf("marshal AllocationInfo failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (i *AllocationInfo) Clone() *AllocationInfo {
	if i == nil {
		return nil
	}

	clone := &AllocationInfo{
		AllocationMeta:      *i.AllocationMeta.Clone(),
		AllocatedAllocation: i.AllocatedAllocation.Clone(),
	}

	if i.TopologyAwareAllocations != nil {
		clone.TopologyAwareAllocations = make(map[string]RDMAAllocation)
		for k, v := range i.TopologyAwareAllocations {
			clone.TopologyAwareAllocations[k] = v.Clone()
		}
	}

	return clone
}

func (e PodEntries) Clone() PodEntries {
	clone := make(PodEntries)
	for podUID, containerEntries := range e {
		clone[podUID] = make(ContainerEntries)
		for containerID, allocationInfo := range containerEntries {
			clone[podUID][containerID] = allocationInfo.Clone()
		}
	}
	return clone
}

func (e PodEntries) GetAllocationInfo(podUID string, containerName string) *AllocationInfo {
	if e == nil {
		return nil
	}

	if containerEntries, ok := e[podUID]; ok {
		if allocationInfo, ok := containerEntries[containerName]; ok {
			return allocationInfo.Clone()
		}
	}
	return nil
}

func (e PodEntries) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	if e == nil {
		return
	}

	if _, ok := e[podUID]; !ok {
		e[podUID] = make(ContainerEntries)
	}
	e[podUID][containerName] = allocationInfo.Clone()
}

func (rs *RDMAState) String() string {
	if rs == nil {
		return ""
	}

	contentBytes, err := json.Marshal(rs)
	if err != nil {
		general.LoggerWithPrefix("RDMAState.String", general.LoggingPKGFull).Errorf("marshal RDMAState failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (rs *RDMAState) Clone() *RDMAState {
	if rs == nil {
		return nil
	}

	return &RDMAState{
		PodEntries: rs.PodEntries.Clone(),
	}
}

func (rs *RDMAState) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	if rs == nil {
		return
	}

	if allocationInfo == nil {
		general.LoggerWithPrefix("RDMAState.SetAllocationInfo", general.LoggingPKGFull).Errorf("passed allocationInfo is nil")
		return
	}

	if rs.PodEntries == nil {
		rs.PodEntries = make(PodEntries)
	}

	if _, ok := rs.PodEntries[podUID]; !ok {
		rs.PodEntries[podUID] = make(ContainerEntries)
	}

	rs.PodEntries[podUID][containerName] = allocationInfo.Clone()
}

func (m RDMAMap) Clone() RDMAMap {
	clone := make(RDMAMap)
	for id, rs := range m {
		clone[id] = rs.Clone()
	}
	return clone
}

func (m RDMAMap) String() string {
	if m == nil {
		return ""
	}

	contentBytes, err := json.Marshal(m)
	if err != nil {
		general.LoggerWithPrefix("RDMAMap.String", general.LoggingPKGFull).Errorf("marshal RDMAMap failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() RDMAMap
	GetPodEntries() PodEntries
	GetAllocationInfo(podUID, containerName string) *AllocationInfo
}

// writer is used to store information from local states,
// and it also provides functionality to maintain the local files
type writer interface {
	SetMachineState(rdmaMap RDMAMap, persist bool)
	SetPodEntries(rdmaMap PodEntries, persist bool)
	SetAllocationInfo(podUid, containerName string, allocationInfo *AllocationInfo, persist bool)

	Delete(podUID, containerName string, persist bool)
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
