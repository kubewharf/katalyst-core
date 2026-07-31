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

package topology

import (
	"context"
	"reflect"
	"syscall"
	"testing"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDomainPhasePipelineRebuildsPlanFromFreshSnapshot(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.cpus["reclaim"] = machine.NewCPUSet(1)

	targets := map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(1),
		"reclaim": machine.NewCPUSet(0),
	}
	pipeline := newDomainPhasePipeline(
		dag,
		cg,
		targets,
		machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
		},
		machine.NewCPUSet(),
		newApplyCache(cg, "primary"),
	)

	first, err := pipeline.nextRound(context.Background())
	if err != nil {
		t.Fatalf("first nextRound: %v", err)
	}
	if got := len(first.plan.expandPrimary); got != 1 {
		t.Fatalf("first expandPrimary len = %d, want 1", got)
	}
	if got := first.plan.expandPrimary[0].crossDomainEntering; !got.Equals(machine.NewCPUSet(1)) {
		t.Fatalf("first primary crossDomainEntering = %s, want 1", got.String())
	}
	if got, want := first.gate.pendingToPrimary, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("first pendingToPrimary = %s, want %s", got.String(), want.String())
	}

	cg.cpus["reclaim"] = machine.NewCPUSet()
	cg.cpus["primary"] = machine.NewCPUSet()
	second, err := pipeline.nextRound(context.Background())
	if err != nil {
		t.Fatalf("second nextRound: %v", err)
	}
	if got := len(second.plan.expandPrimary); got != 1 {
		t.Fatalf("second expandPrimary len = %d, want 1 after fresh snapshot", got)
	}
	if got := second.plan.expandPrimary[0].crossDomainEntering; !got.IsEmpty() {
		t.Fatalf("second primary crossDomainEntering = %s, want empty after fresh snapshot", got.String())
	}
	if !second.gate.pendingToPrimary.IsEmpty() {
		t.Fatalf("second pendingToPrimary = %s, want empty after reclaim released", second.gate.pendingToPrimary.String())
	}
	if got, want := second.snapshot.observedReclaimDomain, machine.NewCPUSet(); !got.Equals(want) {
		t.Fatalf("second observedReclaimDomain = %s, want empty", got.String())
	}
}

func TestDomainPhaseExecutorDrainReclaimNUMABucketRemovesOnlyCrossDomainLeaving(t *testing.T) {
	t.Parallel()

	// A reclaim NUMA bucket that is shrinking to hand CPUs to the primary
	// domain while also being scheduled to receive new CPUs later. The drain
	// phase must only drop crossDomainLeaving CPUs and must never pull in the
	// crossDomainEntering CPUs (33-39,81-87), which the parent kubesandbox does
	// not yet contain at drain time. Writing the final target here would exceed
	// the parent and fail with EACCES on cgroup v1.
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(5, 6, 7, 33, 34, 35, 36, 37, 38, 39, 53, 54, 55, 81, 82, 83, 84, 85, 86, 87), Mems: "0-1"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(33, 34, 35, 36, 37, 38, 39, 81, 82, 83, 84, 85, 86, 87), Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	bucket := dag.index["kubesandbox/reclaimed-1"]
	observed := machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	target := machine.NewCPUSet(33, 34, 35, 36, 37, 38, 39, 81, 82, 83, 84, 85, 86, 87)
	transition := nodeTransition{
		node:                bucket,
		domain:              cpusetDomainReclaim,
		observed:            observed,
		target:              target,
		entering:            target.Difference(observed),
		crossDomainEntering: machine.NewCPUSet(33, 34, 35, 36, 37, 38, 39, 81, 82, 83, 84, 85, 86, 87),
		crossDomainLeaving:  machine.NewCPUSet(29, 30, 31, 73, 74, 75),
	}

	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	// Parent is still mid-shrink: it holds the observed union but not the
	// entering CPUs that will be transferred from the primary domain later.
	cg.cpus["kubesandbox"] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1"] = observed
	// A live (dynamic) child pins CPUs that survive the drain.
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(5, 6, 7, 53, 54, 55)
	cg.children["kubesandbox/reclaimed-1"] = []string{"sandbox022"}

	res := DAGApplyResult{}
	executor := newDomainPhaseExecutor(newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"kubesandbox":             machine.NewCPUSet(5, 6, 7, 33, 34, 35, 36, 37, 38, 39, 53, 54, 55, 81, 82, 83, 84, 85, 86, 87),
		"kubesandbox/reclaimed-1": target,
	}, "0-1", &res))

	drain, err := executor.executeDrainPhase(cpusetDomainReclaim, []nodeTransition{transition})
	if err != nil {
		t.Fatalf("executeDrainPhase: %v writes=%#v", err, cg.writes)
	}
	if got, want := drain.release, machine.NewCPUSet(29, 30, 31, 73, 74, 75); !got.Equals(want) {
		t.Fatalf("drain release = %s, want %s", got.String(), want.String())
	}
	// The bucket must end up at observed minus crossDomainLeaving, keeping the
	// live child union and never introducing the entering CPUs.
	wantBucket := machine.NewCPUSet(5, 6, 7, 53, 54, 55)
	if got := cg.cpus["kubesandbox/reclaimed-1"]; !got.Equals(wantBucket) {
		t.Fatalf("bucket cpuset after drain = %s, want %s", got.String(), wantBucket.String())
	}
	// No write may include the cross-domain entering CPUs during drain.
	entering := machine.NewCPUSet(33, 34, 35, 36, 37, 38, 39, 81, 82, 83, 84, 85, 86, 87)
	for _, w := range cg.writes {
		if w.cpus == "" {
			continue
		}
		if !machine.MustParse(w.cpus).Intersection(entering).IsEmpty() {
			t.Fatalf("drain wrote entering CPUs into rel=%s cpus=%s; writes=%#v", w.rel, w.cpus, cg.writes)
		}
	}
}

