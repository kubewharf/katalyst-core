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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/state"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/bitmask"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	calculatorNameBackTracking = "backTracking"
)

const (
	metricNameAsyncCalculateTimeCost = "async_calculate_time_cost"
)

type backTrackingCalculator struct {
	emitter      metrics.MetricEmitter
	metaServer   *metaserver.MetaServer
	reservedCPUs machine.CPUSet
	maxNUMANum   int
	numaNodes    []int

	simpleCalculator NUMABindingCalculator

	mux               sync.RWMutex
	lastOptimalResult allocation.PodAllocations
}

func NewBackTrackingCalculator(
	_ *coreconfig.Configuration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	reservedCPUs machine.CPUSet,
) NUMABindingCalculator {
	return &backTrackingCalculator{
		emitter:    emitter,
		metaServer: metaServer,
		// todo: use numa num from conf
		maxNUMANum:       1,
		numaNodes:        metaServer.CPUDetails.NUMANodes().ToSliceInt(),
		reservedCPUs:     reservedCPUs,
		simpleCalculator: NewGreedyCalculator(),
	}
}

func (b *backTrackingCalculator) Name() string {
	return calculatorNameBackTracking
}

func (b *backTrackingCalculator) Run(ctx context.Context) {
	wait.UntilWithContext(ctx, b.sync, 30*time.Second)
}

func (b *backTrackingCalculator) getLastOptimalResult() allocation.PodAllocations {
	b.mux.RLock()
	defer b.mux.RUnlock()
	return b.lastOptimalResult
}

func (b *backTrackingCalculator) CalculateNUMABindingResult(current allocation.PodAllocations,
	numaAllocatable state.NUMAResource,
) (allocation.PodAllocations, bool, error) {
	lastOptimalResult := b.getLastOptimalResult()
	if lastOptimalResult == nil {
		return b.simpleCalculator.CalculateNUMABindingResult(current, numaAllocatable)
	}

	result := current.Clone()
	// if the pod is not in last optimal result, we should use a simple calculator
	for podUID, alloc := range result {
		if alloc.BindingNUMA != -1 {
			continue
		}
		if optimalResult, ok := lastOptimalResult[podUID]; ok {
			alloc.BindingNUMA = optimalResult.BindingNUMA
		}
	}

	return b.simpleCalculator.CalculateNUMABindingResult(result, numaAllocatable)
}

func (b *backTrackingCalculator) asyncCalculateNUMABindingResult(current allocation.PodAllocations,
	numaAllocatable state.NUMAResource,
) (allocation.PodAllocations, bool, error) {
	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		general.InfoS("async calculate numa result", "calculator", b.Name(), "costs", costs)
		_ = b.emitter.StoreInt64(metricNameAsyncCalculateTimeCost, costs.Microseconds(), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "calculator", Val: b.Name()})
	}()

	allNUMABindingResults, resultsIndex, err := b.getAllNUMABindingResults(current, numaAllocatable)
	if err != nil {
		general.Errorf("get numa allocation failed: %v", err)
		return nil, false, err
	}

	optimalResults := b.mergeNUMABindingResults(allNUMABindingResults, resultsIndex, current, numaAllocatable)
	podAllocation := current.Clone()
	for i, result := range optimalResults {
		uid := resultsIndex[i]
		pod := podAllocation[uid]
		if pod.BindingNUMA != -1 {
			continue
		}
		if result.numaNodeAffinity.Count() == 1 {
			pod.BindingNUMA = result.numaNodeAffinity.GetBits()[0]
		}
	}
	return podAllocation, len(optimalResults) > 0, nil
}

