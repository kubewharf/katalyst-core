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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TargetMaterialization separates committed semantic ownership from targets
// that can be physically enforced by the current cgroup hierarchy.
type TargetMaterialization struct {
	SemanticByRel map[string]machine.CPUSet
	PhysicalByRel map[string]machine.CPUSet
	DormantRels   map[string]struct{}
	DormantProofs map[string]DormantLeafProof
	Snapshot      *CompleteSnapshot
}

type DormantLeafProof struct {
	Identity     CgroupIdentity
	ObservedCPUs machine.CPUSet
}

// materializeTargets is the only translation point from committed semantic
// ownership to cgroup-version-specific physical targets. A v1 bucket may keep
// a non-empty physical envelope only after dormantLeafProof proves that the
// bucket is an inactive leaf and the envelope introduces no new primary
// ownership. Observed cgroup state is never promoted into semantic ownership.
// This translation runs after advisor placement: it may make an empty semantic
// target physically writable on cgroup v1, but it must not influence NUMA
// quotas, migration targets, or synthetic default-share ownership.
func materializeTargets(
	dag *TopoDAG,
	snapshot *CompleteSnapshot,
	allowEmptyTarget bool,
	semanticTargets map[string]machine.CPUSet,
) TargetMaterialization {
	snapshot = snapshotWithoutOwnershipOverlay(snapshot)
	semantic := model.CloneCPUSetMap(semanticTargets)
	result := TargetMaterialization{
		SemanticByRel: semantic,
		PhysicalByRel: model.CloneCPUSetMap(semantic),
		DormantRels:   make(map[string]struct{}),
		DormantProofs: make(map[string]DormantLeafProof),
		Snapshot:      snapshot,
	}
	if allowEmptyTarget || snapshot == nil {
		return result
	}
	semanticPrimary := desiredDomainUnions(dag, semantic)[DomainPrimary]

	for _, node := range dag.Nodes() {
		if node.Role != TopoNodeRoleReclaimNUMABucket || !semantic[node.Rel].IsEmpty() ||
			hasActiveSemanticDescendant(node, semantic) {
			continue
		}
		proof, inactive := dormantLeafProof(snapshot, node.Rel)
		if !inactive {
			continue
		}
		if !proof.ObservedCPUs.Intersection(node.Constraint.CPUUpperBound).IsSubsetOf(semanticPrimary) {
			continue
		}
		result.DormantRels[node.Rel] = struct{}{}
		result.DormantProofs[node.Rel] = proof
		// A dormant proof is intentionally leaf-only. The observed cpuset is a
		// physical cgroup v1 containment envelope, never semantic ownership.
		envelope := proof.ObservedCPUs.Clone()
		result.PhysicalByRel[node.Rel] = envelope
		for parent := node.parent; parent != nil; parent = parent.parent {
			result.PhysicalByRel[parent.Rel] = result.PhysicalByRel[parent.Rel].Union(envelope)
		}
	}
	result.Snapshot = snapshotWithDormantOwnershipOverlay(snapshot, result.DormantRels, semantic)
	return result
}

// snapshotWithoutOwnershipOverlay restores the raw physical view before a new
// planning round classifies dormant nodes. This prevents an overlay from an
// earlier round from becoming input to the next ownership decision.
func snapshotWithoutOwnershipOverlay(snapshot *CompleteSnapshot) *CompleteSnapshot {
	if snapshot == nil || len(snapshot.OwnershipByRel) == 0 {
		return snapshot
	}
	physical := *snapshot
	physical.OwnershipByRel = nil
	physical.DomainUnion = make(map[DomainID]machine.CPUSet, len(snapshot.DomainUnion))
	for rel, entry := range snapshot.Entries {
		domain := snapshot.DomainByRel[rel]
		physical.DomainUnion[domain] = physical.DomainUnion[domain].Union(entry.CPUs)
	}
	return &physical
}

