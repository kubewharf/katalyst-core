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
)

// DomainsMon keeps memory bandwidth data in both directions of traffic
type DomainsMon struct {
	Incoming map[int]GroupMonStat
	Outgoing map[int]GroupMonStat
}

// NewDomainsMon splits resctrl style incoming mon data into domains w/ both directions,
// and attributes incoming traffic to outgoings, in cross-domain style, among XDomGroups
func NewDomainsMon(statOutgoing GroupMonStat, ccdToDomain map[int]int, XDomGroups sets.String) (*DomainsMon, error) {
	result := &DomainsMon{
		Incoming: make(map[int]GroupMonStat),
		Outgoing: make(map[int]GroupMonStat),
	}

	if err := result.splitOutgoingStat(statOutgoing, ccdToDomain); err != nil {
		return nil, errors.Wrap(err, "failed to split mon data into domains")
	}

	result.deriveIncomingStat(XDomGroups)

	return result, nil
}

func (d *DomainsMon) GetGroupedDomainSummary() map[string][]MBStat {
	// outgoing traffic only, as the original local/remote/total data is collected for outgoing direction
	result := map[string][]MBStat{}
	numDomains := len(d.Outgoing)
	for dom, groupStat := range d.Outgoing {
		for group, stat := range groupStat {
			if _, ok := result[group]; !ok {
				result[group] = make([]MBStat, numDomains)
			}
			result[group][dom] = stat.SumStat()
		}
	}
	return result
}

func (d *DomainsMon) splitOutgoingStat(statOutgoing GroupMonStat, ccdToDomain map[int]int) error {
	for group, groupCCDMB := range statOutgoing {
		for ccd, mb := range groupCCDMB {
			domain, ok := ccdToDomain[ccd]
			if !ok {
				return fmt.Errorf("unknow ccd %d", ccd)
			}
			putGroupCCDMBStat(d.Outgoing, domain, group, ccd, mb)
		}
	}
	return nil
}

func (d *DomainsMon) deriveIncomingStat(XDomGroups sets.String) {
	outgoingSummary, outgoingDomainSummaries := d.summarizeOutgoing(XDomGroups)

	for domain, outgoing := range d.Outgoing {
		domainLocalMB := outgoingDomainSummaries[domain].LocalMB
		domainRemoteIncoming := calcRemoteIncoming(domain, outgoingSummary, outgoingDomainSummaries)
		for group, ccdmb := range outgoing {
			for ccd, mb := range ccdmb {
				ccdRemoteIncoming := mb.RemoteMB
				if XDomGroups.Has(group) {
					ccdRemoteIncoming = getPortion(domainRemoteIncoming, mb.LocalMB, domainLocalMB)
				}
				incomingCCDMB := MBStat{
					LocalMB:  mb.LocalMB,
					RemoteMB: ccdRemoteIncoming,
					TotalMB:  mb.LocalMB + ccdRemoteIncoming,
				}
				putGroupCCDMBStat(d.Incoming, domain, group, ccd, incomingCCDMB)
			}
		}
	}
}

func getPortion(amount, share, total int) int {
	if share <= 0 || total <= 0 {
		return 0
	}

	result := amount * (share * 100) / total / 100
	if result > amount {
		result = amount
	}
	return result
}

func (d *DomainsMon) summarizeOutgoing(XDomGroups sets.String) (MBStat, map[int]*MBStat) {
	summary := MBStat{}
	domainSummaries := map[int]*MBStat{}

	for domain, outgoing := range d.Outgoing {
		domainSummaries[domain] = &MBStat{}
		for group, ccdmb := range outgoing {
			// only take into account the cross-domain groups
			if !XDomGroups.Has(group) {
				continue
			}
			for _, mb := range ccdmb {
				domainSummaries[domain].LocalMB += mb.LocalMB
				domainSummaries[domain].RemoteMB += mb.RemoteMB
			}
		}
		summary.LocalMB += domainSummaries[domain].LocalMB
		summary.RemoteMB += domainSummaries[domain].RemoteMB
	}

	return summary, domainSummaries
}

// calcRemoteIncoming calculates remote incoming of a domain based on the formula:
// given i the domain id,
// for all j except for i,
// sum( remote_outgoing[j] * local_outgoing[i] / {local_outgoing[0] + ... + local_outgoing[n], except for i} ),
func calcRemoteIncoming(domain int, summaryOutgoing MBStat, summaryDomainOutgoing map[int]*MBStat) int {
	domainLocalMB := summaryDomainOutgoing[domain].LocalMB

	result := 0
	for domainID, ccdmb := range summaryDomainOutgoing {
		if domain != domainID {
			result += getPortion(ccdmb.RemoteMB, domainLocalMB, summaryOutgoing.LocalMB-ccdmb.LocalMB)
		}
	}
	return result
}

func putGroupCCDMBStat(domains map[int]GroupMonStat, domain int, group string, ccd int, ccdMB MBStat) {
	if _, ok := domains[domain]; !ok {
		domains[domain] = GroupMonStat{}
	}
	stat := domains[domain]
	putCCDMBStat(stat, group, ccd, ccdMB)
}

func putCCDMBStat(stat GroupMonStat, group string, ccd int, mbStat MBStat) {
	if _, ok := stat[group]; !ok {
		stat[group] = GroupCCDMB{}
	}
	groupCCDMB := stat[group]
	groupCCDMB[ccd] = mbStat
}
