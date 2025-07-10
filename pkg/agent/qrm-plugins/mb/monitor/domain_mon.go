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
func NewDomainsMon(statIncoming GroupMonStat, ccdToDomain map[int]int, XDomGroups sets.String) (*DomainsMon, error) {
	result := &DomainsMon{
		Incoming: make(map[int]GroupMonStat),
		Outgoing: make(map[int]GroupMonStat),
	}

	if err := result.splitIncomingStat(statIncoming, ccdToDomain); err != nil {
		return nil, errors.Wrap(err, "failed to split mon data into domains")
	}

	result.deriveOutgoingStat(XDomGroups)

	return result, nil
}

func (d *DomainsMon) splitIncomingStat(statIncoming GroupMonStat, ccdToDomain map[int]int) error {
	for group, groupCCDMB := range statIncoming.mon {
		for ccd, mb := range groupCCDMB {
			domain, ok := ccdToDomain[ccd]
			if !ok {
				return fmt.Errorf("unknow ccd %d", ccd)
			}
			putGroupCCDMBStat(d.Incoming, domain, group, ccd, mb)
		}
	}
	return nil
}

func (d *DomainsMon) deriveOutgoingStat(XDomGroups sets.String) {
	incomingSummary, incomingDomainSummaries := d.summarizeIncoming(XDomGroups)

	for domain, incoming := range d.Incoming {
		domainLocalMB := incomingDomainSummaries[domain].LocalMB
		domainRemoteOutgoing := calcRemoteOutgoing(domain, incomingSummary, incomingDomainSummaries)
		for group, ccdmb := range incoming.mon {
			for ccd, mb := range ccdmb {
				ccdRemoteOutgoing := mb.RemoteMB
				if XDomGroups.Has(group) {
					ccdRemoteOutgoing = getPortion(domainRemoteOutgoing, mb.LocalMB, domainLocalMB)
				}
				outgoingCCDMB := MBStat{
					LocalMB:  mb.LocalMB,
					RemoteMB: ccdRemoteOutgoing,
					TotalMB:  mb.LocalMB + ccdRemoteOutgoing,
				}
				putGroupCCDMBStat(d.Outgoing, domain, group, ccd, outgoingCCDMB)
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

func (d *DomainsMon) summarizeIncoming(XDomGroups sets.String) (MBStat, map[int]*MBStat) {
	summary := MBStat{}
	domainSummaries := map[int]*MBStat{}

	for domain, incoming := range d.Incoming {
		domainSummaries[domain] = &MBStat{}
		for group, ccdmb := range incoming.mon {
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

// calcRemoteOutgoing calculates remote outgoing of a domain based on the formula:
// given i the domain id,
// for all j except for i,
// sum( remote_incoming[j] * local_incoming[i] / {local_incoming[0] + ... + local_incoming[n], except for i} ),
func calcRemoteOutgoing(domain int, summaryIncoming MBStat, summaryDomainIncoming map[int]*MBStat) int {
	domainLocalMB := summaryDomainIncoming[domain].LocalMB

	result := 0
	for domainID, ccdmb := range summaryDomainIncoming {
		if domain != domainID {
			result += getPortion(ccdmb.RemoteMB, domainLocalMB, summaryIncoming.LocalMB-ccdmb.LocalMB)
		}
	}
	return result
}

func putGroupCCDMBStat(domains map[int]GroupMonStat, domain int, group string, ccd int, ccdMB MBStat) {
	if _, ok := domains[domain]; !ok {
		domains[domain] = GroupMonStat{mon: map[string]GroupCCDMB{}}
	}
	stat := domains[domain]
	putCCDMBStat(stat, group, ccd, ccdMB)
}

func putCCDMBStat(stat GroupMonStat, group string, ccd int, mbStat MBStat) {
	if _, ok := stat.mon[group]; !ok {
		stat.mon[group] = GroupCCDMB{}
	}
	groupCCDMB := stat.mon[group]
	groupCCDMB[ccd] = mbStat
}
