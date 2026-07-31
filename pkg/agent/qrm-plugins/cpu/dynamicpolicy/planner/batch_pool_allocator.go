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

package planner

import (
	"fmt"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type BatchPoolAllocator struct{}

func NewBatchPoolAllocator() *BatchPoolAllocator {
	return &BatchPoolAllocator{}
}

func (a *BatchPoolAllocator) Allocate(
	domain machine.CPUSet,
	quantity map[string]int,
	historical map[string]machine.CPUSet,
) (map[string]machine.CPUSet, error) {
	poolNames, totalQuantity, err := sortedPoolNamesAndTotalQuantity(quantity)
	if err != nil {
		return nil, err
	}
	if totalQuantity > domain.Size() {
		return nil, fmt.Errorf("total pool quantity %d exceeds domain size %d", totalQuantity, domain.Size())
	}

	result := make(map[string]machine.CPUSet, len(poolNames))
	domainCPUs := domain.ToSliceInt()
	free := newFreeCPUMembership(domainCPUs)
	remaining := make(map[string]int, len(poolNames))
	historicalCandidates := buildHistoricalCandidates(poolNames, quantity, historical, domainCPUs)

	for _, poolName := range poolNames {
		result[poolName] = machine.NewCPUSet()
		remaining[poolName] = quantity[poolName]
		assignHistoricalCPUs(result[poolName], historicalCandidates[poolName], free, remaining, poolName)
	}

	nextFreeCPU := 0
	for _, poolName := range poolNames {
		nextFreeCPU = assignTopUpCPUs(result[poolName], domainCPUs, free, remaining, poolName, nextFreeCPU)
	}

	return result, nil
}

func sortedPoolNamesAndTotalQuantity(quantity map[string]int) ([]string, int, error) {
	poolNames := make([]string, 0, len(quantity))
	totalQuantity := 0
	for poolName, poolQuantity := range quantity {
		if poolQuantity < 0 {
			return nil, 0, fmt.Errorf("pool %q has negative quantity %d", poolName, poolQuantity)
		}

		poolNames = append(poolNames, poolName)
		totalQuantity += poolQuantity
	}
	sort.Strings(poolNames)

	return poolNames, totalQuantity, nil
}

func newFreeCPUMembership(domainCPUs []int) map[int]struct{} {
	free := make(map[int]struct{}, len(domainCPUs))
	for _, cpu := range domainCPUs {
		free[cpu] = struct{}{}
	}
	return free
}

func buildHistoricalCandidates(
	poolNames []string,
	quantity map[string]int,
	historical map[string]machine.CPUSet,
	domainCPUs []int,
) map[string][]int {
	candidates := make(map[string][]int, len(poolNames))
	for _, cpu := range domainCPUs {
		for _, poolName := range poolNames {
			if quantity[poolName] <= 0 || !historical[poolName].Contains(cpu) {
				continue
			}
			candidates[poolName] = append(candidates[poolName], cpu)
		}
	}
	return candidates
}

func assignHistoricalCPUs(
	assigned machine.CPUSet,
	candidates []int,
	free map[int]struct{},
	remaining map[string]int,
	poolName string,
) {
	for _, cpu := range candidates {
		if remaining[poolName] <= 0 {
			break
		}
		if _, isFree := free[cpu]; !isFree {
			continue
		}

		assigned.Add(cpu)
		delete(free, cpu)
		remaining[poolName]--
	}
}

func assignTopUpCPUs(
	assigned machine.CPUSet,
	domainCPUs []int,
	free map[int]struct{},
	remaining map[string]int,
	poolName string,
	nextFreeCPU int,
) int {
	for nextFreeCPU < len(domainCPUs) && remaining[poolName] > 0 {
		cpu := domainCPUs[nextFreeCPU]
		nextFreeCPU++
		if _, isFree := free[cpu]; !isFree {
			continue
		}

		assigned.Add(cpu)
		delete(free, cpu)
		remaining[poolName]--
	}

	return nextFreeCPU
}
