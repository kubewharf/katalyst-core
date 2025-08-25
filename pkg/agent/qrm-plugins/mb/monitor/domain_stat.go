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

package monitor

import (
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/syntax"
)

// DomainStats keeps memory bandwidth monitored statistics in both directions of traffics
type DomainStats struct {
	Incomings map[int]DomainMonStat
	Outgoings map[int]DomainMonStat

	// domain outgoing summary by groups
	OutgoingGroupSumStat map[string][]MBInfo
}

// DomainMonStat is memory bandwidth statistic info of one domain, each has multiple groups
type DomainMonStat = GroupMBStats

// NewDomainStats splits group-style outgoing mb stat (as from resctrl mon-data) into corresponding domains,
// and attributes incoming traffic from outgoings for the cross-domain groups
func NewDomainStats(statOutgoing GroupMBStats, ccdToDomain map[int]int, xDomGroups sets.String) (*DomainStats, error) {
	result := &DomainStats{
		Incomings:            map[int]GroupMBStats{},
		Outgoings:            map[int]GroupMBStats{},
		OutgoingGroupSumStat: map[string][]MBInfo{},
	}

	if err := result.populateOutgoingStats(statOutgoing, ccdToDomain); err != nil {
		return nil, errors.Wrap(err, "failed to split mon data into domains")
	}

	result.deriveIncomingStats(xDomGroups)
	result.sumOutgoingByGroup()
	return result, nil
}

func (d *DomainStats) sumOutgoingByGroup() {
	numDomains := len(d.Outgoings)
	for dom, domStat := range d.Outgoings {
		for group, stat := range domStat {
			if _, ok := d.OutgoingGroupSumStat[group]; !ok {
				d.OutgoingGroupSumStat[group] = make([]MBInfo, numDomains)
			}
			d.OutgoingGroupSumStat[group][dom] = stat.SumStat()
		}
	}
}

func (d *DomainStats) populateOutgoingStats(resctrlMonStats GroupMBStats, ccdToDomain map[int]int) error {
	for group, groupStat := range resctrlMonStats {
		for ccd, stat := range groupStat {
			domainID, ok := ccdToDomain[ccd]
			if !ok {
				return fmt.Errorf("unknow ccd %d", ccd)
			}
			d.addDomainOutgoing(domainID, group, ccd, stat)
		}
	}
	return nil
}

func (d *DomainStats) deriveIncomingStats(xDomGroups sets.String) {
	d.Incomings = syntax.DeepCopy(d.Outgoings).(map[int]DomainMonStat)
	d.updateXDomIncomingStats(xDomGroups)
}

func (d *DomainStats) updateXDomIncomingStats(xDomGroups sets.String) {
	outgoingSum, outgoingDomainSums := d.summarizeXDomOutgoings(xDomGroups)

	for domainID := range d.Incomings {
		domainRemoteIncoming := estimateDomainIncomingRemote(domainID, outgoingSum, outgoingDomainSums)
		d.domUpdateXDomIncomingStats(domainID, xDomGroups, domainRemoteIncoming, outgoingDomainSums[domainID])
	}
}

func (d *DomainStats) domUpdateXDomIncomingStats(domainID int, xDomGroups sets.String,
	domainRemoteIncoming int, domainRemoteOutgoing int,
) {
	for group, groupMB := range d.Incomings[domainID] {
		if !xDomGroups.Has(group) {
			continue
		}
		for ccd, mb := range groupMB {
			incomingRemote := getPortion(domainRemoteIncoming, mb.RemoteMB, domainRemoteOutgoing)
			incomingMBStat := MBInfo{
				LocalMB:  mb.LocalMB,
				RemoteMB: incomingRemote,
				TotalMB:  mb.LocalMB + incomingRemote,
			}
			d.Incomings[domainID][group][ccd] = incomingMBStat
		}
	}
}

// summarizeXDomOutgoings sums up cross-domain outgoing remote only
func (d *DomainStats) summarizeXDomOutgoings(xDomGroups sets.String) (int, map[int]int) {
	var sum int
	domainSums := map[int]int{}

	for domain, outgoing := range d.Outgoings {
		for group, ccdmb := range outgoing {
			// only take into account the cross-domain groups
			if !xDomGroups.Has(group) {
				continue
			}
			for _, mb := range ccdmb {
				domainSums[domain] += mb.RemoteMB
			}
		}
		sum += domainSums[domain]
	}

	return sum, domainSums
}

func (d *DomainStats) addDomainOutgoing(domain int, group string, ccd int, stat MBInfo) {
	putGroupCCDMBToDomain(d.Outgoings, domain, group, ccd, stat)
}

func (d *DomainStats) addDomainIncoming(domain int, group string, ccd int, stat MBInfo) {
	putGroupCCDMBToDomain(d.Incomings, domain, group, ccd, stat)
}
