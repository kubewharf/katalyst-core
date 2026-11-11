/*
Copyright 2026 The Katalyst Authors.

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

package dynamicpolicy

import (
	"fmt"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const partitionCoreFloorSolveBudget = 64

type partitionCoreFloorConstraint struct {
	demandKey        string
	committedBlockID string
}

// solveDisjointPartitionsWithCoreFloors keeps complete-core requirements inside
// the joint feasibility solve. The ordinary solver remains the fast path. Only
// a fragmented floor result triggers deterministic, bounded candidate pinning.
func solveDisjointPartitionsWithCoreFloors(
	demands []partitionDemand,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
) (map[string]machine.CPUSet, error) {
	if len(floors) == 0 {
		return solveDisjointPartitions(demands, topology)
	}

	sortedFloors := append([]partitionCoreFloorConstraint(nil), floors...)
	sort.Slice(sortedFloors, func(i, j int) bool {
		return sortedFloors[i].demandKey < sortedFloors[j].demandKey
	})

	attempts := 0
	assignments, err := solveDisjointPartitionsWithPinnedCoreFloors(
		demands, sortedFloors, topology, &attempts)
	if err != nil {
		return nil, err
	}
	return assignments, nil
}

func solveDisjointPartitionsWithPinnedCoreFloors(
	demands []partitionDemand,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
	attempts *int,
) (map[string]machine.CPUSet, error) {
	if *attempts >= partitionCoreFloorSolveBudget {
		return nil, fmt.Errorf(
			"partition core-floor solve budget %d exhausted", partitionCoreFloorSolveBudget)
	}
	*attempts++

	assignments, err := solveDisjointPartitions(demands, topology)
	if err != nil {
		return nil, err
	}

	demandIndexByKey := make(map[string]int, len(demands))
	for i := range demands {
		demandIndexByKey[demands[i].key] = i
	}
	for _, floor := range floors {
		demandIndex, found := demandIndexByKey[floor.demandKey]
		if !found {
			return nil, fmt.Errorf("partition core-floor demand %q is missing", floor.demandKey)
		}
		demand := demands[demandIndex]
		if demand.quantity <= 0 {
			return nil, fmt.Errorf(
				"partition core-floor demand %q has non-positive quantity %d",
				floor.demandKey, demand.quantity)
		}
		assigned := assignments[floor.demandKey]
		if assigned.IsEmpty() {
			return nil, fmt.Errorf(
				"partition core-floor demand %q has empty assignment", floor.demandKey)
		}
		if assertCoreAligned(assigned, topology) == nil {
			continue
		}

		candidates := coreAlignedCandidates(topology, demand.eligible, demand.preferred)
		assignedToOtherDemands := machine.NewCPUSet()
		for key, cpus := range assignments {
			if key != floor.demandKey {
				assignedToOtherDemands = assignedToOtherDemands.Union(cpus)
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			iConflicts := candidates[i].cpus.Intersection(assignedToOtherDemands).Size()
			jConflicts := candidates[j].cpus.Intersection(assignedToOtherDemands).Size()
			return iConflicts < jConflicts
		})
		var lastErr error
		for _, candidate := range candidates {
			constrained := append([]partitionDemand(nil), demands...)
			constrained[demandIndex].eligible = candidate.cpus
			constrained[demandIndex].preferred = candidate.cpus
			result, solveErr := solveDisjointPartitionsWithPinnedCoreFloors(
				constrained, floors, topology, attempts)
			if solveErr == nil {
				return result, nil
			}
			lastErr = solveErr
			if *attempts >= partitionCoreFloorSolveBudget {
				break
			}
		}
		if *attempts >= partitionCoreFloorSolveBudget {
			return nil, fmt.Errorf(
				"partition core-floor solve budget %d exhausted while constraining demand %q",
				partitionCoreFloorSolveBudget, floor.demandKey)
		}
		if lastErr != nil {
			return nil, fmt.Errorf(
				"partition core-floor demand %q has no feasible complete core: %w",
				floor.demandKey, lastErr)
		}
		return nil, fmt.Errorf(
			"partition core-floor demand %q has no complete-core candidate", floor.demandKey)
	}
	return assignments, nil
}