func TestDomainPhaseExecutorDrainParentDoesNotExpandControlledReclaimChildToFinalTarget(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0", Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	parent := dag.index["kubesandbox"]
	transition := nodeTransition{
		node:                parent,
		domain:              cpusetDomainReclaim,
		observed:            machine.NewCPUSet(0, 1),
		target:              machine.NewCPUSet(1, 2),
		entering:            machine.NewCPUSet(2),
		crossDomainLeaving:  machine.NewCPUSet(0),
		crossDomainEntering: machine.NewCPUSet(2),
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox"] = machine.NewCPUSet(0, 1)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(0)
	cg.children["kubesandbox"] = []string{"reclaimed-1"}

	res := DAGApplyResult{}
	executor := newDomainPhaseExecutor(newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"kubesandbox":             machine.NewCPUSet(1, 2),
		"kubesandbox/reclaimed-1": machine.NewCPUSet(2),
	}, "0", &res))

	drain, err := executor.executeDrainPhase(cpusetDomainReclaim, []nodeTransition{transition})
	if err != nil {
		t.Fatalf("executeDrainPhase: %v writes=%#v", err, cg.writes)
	}
	for _, write := range cg.writes {
		if write.rel == "kubesandbox/reclaimed-1" && write.cpus != "" && !machine.MustParse(write.cpus).Intersection(machine.NewCPUSet(2)).IsEmpty() {
			t.Fatalf("parent drain expanded controlled child into final target CPU 2; writes=%#v", cg.writes)
		}
	}
	if got, want := drain.release, machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("drain release=%s, want %s", got.String(), want.String())
	}
}

