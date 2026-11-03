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
	"sync"
)

type stateMemory struct {
	sync.RWMutex
	preparedClaims map[string]PreparedClaim
}

func NewMemoryState() State {
	return &stateMemory{
		preparedClaims: make(map[string]PreparedClaim),
	}
}

func (s *stateMemory) GetPreparedClaim(uid string) (PreparedClaim, bool) {
	s.RLock()
	defer s.RUnlock()

	claim, found := s.preparedClaims[uid]
	if !found {
		return PreparedClaim{}, false
	}
	return claim.DeepCopy(), true
}

func (s *stateMemory) ListPreparedClaims() map[string]PreparedClaim {
	s.RLock()
	defer s.RUnlock()
	return deepCopyClaims(s.preparedClaims)
}

func (s *stateMemory) SetPreparedClaims(claims map[string]PreparedClaim, _ bool) error {
	s.Lock()
	defer s.Unlock()
	s.preparedClaims = deepCopyClaims(claims)
	return nil
}

func (s *stateMemory) SetPreparedClaim(claim PreparedClaim, _ bool) error {
	s.Lock()
	defer s.Unlock()
	s.preparedClaims[claim.UID] = claim.DeepCopy()
	return nil
}

func (s *stateMemory) DeletePreparedClaim(uid string, _ bool) error {
	s.Lock()
	defer s.Unlock()
	delete(s.preparedClaims, uid)
	return nil
}

func (s *stateMemory) StoreState() error {
	return nil
}

func (s *stateMemory) SetProviderState(uid string, raw json.RawMessage) error {
	s.Lock()
	defer s.Unlock()
	claim, ok := s.preparedClaims[uid]
	if !ok {
		return fmt.Errorf("provider state has no core claim %s", uid)
	}
	claim.ProviderState = append(json.RawMessage(nil), raw...)
	s.preparedClaims[uid] = claim
	return nil
}

var _ State = (*stateMemory)(nil)
