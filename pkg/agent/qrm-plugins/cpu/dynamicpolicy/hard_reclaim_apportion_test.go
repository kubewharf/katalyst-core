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

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestApportionReclaimedPoolIsDeterministic pins map-iteration order: the
// allocation loop in apportionReclaimedPool takes CPUs out of the shared
// reclaimed set pool-by-pool, so the order pools are visited decides which pool
// owns which physical CPUs. Go ranges a map in random order, so without sorting
// the eligible pool names first, two runs over identical inputs can assign
// different CPUs to the same pool. This test runs the apportionment many times
// over fresh copies of the input and requires every run to yield the exact same
// per-pool result. It is RED against the unsorted map iteration and GREEN once
// the eligible pool names are sorted before the allocation loop.
func TestApportionReclaimedPoolIsDeterministic(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())

	// Two non-resident, non-binding pools. alpha owns two whole cores, beta one;
	// their different base sizes make the proportional take amounts differ, so
	// iteration order visibly changes which pool receives which CPUs.
	const poolAlpha = "share-alpha"
	const poolBeta = "share-beta"

	alphaBase := machine.NewCPUSet(0, 8, 1, 9) // two NUMA0 cores
	betaBase := machine.NewCPUSet(4, 12)       // one NUMA1 core
	nonBinding := map[string]int{
		poolAlpha: alphaBase.Size(),
		poolBeta:  betaBase.Size(),
	}

	// reclaimed holds every other whole core on both NUMAs (12 cpus, core-aligned).
	reclaimed := machine.NewCPUSet(
		2, 10, 3, 11, // NUMA0 cores 2,3
		5, 13, 6, 14, 7, 15, // NUMA1 cores 5,6,7
	)

	const runs = 40
	type runResult struct {
		alpha string
		beta  string
	}
	var first runResult
	results := make(map[runResult]int)

	for i := 0; i < runs; i++ {
		poolsCPUSet := map[string]machine.CPUSet{
			poolAlpha: alphaBase.Clone(),
			poolBeta:  betaBase.Clone(),
		}
		p := &DynamicPolicy{machine: machineComponent{machineInfo: &machine.KatalystMachineInfo{
			CPUTopology: topology,
		}}, reclaim: reclaimComponent{reservedReclaimedCPUsSize: 2}}
		_ = p.apportionReclaimedPool(poolsCPUSet, reclaimed.Clone(), nonBinding)
		r := runResult{
			alpha: poolsCPUSet[poolAlpha].String(),
			beta:  poolsCPUSet[poolBeta].String(),
		}
		results[r]++
		if i == 0 {
			first = r
		}
		require.Equal(t, first, r,
			"run %d: apportion result diverged (alpha=%s beta=%s) from first run (alpha=%s beta=%s); map iteration order is non-deterministic",
			i, r.alpha, r.beta, first.alpha, first.beta)
	}

	require.Len(t, results, 1,
		"expected a single deterministic apportionment across %d runs, got %d distinct outcomes", runs, len(results))
}

// TestApportionReclaimedPoolKeepsReclaimResidualCoreAligned pins the physical
// core isolation invariant on the reclaim-disabled apportion path. When
// reclaim is disabled we lend reclaimed cpus to the non-binding share pool, and
// the leftover reclaim residual must stay core-aligned so the primary and
// reclaim domains never share a physical core.
//
// Topology: 16 cpus / 2 sockets / 2 NUMAs, CPUsPerCore()==2.
//
//	NUMA0 cores {0,1,2,3} -> cpus {0,1,2,3} + siblings {8,9,10,11}
//	NUMA1 cores {4,5,6,7} -> cpus {4,5,6,7} + siblings {12,13,14,15}
//
// reclaimedCPUs holds three whole cores per NUMA (12 cpus, fully core-aligned),
// reservedReclaimedCPUsSize keeps a 2-cpu floor, so the share pool is lent
// 10 cpus. TakeHTByNUMABalance grabs cpus one-at-a-time round-robin across the
// two NUMAs, i.e. 5 cpus per NUMA — an odd per-NUMA amount that slices a
// physical core in half. The residual reclaim set is then {11,15}, two lone SMT
// siblings whose partners were lent away, violating core alignment.
//
// This test is RED against the current TakeHTByNUMABalance-based apportion: it
// documents the defect that reclaim residual can hold partial cores. It turns
// GREEN once apportion lends whole cores (core-aligned take) so the residual is
// always core-aligned.
func TestApportionReclaimedPoolKeepsReclaimResidualCoreAligned(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())

	p := &DynamicPolicy{machine: machineComponent{machineInfo: &machine.KatalystMachineInfo{
		CPUTopology: topology,
	}}, reclaim: reclaimComponent{reservedReclaimedCPUsSize: 2}}

	// share pool already owns one whole core; it is the only non-binding,
	// non-reclaimed pool so it receives the full apportioned amount.
	shareBase := machine.NewCPUSet(0, 8)
	poolsCPUSet := map[string]machine.CPUSet{
		commonstate.PoolNameShare: shareBase,
	}
	nonBindingPoolsQuantityMap := map[string]int{
		commonstate.PoolNameShare: shareBase.Size(),
	}

	// three whole cores per NUMA: NUMA0 cores {1,2,3}, NUMA1 cores {5,6,7}.
	reclaimedCPUs := machine.NewCPUSet(
		1, 2, 3, 9, 10, 11,
		5, 6, 7, 13, 14, 15,
	)
	require.NoError(t, assertCoreAligned(reclaimedCPUs, topology),
		"reclaim input must start core-aligned")

	residual := p.apportionReclaimedPool(poolsCPUSet, reclaimedCPUs, nonBindingPoolsQuantityMap)

	// invariant A: the reclaim residual must never hold a partial physical core.
	requireCoreAligned(t, topology, residual)

	// invariant B: cpus lent to the share pool must also be whole cores, so the
	// primary domain never gains a lone SMT sibling of a reclaim core.
	apportioned := poolsCPUSet[commonstate.PoolNameShare].Difference(shareBase)
	requireCoreAligned(t, topology, apportioned)
}
