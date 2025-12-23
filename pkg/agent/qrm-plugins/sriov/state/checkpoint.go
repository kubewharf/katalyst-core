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
	"sort"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type VFInfo struct {
	RepName  string `json:"repName"`
	Index    int    `json:"index"`
	PCIAddr  string `json:"pciAddr"`
	PFName   string `json:"pfName"`
	NumaNode int    `json:"numaNode"`
	NSName   string `json:"nsName"`

	*ExtraVFInfo
}

// ExtraVFInfo is the extra vf info that cannot be retrieved directly from machine.GetNetworkVFs,
type ExtraVFInfo struct {
	Name       string   `json:"name"`
	QueueCount int      `json:"queueCount"`
	IBDevices  []string `json:"ibDevices"`
}

func (vf *VFInfo) InitExtraInfo(netNSDirAbsPath string) error {
	if vf.ExtraVFInfo != nil {
		return nil
	}

	info := &ExtraVFInfo{}

	if err := machine.DoNetNS(vf.NSName, netNSDirAbsPath, func(sysFsDir string) error {
		name, err := machine.GetNSNetworkVFName(sysFsDir, vf.PCIAddr)
		if err != nil {
			return fmt.Errorf("failed to get network vf name: %v", err)
		}
		info.Name = name
		ibDevices, err := machine.GetVfIBDevName(sysFsDir, name)
		if err != nil {
			return fmt.Errorf("failed to get vf ib devices: %v", err)
		}
		info.IBDevices = ibDevices
		return nil
	}); err != nil {
		return err
	}

	queueCount, err := machine.GetCombinedChannels(info.Name)
	if err != nil {
		return fmt.Errorf("failed to get vf queue count: %v", err)
	}
	info.QueueCount = queueCount

	vf.ExtraVFInfo = info

	return nil
}

type VFState []VFInfo

func (s VFState) Clone() VFState {
	clone := make(VFState, 0, len(s))
	for _, vf := range s {
		clone = append(clone, vf)
	}

	return clone
}

// ToMap converts VFState to map[PCIAddr]VFInfo
func (s VFState) ToMap() map[string]VFInfo {
	vfMap := make(map[string]VFInfo)
	for _, vf := range s {
		vfMap[vf.PCIAddr] = vf
	}
	return vfMap
}

type VFFilter func(info VFInfo) bool

func (s VFState) Filter(filters ...VFFilter) VFState {
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
		return s[i].Index < s[j].Index
	})
}

func FilterByPodAllocated(podEntries PodEntries, allocated bool) VFFilter {
	vfSet := sets.NewString()
	for _, containerEntries := range podEntries {
		for _, allocationInfo := range containerEntries {
			vfSet.Insert(allocationInfo.VFInfo.PCIAddr)
		}
	}

	return func(vfInfo VFInfo) bool {
		return vfSet.Has(vfInfo.PCIAddr) == allocated
	}
}

func FilterByQueueCount(min int, max int) VFFilter {
	return func(vf VFInfo) bool {
		if vf.ExtraVFInfo == nil {
			return false
		}
		return vf.QueueCount >= min && vf.QueueCount <= max
	}
}

func FilterByPCIAddr(pciAddr string) VFFilter {
	return func(vf VFInfo) bool {
		return vf.PCIAddr == pciAddr
	}
}

func FilterByNumaSet(numaSet machine.CPUSet) VFFilter {
	return func(vf VFInfo) bool {
		return numaSet.Contains(vf.NumaNode)
	}
}

func FilterByRDMA(supported bool) VFFilter {
	return func(vf VFInfo) bool {
		if vf.ExtraVFInfo == nil {
			return false
		}
		return len(vf.IBDevices) > 0 == supported
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
