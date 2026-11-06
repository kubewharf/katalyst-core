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

package utils

import (
	"math/rand"
	"testing"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// snbCase describes one ramp-up container to drop into a ReadonlyState snapshot.
type snbCase struct {
	pod     string
	cpus    machine.CPUSet
	snb     bool
	rampUp  bool
	reclaim bool // reclaimed-cores QoS instead of shared
}

// buildSNBView assembles a snapshot from the given containers and a raw reclaim
// budget, then builds the partition view. It keeps a 128-CPU 4-NUMA SMT2
// topology and no reserve, so available == all CPUs.
func buildSNBView(t *testing.T, overlap bool, reclaimRaw string, cases ...snbCase) *model.DesiredView {
	t.Helper()
	topo := make4NUMASMTTopology()
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(overlap)
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.MustParse(reclaimRaw),
	})
	for _, c := range cases {
		ai := sharedRampUpAllocation(c.pod, c.cpus, c.snb)
		if !c.rampUp {
			ai.RampUp = false
		}
		if c.reclaim {
			ai.QoSLevel = apiconsts.PodAnnotationQoSLevelReclaimedCores
		}
		state.SetAllocationInfo(c.pod, ai.ContainerName, ai)
	}
	view, _ := BuildCPUSetPartitionView(state, topo, CPUSetPartitionViewOptions{HardPartitionEnabled: true})
	return view
}

// activeSNBUnion returns the exact union of CPUs held by every case that matches
// the active-SNB predicate (RampUp && Shared && SharedNUMABinding && !Reclaimed
// && nonempty). It is the independent oracle the view must equal.
func activeSNBUnion(cases ...snbCase) machine.CPUSet {
	union := machine.NewCPUSet()
	for _, c := range cases {
		if c.snb && c.rampUp && !c.reclaim && !c.cpus.IsEmpty() {
			union = union.Union(c.cpus)
		}
	}
	return union
}

// TestSNBProtectionBoundaryMatrix exercises the predicate gates, the overlap
// gate, transient lifecycle (add/remove/materialize), the Desired-vs-physical
// reclaim split, and the ordinary-shared-is-unaffected property as a fixed
// boundary matrix.
func TestSNBProtectionBoundaryMatrix(t *testing.T) {
	t.Parallel()
	const all = "0-127"

	// (1) single active SNB, raw reclaim covers it: desired keeps it as a reclaim
	// target, physical reclaim trims it away.
	c1 := []snbCase{{pod: "a", cpus: machine.MustParse("4-5,68-69"), snb: true, rampUp: true}}
	v1 := buildSNBView(t, false, all, c1...)
	want1 := activeSNBUnion(c1...)
	assertCPUSet(t, "matrix transient protected == active SNB union", v1.TransientProtectedNonReclaim, want1.String())
	if !want1.IsSubsetOf(v1.DesiredReclaimEffective) {
		t.Fatalf("active SNB %s must remain in DesiredReclaimEffective (SA target)", want1)
	}
	if overlap := v1.ReclaimEffective.Intersection(want1); !overlap.IsEmpty() {
		t.Fatalf("active SNB %s leaked into physical ReclaimEffective: %s", want1, overlap)
	}
	if !v1.ReclaimEffective.IsSubsetOf(v1.DesiredReclaimEffective) {
		t.Fatalf("physical reclaim %s must be a subset of desired reclaim %s", v1.ReclaimEffective, v1.DesiredReclaimEffective)
	}

	// (2) multiple active SNB containers union exactly, no double count.
	c2 := []snbCase{
		{pod: "a", cpus: machine.MustParse("4-5"), snb: true, rampUp: true},
		{pod: "b", cpus: machine.MustParse("68-69"), snb: true, rampUp: true},
		{pod: "c", cpus: machine.MustParse("100-101"), snb: true, rampUp: true},
	}
	v2 := buildSNBView(t, false, all, c2...)
	assertCPUSet(t, "matrix multi-SNB union", v2.TransientProtectedNonReclaim, activeSNBUnion(c2...).String())

	// (3) lifecycle: materialized (RampUp=false) and reclaimed-cores SNB variants
	// drop out of the protected set.
	c3 := []snbCase{
		{pod: "active", cpus: machine.MustParse("4-5"), snb: true, rampUp: true},
		{pod: "steady", cpus: machine.MustParse("68-69"), snb: true, rampUp: false},
		{pod: "reclaimed", cpus: machine.MustParse("100-101"), snb: true, rampUp: true, reclaim: true},
		{pod: "empty", cpus: machine.NewCPUSet(), snb: true, rampUp: true},
	}
	v3 := buildSNBView(t, false, all, c3...)
	assertCPUSet(t, "matrix only active non-reclaimed nonempty SNB protected", v3.TransientProtectedNonReclaim, "4-5")

	// (4) ordinary (non-SNB) shared ramp-up is NOT transiently protected: it
	// projects onto the shared domain instead of pinning exact CPUs.
	c4 := []snbCase{
		{pod: "plain", cpus: machine.MustParse("4-5"), snb: false, rampUp: true},
	}
	if v4 := buildSNBView(t, false, all, c4...); !v4.TransientProtectedNonReclaim.IsEmpty() {
		t.Fatalf("ordinary shared ramp-up must not be transient protected, got %s", v4.TransientProtectedNonReclaim)
	}

	// (5) overlap=true disables the transient protection entirely.
	if v5 := buildSNBView(t, true, all, c1...); !v5.TransientProtectedNonReclaim.IsEmpty() {
		t.Fatalf("overlap=true must disable transient SNB protection, got %s", v5.TransientProtectedNonReclaim)
	}

	// (6) disjoint physical reclaim: when raw reclaim does not cover the SNB CPUs,
	// the physical reclaim must still stay disjoint from them (the protection is
	// a trim, never an overlap).
	c6 := []snbCase{{pod: "a", cpus: machine.MustParse("4-5"), snb: true, rampUp: true}}
	v6 := buildSNBView(t, false, "0-3", c6...)
	if overlap := v6.ReclaimEffective.Intersection(machine.MustParse("4-5")); !overlap.IsEmpty() {
		t.Fatalf("physical reclaim must be disjoint from SNB CPUs, overlap=%s", overlap)
	}
}

