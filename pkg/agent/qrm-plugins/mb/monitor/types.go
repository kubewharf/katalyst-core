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

const minActiveMB = 1_000

type GroupCCDMB map[int]MBStat

// MBStat keeps memory bandwidth info
type MBStat struct {
	LocalMB  int
	RemoteMB int
	TotalMB  int
}

func (g GroupCCDMB) SumStat() MBStat {
	sum := MBStat{}
	for _, mbStat := range g {
		sum.TotalMB += mbStat.TotalMB
		sum.LocalMB += mbStat.LocalMB
		sum.RemoteMB += mbStat.RemoteMB
	}

	return sum
}

func (g GroupCCDMB) HasTraffic() bool {
	totalMB := g.SumStat().TotalMB
	return totalMB >= minActiveMB
}
