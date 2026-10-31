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

// Package topology describes the desired bulkhead cpuset cgroup hierarchy and
// provides deterministic traversal helpers for safe shrink and expand writes.
package topology

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	ErrNoControlledPrimaryCandidate        = errors.New("no candidate has a controlled primary ancestor")
	ErrAmbiguousControlledPrimaryCandidate = errors.New("multiple candidates have equally deep controlled primary ancestors")
)

type TopoNodeRole string

const (
	TopoNodeRolePrimary           TopoNodeRole = "primary"
	TopoNodeRoleReclaim           TopoNodeRole = "reclaim"
	TopoNodeRoleReclaimNUMABucket TopoNodeRole = "reclaim_numa_bucket"
	TopoNodeRoleReclaimSibling    TopoNodeRole = "reclaim_sibling"
)

// DomainID / DomainPrimary / DomainReclaim live in topology/model and are
// re-exported above for backward compatibility.

type TopologyScope string

const (
	TopologyScopeNone     TopologyScope = ""
	TopologyScopeNUMANode TopologyScope = "numa_node"
)

// TopologyConstraint records planner-visible placement bounds explicitly.
type TopologyConstraint struct {
	CPUUpperBound machine.CPUSet
	MemUpperBound machine.CPUSet
	Scope         TopologyScope
}

type TopoNode struct {
	Rel            string
	Role           TopoNodeRole
	CPUs           machine.CPUSet
	Mems           string
	Domain         DomainID
	ControlledRoot bool
	TrustAnchor    bool
	Constraint     TopologyConstraint
	Metadata       map[string]string

	// parent is set only by BuildDAG from NodeSpec.ParentRel. Keeping it private
	// preserves the DAG invariants while allowing bridge validation to inspect
	// direct ancestors without scanning all nodes.
	parent   *TopoNode
	children []*TopoNode
}

// NodeSpec defines one desired topology node.
//
// Rel is the cgroup-relative path for the node. ParentRel is empty for a
// top-level node; otherwise it must reference another NodeSpec.Rel in the same
// BuildDAG input. CPUs and Mems carry the desired cpuset values, and Metadata
// preserves caller-specific labels used by the writer.
type NodeSpec struct {
	Rel            string
	Role           TopoNodeRole
	CPUs           machine.CPUSet
	Mems           string
	ParentRel      string
	Domain         DomainID
	ControlledRoot bool
	TrustAnchor    bool
	Constraint     TopologyConstraint
	Metadata       map[string]string
}

type TopoDAG struct {
	topLevel    []*TopoNode
	index       map[string]*TopoNode
	sortedNodes []*TopoNode // cached, immutable after BuildDAG
}

// BuildDAG constructs a topology DAG from node specs.
//
// The input may be unordered. BuildDAG validates rel uniqueness, parent
// existence, reachability, and cycles, then links both child and parent
// references. The returned DAG owns copied Metadata maps so later caller-side
// mutations cannot change node metadata.
func BuildDAG(specs []NodeSpec) (*TopoDAG, error) {
	d := &TopoDAG{index: map[string]*TopoNode{}}
	for _, spec := range specs {
		rel := strings.TrimSpace(spec.Rel)
		if rel == "" {
			return nil, fmt.Errorf("BuildDAG: node rel must be non-empty")
		}
		if _, exists := d.index[rel]; exists {
			return nil, fmt.Errorf("BuildDAG: duplicate node rel %q", rel)
		}
		metadata := map[string]string(nil)
		if len(spec.Metadata) > 0 {
			metadata = make(map[string]string, len(spec.Metadata))
			for k, v := range spec.Metadata {
				metadata[k] = v
			}
		}
		domain := spec.Domain
		if domain == "" {
			domain = DomainID(domainOf(spec.Role))
		}
		d.index[rel] = &TopoNode{
			Rel:            rel,
			Role:           spec.Role,
			CPUs:           spec.CPUs,
			Mems:           spec.Mems,
			Domain:         domain,
			ControlledRoot: spec.ControlledRoot,
			TrustAnchor:    spec.TrustAnchor,
			Constraint:     spec.Constraint,
			Metadata:       metadata,
		}
	}

	pendingChildren := make(map[string][]*TopoNode)
	for _, spec := range specs {
		node := d.index[strings.TrimSpace(spec.Rel)]
		parentRel := strings.TrimSpace(spec.ParentRel)
		if parentRel == "" {
			d.topLevel = append(d.topLevel, node)
			continue
		}
		parent, ok := d.index[parentRel]
		if !ok {
			return nil, fmt.Errorf("BuildDAG: parent %q for rel %q not found", parentRel, node.Rel)
		}
		node.parent = parent
		pendingChildren[parent.Rel] = append(pendingChildren[parent.Rel], node)
	}

	sort.SliceStable(d.topLevel, func(i, j int) bool { return lessNode(d.topLevel[i], d.topLevel[j]) })
	for rel, children := range pendingChildren {
		sort.SliceStable(children, func(i, j int) bool { return lessNode(children[i], children[j]) })
		d.index[rel].children = children
	}
	if err := d.detectCycles(); err != nil {
		return nil, err
	}
	d.sortedNodes = make([]*TopoNode, 0, len(d.index))
	for _, node := range d.index {
		d.sortedNodes = append(d.sortedNodes, node)
	}
	sort.Slice(d.sortedNodes, func(i, j int) bool { return lessNode(d.sortedNodes[i], d.sortedNodes[j]) })
	return d, nil
}