func (b *backTrackingCalculator) sync(ctx context.Context) {
	cpuState, memoryState, err := state.GetCPUMemoryReadonlyState()
	if err != nil {
		return
	}

	podAllocation, err := allocation.GetPodAllocations(ctx, b.metaServer, memoryState)
	if err != nil {
		general.Errorf("get numa allocation failed: %v", err)
		return
	}

	numaAllocatable, err := state.GetSharedNUMAAllocatable(
		b.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt(),
		b.reservedCPUs,
		cpuState, memoryState)
	if err != nil {
		general.Errorf("get numa allocation failed: %v", err)
		return
	}

	podAllocation, _, err = b.asyncCalculateNUMABindingResult(podAllocation, numaAllocatable)
	if err != nil {
		general.Errorf("calculate numa allocation failed: %v", err)
		return
	}

	b.mux.Lock()
	b.lastOptimalResult = podAllocation
	b.mux.Unlock()
}

func (b *backTrackingCalculator) getAllNUMABindingResults(podAllocation allocation.PodAllocations,
	numaAllocatable state.NUMAResource,
) ([][]numaBindingResult, map[int]string, error) {
	numaBindingNUMAs := b.getNUMABindingNUMAs(podAllocation)
	resultsIndex := make(map[int]string)
	allNUMABindingResults := make([][]numaBindingResult, 0, len(podAllocation))
	for podUID, alloc := range podAllocation {
		resultsIndex[len(allNUMABindingResults)] = podUID
		results, err := getNUMABindingResults(alloc, b.numaNodes, numaAllocatable, b.maxNUMANum, numaBindingNUMAs)
		if err != nil {
			general.Errorf("get numa allocation for %s failed: %v", alloc.String(), err)
			return nil, nil, err
		}
		allNUMABindingResults = append(allNUMABindingResults, results)
	}
	return allNUMABindingResults, resultsIndex, nil
}

func (b *backTrackingCalculator) mergeNUMABindingResults(results [][]numaBindingResult, index map[int]string,
	podAllocation allocation.PodAllocations, numaAllocatable state.NUMAResource,
) []numaBindingResult {
	var optimalResults []numaBindingResult
	optimalNUMACount := 0
	minNonNUMABindingCount := 0
	withAllPodNUMABindingResult := false
	iterateAllNUMABindingResults(results, func(results []numaBindingResult) {
		current := numaAllocatable.Clone()
		if b.maxNUMANum > 1 {
			maxNUMACount := 1
			maxNUMANodeAffinity, _ := bitmask.NewBitMask(1)
			for _, result := range results {
				if result.numaNodeAffinity.Count() > maxNUMACount {
					maxNUMACount = result.numaNodeAffinity.Count()
					maxNUMANodeAffinity = result.numaNodeAffinity
				}
			}

			if maxNUMACount > 1 {
				if withAllPodNUMABindingResult {
					return
				}

				totalAllocatable := state.Resource{}
				for i := 0; i < maxNUMACount; i++ {
					totalAllocatable.AddResource(numaAllocatable[i])
				}

				nonNUMABindingCount := 0
				for i, result := range results {
					if bitmask.And(result.numaNodeAffinity, maxNUMANodeAffinity).Count() != 0 {
						if !totalAllocatable.IsSatisfied(podAllocation[index[i]]) {
							return
						}
						totalAllocatable.SubAllocation(podAllocation[index[i]])
						nonNUMABindingCount += 1
					} else {
						numaID := result.numaNodeAffinity.GetBits()[0]
						if !current[numaID].IsSatisfied(podAllocation[index[i]]) {
							return
						}
						current[numaID].SubAllocation(podAllocation[index[i]])
					}
				}
				optimalNUMACount = len(b.numaNodes) - maxNUMACount
				if nonNUMABindingCount < minNonNUMABindingCount {
					minNonNUMABindingCount = nonNUMABindingCount
					optimalResults = deepCopyNUMABindingResults(results)
				}
				return
			}
		}

		numaMask := bitmask.NewEmptyBitMask()
		for i, result := range results {
			numaID := result.numaNodeAffinity.GetBits()[0]
			if !current[numaID].IsSatisfied(podAllocation[index[i]]) {
				return
			}
			numaMask.Or(result.numaNodeAffinity)
			current[numaID].SubAllocation(podAllocation[index[i]])
		}
		numaCount := numaMask.Count()
		if !withAllPodNUMABindingResult || numaCount < optimalNUMACount {
			optimalNUMACount = numaCount
			optimalResults = deepCopyNUMABindingResults(results)
			withAllPodNUMABindingResult = true
		}
	})
	return optimalResults
}

