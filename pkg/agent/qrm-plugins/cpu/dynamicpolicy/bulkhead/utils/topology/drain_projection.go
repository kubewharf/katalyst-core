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
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type DrainProjectionCost struct {
	Rels          int
	Children      int
	ProtectedRels int
}

func (c DrainProjectionCost) Total() int {
	return saturatingAdd(saturatingAdd(c.Rels, c.Children), c.ProtectedRels)
}

type DrainProjection struct {
	TargetByRel   map[string]CPUSetTarget
	DomainUnion   map[DomainID]machine.CPUSet
	EmptyBlockers map[string]machine.CPUSet
	Cost          DrainProjectionCost
}

type DrainProjectionInput struct {
	PlanInput       PhasePlanInput
	DrainBatch      map[DomainID]machine.CPUSet
	LeavingByDomain map[DomainID]machine.CPUSet
	DomainByRel     map[string]DomainID
	ParentByRel     map[string]string
	DepthByRel      map[string]int
	Context         *drainProjectionContext
	TransferCPUs    machine.CPUSet
	ProbeContext    context.Context
	ProbeBudget     *BudgetTracker
}

type drainProjectionContext struct {
	protectedDescendantUnionByRel map[string]machine.CPUSet
	cost                          int
	baseCost                      int
	phase                         string
	protectedOperations           int
	relIndexOperations            int
	childIndexOperations          int
	childMembershipsScanned       int
	frontierIndexOperations       int
	frontierMembershipsScanned    int
	ancestorClosureOperations     int

	baseProjection      DrainProjection
	staticRequiredByRel map[string]machine.CPUSet
	baseChildUnionByRel map[string]machine.CPUSet
	baseChildCountByRel map[string]map[int]int
	bucketUpperByRel    map[string]machine.CPUSet
	affectedRelsByCPU   map[int][]string
	parentByRel         map[string]string
	ready               bool
}

