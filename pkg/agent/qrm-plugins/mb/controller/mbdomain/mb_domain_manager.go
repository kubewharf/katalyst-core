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

package mbdomain

import (
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type MBDomainManager struct {
	Domains map[int]*MBDomain
}

func (m MBDomainManager) StartIncubation(ccds []int) {
	general.InfofV(6, "mbm: need to incubate CCD %v from %v", ccds, time.Now())
	dict := sets.NewInt(ccds...)
	for _, domain := range m.Domains {
		domain.startIncubation(dict)
	}
}

func NewMBDomainManager(dieTopology *machine.DieTopology, incubationInterval time.Duration) *MBDomainManager {
	manager := &MBDomainManager{
		Domains: make(map[int]*MBDomain),
	}

	for packageID := 0; packageID < dieTopology.Packages; packageID++ {
		mbDomain := &MBDomain{
			ID:                 packageID,
			NumaNodes:          dieTopology.NUMAsInPackage[packageID],
			CCDNode:            make(map[int]int),
			NodeCCDs:           make(map[int][]int),
			PreemptyNodes:      make(sets.Int),
			ccdIncubated:       make(IncubatedCCDs),
			incubationInterval: incubationInterval,
		}

		for _, node := range mbDomain.NumaNodes {
			ccds, ok := dieTopology.DiesInNuma[node]
			if !ok {
				// todo: invalid data; may need fail fast
				continue
			}
			for ccd, _ := range ccds {
				mbDomain.CCDNode[ccd] = node
				mbDomain.NodeCCDs[node] = append(mbDomain.NodeCCDs[node], ccd)
				mbDomain.CCDs = append(mbDomain.CCDs, ccd)
			}
			sort.Slice(mbDomain.NodeCCDs[node], func(i, j int) bool {
				return mbDomain.NodeCCDs[node][i] < mbDomain.NodeCCDs[node][j]
			})
		}

		sort.Slice(mbDomain.CCDs, func(i, j int) bool {
			return mbDomain.CCDs[i] < mbDomain.CCDs[j]
		})

		manager.Domains[packageID] = mbDomain
		general.InfofV(6, "mbm: domain %s", mbDomain.String())
	}

	return manager
}
