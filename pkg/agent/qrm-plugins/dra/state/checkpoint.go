/*
Copyright 2026 The Katalyst Authors.

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
	"path/filepath"
	"sync"

	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/util/customcheckpointmanager"
	utilstate "github.com/kubewharf/katalyst-core/pkg/util/state"
)

const (
	checkpointVersion = 1
	checkpointName    = "compute_domain_dra_state"
	pluginName        = "compute_domain_dra"
)

type DRAPluginCheckpoint struct {
	Version        int                      `json:"version"`
	PreparedClaims map[string]PreparedClaim `json:"preparedClaims"`
	Checksum       checksum.Checksum        `json:"checksum"`
}

func NewDRAPluginCheckpoint() *DRAPluginCheckpoint {
	return &DRAPluginCheckpoint{
		Version:        checkpointVersion,
		PreparedClaims: make(map[string]PreparedClaim),
	}
}

func (cp *DRAPluginCheckpoint) MarshalCheckpoint() ([]byte, error) {
	cp.Checksum = 0
	cp.Checksum = checksum.New(cp)
	return json.Marshal(cp)
}

func (cp *DRAPluginCheckpoint) UnmarshalCheckpoint(data []byte) error {
	return json.Unmarshal(data, cp)
}

func (cp *DRAPluginCheckpoint) VerifyChecksum() error {
	saved := cp.Checksum
	cp.Checksum = 0
	err := saved.Verify(cp)
	cp.Checksum = saved
	return err
}

type stateCheckpoint struct {
	sync.RWMutex
	cache             State
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
}

func NewCheckpointState(conf *statedirectory.StateDirectoryConfiguration) (State, error) {
	if conf == nil {
		return nil, fmt.Errorf("state directory configuration is required")
	}
	currentDir, previousDir := conf.GetCurrentAndPreviousStateFileDirectory()

	checkpointState := &stateCheckpoint{
		cache:          NewMemoryState(),
		checkpointName: checkpointName,
	}
	manager, err := customcheckpointmanager.NewCustomCheckpointManager(
		currentDir,
		previousDir,
		checkpointName,
		pluginName,
		checkpointState,
		false,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"initialize DRA checkpoint state %q: %w",
			filepath.Join(currentDir, checkpointName),
			err,
		)
	}
	checkpointState.checkpointManager = manager
	return checkpointState, nil
}

func (s *stateCheckpoint) InitNewCheckpoint(empty bool) checkpointmanager.Checkpoint {
	checkpoint := NewDRAPluginCheckpoint()
	if !empty {
		checkpoint.PreparedClaims = s.cache.ListPreparedClaims()
	}
	return checkpoint
}

func (s *stateCheckpoint) RestoreState(cp checkpointmanager.Checkpoint) (bool, error) {
	checkpoint, ok := cp.(*DRAPluginCheckpoint)
	if !ok {
		return false, fmt.Errorf("expected *DRAPluginCheckpoint, got %T", cp)
	}
	if checkpoint.Version != checkpointVersion {
		return false, fmt.Errorf(
			"unsupported DRA checkpoint version %d",
			checkpoint.Version,
		)
	}
	if err := s.cache.SetPreparedClaims(checkpoint.PreparedClaims, false); err != nil {
		return false, err
	}
	return false, nil
}

func (s *stateCheckpoint) GetPreparedClaim(uid string) (PreparedClaim, bool) {
	s.RLock()
	defer s.RUnlock()
	return s.cache.GetPreparedClaim(uid)
}

func (s *stateCheckpoint) ListPreparedClaims() map[string]PreparedClaim {
	s.RLock()
	defer s.RUnlock()
	return s.cache.ListPreparedClaims()
}

func (s *stateCheckpoint) SetPreparedClaims(
	claims map[string]PreparedClaim,
	persist bool,
) error {
	return s.mutate(persist, func() error {
		return s.cache.SetPreparedClaims(claims, false)
	})
}

func (s *stateCheckpoint) SetPreparedClaim(claim PreparedClaim, persist bool) error {
	return s.mutate(persist, func() error {
		return s.cache.SetPreparedClaim(claim, false)
	})
}

func (s *stateCheckpoint) DeletePreparedClaim(uid string, persist bool) error {
	return s.mutate(persist, func() error {
		return s.cache.DeletePreparedClaim(uid, false)
	})
}

func (s *stateCheckpoint) mutate(persist bool, mutation func() error) error {
	s.Lock()
	defer s.Unlock()

	if err := mutation(); err != nil {
		return err
	}
	if persist {
		return s.storeState()
	}
	return nil
}

func (s *stateCheckpoint) StoreState() error {
	s.Lock()
	defer s.Unlock()
	return s.storeState()
}

func (s *stateCheckpoint) storeState() error {
	return s.checkpointManager.CreateCheckpoint(
		s.checkpointName,
		s.InitNewCheckpoint(false),
	)
}

var _ checkpointmanager.Checkpoint = (*DRAPluginCheckpoint)(nil)
var _ State = (*stateCheckpoint)(nil)
var _ utilstate.Storable = (*stateCheckpoint)(nil)
