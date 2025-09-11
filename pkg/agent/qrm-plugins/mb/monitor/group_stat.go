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

var MinActiveMB int

// GroupMBStats is memory bandwidth statistic info of multiple groups, each of the groups has multiple CCDs,
// in line with resctrl FS mon-group mon-data structure
type GroupMBStats map[string]GroupMB

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
	return totalMB >= MinActiveMB
}
