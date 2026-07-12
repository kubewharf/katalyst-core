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
	"fmt"
	"sort"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type TopoNodeRole string

const (
	TopoNodeRolePrimary           TopoNodeRole = "primary"
	TopoNodeRoleReclaim           TopoNodeRole = "reclaim"
	TopoNodeRoleReclaimNUMABucket TopoNodeRole = "reclaim_numa_bucket"
	TopoNodeRoleReclaimSibling    TopoNodeRole = "reclaim_sibling"
)

type TopoNode struct {
	Rel      string
	Role     TopoNodeRole
	CPUs     machine.CPUSet
	Mems     string
	Metadata map[string]string

	children []*TopoNode
}

type NodeSpec struct {
	Rel       string
	Role      TopoNodeRole
	CPUs      machine.CPUSet
	Mems      string
	ParentRel string
	Metadata  map[string]string
}

type TopoDAG struct {
	topLevel []*TopoNode
	index    map[string]*TopoNode
}

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
		d.index[rel] = &TopoNode{Rel: rel, Role: spec.Role, CPUs: spec.CPUs, Mems: spec.Mems, Metadata: metadata}
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
	return d, nil
}

func (d *TopoDAG) Nodes() []*TopoNode {
	if d == nil {
		return nil
	}
	out := make([]*TopoNode, 0, len(d.index))
	for _, node := range d.index {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return lessNode(out[i], out[j]) })
	return out
}

func lessNode(a, b *TopoNode) bool {
	if a.Role != b.Role {
		return a.Role < b.Role
	}
	return a.Rel < b.Rel
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
