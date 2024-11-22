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
	"math"
	"math/rand"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/state"
)

const (
	calculatorNameRandom = "random"
)

type randomCalculator struct {
	numOfRandomTime int
}

func NewRandomCalculator(numOfRandomTime int) NUMABindingCalculator {
	return &randomCalculator{
		numOfRandomTime: numOfRandomTime,
	}
}

func (g *randomCalculator) Name() string {
	return calculatorNameRandom
}

func (g *randomCalculator) Run(_ context.Context) {}

func (g *randomCalculator) CalculateNUMABindingResult(current allocation.PodAllocations,
	numaAllocatable state.NUMAResource,
) (allocation.PodAllocations, bool, error) {
	sortedPods := make([]string, 0, len(current))
	numaAvailable := numaAllocatable.Clone()
	for uid, pod := range current {
		numaBinding := pod.BindingNUMA
		if numaBinding == -1 {
			sortedPods = append(sortedPods, uid)
			continue
		}

		numaAvailable[numaBinding].SubAllocation(pod)
	}

	var (
		optimalResult      allocation.PodAllocations
		optimalFailedCount = math.MaxInt
	)

	for i := 0; i < g.numOfRandomTime; i++ {
		result := current.Clone()
		numaAvailable := numaAllocatable.Clone()
		rand.Shuffle(len(sortedPods), func(i, j int) {
			sortedPods[i], sortedPods[j] = sortedPods[j], sortedPods[i]
		})

		failedCount := 0
		for _, uid := range sortedPods {
			podSuccess := false
			for numaID := range numaAvailable {
				if numaAvailable[numaID].IsSatisfied(current[uid]) {
					result[uid].BindingNUMA = numaID
					numaAvailable[numaID].SubAllocation(current[uid])
					podSuccess = true
					break
				}
			}
			if !podSuccess {
				failedCount += 1
			}
		}

		if failedCount < optimalFailedCount {
			optimalFailedCount = failedCount
			optimalResult = result.Clone()
		}

		if optimalFailedCount == 0 {
			break
		}
	}

	return optimalResult, optimalFailedCount == 0, nil
}