func TestDomainPhaseExecutorFiltersExpandTargetThroughGate(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	primary := dag.index["primary"]
	transition := nodeTransition{
		node:     primary,
		domain:   cpusetDomainPrimary,
		observed: machine.NewCPUSet(0),
		target:   machine.NewCPUSet(0, 1),
		entering: machine.NewCPUSet(1),
	}
	blockingGate := domainGate{
		releasedToPrimary:    machine.NewCPUSet(),
		safeUnownedToPrimary: machine.NewCPUSet(),
		pendingToPrimary:     machine.NewCPUSet(1),
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	res := DAGApplyResult{}
	executor := newDomainPhaseExecutor(newSafeCPUSetWriter(context.Background(), cg, "0", &res))
	if err := executor.executeExpandPhase([]nodeTransition{transition}, blockingGate); err != nil {
		t.Fatalf("executeExpandPhase blocking gate: %v", err)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("writes with pending CPU = %#v, want none", cg.writes)
	}

	releasedGate := blockingGate
	releasedGate.pendingToPrimary = machine.NewCPUSet()
	releasedGate.releasedToPrimary = machine.NewCPUSet(1)
	if err := executor.executeExpandPhase([]nodeTransition{transition}, releasedGate); err != nil {
		t.Fatalf("executeExpandPhase released gate: %v", err)
	}
	wantWrites := []cpusetWrite{{rel: "primary", cpus: "0-1", mems: "0"}}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes after release = %#v, want %#v", cg.writes, wantWrites)
	}
}

func TestDomainPhaseExecutorUsesSafeShrinkForMixedExpandTransition(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0, 2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	transition := nodeTransition{
		node:               dag.index["reclaim"],
		domain:             cpusetDomainReclaim,
		observed:           machine.NewCPUSet(0, 1),
		target:             machine.NewCPUSet(0, 2),
		entering:           machine.NewCPUSet(2),
		crossDomainLeaving: machine.NewCPUSet(1),
	}
	gate := domainGate{releasedToReclaim: machine.NewCPUSet(2)}
	cg := newTopologyFakeCgroup()
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1)
	cg.applyErr["reclaim"] = syscall.EBUSY
	executor := newDomainPhaseExecutor(newSafeCPUSetWriter(context.Background(), cg, "0", &DAGApplyResult{}))

	if err := executor.executeExpandPhase([]nodeTransition{transition}, gate); err != nil {
		t.Fatalf("executeExpandPhase mixed transition error = %v, want safe deferred convergence", err)
	}
}

func TestDomainPhaseExecutorDrainsBeforeGatePublish(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	reclaim := dag.index["reclaim"]
	transition := nodeTransition{
		node:               reclaim,
		domain:             cpusetDomainReclaim,
		observed:           machine.NewCPUSet(1, 2),
		target:             machine.NewCPUSet(2),
		crossDomainLeaving: machine.NewCPUSet(1),
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["reclaim"] = machine.NewCPUSet(1, 2)
	res := DAGApplyResult{}
	executor := newDomainPhaseExecutor(newSafeCPUSetWriter(context.Background(), cg, "0", &res))

	drain, err := executor.executeDrainPhase(cpusetDomainReclaim, []nodeTransition{transition})
	if err != nil {
		t.Fatalf("executeDrainPhase: %v", err)
	}
	if got, want := drain.release, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("drain release = %s, want %s", got.String(), want.String())
	}
	wantWrites := []cpusetWrite{{rel: "reclaim", cpus: "2", mems: "0"}}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}

	initial := domainSnapshot{
		observedPrimaryDomain: machine.NewCPUSet(),
		targetPrimaryDomain:   machine.NewCPUSet(1),
		observedReclaimDomain: machine.NewCPUSet(1, 2),
		targetReclaimDomain:   machine.NewCPUSet(2),
	}
	gate := newDomainGate(initial)
	refreshed := initial
	refreshed.observedReclaimDomain = machine.NewCPUSet(2)
	gate.publishReleased(drain.fromDomain, drain.release, refreshed)
	if got, want := gate.releasedToPrimary, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("releasedToPrimary = %s, want %s", got.String(), want.String())
	}
}

