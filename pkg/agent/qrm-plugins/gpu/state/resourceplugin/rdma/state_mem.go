package rdma

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type rdmaPluginState struct {
	sync.RWMutex

	topologyRegistry *machine.DeviceTopologyRegistry

	machineState RDMAMap
	podEntries   PodEntries
}

func NewRDMAPluginState(topologyRegistry *machine.DeviceTopologyRegistry) (State, error) {
	generalLog.InfoS("initializing new rdma plugin in-memory state store")

	defaultMachineState, err := GenerateMachineState(topologyRegistry)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %w", err)
	}

	return &rdmaPluginState{
		machineState:     defaultMachineState,
		topologyRegistry: topologyRegistry,
		podEntries:       make(PodEntries),
	}, nil
}

func (s *rdmaPluginState) SetMachineState(rdmaMap RDMAMap, _ bool) {
	s.Lock()
	defer s.Unlock()
	s.machineState = rdmaMap.Clone()
	generalLog.InfoS("updated rdma plugin machine state",
		"rdmaMap", rdmaMap)
}

func (s *rdmaPluginState) SetPodEntries(podEntries PodEntries, _ bool) {
	s.Lock()
	defer s.Unlock()
	s.podEntries = podEntries
}

func (s *rdmaPluginState) SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo, _ bool) {
	s.Lock()
	defer s.Unlock()

	s.podEntries.SetAllocationInfo(podUID, containerName, allocationInfo)
	generalLog.InfoS("updated rdma plugin pod entries",
		"podUID", podUID,
		"containerName", containerName,
		"allocationInfo", allocationInfo.String())
}

func (s *rdmaPluginState) Delete(podUID, containerName string, _ bool) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podEntries[podUID]; !ok {
		return
	}

	delete(s.podEntries[podUID], containerName)
	if len(s.podEntries[podUID]) == 0 {
		delete(s.podEntries, podUID)
	}
	generalLog.InfoS("deleted container entry", "podUID", podUID, "containerName", containerName)
}

func (s *rdmaPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	machineState, err := GenerateMachineState(s.topologyRegistry)
	if err != nil {
		generalLog.ErrorS(err, "failed to generate machine state")
	}
	s.machineState = machineState
	s.podEntries = make(PodEntries)

	generalLog.InfoS("cleared state")
}

func (s *rdmaPluginState) StoreState() error {
	// nothing to do
	return nil
}

func (s *rdmaPluginState) GetMachineState() RDMAMap {
	s.RLock()
	defer s.RUnlock()

	return s.machineState.Clone()
}

func (s *rdmaPluginState) GetPodEntries() PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.Clone()
}

func (s *rdmaPluginState) GetAllocationInfo(podUID, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.GetAllocationInfo(podUID, containerName)
}
