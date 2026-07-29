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
	"errors"
	"os"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestBuildDomainSnapshotTracksDescendantOwners(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 2)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.cpus["primary/pod-a"] = machine.NewCPUSet(2)
	cg.cpus["reclaim"] = machine.NewCPUSet(1)
	cg.children["primary"] = []string{"pod-a"}

	targets := map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(0, 2),
		"reclaim": machine.NewCPUSet(1),
	}
	snapshot, err := buildDomainSnapshot(
		context.Background(),
		cg,
		dag,
		targets,
		machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
			2: {NUMANodeID: 1},
			3: {NUMANodeID: 1},
		},
		machine.NewCPUSet(3),
		newApplyCache(cg, "primary"),
	)
	if err != nil {
		t.Fatalf("buildDomainSnapshot: %v", err)
	}
	if got, want := snapshot.allowedCPUs, machine.NewCPUSet(0, 1, 2); !got.Equals(want) {
		t.Fatalf("allowedCPUs = %s, want %s", got.String(), want.String())
	}
	if got, want := snapshot.observedPrimaryDomain, machine.NewCPUSet(0, 2); !got.Equals(want) {
		t.Fatalf("observedPrimaryDomain = %s, want %s", got.String(), want.String())
	}
	if unowned := snapshot.unownedCPUs(); !unowned.IsEmpty() {
		t.Fatalf("unownedCPUs = %s, want empty", unowned.String())
	}
	if safeUnowned := snapshot.safeUnownedToPrimary(); !safeUnowned.IsEmpty() {
		t.Fatalf("safeUnownedToPrimary = %s, want empty", safeUnowned.String())
	}
}

func TestBuildDomainSnapshotFailsClosedOnCPUSetReadError(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.readErr["primary"] = errors.New("forced cpuset read failure")

	_, err = buildDomainSnapshot(
		context.Background(),
		cg,
		dag,
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
	if err == nil {
		t.Fatal("buildDomainSnapshot returned nil error after ownership cpuset read failed")
	}
}

func TestBuildDomainSnapshotFailsClosedOnChildEnumerationError(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.cpus["reclaim"] = machine.NewCPUSet(1)
	cg.listErr["primary"] = errors.New("forced child enumeration failure")

	_, err = buildDomainSnapshot(
		context.Background(),
		cg,
		dag,
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
	if err == nil {
		t.Fatal("buildDomainSnapshot returned nil error after child ownership enumeration failed")
	}
}

func TestBuildDomainSnapshotIgnoresVanishedDynamicDescendant(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.cpus["reclaim"] = machine.NewCPUSet(1)
	cg.children["primary"] = []string{"vanished-container"}
	cg.readErr["primary/vanished-container"] = os.ErrNotExist

	snapshot, err := buildDomainSnapshot(
		context.Background(),
		cg,
		dag,
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
	if err != nil {
		t.Fatalf("buildDomainSnapshot returned error for vanished dynamic descendant: %v", err)
	}
	if _, ok := snapshot.observedByRel["primary/vanished-container"]; ok {
		t.Fatal("vanished dynamic descendant must not be recorded as an owner")
	}
}

func TestBuildDomainSnapshotIgnoresDynamicDescendantVanishedBeforeEnumeration(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.cpus["reclaim"] = machine.NewCPUSet(1)
	cg.children["primary"] = []string{"vanished-container"}
	cg.cpus["primary/vanished-container"] = machine.NewCPUSet(0)
	cg.listErr["primary/vanished-container"] = os.ErrNotExist

	if _, err := buildDomainSnapshot(
		context.Background(),
		cg,
		dag,
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
	); err != nil {
		t.Fatalf("buildDomainSnapshot returned error for dynamic descendant vanished before enumeration: %v", err)
	}
}
