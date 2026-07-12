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

func TestBuildDAGValidationAndTraversal(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Metadata: map[string]string{"k": "v"}},
		{Rel: "primary/child-b", ParentRel: "primary", Role: TopoNodeRoleReclaimSibling, CPUs: machine.NewCPUSet(1)},
		{Rel: "primary/child-a", ParentRel: "primary", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	dag.index["primary"].Metadata["k"] = "mutated"

	nodes := dag.Nodes()
	var nodeRels []string
	for _, n := range nodes {
		nodeRels = append(nodeRels, n.Rel)
	}
	if !reflect.DeepEqual(nodeRels, []string{"primary", "primary/child-a", "primary/child-b"}) {
		t.Fatalf("Nodes order = %v", nodeRels)
	}

	var shrinkOrder []string
	if err := dag.ForEachShrink(func(n *TopoNode) error {
		shrinkOrder = append(shrinkOrder, n.Rel)
		return nil
	}); err != nil {
		t.Fatalf("ForEachShrink: %v", err)
	}
	if !reflect.DeepEqual(shrinkOrder, []string{"primary/child-a", "primary/child-b", "primary"}) {
		t.Fatalf("shrink order = %v", shrinkOrder)
	}

	var expandOrder []string
	if err := dag.ForEachExpand(func(n *TopoNode) error {
		expandOrder = append(expandOrder, n.Rel)
		return nil
	}); err != nil {
		t.Fatalf("ForEachExpand: %v", err)
	}
	if !reflect.DeepEqual(expandOrder, []string{"primary", "primary/child-a", "primary/child-b"}) {
		t.Fatalf("expand order = %v", expandOrder)
	}
}

func TestBuildDAGErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		specs []NodeSpec
	}{
		{name: "empty rel", specs: []NodeSpec{{Rel: ""}}},
		{name: "duplicate rel", specs: []NodeSpec{{Rel: "a"}, {Rel: "a"}}},
		{name: "missing parent", specs: []NodeSpec{{Rel: "a", ParentRel: "missing"}}},
		{name: "unreachable cycle", specs: []NodeSpec{{Rel: "a", ParentRel: "b"}, {Rel: "b", ParentRel: "a"}}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildDAG(tt.specs); err == nil {
				t.Fatalf("BuildDAG expected error")
			}
		})
	}
}