func (b *backTrackingCalculator) getNUMABindingNUMAs(podAllocation allocation.PodAllocations) sets.Int {
	numaSet := sets.NewInt()
	for _, alloc := range podAllocation {
		if alloc.BindingNUMA != -1 {
			numaSet.Insert(alloc.BindingNUMA)
		}
	}
	return numaSet
}

// numaBindingResult is a struct containing the numaNodeAffinity for a pod
type numaBindingResult struct {
	numaNodeAffinity bitmask.BitMask
}

func deepCopyNUMABindingResults(results []numaBindingResult) []numaBindingResult {
	c := make([]numaBindingResult, 0, len(results))
	for _, result := range results {
		c = append(c, result)
	}
	return c
}

func getNUMABindingResults(allocation *allocation.Allocation, numaNodes []int,
	numaAllocatable state.NUMAResource, maxNUMANum int, numaBindingNUMAs sets.Int,
) ([]numaBindingResult, error) {
	numaBindingResults := make([]numaBindingResult, 0, len(numaNodes))
	for _, n := range numaNodes {
		m, _ := bitmask.NewBitMask(n)
		if !numaAllocatable[n].IsSatisfied(allocation) {
			continue
		}

		if allocation.BindingNUMA != -1 && allocation.BindingNUMA != n {
			continue
		}

		numaBindingResults = append(numaBindingResults, numaBindingResult{
			numaNodeAffinity: m,
		})
	}

	if allocation.BindingNUMA != -1 {
		return numaBindingResults, nil
	}

	maxNUMANum = general.Min(maxNUMANum, len(numaNodes))
	if maxNUMANum > 1 {
		for i := 2; i < maxNUMANum; i++ {
			m := bitmask.NewEmptyBitMask()
			totalResource := state.Resource{}
			for j := 0; j < i; j++ {
				err := m.Add(j)
				if err != nil {
					return nil, err
				}
				totalResource.AddResource(numaAllocatable[j])
			}

			if numaBindingNUMAs.Intersection(sets.NewInt(m.GetBits()...)).Len() > 0 {
				continue
			}

			if !totalResource.IsSatisfied(allocation) {
				continue
			}

			numaBindingResults = append(numaBindingResults, numaBindingResult{
				numaNodeAffinity: m,
			})
		}
	}
	return numaBindingResults, nil
}

// Iterate over all permutations of hints in 'allNUMABindingResults [][]numaBindingResult'.
//
// This procedure is implemented as a recursive function over the set of results
// in 'allNUMABindingResults[i]'. It applies the function 'callback' to each
// permutation as it is found. It is the equivalent of:
//
// for i := 0; i < len(allNUMABindingResults[0]); i++
//
//	for j := 0; j < len(allNUMABindingResults[1]); j++
//	    for k := 0; k < len(allNUMABindingResults[2]); k++
//	        ...
//	        for z := 0; z < len(allNUMABindingResults[-1]); z++
//	            permutation := []numaBindingResult{
//	                allNUMABindingResults[0][i],
//	                allNUMABindingResults[1][j],
//	                allNUMABindingResults[2][k],
//	                ...
//	                allNUMABindingResults[-1][z]
//	            }
//	            callback(permutation)
func iterateAllNUMABindingResults(allNUMABindingResults [][]numaBindingResult, callback func([]numaBindingResult)) {
	// Internal helper function to accumulate the permutation before calling the callback.
	var iterate func(i int, accum []numaBindingResult)
	iterate = func(i int, accum []numaBindingResult) {
		// Base case: we have looped through all providers and have a full permutation.
		if i == len(allNUMABindingResults) {
			callback(accum)
			return
		}

		// Loop through all hints for provider 'i', and recurse to build the
		// the permutation of this hint with all hints from providers 'i++'.
		for j := range allNUMABindingResults[i] {
			iterate(i+1, append(accum, allNUMABindingResults[i][j]))
		}
	}
	iterate(0, []numaBindingResult{})
}
