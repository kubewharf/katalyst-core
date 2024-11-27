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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	onceDomainManagerInit sync.Once
	domainManager         *MBDomainManager
)

type MBDomainManager struct {
	Domains  map[int]*MBDomain
	nodeCCDs map[int]sets.Int
}

// StartIncubation marks the specified CCDs the beginning of incubation time.
// Since it is currently applicable only to Socket pods, the CCDs is of dedicated QoS group.
// Incubation is the grace period for a new pod to maintain its mb privilege.
// It suffices to approximate the pod admission with pod start in POC phase.
func (m MBDomainManager) StartIncubation(ccds []int) {
	general.InfofV(6, "mbm: need to incubate CCD %v from %v", ccds, time.Now())
	dict := sets.NewInt(ccds...)
	for _, domain := range m.Domains {
		domain.startIncubation(dict)
	}
}

func (m MBDomainManager) PreemptNodes(nodes []int) bool {
	hasChange := false

	general.InfofV(6, "mbm: reserving numa node %v", nodes)
	for _, domain := range m.Domains {
		hasChange = hasChange || domain.PreemptNodes(nodes)
	}

	if hasChange {
		// though technically incubation starts from pod being alive, it is practically same effect as pod admission
		ccds := make([]int, 0)
		for _, node := range nodes {
			ccds = append(ccds, m.nodeCCDs[node].List()...)
		}
		m.StartIncubation(ccds)
	}

	return hasChange
}

func NewMBDomainManager(dieTopology *machine.DieTopology, incubationInterval time.Duration) *MBDomainManager {
	onceDomainManagerInit.Do(func() {
		domainManager = newMBDomainManager(dieTopology, incubationInterval)
	})
	return domainManager
}

func newMBDomainManager(dieTopology *machine.DieTopology, incubationInterval time.Duration) *MBDomainManager {
	manager := &MBDomainManager{
		Domains:  make(map[int]*MBDomain),
		nodeCCDs: dieTopology.DiesInNuma,
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
