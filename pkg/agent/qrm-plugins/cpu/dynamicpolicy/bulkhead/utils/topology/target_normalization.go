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
//  2. widen primary targets with protected pending/current CPUs
//  3. deduct primary effective targets from reclaim roles
//  4. widen reclaim parents to contain NUMA bucket targets
//  5. validate primary/reclaim no-overlap
//  6. validate reclaim NUMA bucket sibling disjointness
//  7. validate reclaim NUMA bucket target belongs to its NUMA node
func computeEffectiveTargets(dag *TopoDAG, allowEmptyTarget bool, cpuDetails machine.CPUDetails, protectedPending machine.CPUSet, protectedByRel ...map[string]machine.CPUSet) (map[string]machine.CPUSet, error) {
	effective := desiredTargets(dag)
	widenPrimaryTargetsWithProtectedCPUs(dag, effective, allowEmptyTarget, protectedPending, protectedByRel...)
	normalizeReclaimTargetsByPrimary(dag, effective)
	normalizeReclaimParentContainsNUMABuckets(dag, effective)
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

func widenPrimaryTargetsWithProtectedCPUs(dag *TopoDAG, effective map[string]machine.CPUSet, allowEmptyTarget bool, protectedPending machine.CPUSet, protectedByRel ...map[string]machine.CPUSet) {
	var protected map[string]machine.CPUSet
	if len(protectedByRel) > 0 {
		protected = protectedByRel[0]
	}
	for _, n := range domainNodes(dag, cpusetDomainPrimary) {
		// On cgroup v2, an empty cpuset.cpus is a valid explicit target and
		// means the node inherits its effective CPUs from ancestors. Do not widen
		// an intentionally empty target with protected current/pending CPUs; otherwise
		// we would erase the empty-target semantics before expandDescendants has a
		// chance to propagate it.
		if allowEmptyTarget && n.CPUs.IsEmpty() {
			continue
		}
		protectedUnion := machine.NewCPUSet()
		if !protectedPending.IsEmpty() {
			protectedUnion = protectedUnion.Union(protectedPending)
		}
		for rel, cpus := range protected {
			if cpus.IsEmpty() || !isRelAtOrUnder(rel, n.Rel) {
				continue
			}
			protectedUnion = protectedUnion.Union(cpus)
		}
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
