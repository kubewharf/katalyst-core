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

import (
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type (
	CORE_MB_EVENT_TYPE    int
	PACKAGE_MB_EVENT_TYPE int
)

const (
	MAX_NUMA_DISTANCE = 32 // this is the inter-socket distance on AMD, intel inter-socket distance < 32
	MIN_NUMA_DISTANCE = 11 // the distance to local numa is 10 in Linux, thus 11 is the minimum inter-numa distance

	INTEL_FAM6_SKYLAKE_X        = 0x55
	INTEL_FAM6_ICELAKE_X        = 0x6A
	INTEL_FAM6_SAPPHIRERAPIDS_X = 0x8F
	INTEL_FAM6_EMERALDRAPIDS_X  = 0xCF

	AMD_ZEN2_ROME    = 0x31
	AMD_ZEN3_MILAN   = 0x01
	AMD_ZEN4_GENOA_A = 0x10
	AMD_ZEN4_GENOA_B = 0x11

	CORE_MB_READ_LOCAL CORE_MB_EVENT_TYPE = 1
	CORE_MB_READ_TOTAL CORE_MB_EVENT_TYPE = 2

	PACKAGE_MB_READ  PACKAGE_MB_EVENT_TYPE = 1
	PACKAGE_MB_WRITE PACKAGE_MB_EVENT_TYPE = 2
)

func (s *KatalystMachineInfo) Is_Rome() bool {
	if s.Vendor != CPU_VENDOR_AMD {
		return false
	}

	if s.Family >= 0x17 && s.Model == AMD_ZEN2_ROME {
		return true
	}

	return false
}

func (s *KatalystMachineInfo) Is_Milan() bool {
	if s.Vendor != CPU_VENDOR_AMD {
		return false
	}

	if s.Family == 0x19 && s.Model == AMD_ZEN3_MILAN {
		return true
	}

	return false
}

func (s *KatalystMachineInfo) Is_Genoa() bool {
	if s.Vendor != CPU_VENDOR_AMD {
		return false
	}

	if s.Family == 0x19 && (s.Model == AMD_ZEN4_GENOA_A || s.Model == AMD_ZEN4_GENOA_B) {
		// 0x10 A0 A1 0x11 B0
		return true
	}

	return false
}

func (s *KatalystMachineInfo) Is_SPR() bool {
	if s.Vendor != CPU_VENDOR_INTEL {
		return false
	}

	if s.Model == INTEL_FAM6_SAPPHIRERAPIDS_X {
		return true
	}

	return false
}

func (s *KatalystMachineInfo) FakeNumaConfigured() bool {
	if s.ExtraTopologyInfo.SiblingNumaMap[0].Len() > 0 {
		// NOTE: might be wrong in some old machines
		return s.ExtraTopologyInfo.NumaDistanceMap[0][s.ExtraTopologyInfo.SiblingNumaMap[0].List()[0]].Distance == MIN_NUMA_DISTANCE
	}

	return false
}

func (s *KatalystMachineInfo) GetPkgByNuma(numa int) int {
	for i, v := range s.NUMAsInPackage {
		for _, n := range v {
			if n == numa {
				return i
			}
		}
	}

	general.Errorf("failed to find the corresponding package for numa %d", numa)
	return -1
}

// GetPackageMap derives relationship of package and numa nodes, based on known SiblingNumaInfo.SiblingNumaMap
func (s *KatalystMachineInfo) GetPackageMap() map[int][]int {
	return GetNUMAsInPackage(s.SiblingNumaMap)
}

func GetNUMAsInPackage(siblingNumaMap map[int]sets.Int) map[int][]int {
	// numa ids of a package are consecutive, i.e. numa 3,4,5 are of package 1 (assuming each package has 3 numa nodes)
	// hence package 1 related SiblingNumaInfo's records are
	// 3: {4, 5}
	// 4: {3, 5}
	// 5: {3, 4}
	numasPerPackage := siblingNumaMap[0].Len() + 1

	numasInPackages := make(map[int][]int)
	for node, siblings := range siblingNumaMap {
		packageID := node / numasPerPackage
		if _, ok := numasInPackages[packageID]; ok {
			// already populated
			continue
		}

		nodes := []int{node}
		for sibling := range siblings {
			nodes = append(nodes, sibling)
		}

		sort.Ints(nodes)
		numasInPackages[packageID] = nodes
	}

	return numasInPackages
}

func (s *KatalystMachineInfo) GetNumaCCDMap() (map[int][]int, error) {
	numaMap := make(map[int][]int)

	for idx, cores := range s.CPUsInDie {
		cpuset := NewCPUSet(cores...)

		for i := 0; i < s.CPUTopology.NumNUMANodes; i++ {
			if cpuset.IsSubsetOf(s.CPUTopology.CPUDetails.CPUsInNUMANodes(i)) {
				numaMap[i] = append(numaMap[i], idx)
			}
		}
	}

	if len(numaMap) != s.CPUTopology.NumNUMANodes {
		general.Errorf("invalide mapping from numa node to ccds - map len: %d, num of numa: %d", len(numaMap), s.CPUTopology.NumNUMANodes)
		return nil, fmt.Errorf("invalide mapping from numa node to ccds - map len: %d, num of numa: %d", len(numaMap), s.CPUTopology.NumNUMANodes)
	}

	return numaMap, nil
}
