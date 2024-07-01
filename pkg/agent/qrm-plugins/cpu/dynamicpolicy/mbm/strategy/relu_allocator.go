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

package strategy

// reluAllocator is the reLU style allocator to decide the shares among the participating numa nodes
type reluAllocator struct{}

func (r reluAllocator) AllocateDeductions(activeGroups GroupMBs, packageMB, mbThreshold float64) []map[int]float64 {
	excessiveByPackage := packageMB - mbThreshold
	totalByNodes := activeGroups.sum()
	ratio := totalByNodes / packageMB
	excessiveByNodes := excessiveByPackage * ratio
	return r.reluCalcShares(excessiveByNodes, activeGroups)
}

// reluCalcShares distributes the shares of total to each slot based on the current in use amount, e.g.
// [{0:100}, {1:150}, {2:80}] : current bandwidths of 3 numa nodes, the target deduction 40,
// [{0:  2}, {1: 38], {2: 0}] : the optimal share distributions of deduction 40,
// [{0: 98}, {1:112}, {2:80}] : the next in-use quotas,
// as the bar being noisy neighbor is ((100+150+80)-40) / 3 = 97; a more complicated example is
// [{4:110}, {3:160, 5:20}]: current bandwidths of 2 groups (including a 2-node group), total to decrease 20,
// [{4: 20}, {3:  0, 5: 0}], as the set [3,5] consumes no more than they are entitled to combined.
func (r reluAllocator) reluCalcShares(targetDeduction float64, useGroups GroupMBs) []map[int]float64 {
	fairAverage := useGroups.fairAverage(targetDeduction)
	noises := useGroups.sumNoises(fairAverage)

	// knowing the total noises (also the noise-makers), we should be able to decide the share to decrease
	shares := make([]map[int]float64, len(useGroups))
	for i, uses := range useGroups {
		// todo: consider punishing group exceeding its profiling value, since some pods use more mem bw than others reasonably.
		groupFairAmount := fairAverage * float64(len(uses))
		var groupCurrAmount float64
		for _, use := range uses {
			groupCurrAmount += use
		}
		if groupCurrAmount > groupFairAmount {
			// identify a noisy neighbor
			groupDeductions := (groupCurrAmount - groupFairAmount) / noises * targetDeduction
			// distribute further to the node in group
			shares[i] = r.reluDistribute(groupDeductions, fairAverage, uses)
		}
	}

	return shares
}

func (r reluAllocator) AllocateIncreases(throttleds GroupMB, activeGroupMBs GroupMBs, packageMB, mbThreshold float64) map[int]float64 {
	if packageMB > mbThreshold {
		return nil
	}

	// distribute the target increase among nodes under throttle
	// total MB below the threshold - to grant some nodes' a little more bandwidth
	// we take precautious and gradual steps when releasing the throttle
	underdoneByPackage := (mbThreshold - packageMB) / 2.0
	totalByNodes := activeGroupMBs.sum()
	ratio := totalByNodes / packageMB
	underdoneByNodes := underdoneByPackage * ratio

	return r.reluReversalCalcShares(underdoneByNodes, throttleds)
}

// to calc the reversal reLU shares among all nodes in the group
// the reversal reLU formula is y[i] = target * (1-weight[i]),
// whereas weight[i] = mb[i]/sum(mb)
func (r reluAllocator) reluReversalCalcShares(target float64, groupMBs GroupMB) map[int]float64 {
	var sumMB float64
	for _, mb := range groupMBs {
		sumMB += mb
	}

	shares := make(map[int]float64)
	for i, mb := range groupMBs {
		shares[i] = target / sumMB * (sumMB - mb)
	}

	return shares
}

// reluDistribute to distribute target values to each item in the group
// in linear proportional (reLU) way
func (r reluAllocator) reluDistribute(target, fairBar float64, groupMB GroupMB) map[int]float64 {
	var noises float64
	for _, nodeMB := range groupMB {
		if nodeMB > fairBar {
			noises += nodeMB - fairBar
		}
	}

	shares := make(map[int]float64)
	for i, use := range groupMB {
		if use > fairBar {
			shares[i] = (use - fairBar) * target / noises
		}
	}

	return shares
}

func NewAllocator() ShareAllocator {
	return &reluAllocator{}
}
