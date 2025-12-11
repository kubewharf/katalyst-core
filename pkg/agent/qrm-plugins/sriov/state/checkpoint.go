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

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// VfInfo is the information of a single VF
// todo: adjust fields lately
type VfInfo struct {
	Name        string                 `json:"name"`
	RepName     string                 `json:"repName"`
	PfName      string                 `json:"pfName"`
	Index       int                    `json:"index"`
	NetNS       string                 `json:"netNS"`
	NumaID      int                    `json:"numaID"`
	QueueCount  int                    `json:"queueCount"`
	PciAddress  string                 `json:"pciAddress"`
	RdmaDevices []pluginapi.DeviceSpec `json:"rdmaDevices"`
}

type VfState map[string]VfInfo // keyed by vf rep name

func (s VfState) Clone() VfState {
	clone := make(VfState)
	for k, v := range s {
		clone[k] = v
	}

	return clone
}

type VfFilter func(VfInfo) bool

func (s VfState) Filter(filters ...VfFilter) VfState {
	filtered := make(VfState)
	for k, v := range s {
		keep := true
		for _, filter := range filters {
			if !filter(v) {
				keep = false
				break
			}
		}
		if keep {
			filtered[k] = v
		}
	}
	return filtered
}

func (s VfState) SortByIndex() []VfInfo {
	sorted := make([]VfInfo, 0, len(s))

	vfList := make([]string, 0, len(s))
	for k := range s {
		vfList = append(vfList, k)
	}
	sort.Slice(vfList, func(i, j int) bool {
		return s[vfList[i]].Index < s[vfList[j]].Index
	})
	for _, vfName := range vfList {
		sorted = append(sorted, s[vfName])
	}

	return sorted
}

func FilterByQueueCount(min int, max int) VfFilter {
	return func(v VfInfo) bool {
		return v.QueueCount >= min && v.QueueCount <= max
	}
}

func FilterByName(name string) VfFilter {
	return func(v VfInfo) bool {
		return v.Name == name
	}
}

func FilterByNumaID(numaSet machine.CPUSet) VfFilter {
	return func(v VfInfo) bool {
		return numaSet.Contains(v.NumaID)
	}
}

var _ checkpointmanager.Checkpoint = &SriovPluginCheckpoint{}

type SriovPluginCheckpoint struct {
	PolicyName   string            `json:"policyName"`
	MachineState VfState           `json:"machineState"`
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