// FuzzBuildCPUSetPartitionViewActiveSNBInvariants grows a randomized number of
// disjoint active-SNB containers (plus occasional decoys) and checks the
// invariants that must hold for every revision:
//
//   - TransientProtectedNonReclaim is exactly the active-SNB predicate union
//     (no reserve, so no availability trim);
//   - the physical ReclaimEffective is always disjoint from that union while the
//     DesiredReclaimEffective keeps it as a reclaim target;
//   - ReclaimEffective is a subset of DesiredReclaimEffective (TPNR only shrinks);
//   - overlap mode yields an empty transient set.
func FuzzBuildCPUSetPartitionViewActiveSNBInvariants(f *testing.F) {
	f.Add(int64(1))
	f.Add(int64(7))
	f.Add(int64(42))
	f.Fuzz(func(t *testing.T, seed int64) {
		rng := rand.New(rand.NewSource(seed))

		const numPods = 8
		const block = 4 // CPUs per container
		cases := make([]snbCase, 0, numPods)
		cursor := 0
		expected := machine.NewCPUSet()
		for i := 0; i < numPods; i++ {
			if cursor+block > 128 {
				break
			}
			cpus := machine.NewCPUSet()
			for c := 0; c < block; c++ {
				cpus.Add(cursor + c)
			}
			cursor += block
			// Randomly decide whether this slot is an active-SNB (1/2), an
			// ordinary shared ramp-up decoy (1/4), or a steady (non-ramp-up) decoy
			// (1/4). Each container gets a unique pod UID so SetAllocationInfo
			// does not overwrite earlier ones.
			pod := string(rune('a' + i))
			switch rng.Intn(4) {
			case 0, 1:
				c := snbCase{pod: pod, cpus: cpus, snb: true, rampUp: true}
				cases = append(cases, c)
				expected = expected.Union(cpus)
			case 2:
				cases = append(cases, snbCase{pod: pod, cpus: cpus, snb: false, rampUp: true})
			default:
				cases = append(cases, snbCase{pod: pod, cpus: cpus, snb: true, rampUp: false})
			}
		}

		// overlap=false: full protection contract.
		view := buildSNBView(t, false, "0-127", cases...)
		if !view.TransientProtectedNonReclaim.Equals(expected) {
			t.Fatalf("seed=%d: transient protected = %s, want active-SNB union %s",
				seed, view.TransientProtectedNonReclaim, expected)
		}
		if overlap := view.ReclaimEffective.Intersection(expected); !overlap.IsEmpty() {
			t.Fatalf("seed=%d: active SNB %s leaked into physical reclaim %s", seed, expected, view.ReclaimEffective)
		}
		if !expected.IsSubsetOf(view.DesiredReclaimEffective) {
			t.Fatalf("seed=%d: active SNB %s must stay in DesiredReclaimEffective", seed, expected)
		}
		if !view.ReclaimEffective.IsSubsetOf(view.DesiredReclaimEffective) {
			t.Fatalf("seed=%d: physical reclaim %s not subset of desired %s", seed, view.ReclaimEffective, view.DesiredReclaimEffective)
		}

		// overlap=true: protection silent.
		if ov := buildSNBView(t, true, "0-127", cases...); !ov.TransientProtectedNonReclaim.IsEmpty() {
			t.Fatalf("seed=%d: overlap mode must yield empty transient protected, got %s", seed, ov.TransientProtectedNonReclaim)
		}
	})
}

// BenchmarkBuildCPUSetPartitionViewActiveSNBScaling measures the cost of building
// the partition view as the number of concurrently active SNB ramp-up containers
// grows. The active-SNB union and transient protection must stay linear and
// allocation-cheap relative to the 128-CPU topology.
func BenchmarkBuildCPUSetPartitionViewActiveSNBScaling(b *testing.B) {
	for _, n := range []int{1, 4, 16} {
		cases := make([]snbCase, 0, n)
		for i := 0; i < n; i++ {
			base := i * 8
			cases = append(cases, snbCase{
				pod:    string(rune('a' + i)),
				cpus:   machine.NewCPUSet(base, base+1),
				snb:    true,
				rampUp: true,
			})
		}
		topo := make4NUMASMTTopology()
		b.Run(subName(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				state := cpustate.NewCPUPluginState(nil)
				state.SetAllowSharedCoresOverlapReclaimedCores(false)
				state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult: machine.MustParse("0-127"),
				})
				for _, c := range cases {
					state.SetAllocationInfo(c.pod, "main", sharedRampUpAllocation(c.pod, c.cpus, c.snb))
				}
				view, _ := BuildCPUSetPartitionView(state, topo, CPUSetPartitionViewOptions{HardPartitionEnabled: true})
				if view.ReclaimEffective.IsEmpty() {
					b.Fatal("unexpected empty reclaim effective")
				}
			}
		})
	}
}

func subName(n int) string {
	if n == 1 {
		return "1"
	}
	if n == 4 {
		return "4"
	}
	return "16"
}
