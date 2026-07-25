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
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDomainOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		role TopoNodeRole
		want cpusetDomain
	}{
		{name: "primary", role: TopoNodeRolePrimary, want: cpusetDomainPrimary},
		{name: "reclaim", role: TopoNodeRoleReclaim, want: cpusetDomainReclaim},
		{name: "reclaim numa bucket", role: TopoNodeRoleReclaimNUMABucket, want: cpusetDomainReclaim},
		{name: "reclaim sibling", role: TopoNodeRoleReclaimSibling, want: cpusetDomainReclaim},
		{name: "unknown", role: TopoNodeRole("unknown"), want: cpusetDomainUnknown},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := domainOf(tc.role); got != tc.want {
				t.Fatalf("domainOf(%q) = %q, want %q", tc.role, got, tc.want)
			}
		})
	}
}

func TestDomainNodesAndTargetUnion(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2)},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1)},
		{Rel: "sibling", Role: TopoNodeRoleReclaimSibling, CPUs: machine.NewCPUSet(2)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	primary := domainNodes(dag, cpusetDomainPrimary)
	if got, want := relsOf(primary), []string{"primary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary domain rels = %#v, want %#v", got, want)
	}
	reclaim := domainNodes(dag, cpusetDomainReclaim)
	if got, want := relsOf(reclaim), []string{"reclaim", "reclaim/numa-0", "sibling"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reclaim domain rels = %#v, want %#v", got, want)
	}
	targets := map[string]machine.CPUSet{
		"reclaim":        machine.NewCPUSet(1),
		"reclaim/numa-0": machine.NewCPUSet(1),
		"sibling":        machine.NewCPUSet(2),
	}
	if got, want := domainTargetUnion(reclaim, targets), machine.NewCPUSet(1, 2); !got.Equals(want) {
		t.Fatalf("domainTargetUnion = %s, want %s", got.String(), want.String())
	}
}

func relsOf(nodes []*TopoNode) []string {
	rels := make([]string, 0, len(nodes))
	for _, node := range nodes {
		rels = append(rels, node.Rel)
	}
	return rels
}
