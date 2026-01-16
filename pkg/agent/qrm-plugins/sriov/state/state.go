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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
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
		name, err := machine.GetVFName(sysFsDir, vf.PCIAddr)
		if err != nil {
			return fmt.Errorf("failed to get network vf name: %v", err)
		}
		info.Name = name
		ibDevices, err := machine.GetVfIBDevices(sysFsDir, name)
		if err != nil {
			return fmt.Errorf("failed to get vf ib devices: %v", err)
		}
		info.IBDevices = ibDevices
		return nil
	}); err != nil {
		return err
	}

	combinedCount, err := machine.GetInterfaceChannelsCombinedCount(info.Name)
	if err != nil {
		return fmt.Errorf("failed to get vf channels: %v", err)
	}
	info.QueueCount = combinedCount

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

func (s VFState) Sort() {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Index != s[j].Index {
			return s[i].Index < s[j].Index
		}

		if s[i].NumaNode != s[j].NumaNode {
			return s[i].NumaNode < s[j].NumaNode
		}

		return s[i].PFName < s[j].PFName
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

type AllocationInfo struct {
	commonstate.AllocationMeta `json:",inline"`

	VFInfo VFInfo `json:"vfInfo"`
}

func (ai *AllocationInfo) String() string {
	if ai == nil {
		return ""
	}

	contentBytes, err := json.Marshal(ai)
	if err != nil {
		general.LoggerWithPrefix("AllocationInfo.String", general.LoggingPKGFull).
			Errorf("marshal AllocationInfo failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (ai *AllocationInfo) Clone() *AllocationInfo {
	clone := &AllocationInfo{
		AllocationMeta: *ai.AllocationMeta.Clone(),
		VFInfo: VFInfo{
			RepName:  ai.VFInfo.RepName,
			Index:    ai.VFInfo.Index,
			PCIAddr:  ai.VFInfo.PCIAddr,
			PFName:   ai.VFInfo.PFName,
			NumaNode: ai.VFInfo.NumaNode,
			NSName:   ai.VFInfo.NSName,
		},
	}

	if ai.VFInfo.ExtraVFInfo != nil {
		clone.VFInfo.ExtraVFInfo = &ExtraVFInfo{
			Name:       ai.VFInfo.ExtraVFInfo.Name,
			QueueCount: ai.VFInfo.ExtraVFInfo.QueueCount,
			IBDevices:  append([]string(nil), ai.VFInfo.ExtraVFInfo.IBDevices...),
		}
	}

	return clone
}

type (
	ContainerEntries map[string]*AllocationInfo  // keyed by container name
	PodEntries       map[string]ContainerEntries // Keyed by pod UID
)

func (pe *PodEntries) Clone() PodEntries {
	clone := make(PodEntries)
	for podUID, containerEntries := range *pe {
		clone[podUID] = make(ContainerEntries)
		for containerName, allocationInfo := range containerEntries {
			clone[podUID][containerName] = allocationInfo.Clone()
		}
	}
	return clone
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() VFState
	GetPodEntries() PodEntries
	GetAllocationInfo(podUID, containerName string) *AllocationInfo
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
// todo: optimize me according to actual needs
type writer interface {
	SetMachineState(state VFState, persist bool)
	SetPodEntries(podEntries PodEntries, persist bool)
	SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo, persist bool)
	Delete(podUID string, persist bool)

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

func GenerateDummyState(pfCount int, vfPerPF int, allocatedVFSet map[int]sets.Int) (VFState, PodEntries) {
	machineState := make(VFState, 0, pfCount*pfCount)
	podEntries := make(PodEntries)

	pfNumaNode := []int{0, 2}

	fullIdx := 0

	for i := 0; i < pfCount; i++ {
		pfName := fmt.Sprintf("eth%d", i)

		nsName := ""
		if i%2 == 0 {
			nsName = "ns2"
		}

		for j := 0; j < vfPerPF; j++ {
			vfName := fmt.Sprintf("%s_%d", pfName, j)

			ibDevices := []string{fmt.Sprintf("umad%d", fullIdx), fmt.Sprintf("uverbs%d", fullIdx)}

			queueCount := 8
			if j >= pfCount/2 {
				queueCount = 32
			}

			vf := VFInfo{
				RepName:  vfName,
				Index:    j,
				PCIAddr:  fmt.Sprintf("0000:%02d:00.%d", 40+i, j),
				PFName:   pfName,
				NumaNode: pfNumaNode[i%len(pfNumaNode)],
				NSName:   nsName,
				ExtraVFInfo: &ExtraVFInfo{
					Name:       vfName,
					QueueCount: queueCount,
					IBDevices:  ibDevices,
				},
			}

			machineState = append(machineState, vf)

			if allocatedVFSet[i].Has(j) {
				containerName := fmt.Sprintf("container%d", fullIdx)
				pod := fmt.Sprintf("pod%d", fullIdx)
				containerEntries := ContainerEntries{
					containerName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        pod,
							PodName:       pod,
							ContainerName: containerName,
						},
						VFInfo: vf,
					},
				}
				podEntries[pod] = containerEntries
			}

			fullIdx++
		}
	}

	machineState.Sort()

	return machineState, podEntries
}