func TestDomainPhasePipelineDoesNotPublishPrimaryReleaseHiddenByCachedChildren(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0"},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["reclaim"] = machine.NewCPUSet()
	cg.children["primary"] = nil
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		switch {
		case rel == "primary" && data.CPUs == "0":
			// A new kube-managed child appears after the primary drain starts.
			cg.children["primary"] = []string{"pod0"}
			cg.cpus["primary/pod0"] = machine.NewCPUSet(1)
		case rel == "primary/pod0" && data.CPUs == "0":
			// Kubelet/admission can race with the shrink and re-inherit the
			// previous primary generation. The publish snapshot must rediscover
			// this live owner and block reclaim expansion.
			cg.cpus["primary/pod0"] = machine.NewCPUSet(1)
		}
	}

	res := DAGApplyResult{}
	pipeline := newDomainPhasePipeline(
		dag,
		cg,
		map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0),
			"reclaim": machine.NewCPUSet(1),
		},
		machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
		},
		machine.NewCPUSet(),
		newApplyCache(cg, "primary"),
	)

	if err := pipeline.executeTransferCycle(context.Background(), "0", &res); err != nil {
		t.Fatalf("executeTransferCycle: %v", err)
	}
	for _, write := range cg.writes {
		if write.rel == "reclaim" && write.cpus == "1" {
			t.Fatalf("reclaim expanded onto CPU still owned by newly created primary child; writes=%#v", cg.writes)
		}
	}
}

func TestDomainPhasePipelineDoesNotPublishReclaimReleaseHiddenByCachedChildren(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1), Mems: "0"},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet()
	cg.cpus["reclaim"] = machine.NewCPUSet(1, 2)
	cg.children["reclaim"] = nil
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		switch {
		case rel == "reclaim" && data.CPUs == "2":
			// A new reclaim child appears after the reclaim drain starts.
			cg.children["reclaim"] = []string{"pod0"}
			cg.cpus["reclaim/pod0"] = machine.NewCPUSet(1)
		case rel == "reclaim/pod0" && data.CPUs == "2":
			// The child can snap back to the previous reclaim generation.
			cg.cpus["reclaim/pod0"] = machine.NewCPUSet(1)
		}
	}

	res := DAGApplyResult{}
	pipeline := newDomainPhasePipeline(
		dag,
		cg,
		map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(1),
			"reclaim": machine.NewCPUSet(2),
		},
		machine.CPUDetails{
			1: {NUMANodeID: 0},
			2: {NUMANodeID: 0},
		},
		machine.NewCPUSet(),
		newApplyCache(cg, "primary"),
	)

	if err := pipeline.executeTransferCycle(context.Background(), "0", &res); err != nil {
		t.Fatalf("executeTransferCycle: %v", err)
	}
	for _, write := range cg.writes {
		if write.rel == "primary" && write.cpus == "1" {
			t.Fatalf("primary expanded onto CPU still owned by newly created reclaim child; writes=%#v", cg.writes)
		}
	}
}

func TestDomainPhasePipelineDrainsBothDomainsBeforeExpandingBidirectionalSwap(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2), Mems: "0"},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(1, 3)
	cg.cpus["reclaim"] = machine.NewCPUSet(2, 4)
	res := DAGApplyResult{}
	pipeline := newDomainPhasePipeline(
		dag,
		cg,
		map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(1, 2),
			"reclaim": machine.NewCPUSet(3, 4),
		},
		machine.CPUDetails{
			1: {NUMANodeID: 0},
			2: {NUMANodeID: 0},
			3: {NUMANodeID: 0},
			4: {NUMANodeID: 0},
		},
		machine.NewCPUSet(),
		newApplyCache(cg, "primary"),
	)

	if err := pipeline.executeTransferCycle(context.Background(), "0", &res); err != nil {
		t.Fatalf("executeTransferCycle: %v", err)
	}
	primaryExpandedAt := -1
	primaryDrainedAt := -1
	for i, write := range cg.writes {
		if write.rel == "primary" && write.cpus == "1-2" && primaryExpandedAt < 0 {
			primaryExpandedAt = i
		}
		if write.rel == "primary" && write.cpus == "1" && primaryDrainedAt < 0 {
			primaryDrainedAt = i
		}
	}
	if primaryExpandedAt >= 0 && (primaryDrainedAt < 0 || primaryExpandedAt < primaryDrainedAt) {
		t.Fatalf("primary expanded before its cross-domain leaving CPUs were drained; primaryExpandedAt=%d primaryDrainedAt=%d writes=%#v",
			primaryExpandedAt, primaryDrainedAt, cg.writes)
	}
}

