package rdma

import (
	"fmt"
	"path"
	"reflect"
	"sync"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/pkg/errors"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	cmerrors "k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
)

const (
	metricMetaCacheStoreStateDuration = "metacache_store_state_duration"
)

var (
	_          State = &stateCheckpoint{}
	generalLog       = general.LoggerWithPrefix("rdma_plugin", general.LoggingPKGFull)
)

type stateCheckpoint struct {
	sync.RWMutex
	cache             State
	policyName        string
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
	// when we add new properties to checkpoint,
	// it will cause checkpoint corruption and we should skip it
	skipStateCorruption bool
	emitter             metrics.MetricEmitter
}

func NewCheckpointState(
	stateDir, checkpointName, policyName string,
	topologyRegistry *machine.DeviceTopologyRegistry, skipStateCorruption bool, emitter metrics.MetricEmitter,
) (State, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	defaultCache, err := NewRDMAPluginState(topologyRegistry)
	if err != nil {
		return nil, fmt.Errorf("NewRDMAPluginState failed with error: %v", err)
	}

	sc := &stateCheckpoint{
		cache:               defaultCache,
		policyName:          policyName,
		checkpointName:      checkpointName,
		checkpointManager:   checkpointManager,
		skipStateCorruption: skipStateCorruption,
		emitter:             emitter,
	}

	if err := sc.restoreState(topologyRegistry); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v, please drain this node and delete "+
			"the rdma plugin checkpoint file %q before restarting Kubelet",
			err, path.Join(stateDir, checkpointName))
	}

	return sc, nil
}

func (s *stateCheckpoint) SetMachineState(rdmaMap RDMAMap, persist bool) {
	s.Lock()
	defer s.Unlock()

	s.cache.SetMachineState(rdmaMap, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store machineState to checkpoint error")
		}
	}
}

func (s *stateCheckpoint) SetPodEntries(podEntries PodEntries, persist bool) {
	s.Lock()
	defer s.Unlock()

	s.cache.SetPodEntries(podEntries, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store pod entries to checkpoint error", "err")
		}
	}
}

func (s *stateCheckpoint) SetAllocationInfo(
	podUID, containerName string, allocationInfo *AllocationInfo, persist bool,
) {
	s.Lock()
	defer s.Unlock()

	s.cache.SetAllocationInfo(podUID, containerName, allocationInfo, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store allocationInfo to checkpoint error")
		}
	}
}

func (s *stateCheckpoint) storeState() error {
	startTime := time.Now()
	general.InfoS("called")
	defer func() {
		elapsed := time.Since(startTime)
		general.InfoS("finished", "duration", elapsed)
		_ = s.emitter.StoreFloat64(metricMetaCacheStoreStateDuration, float64(elapsed/time.Millisecond), metrics.MetricTypeNameRaw)
	}()
	checkpoint := NewRDMAPluginCheckpoint()
	checkpoint.PolicyName = s.policyName
	checkpoint.MachineState = s.cache.GetMachineState()
	checkpoint.PodEntries = s.cache.GetPodEntries()

	err := s.checkpointManager.CreateCheckpoint(s.checkpointName, checkpoint)
	if err != nil {
		generalLog.ErrorS(err, "could not save checkpoint")
		return err
	}
	return nil
}

func (s *stateCheckpoint) restoreState(topologyRegistry *machine.DeviceTopologyRegistry) error {
	s.Lock()
	defer s.Unlock()
	var err error
	var foundAndSkippedStateCorruption bool

	checkpoint := NewRDMAPluginCheckpoint()
	if err = s.checkpointManager.GetCheckpoint(s.checkpointName, checkpoint); err != nil {
		if errors.Is(err, cmerrors.ErrCheckpointNotFound) {
			return s.storeState()
		} else if errors.Is(err, cmerrors.ErrCorruptCheckpoint) {
			if !s.skipStateCorruption {
				return err
			}

			foundAndSkippedStateCorruption = true
			generalLog.Infof("restore checkpoint failed with err: %s, but we skip it", err)
		} else {
			return err
		}
	}

	if s.policyName != checkpoint.PolicyName && !s.skipStateCorruption {
		return fmt.Errorf("configured policy %q differs from state checkpoint policy %q", s.policyName, checkpoint.PolicyName)
	}

	machineState, err := GenerateMachineStateFromPodEntries(checkpoint.PodEntries, topologyRegistry)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	s.cache.SetMachineState(machineState, false)
	s.cache.SetPodEntries(checkpoint.PodEntries, false)

	if !reflect.DeepEqual(machineState, checkpoint.MachineState) {
		generalLog.Warningf("machine state changed: "+
			"machineState: %s; checkpointMachineState: %s",
			machineState.String(), checkpoint.MachineState.String())

		err = s.storeState()
		if err != nil {
			return fmt.Errorf("storeState when machine state changed failed with error: %v", err)
		}
	}

	if foundAndSkippedStateCorruption {
		generalLog.Infof("found and skipped state corruption, we shoud store to rectify the checksum")

		err = s.storeState()
		if err != nil {
			return fmt.Errorf("storeState failed with error: %v", err)
		}
	}

	generalLog.InfoS("state checkpoint: restored state from checkpoint")

	return nil
}

func (s *stateCheckpoint) Delete(podUID, containerName string, persist bool) {
	s.Lock()
	defer s.Unlock()

	s.cache.Delete(podUID, containerName, persist)
	if persist {
		err := s.storeState()
		if err != nil {
			generalLog.ErrorS(err, "store state after delete operation to checkpoint error")
		}
	}
}

func (s *stateCheckpoint) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.cache.ClearState()
	err := s.storeState()
	if err != nil {
		generalLog.ErrorS(err, "store state after clear operation to checkpoint error")
	}
}

func (s *stateCheckpoint) StoreState() error {
	s.Lock()
	defer s.Unlock()
	return s.storeState()
}

func (s *stateCheckpoint) GetMachineState() RDMAMap {
	s.RLock()
	defer s.RUnlock()

	return s.cache.GetMachineState()
}

func (s *stateCheckpoint) GetPodEntries() PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.cache.GetPodEntries()
}

func (s *stateCheckpoint) GetAllocationInfo(podUID, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	return s.cache.GetAllocationInfo(podUID, containerName)
}
