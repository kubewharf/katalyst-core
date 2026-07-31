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

import "github.com/kubewharf/katalyst-core/pkg/util/general"

// TargetState is the calculated committed state for an advisor update.
// It never falls back to live state: every getter reads only this candidate
// snapshot so bulkhead and source-pool helpers cannot accidentally mix states.
type TargetState struct {
	PodEntries   PodEntries
	MachineState NUMANodeMap
	NUMAHeadroom map[int]float64

	AllowSharedCoresOverlapReclaimedCores      bool
	DisableDedicatedCoresOverlapReclaimedCores bool
}

func (s *TargetState) GetMachineState() NUMANodeMap {
	if s == nil {
		return nil
	}
	return s.MachineState.Clone()
}

func (s *TargetState) GetNUMAHeadroom() map[int]float64 {
	if s == nil {
		return nil
	}
	return general.DeepCopyIntToFloat64Map(s.NUMAHeadroom)
}

func (s *TargetState) GetPodEntries() PodEntries {
	if s == nil {
		return nil
	}
	return s.PodEntries.Clone()
}

func (s *TargetState) GetAllocationInfo(podUID string, containerName string) *AllocationInfo {
	if s == nil {
		return nil
	}
	if entries, ok := s.PodEntries[podUID]; ok {
		return entries[containerName].Clone()
	}
	return nil
}

func (s *TargetState) GetAllowSharedCoresOverlapReclaimedCores() bool {
	if s == nil {
		return false
	}
	return s.AllowSharedCoresOverlapReclaimedCores
}

func (s *TargetState) GetDisableDedicatedCoresOverlapReclaimedCores() bool {
	if s == nil {
		return false
	}
	return s.DisableDedicatedCoresOverlapReclaimedCores
}