func TestDomainPhaseExecutorDrainShrinksLiveChildBeforeParent(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	reclaim := dag.index["reclaim"]
	transition := nodeTransition{
		node:               reclaim,
		domain:             cpusetDomainReclaim,
		observed:           machine.NewCPUSet(1, 2, 3),
		target:             machine.NewCPUSet(2, 3),
		crossDomainLeaving: machine.NewCPUSet(1),
	}
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["reclaim"] = machine.NewCPUSet(1, 2, 3)
	cg.cpus["reclaim/pod-a"] = machine.NewCPUSet(1, 3)
	cg.children["reclaim"] = []string{"pod-a"}
	res := DAGApplyResult{}
	executor := newDomainPhaseExecutor(newSafeCPUSetWriter(context.Background(), cg, "0", &res))

	drain, err := executor.executeDrainPhase(cpusetDomainReclaim, []nodeTransition{transition})
	if err != nil {
		t.Fatalf("executeDrainPhase: %v", err)
	}
	if got, want := drain.release, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("drain release = %s, want %s", got.String(), want.String())
	}
	wantWrites := []cpusetWrite{
		{rel: "reclaim/pod-a", cpus: "3", mems: ""},
		{rel: "reclaim", cpus: "2-3", mems: "0"},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}
}

func TestDomainPhaseExecutorDoesNotShrinkParentBelowControlledChildSkippedByEmptyV1Drain(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0), Mems: "0"},
		{Rel: "reclaim/bucket-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(), Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	parent := dag.index["reclaim"]
	child := dag.index["reclaim/bucket-1"]
	transitions := []nodeTransition{
		{
			node:               parent,
			domain:             cpusetDomainReclaim,
			observed:           machine.NewCPUSet(0, 1),
			target:             machine.NewCPUSet(0),
			crossDomainLeaving: machine.NewCPUSet(1),
		},
		{
			node:               child,
			domain:             cpusetDomainReclaim,
			observed:           machine.NewCPUSet(1),
			target:             machine.NewCPUSet(),
			crossDomainLeaving: machine.NewCPUSet(1),
		},
	}
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1)
	cg.cpus["reclaim/bucket-1"] = machine.NewCPUSet(1)
	cg.children["reclaim"] = []string{"bucket-1"}
	res := DAGApplyResult{}
	executor := newDomainPhaseExecutor(newSafeCPUSetWriterForDAG(
		context.Background(),
		cg,
		dag,
		map[string]machine.CPUSet{
			"reclaim":          machine.NewCPUSet(0),
			"reclaim/bucket-1": machine.NewCPUSet(),
		},
		"0",
		&res,
	))

	drain, err := executor.executeDrainPhase(cpusetDomainReclaim, transitions)
	if err != nil {
		t.Fatalf("executeDrainPhase error = %v, want bridge/no error; writes=%#v", err, cg.writes)
	}
	if got, want := drain.release, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("drain release = %s, want %s for gate filtering", got.String(), want.String())
	}
	if got := cg.cpus["reclaim"]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("parent cpuset = %s, want unchanged bridge 0-1; writes=%#v", got.String(), cg.writes)
	}
}

func TestDomainPhasePipelineTransferCycleDrainsRefreshesThenExpands(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.cpus["reclaim"] = machine.NewCPUSet(1, 2)
	targets := map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(0, 1),
		"reclaim": machine.NewCPUSet(2),
	}
	pipeline := newDomainPhasePipeline(
		dag,
		cg,
		targets,
		machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
			2: {NUMANodeID: 0},
		},
		machine.NewCPUSet(),
		newApplyCache(cg, "primary"),
	)
	res := DAGApplyResult{}

	if err := pipeline.executeTransferCycle(context.Background(), "0", &res); err != nil {
		t.Fatalf("executeTransferCycle: %v", err)
	}
	wantWrites := []cpusetWrite{
		{rel: "reclaim", cpus: "2", mems: "0"},
		{rel: "primary", cpus: "0-1", mems: "0"},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}
	if got, want := cg.cpus["primary"], machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("primary cpuset = %s, want %s", got.String(), want.String())
	}
	if got, want := cg.cpus["reclaim"], machine.NewCPUSet(2); !got.Equals(want) {
		t.Fatalf("reclaim cpuset = %s, want %s", got.String(), want.String())
	}
}
