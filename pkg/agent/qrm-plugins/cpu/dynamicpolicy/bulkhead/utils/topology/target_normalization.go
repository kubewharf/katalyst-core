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
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func desiredTargets(dag *TopoDAG) map[string]machine.CPUSet {
	effective := map[string]machine.CPUSet{}
	for _, n := range dag.Nodes() {
		effective[n.Rel] = n.CPUs
	}
	return effective
}

// computeEffectiveTargets returns, per controlled node, the cpuset that must be
// enforced so the cgroup v1 parent-superset invariant holds while expected kube
// cgroups and reclaim NUMA buckets converge.
//
// The normalization order is fixed:
//  1. initialize desired target from DAG nodes
//  2. widen primary targets with scoped pending/current CPUs and the controlled
//     DomainPrimary nodes' own required floors (RequiredCPUSetByRel)
//  3. deduct primary effective targets from reclaim roles
//  4. widen reclaim parents to contain NUMA bucket targets
//  5. validate primary/reclaim no-overlap
//  6. validate reclaim NUMA bucket sibling disjointness
//  7. validate reclaim NUMA bucket target belongs to its NUMA node
func computeEffectiveTargets(dag *TopoDAG, allowEmptyTarget bool, cpuDetails machine.CPUDetails, pendingRequiredByRel map[string]machine.CPUSet, requiredByRel map[string]machine.CPUSet, protectedByRel ...map[string]machine.CPUSet) (map[string]machine.CPUSet, error) {
	effective := desiredTargets(dag)
	widenPrimaryTargetsWithScopedProtections(dag, effective, allowEmptyTarget, pendingRequiredByRel, requiredByRel, protectedByRel...)
	normalizeReclaimTargetsByPrimary(dag, effective)
	normalizeReclaimParentContainsNUMABuckets(dag, effective)
	reconcileReclaimNUMABucketRequiredFloors(dag, effective, requiredByRel)
	if err := validateNoPrimaryReclaimOverlap(dag, effective); err != nil {
		return nil, err
	}
	if err := validateReclaimNUMABucketSiblingsDisjoint(dag, effective); err != nil {
		return nil, err
	}
	if err := validateReclaimNUMABucketNUMABinding(dag, effective, cpuDetails); err != nil {
		return nil, err
	}
	return effective, nil
}

func widenPrimaryTargetsWithScopedProtections(dag *TopoDAG, effective map[string]machine.CPUSet, allowEmptyTarget bool, pendingRequiredByRel map[string]machine.CPUSet, requiredByRel map[string]machine.CPUSet, protectedByRel ...map[string]machine.CPUSet) {
	var protected map[string]machine.CPUSet
	if len(protectedByRel) > 0 {
		protected = protectedByRel[0]
	}
	for _, n := range domainNodes(dag, cpusetDomainPrimary) {
		// On cgroup v2, an empty cpuset.cpus is a valid explicit target and
		// means the node inherits its effective CPUs from ancestors. Do not widen
		// an intentionally empty target with protected current/pending CPUs or its
		// required floor; otherwise we would erase the empty-target semantics before
		// expandDescendants has a chance to propagate it.
		if allowEmptyTarget && n.CPUs.IsEmpty() {
			continue
		}
		protectedUnion := pendingRequiredByRel[n.Rel].Clone()
		for rel, cpus := range protected {
			if cpus.IsEmpty() || !isRelAtOrUnder(rel, n.Rel) {
				continue
			}
			protectedUnion = protectedUnion.Union(cpus)
		}
		// Union only this controlled DomainPrimary node's own required floor into
		// its effective target. Descendant required floors are folded into the
		// descendants' own effective targets; reclaim roles are never widened here.
		// Without this, a seed leaf whose desired target is not yet projected can
		// sit below its required floor forever, leaving ParentSafety unable to clear
		// RequiredFloorDeficit and stalling the fixed-point loop with no writes.
		protectedUnion = protectedUnion.Union(requiredByRel[n.Rel])
		if protectedUnion.IsEmpty() {
			continue
		}
		effective[n.Rel] = n.CPUs.Union(protectedUnion)
		general.InfofV(5, "topo_dag_writer: widen primary effective target for pending allocations, rel=%q desired=%s pending=%s effective=%s",
			n.Rel, n.CPUs.String(), protectedUnion.String(), effective[n.Rel].String())
	}
}

func isRelAtOrUnder(rel, ancestor string) bool {
	rel = strings.Trim(rel, "/")
	ancestor = strings.Trim(ancestor, "/")
	if rel == "" || ancestor == "" {
		return false
	}
	return rel == ancestor || strings.HasPrefix(rel, ancestor+"/")
}

// reconcileReclaimNUMABucketRequiredFloors tightens a reclaim NUMA bucket's
// required floor to the CPUs its effective target actually retains.
//
// RequiredCPUSetByRelFromNodeSpecs freezes each reclaim NUMA bucket's floor at
// the desired per-NUMA reclaim budget. When a pending admission folds a CPU out
// of reclaim and onto the primary side, normalizeReclaimTargetsByPrimary deducts
// that same CPU from the bucket's effective target. Leaving the frozen floor
// untouched would demand CPUs the bucket is no longer allowed to keep, so
// ParentSafety's RequiredFloorDeficit (and the frozen trace's physical proof)
// can never clear and admission stalls with ErrNoProgress. Intersecting the
// frozen floor with the post-deduction effective target keeps the floor a true
// lower bound over what the bucket is actually enforced to retain. This only
// ever shrinks a bucket floor; it never widens one, and it leaves primary and
// non-bucket reclaim required floors untouched.
func reconcileReclaimNUMABucketRequiredFloors(
	dag *TopoDAG,
	effective map[string]machine.CPUSet,
	requiredByRel map[string]machine.CPUSet,
) {
	if dag == nil || requiredByRel == nil {
		return
	}
	for _, n := range dag.Nodes() {
		if n.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		floor, ok := requiredByRel[n.Rel]
		if !ok {
			continue
		}
		target, hasTarget := effective[n.Rel]
		if !hasTarget {
			continue
		}
		adjusted := floor.Intersection(target)
		requiredByRel[n.Rel] = adjusted
	}
}
