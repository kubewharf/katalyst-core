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

package provisionassembler

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// These benchmarks are the P0 performance safety net: they pin the cost of the
// reclaim-pool and whole-core-quantize algorithms so a refactor of the
// assembler cannot silently regress wall-clock per cycle. They are hermetic
// (no metaServer, dummy metaReader -> quota knob disabled) and deterministic.

func benchNonOverlapData() *reclaimPoolCalculationData {
	return &reclaimPoolCalculationData{
		shareAndIsolateDedicatedPoolSizes: map[string]int{
			"share-a": 40, "share-b": 25, "isolation": 10,
		},
		shareAndIsolatedDedicatedPoolAvailable: 128,
		totalUnusedNonReclaimablePinnedCPUSize: 6,
		dedicatedReclaimCoresSize:              8,
		reservedForReclaim:                     4,
		nodeEnableReclaim:                      true,
		numaID:                                 0,
		dedicatedInfo: regionInfo{
			requests:      map[string]int{"d1": 20, "d2": 14},
			requirements:  map[string]int{"d1": 12, "d2": 14},
			reclaimEnable: map[string]bool{"d1": true, "d2": true},
			podSet: map[string]types.PodSet{
				"d1": {"pod-1": sets.NewString("c1", "c2"), "pod-2": sets.NewString("c3")},
				"d2": {"pod-9": sets.NewString("c9")},
			},
		},
	}
}

func BenchmarkCalculateNonOverlapReclaimPool(b *testing.B) {
	pa := newSafetyNetAssembler()
	policy := reclaimPoolCalculationPolicy{allowDedicatedOverlap: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data := benchNonOverlapData()
		if _, _, _, err := pa.calculateNonOverlapReclaimPool(data, policy); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCalculateEnabledReclaimPool(b *testing.B) {
	pa := newSafetyNetAssembler()
	policy := reclaimPoolCalculationPolicy{allowSharedOverlap: true, allowDedicatedOverlap: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data := &reclaimPoolCalculationData{
			shareInfo: regionInfo{
				requirements:  map[string]int{"share-a": 20, "share-b": 15},
				reclaimEnable: map[string]bool{"share-a": true, "share-b": true},
			},
			dedicatedInfo: regionInfo{
				requirements:  map[string]int{"d1": 10},
				reclaimEnable: map[string]bool{"d1": true},
				podSet:        map[string]types.PodSet{"d1": {"pod-1": sets.NewString("c1")}},
			},
			shareAndIsolateDedicatedPoolSizes:      map[string]int{"share-a": 30, "share-b": 22, "d1": 18},
			dedicatedPoolSizes:                     map[string]int{"d1": 18},
			shareAndIsolatedDedicatedPoolAvailable: 96,
			reservedForReclaim:                     4,
			reserveHeldOutsidePools:                true,
		}
		if _, _, _, err := pa.calculateEnabledReclaimPool(data, policy); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuantizeDisjointReclaimTargetToWholeCore(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Mix aligned, odd and reserved-boundary inputs.
		_, _ = quantizeDisjointReclaimTargetToWholeCore(13, 4, 4)
		_, _ = quantizeDisjointReclaimTargetToWholeCore(7, 2, 2)
		_, _ = quantizeDisjointReclaimTargetToWholeCore(8, 2, 2)
		_, _ = quantizeDisjointReclaimTargetToWholeCore(1, 8, 4)
	}
}

// BenchmarkAssembleProvision_Representative exercises the full orchestration
// (validate -> reserve -> per-NUMA binding -> non-binding -> NUMA-exclusive ->
// backfill finalize) on a small two-NUMA, no-region node. It pins the
// end-to-end per-cycle cost and allocation profile.
func BenchmarkAssembleProvision_Representative(b *testing.B) {
	pa := newDefaultShareAssembler(b,
		map[int]int{0: 16, 1: 16}, // numaAvailable
		machine.NewCPUSet(0, 1),   // nonBindingNumas
		nil,                       // regionMap
		map[int]int{0: 2, 1: 2},   // reserved
		false,                     // allowShared
		true,                      // disableDedicated
		nil,                       // resource package config
	)
	ctx := pa.ctx()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := pa.AssembleProvision(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
