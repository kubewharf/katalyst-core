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
	"strings"

	"go.uber.org/atomic"
)

var minActiveMB atomic.Int64

func SetMinActiveMB(value int) {
	minActiveMB.Store(int64(value))
}

func getMinActiveMB() int {
	return int(minActiveMB.Load())
}

// GroupMBStats is memory bandwidth statistic info of multiple groups, each of the groups has multiple CCDs,
// in line with resctrl FS mon-group mon-data structure
type GroupMBStats map[string]GroupMB

// NormalizeShare changes subgroup names shared-xx to share-xx, and
// returns indition whether the group names use the old style of "shared-xx" conventions.
// Some resctrl FS may present share subgroup in "shared-xx" form; this conversion replaces with the desired form "share-xx"
func (gms GroupMBStats) NormalizeShareSubgroups() (stats GroupMBStats, isSharedSubgroupConvention bool) {
	for name := range gms {
		if isObsoleteSharedGroupName(name) {
			isSharedSubgroupConvention = true
			break
		}
	}

	if !isSharedSubgroupConvention {
		return gms, false
	}

	stats = make(GroupMBStats)
	for name, groupMB := range gms {
		name = ensureShareGroupName(name)
		stats[name] = groupMB
	}

	return stats, true
}

func isObsoleteSharedGroupName(name string) bool {
	return strings.HasPrefix(name, "shared-")
}

// ensureShareGroupName converts shared-xx group name to share-xx
func ensureShareGroupName(name string) string {
	if strings.HasPrefix(name, "shared-") {
		name = "share-" + strings.TrimPrefix(name, "shared-")
	}
	return name
}

// GroupMB keeps one group mb info, each group may have multiple ccds
type GroupMB map[int]MBInfo

// MBInfo is mb of one unit (e.g. one ccd, or one domain, even whole machine)
type MBInfo struct {
	LocalMB  int
	RemoteMB int
	TotalMB  int
}

func (g GroupMB) SumStat() MBInfo {
	sum := MBInfo{}
	for _, mbStat := range g {
		sum.TotalMB += mbStat.TotalMB
		sum.LocalMB += mbStat.LocalMB
		sum.RemoteMB += mbStat.RemoteMB
	}

	return sum
}

func (g GroupMB) HasTraffic() bool {
	totalMB := g.SumStat().TotalMB
	return totalMB >= getMinActiveMB()
}
