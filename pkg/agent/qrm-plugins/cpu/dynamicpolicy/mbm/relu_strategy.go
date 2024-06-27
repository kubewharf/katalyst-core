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

package mbm

// calcShares distributes the shares of total to each slot based on the current in use amount
// e.g. in uses [{0:100}, {1:150}, {2:80}], total 40, the optimal share distributions could be
//              [{0:  2}, {1: 38], {2: 0}], resulting the next in-use quotas
//              [{0: 98}, {1:112}, {2:80}] (the bar being noisy neighbour is ((100+150+80)-40) / 3 = 97
// a more complicated example is
//              [{4:110}, {3:160, 5:20}], total to decrease 20, the deduction share should be
//              [{4: 20}, {3:  0, 5: 0}], as the set [3,5] consumes no more than it is entitled to
func calcShares(targetDeduction float64, useGroups []map[int]float64) []map[int]float64 {
	fairAverage := getFairAverage(useGroups, targetDeduction)
	noises := sumNoises(useGroups, fairAverage)

	// knowing the total noises (also the noise-makers), we should be able to decide the share to decrease
	shares := make([]map[int]float64, len(useGroups))
	for i, uses := range useGroups {
		groupFairAmount := fairAverage * float64(len(uses))
		var groupCurrAmount float64
		for _, use := range uses {
			groupCurrAmount += use
		}
		if groupCurrAmount > groupFairAmount {
			// identify a noisy neighbor
			groupDeductions := (groupCurrAmount - groupFairAmount) / noises * targetDeduction
			// distribute further to the node in group
			shares[i] = distributeInGroup(groupDeductions, fairAverage, uses)
		}
	}

	return shares
}

// distributeInGroup to distribute target values to each item in the group
// in linear proportional (reLU) way
func distributeInGroup(target, fairBar float64, groupUses map[int]float64) map[int]float64 {
	var noises float64
	for _, use := range groupUses {
		if use > fairBar {
			noises += use - fairBar
		}
	}

	shares := make(map[int]float64)
	for i, use := range groupUses {
		if use > fairBar {
			shares[i] = (use - fairBar) * target / noises
		}
	}

	return shares
}

// getFairAverage gets the fair average per uint w/ consideration of the excessive
// the value (per unit) above which would be a noisy neighbor
func getFairAverage(useGroups []map[int]float64, excessive float64) float64 {
	sum := sumMB(useGroups)
	count := countNodes(useGroups)
	return (sum - excessive) / float64(count)
}

func sumMB(mbs []map[int]float64) float64 {
	var sum float64
	for _, groupMB := range mbs {
		for _, nodeMB := range groupMB {
			sum += nodeMB
		}
	}
	return sum
}

func countNodes(groupMBs []map[int]float64) int {
	var count int
	for _, groupMB := range groupMBs {
		count += len(groupMB)
	}
	return count
}

// sumNoises sums up the noises (over the fair average) of all noisy neighbors
func sumNoises(groupMBs []map[int]float64, fairAverage float64) float64 {
	var noises float64
	for _, uses := range groupMBs {
		groupFairAmount := fairAverage * float64(len(uses))
		var groupCurrAmount float64
		for _, use := range uses {
			groupCurrAmount += use
		}
		if groupCurrAmount > groupFairAmount {
			noises += groupCurrAmount - groupFairAmount
		}
	}
	return noises
}

func calcSharesInThrottleds(target float64, throttledMBs map[int]float64) map[int]float64 {
	var sumMB float64
	for _, mb := range throttledMBs {
		sumMB += mb
	}

	// the algorithm to allocate the share if y[i] = target * (1-weight[i]), whereas
	// weight[i] = mb[i]/sum(mb)
	shares := make(map[int]float64)
	for i, mb := range throttledMBs {
		shares[i] = target / sumMB * (sumMB - mb)
	}

	return shares
}
