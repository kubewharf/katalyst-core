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

package domain

import (
	"fmt"
	"sort"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type Domains map[int]*Domain

// GetCCDMapping gets the mapping from ccd to domain id
func (d Domains) GetCCDMapping() map[int]int {
	result := map[int]int{}
	for domID, dom := range d {
		for ccd := range dom.CCDs {
			result[ccd] = domID
		}
	}
	return result
}

// Domain is the unit of memory bandwidth to share and compete with
type Domain struct {
	ID   int
	CCDs sets.Int

	minMBPerCCD int
	maxMBPerCCD int

	// below are for incoming memory bandwidth
	defaultCapacityMB  int
	maxAlienIncomingMB int
}

func (d *Domain) GetAlienMBLimit() int {
	return d.maxAlienIncomingMB
}

func newDomain(id int, ccds sets.Int, capacity, ccdMin, ccdMax, ccdAlienMBLimit int) *Domain {
	domain := Domain{
		ID:                 id,
		CCDs:               sets.NewInt(ccds.List()...),
		defaultCapacityMB:  capacity,
		minMBPerCCD:        ccdMin,
		maxMBPerCCD:        ccdMax,
		maxAlienIncomingMB: ccdAlienMBLimit,
	}

	return &domain
}

func NewDomains(domains ...*Domain) (Domains, error) {
	result := Domains{}
	ccds := sets.Int{}
	for _, domain := range domains {
		ccdsToAdd := domain.CCDs.List()
		if ccds.HasAny(ccdsToAdd...) {
			return nil, fmt.Errorf("duplicate ccd in domain %d", domain.ID)
		}
		ccds.Insert(domain.CCDs.List()...)
		result[domain.ID] = domain
	}

	return result, nil
}

func getMinNumaID(numaIDs sets.Int) (int, error) {
	if numaIDs.Len() == 0 {
		return 0, errors.New("invalid empty set")
	}

	var result int
	// initialize with a value
	for v := range numaIDs {
		result = v
		break
	}

	for v := range numaIDs {
		if v < result {
			result = v
		}
	}
	return result, nil
}

func getMinSiblingNumaID(numaMap map[int]sets.Int) ([]int, error) {
	result := []int{}

	knownNumas := sets.NewInt()
	for numaID, siblings := range numaMap {
		if knownNumas.Has(numaID) {
			continue
		}

		knownNumas.Insert(numaID)

		if len(siblings) == 0 {
			result = append(result, numaID)
			continue
		}

		knownNumas.Insert(siblings.List()...)
		minNumaID, err := getMinNumaID(siblings)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid sibling of numa %d", result)
		}

		if numaID < minNumaID {
			minNumaID = numaID
		}

		result = append(result, minNumaID)
	}

	return result, nil
}

func identifyDomainByNumas(numaMap map[int]sets.Int) (map[int]sets.Int, error) {
	if len(numaMap) == 0 {
		return nil, errors.New("invalid empty machine sibling numa info")
	}

	minNumaIDs, err := getMinSiblingNumaID(numaMap)
	if err != nil {
		return nil, errors.Wrap(err, "failed to identify domain by numas")
	}

	sort.Ints(minNumaIDs)

	result := map[int]sets.Int{}
	for id, minNumaID := range minNumaIDs {
		result[id] = sets.NewInt(minNumaID)
		result[id].Insert(numaMap[minNumaID].List()...)
	}
	return result, nil
}

func getDiesByNUMAs(numas sets.Int, dieTopology *machine.DieTopology) (sets.Int, error) {
	result := sets.Int{}
	for numa := range numas {
		dies, ok := dieTopology.NUMAToDie[numa]
		if !ok {
			return nil, fmt.Errorf("unknown numa id %d", numa)
		}
		result.Insert(dies.List()...)
	}
	return result, nil
}

func NewDomainsByMachineInfo(info *machine.KatalystMachineInfo,
	defaultDomainCapacity int, ccdMinMB, ccdMaxMB, maxRemoteMB int,
) (Domains, error) {
	if info == nil {
		return nil, errors.New("invalid nil machine sibling numa info")
	}

	domainToNumas, err := identifyDomainByNumas(info.SiblingNumaMap)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get domains out of machine info")
	}

	result := Domains{}
	for domainID, numas := range domainToNumas {
		dies, errCurr := getDiesByNUMAs(numas, info.DieTopology)
		if errCurr != nil {
			return nil, errors.Wrapf(err, "failed to locate numa ccds for domain %d", domainID)
		}
		result[domainID] = newDomain(domainID, dies, defaultDomainCapacity, ccdMinMB, ccdMaxMB, maxRemoteMB)

		if klog.V(6).Enabled() {
			klog.Infof("[mbm] mb domain = %d, numa nodes = %v, ccds = %v", domainID, numas.List(), dies.List())
		}
	}

	return result, nil
}
