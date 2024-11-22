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

package calculator

import (
	"context"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/state"
)

const (
	calculatorNameGreedy = "greedy"
)

type greedyCalculator struct{}

func NewGreedyCalculator() NUMABindingCalculator {
	return &greedyCalculator{}
}

func (g greedyCalculator) Name() string {
	return calculatorNameGreedy
}

func (g greedyCalculator) Run(_ context.Context) {}

func (g greedyCalculator) CalculateNUMABindingResult(current allocation.PodAllocations,
	numaAllocatable state.NUMAResource,
) (allocation.PodAllocations, bool, error) {
	sortedPods := make([]string, 0, len(current))
	result := current.Clone()
	numaAvailable := numaAllocatable.Clone()
	for uid, pod := range current {
		numaBinding := pod.BindingNUMA
		if numaBinding == -1 {
			sortedPods = append(sortedPods, uid)
			continue
		}

		numaAvailable[numaBinding].SubAllocation(pod)
	}

	// sort pods by cpu and memory request
	sort.SliceStable(sortedPods, func(i, j int) bool {
		if current[sortedPods[i]].CPUMilli != current[sortedPods[j]].CPUMilli {
			return current[sortedPods[i]].CPUMilli > current[sortedPods[j]].CPUMilli
		}

		return current[sortedPods[i]].Memory > current[sortedPods[j]].Memory
	})

	// sort available numa by cpu available and memory available
	// and the more available, the more preferred
	availableNUMAs := make([]int, 0, len(numaAvailable))
	for numaID := range numaAvailable {
		availableNUMAs = append(availableNUMAs, numaID)
	}
	sort.SliceStable(availableNUMAs, func(i, j int) bool {
		availableI := numaAvailable[availableNUMAs[i]]
		availableJ := numaAvailable[availableNUMAs[j]]
		if availableI.CPU != availableJ.CPU {
			return availableI.CPU > availableJ.CPU
		}

		if availableI.Memory != availableJ.Memory {
			return availableI.Memory > availableJ.Memory
		}

		return availableNUMAs[i] < availableNUMAs[j]
	})

	success := true
	for _, uid := range sortedPods {
		podSuccess := false
		for _, numaID := range availableNUMAs {
			if numaAvailable[numaID].IsSatisfied(current[uid]) {
				result[uid].BindingNUMA = numaID
				numaAvailable[numaID].SubAllocation(current[uid])
				podSuccess = true
				break
			}
		}
		if !podSuccess {
			success = false
		}
	}

	return result, success, nil
}
