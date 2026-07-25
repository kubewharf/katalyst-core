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
	"testing"

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
	if got := first.plan.byRel["primary"].kind; got != transitionCrossDomain {
		t.Fatalf("first primary kind = %s, want %s", got, transitionCrossDomain)
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
	if got := second.plan.byRel["primary"].kind; got != transitionGrow {
		t.Fatalf("second primary kind = %s, want %s after fresh snapshot", got, transitionGrow)
	}
	if !second.gate.pendingToPrimary.IsEmpty() {
		t.Fatalf("second pendingToPrimary = %s, want empty after reclaim released", second.gate.pendingToPrimary.String())
	}
	if got, want := second.snapshot.observedReclaimDomain, machine.NewCPUSet(); !got.Equals(want) {
		t.Fatalf("second observedReclaimDomain = %s, want empty", got.String())
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
		leaving:            machine.NewCPUSet(1),
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
		leaving:            machine.NewCPUSet(1),
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
