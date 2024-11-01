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
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
)

type MBDomain struct {
	ID        int
	NumaNodes []int
	CCDNode   map[int]int
	NodeCCDs  map[int][]int
	CCDs      []int

	rwLock sync.RWMutex

	// numa nodes that will be assigned to dedicated pods that still are in Admit state
	PreemptyNodes sets.Int

	ccdIncubated       IncubatedCCDs
	incubationInterval time.Duration
}

func (m *MBDomain) String() string {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	var sb strings.Builder
	sb.WriteString("----- mb domain summary -----\n")
	sb.WriteString(fmt.Sprintf("    id: %d\n", m.ID))
	for _, node := range m.NumaNodes {
		sb.WriteString(fmt.Sprintf("    numa node: %d\n", node))
		for _, ccd := range m.NodeCCDs[node] {
			sb.WriteString(fmt.Sprintf("      ccd %d\n", ccd))
		}
	}

	return sb.String()
}

func (m *MBDomain) startIncubation(ccds sets.Int) {
	m.rwLock.Lock()
	defer m.rwLock.Unlock()

	for _, ccd := range m.CCDs {
		if ccds.Has(ccd) {
			m.ccdIncubated[ccd] = time.Now().Add(m.incubationInterval)
			m.undoPreemptNodeByCCD(ccd)
		}
	}
}

func (m *MBDomain) undoPreemptNodeByCCD(ccd int) {
	if node, ok := m.CCDNode[ccd]; ok {
		m.PreemptyNodes.Delete(node)
	}
}

func (m *MBDomain) PreemptNodes(nodes []int) bool {
	hasChange := false

	m.rwLock.Lock()
	defer m.rwLock.Unlock()

	for _, node := range nodes {
		if _, ok := m.NodeCCDs[node]; ok {
			if !m.PreemptyNodes.Has(node) {
				m.PreemptyNodes.Insert(node)
				hasChange = true
			}
		}
	}

	return hasChange
}

func (m *MBDomain) UndoPreemptNodes(nodes []int) {
	m.rwLock.Lock()
	defer m.rwLock.Unlock()

	for _, node := range nodes {
		delete(m.PreemptyNodes, node)
	}
}

func (m *MBDomain) GetPreemptingNodes() []int {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()
	return m.PreemptyNodes.List()
}

func (m *MBDomain) CleanseIncubates() {
	m.rwLock.Lock()
	m.rwLock.Unlock()

	for ccd, v := range m.ccdIncubated {
		if !isIncubated(v) {
			delete(m.ccdIncubated, ccd)
		}
	}
}

func (m *MBDomain) CloneIncubates() IncubatedCCDs {
	m.rwLock.RLock()
	m.rwLock.RUnlock()

	clone := make(IncubatedCCDs)
	for ccd, v := range m.ccdIncubated {
		clone[ccd] = v
	}

	return clone
}
