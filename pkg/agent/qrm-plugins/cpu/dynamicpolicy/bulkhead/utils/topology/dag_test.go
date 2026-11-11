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
	"errors"
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSelectUniqueCandidateByDeepestControlledPrimaryAncestor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		primaryRel string
		candidates []string
		want       string
	}{
		{
			name:       "ordinary shared",
			primaryRel: "kubepods/burstable",
			candidates: []string{
				"kubepods/pod-shared",
				"kubepods/besteffort/pod-shared",
				"kubepods/burstable/pod-shared",
			},
			want: "kubepods/burstable/pod-shared",
		},
		{
			name:       "snb",
			primaryRel: "kubepods/shared-numa-binding",
			candidates: []string{
				"kubepods/pod-snb",
				"kubepods/shared-numa-binding/pod-snb",
			},
			want: "kubepods/shared-numa-binding/pod-snb",
		},
		{
			name:       "dnb",
			primaryRel: "kubepods/dedicated-numa-binding",
			candidates: []string{
				"kubepods/pod-dnb",
				"kubepods/dedicated-numa-binding/pod-dnb",
			},
			want: "kubepods/dedicated-numa-binding/pod-dnb",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dag, err := BuildDAG([]NodeSpec{{
				Rel:            tt.primaryRel,
				Role:           TopoNodeRolePrimary,
				Domain:         DomainPrimary,
				ControlledRoot: true,
			}})
			if err != nil {
				t.Fatalf("BuildDAG() error = %v", err)
			}

			got, err := dag.SelectUniqueControlledPrimaryCandidate(tt.candidates)
			if err != nil {
				t.Fatalf("SelectUniqueControlledPrimaryCandidate() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("selected candidate = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelectUniqueControlledPrimaryCandidateFailsClosed(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel:            "kubepods",
		Role:           TopoNodeRolePrimary,
		Domain:         DomainPrimary,
		ControlledRoot: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}

	tests := []struct {
		name       string
		candidates []string
		wantErr    error
	}{
		{
			name:       "no matching controlled primary ancestor",
			candidates: []string{"outside/pod-a"},
			wantErr:    ErrNoControlledPrimaryCandidate,
		},
		{
			name: "ambiguous candidates at same ancestor depth",
			candidates: []string{
				"kubepods/burstable/pod-a",
				"kubepods/besteffort/pod-a",
			},
			wantErr: ErrAmbiguousControlledPrimaryCandidate,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := dag.SelectUniqueControlledPrimaryCandidate(tt.candidates); !errors.Is(err, tt.wantErr) {
				t.Fatalf("SelectUniqueControlledPrimaryCandidate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

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
	if dag.index["primary"].parent != nil {
		t.Fatalf("top-level node parent = %v, want nil", dag.index["primary"].parent.Rel)
	}
	if parentNodeOf(nil) != nil {
		t.Fatalf("nil node parent should be nil")
	}
	if parentNodeOf(dag.index["primary"]) != nil {
		t.Fatalf("top-level parentNodeOf should be nil")
	}
	for _, rel := range []string{"primary/child-a", "primary/child-b"} {
		if dag.index[rel].parent != dag.index["primary"] {
			t.Fatalf("node %q parent = %v, want primary", rel, dag.index[rel].parent)
		}
		if parentNodeOf(dag.index[rel]) != dag.index["primary"] {
			t.Fatalf("node %q parentNodeOf mismatch", rel)
		}
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

func TestBuildDAGPreservesExplicitDomainBoundaryMetadata(t *testing.T) {
	t.Parallel()
	const domain DomainID = "exclusive-a"
	constraint := TopologyConstraint{
		CPUUpperBound: machine.NewCPUSet(2, 3),
		MemUpperBound: machine.NewCPUSet(1),
		Scope:         TopologyScopeNUMANode,
	}
	dag, err := BuildDAG([]NodeSpec{{
		Rel:            "root",
		Role:           TopoNodeRolePrimary,
		Domain:         domain,
		ControlledRoot: true,
		TrustAnchor:    true,
		Constraint:     constraint,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	node := dag.index["root"]
	if node.Domain != domain || !node.ControlledRoot || !node.TrustAnchor || !reflect.DeepEqual(node.Constraint, constraint) {
		t.Fatalf("node metadata = %+v, want explicit domain/boundary metadata", node)
	}
}
