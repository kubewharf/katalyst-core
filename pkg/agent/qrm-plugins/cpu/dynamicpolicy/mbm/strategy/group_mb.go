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

// GroupMB is type of node group, which keeps MB metrics of all nodes
type GroupMB map[int]float64

func (g GroupMB) sum() float64 {
	var sum float64
	for _, nodeMB := range g {
		sum += nodeMB
	}
	return sum
}

// sumNoises sums up the noises (over the fair average) of all noisy neighbors
func (g GroupMB) sumNoises(nodeFairAmount float64) float64 {
	groupFairAmount := nodeFairAmount * float64(len(g))

	var groupCurrAmount float64
	for _, nodeMB := range g {
		groupCurrAmount += nodeMB
	}

	if groupCurrAmount <= groupFairAmount {
		return 0
	}
	return groupCurrAmount - groupFairAmount
}

func (g GroupMB) NodeMBAverage() float64 {
	nodes := len(g)
	if nodes == 0 {
		return 0
	}

	return g.sum() / float64(nodes)
}

// GroupMBs type keeps multiple node groups of one package
type GroupMBs []GroupMB

func (mbs GroupMBs) sum() float64 {
	var sum float64
	for _, groupMB := range mbs {
		sum += groupMB.sum()
	}
	return sum
}

// sumNoises sums up the noises (over the fair average) of all noisy neighbors
func (mbs GroupMBs) sumNoises(nodeFairAmount float64) float64 {
	var noises float64
	for _, groupMB := range mbs {
		noises += groupMB.sumNoises(nodeFairAmount)
	}
	return noises
}

func (mbs GroupMBs) nodes() int {
	var count int
	for _, groupMB := range mbs {
		count += len(groupMB)
	}
	return count
}

// fairAverage gets the fair average per node w/ consideration of the excessive;
// the returned value (per node) can be used to check for noisy neighbor
func (mbs GroupMBs) fairAverage(excessive float64) float64 {
	sum := mbs.sum()
	nodes := mbs.nodes()
	return (sum - excessive) / float64(nodes)
}

func (mbs GroupMBs) NodeMBAverage() float64 {
	nodes := mbs.nodes()
	if nodes == 0 {
		return 0
	}

	return mbs.sum() / float64(nodes)
}