func dormantLeafProof(snapshot *CompleteSnapshot, root string) (DormantLeafProof, bool) {
	if snapshot == nil {
		return DormantLeafProof{}, false
	}
	entry, found := snapshot.Entries[root]
	if !found || !entry.Activity.Inactive() {
		return DormantLeafProof{}, false
	}
	children, expanded := snapshot.Children[root]
	// Dormant is deliberately leaf-only. Supporting an inactive subtree would
	// require recursively binding every descendant identity and activity state,
	// which is unnecessary for per-NUMA bucket placeholders and would create a
	// second subtree-retirement protocol.
	if !expanded || len(children) != 0 {
		return DormantLeafProof{}, false
	}
	for rel := range snapshot.Entries {
		if rel != root && isRelAtOrUnder(rel, root) {
			return DormantLeafProof{}, false
		}
	}
	return DormantLeafProof{
		Identity:     entry.Identity,
		ObservedCPUs: entry.CPUs.Clone(),
	}, true
}

func validateDormantProofs(snapshot *CompleteSnapshot, proofs map[string]DormantLeafProof) error {
	if snapshot == nil {
		return &PlanStaleError{Direction: WritePublish, Resource: "dormant_snapshot", Current: "nil", Target: "complete"}
	}
	roots := make([]string, 0, len(proofs))
	for root := range proofs {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		proof := proofs[root]
		currentProof, complete := dormantLeafProof(snapshot, root)
		if !complete {
			return &PlanStaleError{
				Rel: root, Direction: WritePublish, Resource: "dormant_subtree",
				Current: "incomplete or active", Target: "complete inactive child closure",
			}
		}
		if currentProof.Identity != proof.Identity {
			return &PlanStaleError{
				Rel: root, Direction: WritePublish, Resource: "dormant_identity",
				Current: fmt.Sprintf("%v", currentProof.Identity), Target: fmt.Sprintf("%v", proof.Identity),
			}
		}
		if !currentProof.ObservedCPUs.Equals(proof.ObservedCPUs) {
			return &PlanStaleError{
				Rel: root, Direction: WritePublish, Resource: "dormant_cpuset",
				Current: currentProof.ObservedCPUs.String(), Target: proof.ObservedCPUs.String(),
			}
		}
	}
	return nil
}

func hasActiveSemanticDescendant(node *TopoNode, semantic map[string]machine.CPUSet) bool {
	for _, child := range node.children {
		if !semantic[child.Rel].IsEmpty() || hasActiveSemanticDescendant(child, semantic) {
			return true
		}
	}
	return false
}

// snapshotWithDormantOwnershipOverlay keeps Entries as exact physical
// observations while defining which CPUs those entries semantically own.
// cgroup v1 may require a dormant bucket and its ancestors to retain non-empty
// cpusets for containment; those envelopes must never leak into AppliedView,
// domain transfer, or parent-safety ownership. DomainUnion stays the closed
// union of the physical entries (the evidence the frozen-trace validator
// recomputes), and the dormant semantic ownership is recorded only in
// OwnershipByRel so it cannot drift from the entry set it is derived from.
func snapshotWithDormantOwnershipOverlay(
	snapshot *CompleteSnapshot,
	dormant map[string]struct{},
	semanticTargets map[string]machine.CPUSet,
) *CompleteSnapshot {
	if snapshot == nil || len(dormant) == 0 {
		return snapshot
	}
	filtered := *snapshot
	filtered.DomainUnion = make(map[DomainID]machine.CPUSet, len(snapshot.DomainUnion))
	filtered.OwnershipByRel = make(map[string]machine.CPUSet)
	for rel, entry := range snapshot.Entries {
		domain := snapshot.DomainByRel[rel]
		if target, controlled := semanticTargets[rel]; controlled &&
			(relInDormantSubtree(rel, dormant) || isAncestorOfDormant(rel, dormant)) {
			filtered.OwnershipByRel[rel] = target.Clone()
		}
		filtered.DomainUnion[domain] = filtered.DomainUnion[domain].Union(entry.CPUs)
	}
	return &filtered
}

func isAncestorOfDormant(rel string, dormant map[string]struct{}) bool {
	for root := range dormant {
		if rel != root && isRelAtOrUnder(root, rel) {
			return true
		}
	}
	return false
}

func relInDormantSubtree(rel string, dormant map[string]struct{}) bool {
	for root := range dormant {
		if isRelAtOrUnder(rel, root) {
			return true
		}
	}
	return false
}
