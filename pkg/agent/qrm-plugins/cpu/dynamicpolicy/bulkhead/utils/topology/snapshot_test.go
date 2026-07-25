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
	if got, want := snapshot.ownerByCPU[2], []string{"primary/pod-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ownerByCPU[2] = %#v, want %#v", got, want)
	}
	if !snapshot.unownedCPUs.IsEmpty() {
		t.Fatalf("unownedCPUs = %s, want empty", snapshot.unownedCPUs.String())
	}
	if !snapshot.safeUnownedToPrimary.IsEmpty() {
		t.Fatalf("safeUnownedToPrimary = %s, want empty", snapshot.safeUnownedToPrimary.String())
	}
}
