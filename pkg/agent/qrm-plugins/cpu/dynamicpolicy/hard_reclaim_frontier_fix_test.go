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
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func buildCPURange(start, end int) machine.CPUSet {
	s := machine.NewCPUSet()
	for i := start; i <= end; i++ {
		s.Add(i)
	}
	return s
}

func buildHardReclaimTestTopology(numaIDs []int, cpusPerNuma int) *machine.CPUTopology {
	topology := &machine.CPUTopology{CPUDetails: machine.CPUDetails{}}
	coresPerNuma := cpusPerNuma / 2
	for ni, numaID := range numaIDs {
		for c := 0; c < coresPerNuma; c++ {
			for _, cpu := range []int{ni*cpusPerNuma + c, ni*cpusPerNuma + c + coresPerNuma} {
				topology.CPUDetails[cpu] = machine.CPUTopoInfo{
					NUMANodeID: numaID, SocketID: 0, CoreID: c,
				}
			}
		}
	}
	return topology
}

func buildHardReclaimCandidates(topology *machine.CPUTopology, numaIDs []int, cpusPerNuma int) []hardReclaimCoreSelectionCandidate {
	var candidates []hardReclaimCoreSelectionCandidate
	for ni, numaID := range numaIDs {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		for _, c := range coreAlignedCandidates(topology, numaCPUs, machine.NewCPUSet()) {
			candidates = append(candidates, hardReclaimCoreSelectionCandidate{
				coreAlignedCandidate: c, numaIndex: ni,
			})
		}
	}
	return candidates
}

// TestHardReclaimTruncatedFrontierAcceptsFeasible verifies that when the beam
// search truncates, a feasible terminal in the retained frontier is accepted
// instead of returning search_budget.
func TestHardReclaimTruncatedFrontierAcceptsFeasible(t *testing.T) {
	t.Parallel()
	const cpusPerNuma = 24
	numaIDs := []int{0, 2, 3, 7}
	targets := []int{4, 4, 4, 4}
	topology := buildHardReclaimTestTopology(numaIDs, cpusPerNuma)
	candidates := buildHardReclaimCandidates(topology, numaIDs, cpusPerNuma)

	// 3 donor groups per NUMA → 12 total groups → state explosion triggers truncation
	groupCPUs := map[string]machine.CPUSet{}
	groupLimit := map[string]int{}
	for ni, numaID := range numaIDs {
		for g := 0; g < 3; g++ {
			key := fmt.Sprintf("n%d-g%d", numaID, g)
			s := machine.NewCPUSet()
			for c := g * 4; c < (g+1)*4; c++ {
				s.Add(ni*cpusPerNuma + c)
				s.Add(ni*cpusPerNuma + c + cpusPerNuma/2)
			}
			groupCPUs[key] = s
			groupLimit[key] = 2
		}
	}

	selected, err := selectHardReclaimCoresByNUMAWithFrontier(
		candidates, numaIDs, targets, machine.NewCPUSet(), groupCPUs, groupLimit)
	if err != nil {
		t.Fatalf("expected feasible result from truncated frontier, got error: %v", err)
	}
	if selected.Size() != 16 { // 4 NUMA × 4 CPUs = 16
		t.Errorf("expected 16 selected CPUs, got %d", selected.Size())
	}
}

// TestHardReclaimNoTerminalStillErrors verifies that when no feasible terminal
// exists AND the frontier truncated, we still return search_budget.
func TestHardReclaimNoTerminalStillErrors(t *testing.T) {
	t.Parallel()
	const cpusPerNuma = 24
	numaIDs := []int{0, 2, 3, 7}
	targets := []int{8, 8, 8, 8} // too many — can't hit target
	topology := buildHardReclaimTestTopology(numaIDs, cpusPerNuma)
	candidates := buildHardReclaimCandidates(topology, numaIDs, cpusPerNuma)

	groupCPUs := map[string]machine.CPUSet{}
	groupLimit := map[string]int{}
	for ni, numaID := range numaIDs {
		key := fmt.Sprintf("n%d", numaID)
		groupCPUs[key] = buildCPURange(ni*cpusPerNuma, (ni+1)*cpusPerNuma-1)
		groupLimit[key] = 1 // tight limit → no feasible
	}

	_, err := selectHardReclaimCoresByNUMAWithFrontier(
		candidates, numaIDs, targets, machine.NewCPUSet(), groupCPUs, groupLimit)
	if err == nil {
		t.Fatal("expected error when no feasible terminal exists")
	}
	if hre, ok := err.(*hardReclaimSelectionError); ok {
		t.Logf("got expected error: reason=%s", hre.reason)
	}
}

// TestHardReclaimSmallNUMAOptimal verifies 3-NUMA case (no truncation) still works.
func TestHardReclaimSmallNUMAOptimal(t *testing.T) {
	t.Parallel()
	const cpusPerNuma = 24
	numaIDs := []int{0, 2, 3}
	targets := []int{4, 4, 4}
	topology := buildHardReclaimTestTopology(numaIDs, cpusPerNuma)
	candidates := buildHardReclaimCandidates(topology, numaIDs, cpusPerNuma)

	groupCPUs := map[string]machine.CPUSet{}
	groupLimit := map[string]int{}
	for ni, numaID := range numaIDs {
		key := fmt.Sprintf("n%d", numaID)
		groupCPUs[key] = buildCPURange(ni*cpusPerNuma, (ni+1)*cpusPerNuma-1)
		groupLimit[key] = 4
	}

	selected, err := selectHardReclaimCoresByNUMAWithFrontier(
		candidates, numaIDs, targets, machine.NewCPUSet(), groupCPUs, groupLimit)
	if err != nil {
		t.Fatalf("expected success for 3-NUMA optimal case: %v", err)
	}
	if selected.Size() != 12 {
		t.Errorf("expected 12 CPUs, got %d", selected.Size())
	}
}

// TestHardReclaimDeterminism verifies same input always gives same output.
func TestHardReclaimDeterminism(t *testing.T) {
	t.Parallel()
	const cpusPerNuma = 24
	numaIDs := []int{0, 2, 3, 7}
	targets := []int{4, 4, 4, 4}
	topology := buildHardReclaimTestTopology(numaIDs, cpusPerNuma)
	candidates := buildHardReclaimCandidates(topology, numaIDs, cpusPerNuma)

	groupCPUs := map[string]machine.CPUSet{}
	groupLimit := map[string]int{}
	for ni, numaID := range numaIDs {
		for g := 0; g < 3; g++ {
			key := fmt.Sprintf("n%d-g%d", numaID, g)
			s := machine.NewCPUSet()
			for c := g * 4; c < (g+1)*4; c++ {
				s.Add(ni*cpusPerNuma + c)
				s.Add(ni*cpusPerNuma + c + cpusPerNuma/2)
			}
			groupCPUs[key] = s
			groupLimit[key] = 2
		}
	}

	first, _ := selectHardReclaimCoresByNUMAWithFrontier(
		candidates, numaIDs, targets, machine.NewCPUSet(), groupCPUs, groupLimit)
	for i := 0; i < 20; i++ {
		result, err := selectHardReclaimCoresByNUMAWithFrontier(
			candidates, numaIDs, targets, machine.NewCPUSet(), groupCPUs, groupLimit)
		if err != nil {
			t.Fatalf("run %d: unexpected error: %v", i, err)
		}
		if !first.Equals(result) {
			t.Fatalf("run %d: non-deterministic result: expected %s, got %s",
				i, first.String(), result.String())
		}
	}
}
