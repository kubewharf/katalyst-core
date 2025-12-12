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
	"sort"

	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type VFState []machine.VFInterfaceInfo

func (s VFState) Clone() VFState {
	clone := make(VFState, 0, len(s))
	for _, vf := range s {
		clone = append(clone, vf)
	}

	return clone
}

type VFilter func(machine.VFInterfaceInfo) bool

func (s VFState) Filter(filters ...VFilter) VFState {
	filtered := make(VFState, 0)
	for _, v := range s {
		keep := true
		for _, filter := range filters {
			if !filter(v) {
				keep = false
				break
			}
		}
		if keep {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

func (s VFState) SortByIndex() {
	sort.Slice(s, func(i, j int) bool {
		return s[i].IfIndex < s[j].IfIndex
	})
}

func FilterByQueueCount(min int, max int) VFilter {
	return func(v machine.VFInterfaceInfo) bool {
		return v.CombinedCount >= min && v.CombinedCount <= max
	}
}

func FilterByName(name string) VFilter {
	return func(vf machine.VFInterfaceInfo) bool {
		return vf.Name == name
	}
}

func FilterByNumaID(numaSet machine.CPUSet) VFilter {
	return func(vf machine.VFInterfaceInfo) bool {
		return numaSet.Contains(vf.NumaNode)
	}
}

func FilterByIbDevice(supported bool) VFilter {
	return func(vf machine.VFInterfaceInfo) bool {
		return vf.IBDevName != "" == supported
	}
}

var _ checkpointmanager.Checkpoint = &SriovPluginCheckpoint{}

type SriovPluginCheckpoint struct {
	PolicyName   string            `json:"policyName"`
	MachineState VFState           `json:"machineState"`
	PodEntries   PodEntries        `json:"pod_entries"`
	Checksum     checksum.Checksum `json:"checksum"`
}

func NewSriovPluginCheckpoint() *SriovPluginCheckpoint {
	return &SriovPluginCheckpoint{}
}

// MarshalCheckpoint returns marshaled checkpoint
func (cp *SriovPluginCheckpoint) MarshalCheckpoint() ([]byte, error) {
	// make sure checksum wasn't set before, so it doesn't affect output checksum
	cp.Checksum = 0
	cp.Checksum = checksum.New(cp)
	return json.Marshal(*cp)
}

// UnmarshalCheckpoint tries to unmarshal passed bytes to checkpoint
func (cp *SriovPluginCheckpoint) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, cp)
}

// VerifyChecksum verifies that current checksum of checkpoint is valid
func (cp *SriovPluginCheckpoint) VerifyChecksum() error {
	ck := cp.Checksum
	cp.Checksum = 0
	err := ck.Verify(cp)
	cp.Checksum = ck
	return err
}
