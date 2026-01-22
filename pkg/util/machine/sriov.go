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

package machine

import "sort"

type SriovVFInfo struct {
	PFInfo InterfaceInfo
	// RepName is the name of the representer device for this VF. (e.g., eth0_1)
	RepName string
	// Index the VF (e.g., 0, 1, 2...)
	Index int
	// PCIAddr is the PCI address of the VF. (e.g., 0000:3b:00.1)
	PCIAddr string
}

type SriovVFList []SriovVFInfo

func (vfList SriovVFList) Sort() {
	sort.Slice(vfList, func(i, j int) bool {
		if vfList[i].PFInfo.Name != vfList[j].PFInfo.Name {
			return vfList[i].PFInfo.Name < vfList[j].PFInfo.Name
		}
		return vfList[i].Index < vfList[j].Index
	})
}
