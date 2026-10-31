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
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// RelTransition contains only the inputs needed to derive one phase target.
// SafeDrainTarget is computed by the bottom-up drain projection. Entering CPUs
// are supplied exclusively by DomainGate.
type RelTransition struct {
	Current            machine.CPUSet
	Final              machine.CPUSet
	SafeDrainTarget    machine.CPUSet
	AuthorizedEntering machine.CPUSet
	AllowEmptyTarget   bool
}

func buildPhaseTransition(kind PhaseKind, transition RelTransition) (machine.CPUSet, error) {
	switch kind {
	case PhaseDrain:
		target := transition.SafeDrainTarget.Intersection(transition.Current)
		if !transition.AllowEmptyTarget && target.IsEmpty() &&
			!transition.Current.IsEmpty() {
			return transition.Current.Clone(), nil
		}
		return target, nil
	case PhaseExpand:
		entering := transition.Final.
			Difference(transition.Current).
			Intersection(transition.AuthorizedEntering)
		return transition.Current.Union(entering), nil
	default:
		return machine.NewCPUSet(), fmt.Errorf("unsupported phase kind %q", kind)
	}
}

// validateFinalTargets validates immutable placement, before transfer graph or
// operation generation. Observed state is intentionally not constrained here:
// an observed overflow is repair input, not an invalid desired partition.
func validateFinalTargets(in PhasePlanInput) error {
	if in.DAG == nil {
		return nil
	}
	semanticByRel := semanticTargetsForPlan(in)
	bucketUnionByRoot := make(map[string]machine.CPUSet)
	bucketCountByRoot := make(map[string]int)
	reclaimFinalByRoot := make(map[string]machine.CPUSet)
	for _, node := range in.DAG.Nodes() {
		final := semanticByRel[node.Rel]
		if !node.Constraint.CPUUpperBound.IsEmpty() &&
			!final.IsSubsetOf(node.Constraint.CPUUpperBound) {
			return fmt.Errorf("%w: rel=%q final CPUs=%s upper=%s",
				ErrInvalidReclaimBucketTarget, node.Rel, final.String(),
				node.Constraint.CPUUpperBound.String())
		}
		if node.Role == TopoNodeRoleReclaim {
			reclaimFinalByRoot[reclaimValidationGroup(node)] = final
		}
		if node.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		root := reclaimValidationGroup(node)
		if overlap := bucketUnionByRoot[root].Intersection(final); !overlap.IsEmpty() {
			return fmt.Errorf("%w: reclaim root=%q bucket=%q overlaps sibling final CPUs=%s",
				ErrInvalidReclaimBucketTarget, root, node.Rel, overlap.String())
		}
		bucketUnionByRoot[root] = bucketUnionByRoot[root].Union(final)
		bucketCountByRoot[root]++
	}
	for root, count := range bucketCountByRoot {
		if count == 0 {
			continue
		}
		reclaimFinal, hasReclaimRoot := reclaimFinalByRoot[root]
		if !hasReclaimRoot {
			continue
		}
		if !bucketUnionByRoot[root].Equals(reclaimFinal) {
			return fmt.Errorf("%w: reclaim root=%q covered final CPUs=%s per-NUMA bucket union=%s",
				ErrInvalidReclaimBucketTarget, root, reclaimFinal.String(),
				bucketUnionByRoot[root].String())
		}
	}
	return nil
}

func reclaimValidationGroup(node *TopoNode) string {
	if node == nil {
		return ""
	}
	for current := node; current != nil; current = current.parent {
		if current.Role == TopoNodeRoleReclaim {
			if reclaimIndex := strings.TrimSpace(current.Metadata["reclaim-index"]); reclaimIndex != "" {
				return "reclaim-index:" + reclaimIndex
			}
			return "reclaim-root:" + current.Rel
		}
	}
	if reclaimIndex := strings.TrimSpace(node.Metadata["reclaim-index"]); reclaimIndex != "" {
		return "reclaim-index:" + reclaimIndex
	}
	return ""
}
