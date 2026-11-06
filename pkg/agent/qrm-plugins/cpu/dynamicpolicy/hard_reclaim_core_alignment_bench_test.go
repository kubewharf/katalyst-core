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
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newBenchmarkTopology builds a representative 2-socket / 2-NUMA SMT node
// (128 logical CPUs = 32 cores/socket * 2 threads). This matches the shape of a
// real production node so the per-call cost of rebuilding the physical-core map
// is measured at production scale, not on a toy 4-CPU topology.
func newBenchmarkTopology(b *testing.B) *machine.CPUTopology {
	b.Helper()
	topology, err := machine.GenerateDummyCPUTopology(128, 2, 2)
	if err != nil {
		b.Fatalf("generate topology: %v", err)
	}
	return topology
}

// BenchmarkPhysicalCoreCPUsSingle measures the cost of one full rebuild of the
// numa/socket/core -> CPUSet index. Every coreAlignedCandidates / assertCoreAligned
// call pays this once, so this is the unit cost being multiplied across loops.
func BenchmarkPhysicalCoreCPUsSingle(b *testing.B) {
	topology := newBenchmarkTopology(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = newPhysicalCoreIndex(topology)
	}
}

// BenchmarkPlannerLoopRebuild mirrors expandSteadyFakeNUMAReclaimPhase: for each
// eligible NUMA the planner calls coreAlignedCandidates AND
// completePreferredCoreCPUCount, each of which rebuilds the whole physical-core
// map from scratch. With 2 NUMAs that is 4 full rebuilds per planning round.
func BenchmarkPlannerLoopRebuild(b *testing.B) {
	topology := newBenchmarkTopology(b)
	all := topology.CPUDetails.CPUs()
	numaIDs := topology.CPUDetails.NUMANodes().ToSliceInt()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, numaID := range numaIDs {
			numaCPUs := all.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
			_ = coreAlignedCandidates(topology, numaCPUs, numaCPUs)
			_ = completePreferredCoreCPUCount(topology, numaCPUs, numaCPUs)
		}
	}
}

// BenchmarkPlannerLoopCached measures the same work but with the physical-core
// index built once and reused, isolating the cost of the redundant rebuilds.
func BenchmarkPlannerLoopCached(b *testing.B) {
	topology := newBenchmarkTopology(b)
	all := topology.CPUDetails.CPUs()
	numaIDs := topology.CPUDetails.NUMANodes().ToSliceInt()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := newPhysicalCoreIndex(topology)
		for _, numaID := range numaIDs {
			numaCPUs := all.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
			_ = coreAlignedCandidatesWithIndex(index, numaCPUs, numaCPUs)
			_ = completePreferredCoreCPUCountWithIndex(index, numaCPUs, numaCPUs)
		}
	}
}
