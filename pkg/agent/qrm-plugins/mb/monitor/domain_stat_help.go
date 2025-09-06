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

// estimateDomainIncomingRemote calculates incoming remote of a domain based on the formula:
// given i the domain id, for all j except for i,
// sum( remote_outgoing[j] * remote_outgoing[i] / {remote_outgoing[0] + ... + remote_outgoing[n], except for i} ),
// i.e. dom-I incoming == for all J<>I, dom-J outgoing * {weight to I wrt all-other-than-J}
func estimateDomainIncomingRemote(domain int, sumOutgoing int, sumDomainOutgoing map[int]int) int {
	iOutgoingRemote := sumDomainOutgoing[domain]

	result := 0
	for j, jOutgoingRemote := range sumDomainOutgoing {
		if domain != j {
			result += getPortion(jOutgoingRemote, iOutgoingRemote, sumOutgoing-jOutgoingRemote)
		}
	}
	return result
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

func putGroupCCDMBToDomain(domainGroupStats map[int]GroupMBStats, domainID int, group string, ccd int, ccdMB MBInfo) {
	if _, ok := domainGroupStats[domainID]; !ok {
		domainGroupStats[domainID] = GroupMBStats{}
	}
	groupStats := domainGroupStats[domainID]
	putCCDMBStat(groupStats, group, ccd, ccdMB)
}

func putCCDMBStat(stat GroupMBStats, group string, ccd int, mbStat MBInfo) {
	if _, ok := stat[group]; !ok {
		stat[group] = GroupMB{}
	}
	groupCCDMB := stat[group]
	groupCCDMB[ccd] = mbStat
}