// Nodes returns all nodes in deterministic role/path order. The DAG is
// immutable after BuildDAG, so the sorted slice is cached and returned
// directly; callers must not mutate the returned slice.
func (d *TopoDAG) Nodes() []*TopoNode {
	if d == nil {
		return nil
	}
	return d.sortedNodes
}

// SelectUniqueControlledPrimaryCandidate selects the sole candidate covered by
// the deepest controlled primary DAG node. It fails closed when no candidate is
// covered or distinct candidates tie at the best ancestor depth.
func (d *TopoDAG) SelectUniqueControlledPrimaryCandidate(candidates []string) (string, error) {
	if d == nil {
		return "", ErrNoControlledPrimaryCandidate
	}

	bestDepth := -1
	best := make(map[string]struct{})
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.Trim(strings.TrimSpace(candidate), "/")
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}

		candidateDepth := -1
		for _, node := range d.index {
			if node == nil || node.Role != TopoNodeRolePrimary ||
				node.Domain != DomainPrimary || !node.ControlledRoot {
				continue
			}
			ancestor := strings.Trim(strings.TrimSpace(node.Rel), "/")
			if ancestor == "" || (candidate != ancestor && !strings.HasPrefix(candidate, ancestor+"/")) {
				continue
			}
			if depth := relDepth(ancestor); depth > candidateDepth {
				candidateDepth = depth
			}
		}
		if candidateDepth < 0 {
			continue
		}
		switch {
		case candidateDepth > bestDepth:
			bestDepth = candidateDepth
			best = map[string]struct{}{candidate: {}}
		case candidateDepth == bestDepth:
			best[candidate] = struct{}{}
		}
	}

	if len(best) == 0 {
		return "", ErrNoControlledPrimaryCandidate
	}
	if len(best) != 1 {
		matches := make([]string, 0, len(best))
		for candidate := range best {
			matches = append(matches, candidate)
		}
		sort.Strings(matches)
		return "", fmt.Errorf("%w: depth=%d candidates=%v",
			ErrAmbiguousControlledPrimaryCandidate, bestDepth, matches)
	}
	for candidate := range best {
		return candidate, nil
	}
	panic("unreachable")
}

func relDepth(rel string) int {
	if rel == "" {
		return 0
	}
	return len(strings.Split(rel, "/"))
}

func lessNode(a, b *TopoNode) bool {
	if a.Role != b.Role {
		return a.Role < b.Role
	}
	return a.Rel < b.Rel
}

// parentNodeOf returns the direct parent recorded during BuildDAG.
//
// Nil nodes and top-level nodes return nil. Callers should use this helper
// instead of walking the DAG when they need immediate ancestor context.
func parentNodeOf(node *TopoNode) *TopoNode {
	if node == nil {
		return nil
	}
	return node.parent
}

func (d *TopoDAG) detectCycles() error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(d.index))
	var visit func(*TopoNode) error
	visit = func(node *TopoNode) error {
		switch state[node.Rel] {
		case visiting:
			return fmt.Errorf("BuildDAG: cycle detected at rel %q", node.Rel)
		case visited:
			return nil
		}
		state[node.Rel] = visiting
		for _, child := range node.children {
			if err := visit(child); err != nil {
				return err
			}
		}
		state[node.Rel] = visited
		return nil
	}
	for _, node := range d.topLevel {
		if err := visit(node); err != nil {
			return err
		}
	}
	for rel := range d.index {
		if state[rel] == unvisited {
			return fmt.Errorf("BuildDAG: rel %q is not reachable from any top-level node", rel)
		}
	}
	return nil
}

func (d *TopoDAG) ForEachShrink(fn func(*TopoNode) error) error {
	if d == nil {
		return nil
	}
	for _, n := range d.topLevel {
		if err := walkPostOrder(n, fn); err != nil {
			return err
		}
	}
	return nil
}

func (d *TopoDAG) ForEachExpand(fn func(*TopoNode) error) error {
	if d == nil {
		return nil
	}
	for _, n := range d.topLevel {
		if err := walkPreOrder(n, fn); err != nil {
			return err
		}
	}
	return nil
}

func walkPostOrder(n *TopoNode, fn func(*TopoNode) error) error {
	for _, c := range n.children {
		if err := walkPostOrder(c, fn); err != nil {
			return err
		}
	}
	return fn(n)
}

func walkPreOrder(n *TopoNode, fn func(*TopoNode) error) error {
	if err := fn(n); err != nil {
		return err
	}
	for _, c := range n.children {
		if err := walkPreOrder(c, fn); err != nil {
			return err
		}
	}
	return nil
}
