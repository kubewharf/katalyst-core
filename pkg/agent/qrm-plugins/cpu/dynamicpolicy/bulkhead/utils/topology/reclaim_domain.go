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
	"strconv"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type reclaimDomainPlan struct {
	parents     []*TopoNode
	bucketsByID map[string][]*TopoNode
	siblings    []*TopoNode
	targetByRel map[string]machine.CPUSet
}

func buildReclaimDomainPlan(dag *TopoDAG, effective map[string]machine.CPUSet) reclaimDomainPlan {
	plan := reclaimDomainPlan{
		bucketsByID: map[string][]*TopoNode{},
		targetByRel: effective,
	}
	for _, node := range dag.Nodes() {
		switch node.Role {
		case TopoNodeRoleReclaim:
			plan.parents = append(plan.parents, node)
		case TopoNodeRoleReclaimNUMABucket:
			parent := parentNodeOf(node)
			parentRel := ""
			if parent != nil {
				parentRel = parent.Rel
			}
			plan.bucketsByID[parentRel] = append(plan.bucketsByID[parentRel], node)
		case TopoNodeRoleReclaimSibling:
			plan.siblings = append(plan.siblings, node)
		}
	}
	return plan
}

func normalizeReclaimTargetsByPrimary(dag *TopoDAG, effective map[string]machine.CPUSet) {
	primaryUnion := domainTargetUnion(domainNodes(dag, cpusetDomainPrimary), effective)
	if primaryUnion.IsEmpty() {
		return
	}
	for _, n := range domainNodes(dag, cpusetDomainReclaim) {
		original := effective[n.Rel]
		deducted := original.Difference(primaryUnion)
		if !deducted.Equals(original) {
			general.InfofV(5, "topo_dag_writer: deduct primary effective cpuset from reclaim target, rel=%q original=%s primary=%s effective=%s",
				n.Rel, original.String(), primaryUnion.String(), deducted.String())
			effective[n.Rel] = deducted
		}
	}
}

func normalizeReclaimParentContainsNUMABuckets(dag *TopoDAG, effective map[string]machine.CPUSet) {
	plan := buildReclaimDomainPlan(dag, effective)
	for _, parent := range plan.parents {
		for _, bucket := range plan.bucketsByID[parent.Rel] {
			childTarget := effective[bucket.Rel]
			parentTarget := effective[parent.Rel]
			if childTarget.IsSubsetOf(parentTarget) {
				continue
			}
			widened := parentTarget.Union(childTarget)
			general.InfofV(5, "topo_dag_writer: widen reclaim parent target for numa bucket, parent=%q child=%q parentTarget=%s childTarget=%s effective=%s",
				parent.Rel, bucket.Rel, parentTarget.String(), childTarget.String(), widened.String())
			effective[parent.Rel] = widened
		}
	}
}

// validateNoPrimaryReclaimOverlap rejects the apply if any primary/non-reclaim
// effective target overlaps a reclaim target. The overlap is reported per rel so
// the operator can see the conflicting partition and the offending cpus.
func validateNoPrimaryReclaimOverlap(dag *TopoDAG, effective map[string]machine.CPUSet) error {
	reclaims := domainNodes(dag, cpusetDomainReclaim)
	if len(reclaims) == 0 {
		return nil
	}
	for _, n := range domainNodes(dag, cpusetDomainPrimary) {
		primaryTarget := effective[n.Rel]
		for _, r := range reclaims {
			overlap := primaryTarget.Intersection(effective[r.Rel])
			if !overlap.IsEmpty() {
				return fmt.Errorf("ApplyDAGDiff: partition cpuset overlap: primary=%s target=%s reclaim=%s target=%s overlap=%s",
					n.Rel, primaryTarget.String(), r.Rel, effective[r.Rel].String(), overlap.String())
			}
		}
	}
	return nil
}

func validateReclaimNUMABucketSiblingsDisjoint(dag *TopoDAG, effective map[string]machine.CPUSet) error {
	plan := buildReclaimDomainPlan(dag, effective)
	for _, parent := range plan.parents {
		buckets := plan.bucketsByID[parent.Rel]
		for i := range buckets {
			for j := i + 1; j < len(buckets); j++ {
				left := buckets[i]
				right := buckets[j]
				overlap := effective[left.Rel].Intersection(effective[right.Rel])
				if !overlap.IsEmpty() {
					return fmt.Errorf("ApplyDAGDiff: reclaim numa bucket overlap: parent=%s left=%s target=%s right=%s target=%s overlap=%s",
						parent.Rel,
						left.Rel, effective[left.Rel].String(),
						right.Rel, effective[right.Rel].String(),
						overlap.String())
				}
			}
		}
	}
	return nil
}

func validateReclaimNUMABucketNUMABinding(dag *TopoDAG, effective map[string]machine.CPUSet, cpuDetails machine.CPUDetails) error {
	if len(cpuDetails) == 0 {
		return nil
	}
	for _, node := range domainNodes(dag, cpusetDomainReclaim) {
		if node.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		rawNUMA := node.Metadata["numa"]
		numaID, err := strconv.Atoi(rawNUMA)
		if err != nil {
			return fmt.Errorf("ApplyDAGDiff: reclaim numa bucket missing numa metadata: rel=%s numa=%q", node.Rel, rawNUMA)
		}
		allowed := cpuDetails.CPUsInNUMANodes(numaID)
		target := effective[node.Rel]
		if target.IsSubsetOf(allowed) {
			continue
		}
		outside := target.Difference(allowed)
		return fmt.Errorf("ApplyDAGDiff: reclaim numa bucket outside numa cpuset: rel=%s numa=%d target=%s allowed=%s outside=%s",
			node.Rel, numaID, target.String(), allowed.String(), outside.String())
	}
	return nil
}