func buildDrainProjectionContext(
	ctx context.Context,
	snapshot *CompleteSnapshot,
	protectedByRel map[string]machine.CPUSet,
	probeBudget *BudgetTracker,
) (*drainProjectionContext, error) {
	out := &drainProjectionContext{
		protectedDescendantUnionByRel: make(map[string]machine.CPUSet),
		phase:                         "protected_descendant_preaggregation",
	}
	if snapshot == nil || len(protectedByRel) == 0 {
		return out, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	protectedRels := make([]string, 0, len(protectedByRel))
	for rel := range protectedByRel {
		protectedRels = append(protectedRels, rel)
	}
	sort.Strings(protectedRels)
	for _, originalRel := range protectedRels {
		protectedRel := filepath.Clean(originalRel)
		cpus := protectedByRel[originalRel]
		for rel := protectedRel; rel != "." && rel != ""; rel = filepath.Dir(rel) {
			if _, ok := snapshot.Entries[rel]; !ok {
				continue
			}
			if probeBudget != nil {
				if err := probeBudget.ConsumeDeadlockProbeOperations(ctx, 1); err != nil {
					return out, err
				}
			}
			out.cost++
			out.protectedOperations++
			out.protectedDescendantUnionByRel[rel] = out.protectedDescendantUnionByRel[rel].Union(cpus)
		}
	}
	return out, nil
}

func prepareIncrementalDrainProjectionContext(
	input DrainProjectionInput,
	out *drainProjectionContext,
) error {
	if out == nil || input.PlanInput.Snapshot == nil {
		return fmt.Errorf("incremental drain projection requires context and snapshot")
	}
	ctx := input.ProbeContext
	if ctx == nil {
		ctx = context.Background()
	}
	charge := func() error {
		if input.ProbeBudget != nil {
			if err := input.ProbeBudget.ConsumeDeadlockProbeOperations(ctx, 1); err != nil {
				return err
			}
		}
		out.cost++
		switch out.phase {
		case "rel_index":
			out.relIndexOperations++
		case "child_index":
			out.childIndexOperations++
		case "frontier_index":
			out.frontierIndexOperations++
		case "ancestor_closure":
			out.ancestorClosureOperations++
		}
		return nil
	}
	in := input.PlanInput
	transferCPUs := input.TransferCPUs
	if transferCPUs.IsEmpty() {
		transferCPUs = in.AllowedCPUs
	}
	emptyDrain := make(map[DomainID]machine.CPUSet)
	baseInput := input
	baseInput.DrainBatch = emptyDrain
	baseInput.Context = out
	out.phase = "base_projection"
	baseProjection, err := projectDrainTargets(baseInput)
	if err != nil {
		return err
	}
	out.baseProjection = baseProjection
	out.baseCost = baseProjection.Cost.Total()
	out.cost += out.baseCost
	out.staticRequiredByRel = make(map[string]machine.CPUSet, len(in.Snapshot.Entries))
	out.baseChildUnionByRel = make(map[string]machine.CPUSet, len(in.Snapshot.Entries))
	out.baseChildCountByRel = make(map[string]map[int]int, len(in.Snapshot.Entries))
	out.bucketUpperByRel = make(map[string]machine.CPUSet, len(in.Snapshot.Entries))
	out.affectedRelsByCPU = make(map[int][]string)
	out.parentByRel = input.ParentByRel

	rels := sortedSnapshotRels(in.Snapshot, input.DepthByRel)
	frontierRelsByCPU := make(map[int][]string)
	ancestorsByRel := make(map[string][]string, len(rels))
	for _, rel := range rels {
		if relInDormantSubtree(rel, in.DormantRels) {
			continue
		}
		out.phase = "rel_index"
		if err := charge(); err != nil {
			return err
		}
		node := in.DAG.index[rel]
		required := machine.NewCPUSet()
		if node != nil {
			required = required.Union(in.DesiredByRel[rel])
			if node.Role == TopoNodeRolePrimary {
				required = required.Union(in.ProtectedPending)
			}
		} else if explicit, ok := in.DynamicByRel[rel]; ok {
			required = required.Union(explicit)
		}
		required = required.Union(out.protectedDescendantUnionByRel[rel])
		out.staticRequiredByRel[rel] = required
		relevantTransferCPUs := transferCPUs.Difference(required)
		if node != nil && node.Role == TopoNodeRoleReclaimNUMABucket &&
			!node.Constraint.CPUUpperBound.IsEmpty() {
			out.bucketUpperByRel[rel] = node.Constraint.CPUUpperBound
		} else if upper, ok := out.bucketUpperByRel[input.ParentByRel[rel]]; ok {
			out.bucketUpperByRel[rel] = upper
		}

		childCurrent := machine.NewCPUSet()
		childTargetUnion := machine.NewCPUSet()
		childCount := make(map[int]int)
		for _, child := range in.Snapshot.Children[rel] {
			childRel := filepath.Join(rel, child.Name)
			if relInDormantSubtree(childRel, in.DormantRels) {
				continue
			}
			childCurrent = childCurrent.Union(in.Snapshot.Entries[childRel].CPUs)
			childTarget := baseProjection.TargetByRel[childRel].CPUs
			childTargetUnion = childTargetUnion.Union(childTarget)
			out.phase = "child_index"
			if err := charge(); err != nil {
				return err
			}
			relevantChildTarget := childTarget.Intersection(relevantTransferCPUs)
			out.childMembershipsScanned += relevantChildTarget.Size()
			for _, cpu := range relevantChildTarget.ToSliceInt() {
				childCount[cpu]++
			}
		}
		out.baseChildUnionByRel[rel] = childTargetUnion
		out.baseChildCountByRel[rel] = childCount
		frontier := in.Snapshot.Entries[rel].CPUs.
			Difference(childCurrent).
			Intersection(relevantTransferCPUs)
		out.phase = "frontier_index"
		if err := charge(); err != nil {
			return err
		}
		out.frontierMembershipsScanned += frontier.Size()
		for _, cpu := range frontier.ToSliceInt() {
			frontierRelsByCPU[cpu] = append(frontierRelsByCPU[cpu], rel)
		}
	}

	var buildAncestors func(string, map[string]bool) ([]string, error)
	buildAncestors = func(rel string, visiting map[string]bool) ([]string, error) {
		if cached, ok := ancestorsByRel[rel]; ok {
			return cached, nil
		}
		if visiting[rel] {
			return nil, fmt.Errorf("incremental drain projection parent cycle at %q", rel)
		}
		visiting[rel] = true
		parent := input.ParentByRel[rel]
		var ancestors []string
		if parent != "" {
			if _, ok := in.Snapshot.Entries[parent]; ok {
				out.phase = "ancestor_closure"
				if err := charge(); err != nil {
					return nil, err
				}
				parentAncestors, err := buildAncestors(parent, visiting)
				if err != nil {
					return nil, err
				}
				ancestors = append([]string{parent}, parentAncestors...)
			}
		}
		delete(visiting, rel)
		ancestorsByRel[rel] = ancestors
		return ancestors, nil
	}
	for _, rel := range rels {
		if relInDormantSubtree(rel, in.DormantRels) {
			continue
		}
		if _, err := buildAncestors(rel, make(map[string]bool)); err != nil {
			return err
		}
	}
	for cpu, owners := range frontierRelsByCPU {
		seen := make(map[string]bool)
		affected := make([]string, 0)
		for _, owner := range owners {
			for _, rel := range append([]string{owner}, ancestorsByRel[owner]...) {
				if !seen[rel] {
					seen[rel] = true
					affected = append(affected, rel)
				}
			}
		}
		sort.Slice(affected, func(i, j int) bool {
			if input.DepthByRel[affected[i]] != input.DepthByRel[affected[j]] {
				return input.DepthByRel[affected[i]] > input.DepthByRel[affected[j]]
			}
			return affected[i] < affected[j]
		})
		out.affectedRelsByCPU[cpu] = affected
	}
	out.phase = "complete"
	out.ready = true
	return nil
}

func projectSingleAtomIncremental(
	input DrainProjectionInput,
	atom deadlock.DrainAtom,
) (DrainProjection, error) {
	if input.Context == nil || !input.Context.ready || atom.CPUs.Size() != 1 {
		return projectDrainTargets(input)
	}
	cpu := atom.CPUs.ToSliceInt()[0]
	affected := input.Context.affectedRelsByCPU[cpu]
	if len(affected) == 0 {
		return projectDrainTargets(input)
	}
	ctx := input.ProbeContext
	if ctx == nil {
		ctx = context.Background()
	}
	charge := func() error {
		if input.ProbeBudget == nil {
			return nil
		}
		return input.ProbeBudget.ConsumeDeadlockProbeOperations(ctx, 1)
	}
	projection := DrainProjection{
		TargetByRel:   make(map[string]CPUSetTarget, len(affected)),
		DomainUnion:   make(map[DomainID]machine.CPUSet, len(input.Context.baseProjection.DomainUnion)),
		EmptyBlockers: make(map[string]machine.CPUSet, len(input.Context.baseProjection.EmptyBlockers)),
	}
	for domain, cpus := range input.Context.baseProjection.DomainUnion {
		projection.DomainUnion[domain] = cpus.Clone()
	}
	for rel, cpus := range input.Context.baseProjection.EmptyBlockers {
		projection.EmptyBlockers[rel] = cpus.Clone()
	}
	changedChildrenByParent := make(map[string][]string)
	affectedDomains := make(map[DomainID]bool)
	for _, rel := range affected {
		if err := charge(); err != nil {
			return DrainProjection{}, err
		}
		projection.Cost.Rels++
		required := input.Context.staticRequiredByRel[rel]
		childRequired := input.Context.baseChildUnionByRel[rel]
		count := input.Context.baseChildCountByRel[rel][cpu]
		for _, childRel := range changedChildrenByParent[rel] {
			if err := charge(); err != nil {
				return DrainProjection{}, err
			}
			projection.Cost.Children++
			baseHas := input.Context.baseProjection.TargetByRel[childRel].CPUs.Contains(cpu)
			nextHas := projection.TargetByRel[childRel].CPUs.Contains(cpu)
			if baseHas && !nextHas {
				count--
			} else if !baseHas && nextHas {
				count++
			}
		}
		if count > 0 {
			childRequired = childRequired.Union(atom.CPUs)
		} else {
			childRequired = childRequired.Difference(atom.CPUs)
		}
		required = required.Union(childRequired)
		target, blocker, err := projectDrainRel(
			input, rel, required, input.Context.bucketUpperByRel[rel])
		if err != nil {
			return DrainProjection{}, err
		}
		if diff := target.CPUs.Difference(input.Context.baseProjection.TargetByRel[rel].CPUs).
			Union(input.Context.baseProjection.TargetByRel[rel].CPUs.Difference(target.CPUs)); !diff.IsSubsetOf(atom.CPUs) {
			full, err := projectDrainTargets(input)
			if err != nil {
				return DrainProjection{}, err
			}
			full.Cost.Rels = saturatingAdd(full.Cost.Rels, projection.Cost.Rels)
			full.Cost.Children = saturatingAdd(full.Cost.Children, projection.Cost.Children)
			full.Cost.ProtectedRels = saturatingAdd(
				full.Cost.ProtectedRels, projection.Cost.ProtectedRels)
			return full, nil
		}
		projection.TargetByRel[rel] = target
		delete(projection.EmptyBlockers, rel)
		if !blocker.IsEmpty() {
			projection.EmptyBlockers[rel] = blocker
		}
		parent := input.ParentByRel[rel]
		if parent != "" {
			changedChildrenByParent[parent] = append(changedChildrenByParent[parent], rel)
		}
		affectedDomains[input.DomainByRel[rel]] = true
	}
	for domain := range affectedDomains {
		contains := false
		for _, rel := range affected {
			if input.DomainByRel[rel] == domain && projection.TargetByRel[rel].CPUs.Contains(cpu) {
				contains = true
				break
			}
		}
		if contains {
			projection.DomainUnion[domain] = projection.DomainUnion[domain].Union(atom.CPUs)
		} else {
			projection.DomainUnion[domain] = projection.DomainUnion[domain].Difference(atom.CPUs)
		}
	}
	if err := charge(); err != nil {
		return DrainProjection{}, err
	}
	projection.Cost.Rels++
	return projection, nil
}

// projectDrainTargets is the canonical, side-effect-free bottom-up drain
// calculation shared by planning and deadlock analysis.
func projectDrainTargets(input DrainProjectionInput) (DrainProjection, error) {
	in := input.PlanInput
	chargeProbe := func() error {
		if input.ProbeBudget == nil {
			return nil
		}
		ctx := input.ProbeContext
		if ctx == nil {
			ctx = context.Background()
		}
		return input.ProbeBudget.ConsumeDeadlockProbeOperations(ctx, 1)
	}
	projection := DrainProjection{
		TargetByRel:   make(map[string]CPUSetTarget, len(in.Snapshot.Entries)),
		DomainUnion:   make(map[DomainID]machine.CPUSet),
		EmptyBlockers: make(map[string]machine.CPUSet),
	}
	rels := sortedSnapshotRels(in.Snapshot, input.DepthByRel)
	bucketUpperByRel := make(map[string]machine.CPUSet)
	for _, rel := range rels {
		if relInDormantSubtree(rel, in.DormantRels) {
			continue
		}
		if node := in.DAG.index[rel]; node != nil {
			if node.Role == TopoNodeRoleReclaimNUMABucket &&
				!node.Constraint.CPUUpperBound.IsEmpty() {
				bucketUpperByRel[rel] = node.Constraint.CPUUpperBound
			} else if upper, ok := bucketUpperByRel[input.ParentByRel[rel]]; ok {
				bucketUpperByRel[rel] = upper
			}
			continue
		}
		if upper, ok := bucketUpperByRel[input.ParentByRel[rel]]; ok {
			bucketUpperByRel[rel] = upper
		}
	}
	for i := len(rels) - 1; i >= 0; i-- {
		if err := chargeProbe(); err != nil {
			return DrainProjection{}, err
		}
		rel := rels[i]
		if relInDormantSubtree(rel, in.DormantRels) {
			continue
		}
		projection.Cost.Rels++
		required := machine.NewCPUSet()
		node := in.DAG.index[rel]
		if node != nil {
			required = required.Union(in.DesiredByRel[rel])
			if node.Role == TopoNodeRolePrimary {
				required = required.Union(in.ProtectedPending)
			}
		} else if explicit, ok := in.DynamicByRel[rel]; ok {
			required = required.Union(explicit)
		}
		if input.Context != nil {
			required = required.Union(input.Context.protectedDescendantUnionByRel[rel])
		} else {
			for protectedRel, cpus := range in.ProtectedByRel {
				if err := chargeProbe(); err != nil {
					return DrainProjection{}, err
				}
				projection.Cost.ProtectedRels++
				if protectedRel == rel || strings.HasPrefix(protectedRel, rel+"/") {
					required = required.Union(cpus)
				}
			}
		}
		for _, child := range in.Snapshot.Children[rel] {
			childRel := filepath.Join(rel, child.Name)
			if relInDormantSubtree(childRel, in.DormantRels) {
				continue
			}
			if err := chargeProbe(); err != nil {
				return DrainProjection{}, err
			}
			projection.Cost.Children++
			if childTarget, ok := projection.TargetByRel[childRel]; ok {
				required = required.Union(childTarget.CPUs)
			}
		}
		target, blocker, err := projectDrainRel(input, rel, required, bucketUpperByRel[rel])
		if err != nil {
			return DrainProjection{}, err
		}
		if !blocker.IsEmpty() {
			projection.EmptyBlockers[rel] = blocker
		}
		projection.TargetByRel[rel] = target
		domain := input.DomainByRel[rel]
		projection.DomainUnion[domain] = projection.DomainUnion[domain].Union(target.CPUs)
	}
	return projection, nil
}

func projectDrainRel(
	input DrainProjectionInput,
	rel string,
	required machine.CPUSet,
	bucketUpper machine.CPUSet,
) (CPUSetTarget, machine.CPUSet, error) {
	in := input.PlanInput
	entry := in.Snapshot.Entries[rel]
	node := in.DAG.index[rel]
	domain := input.DomainByRel[rel]
	required = required.Intersection(entry.CPUs)
	var safeTarget machine.CPUSet
	if node != nil && node.Role == TopoNodeRoleReclaimNUMABucket {
		desired := in.DesiredByRel[rel]
		relLeaving := entry.CPUs.Difference(desired).Difference(required)
		relDrain := relLeaving.Intersection(input.DrainBatch[domain])
		if input.LeavingByDomain[domain].IsEmpty() {
			relDrain = relLeaving
		}
		safeTarget = entry.CPUs.Difference(relDrain).Union(required)
	} else {
		safeTarget = entry.CPUs.Difference(input.DrainBatch[domain]).Union(required)
		if input.LeavingByDomain[domain].IsEmpty() {
			safeTarget = required
		}
	}
	final := finalCPUSetForRel(rel, in.DAG, input.ParentByRel, in.DynamicByRel, in.DesiredByRel)
	if node == nil && input.LeavingByDomain[domain].IsEmpty() && safeTarget.IsEmpty() {
		safeTarget = safeTarget.Union(entry.CPUs.Intersection(final))
	}
	target, err := buildPhaseTransition(PhaseDrain, RelTransition{
		Current: entry.CPUs, Final: final, SafeDrainTarget: safeTarget,
		AllowEmptyTarget: in.AllowEmptyTarget,
	})
	if err != nil {
		return CPUSetTarget{}, machine.NewCPUSet(), err
	}
	blocker := machine.NewCPUSet()
	if !in.AllowEmptyTarget && safeTarget.IsEmpty() && !target.IsEmpty() &&
		!entry.CPUs.Difference(final).IsEmpty() {
		blocker = entry.CPUs.Difference(final)
	}
	if node == nil && !bucketUpper.IsEmpty() {
		if !required.IsSubsetOf(bucketUpper) {
			return CPUSetTarget{}, machine.NewCPUSet(), fmt.Errorf(
				"%w: dynamic descendant=%q required CPUs=%s outside nearest bucket upper=%s",
				ErrInvalidReclaimBucketTarget, rel, required.String(), bucketUpper.String())
		}
		target = constrainDrainTargetToUpper(target, required, bucketUpper, in.AllowEmptyTarget)
	}
	if node != nil && node.Role == TopoNodeRoleReclaimNUMABucket &&
		!node.Constraint.CPUUpperBound.IsEmpty() {
		target = constrainDrainTargetToUpper(
			target, required, node.Constraint.CPUUpperBound, in.AllowEmptyTarget,
		)
	}
	mems := entry.Mems
	if node != nil {
		if desiredMems := in.DesiredMemsByRel[rel]; desiredMems != "" {
			mems = desiredMems
		} else if node.Mems != "" {
			mems = node.Mems
		}
	}
	return CPUSetTarget{CPUs: target, Mems: mems}, blocker, nil
}

func constrainDrainTargetToUpper(
	target, required, upper machine.CPUSet,
	allowEmptyTarget bool,
) machine.CPUSet {
	constrained := target.Intersection(upper).Union(required)
	if !allowEmptyTarget && !target.IsEmpty() && constrained.IsEmpty() {
		return target
	}
	return constrained
}
