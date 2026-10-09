/*
Copyright 2026 The Katalyst Authors.

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
	"reflect"
	"sort"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type FrozenBoundaryVersion uint8

const FrozenBoundaryVersionV1 FrozenBoundaryVersion = 1

// FrozenBoundary is the compiler-owned semantic read boundary for one frozen
// trace. ScanBoundary.ExpandedRels remains diagnostic and is not part of this
// execution contract.
type FrozenBoundary struct {
	Version             FrozenBoundaryVersion
	Roots               []string
	ControlledRels      []string
	ShrinkChildrenByRel map[string][]ChildRef
	RelevantCPUHolders  []string
	RetirableCPUHolders []string
	RelevantHolderPaths map[string][]FrozenRelIdentity
	RelevantCPUs        machine.CPUSet
}

type FrozenRelIdentity struct {
	Rel      string
	Identity CgroupIdentity
}

type FrozenBoundaryEvaluation struct {
	Snapshot *CompleteSnapshot
}

// compileFrozenBoundaryV1 is the sole owner of deriving the V1 semantic
// closure from the compiler's initial snapshot and immutable phase sequence.
// The closure pins every controlled rel, shrink child listing, relevant CPU
// holder, and holder ancestry identity that live execution is allowed to
// observe; missing required evidence is an error rather than implicit absence.
func compileFrozenBoundaryV1(
	snapshot *CompleteSnapshot,
	input FrozenCoordinatorEvaluationInput,
	phases []CompiledPhase,
) (FrozenBoundary, error) {
	if snapshot == nil {
		return FrozenBoundary{}, fmt.Errorf("compile frozen boundary requires initial snapshot")
	}
	controlled := make(map[string]struct{}, len(input.DAGSpecs))
	for _, spec := range input.DAGSpecs {
		controlled[spec.Rel] = struct{}{}
	}
	relevant := input.ProtectedPending.Clone()
	shrinkRels := make(map[string]struct{})
	for _, values := range []map[string]machine.CPUSet{
		input.ExpectedByRel,
		input.TargetByRel,
		input.ParentSafetyTargetByRel,
		input.RequiredByRel,
		input.DeferredByRel,
		input.PendingRequiredByRel,
	} {
		for _, cpus := range values {
			relevant = relevant.Union(cpus)
		}
	}
	for _, phase := range phases {
		for _, operation := range phase.Operations {
			if operation.Direction == WriteShrink {
				shrinkRels[operation.Rel] = struct{}{}
			}
			relevant = relevant.Union(operation.ExpectedCurrent.CPUs).
				Union(operation.Target.CPUs)
		}
	}

	boundary := FrozenBoundary{
		Version:             FrozenBoundaryVersionV1,
		Roots:               normalizeRels(snapshot.ScanBoundary.Roots),
		ControlledRels:      sortedStringKeys(controlled),
		ShrinkChildrenByRel: make(map[string][]ChildRef, len(shrinkRels)),
		RelevantCPUs:        relevant,
	}
	for _, rel := range boundary.ControlledRels {
		if _, ok := snapshot.Entries[rel]; !ok {
			return FrozenBoundary{}, fmt.Errorf("controlled rel %q is absent from initial snapshot", rel)
		}
	}
	for rel := range shrinkRels {
		if _, ok := snapshot.Entries[rel]; !ok {
			return FrozenBoundary{}, fmt.Errorf("shrink rel %q is absent from initial snapshot", rel)
		}
		boundary.ShrinkChildrenByRel[rel] = cloneSortedChildRefs(snapshot.Children[rel])
	}
	for rel, entry := range snapshot.Entries {
		if _, ok := controlled[rel]; ok {
			continue
		}
		if !entry.CPUs.Intersection(relevant).IsEmpty() {
			boundary.RelevantCPUHolders = append(boundary.RelevantCPUHolders, rel)
		}
	}
	sort.Strings(boundary.RelevantCPUHolders)
	childIdentities, err := indexFrozenChildIdentities(snapshot)
	if err != nil {
		return FrozenBoundary{}, err
	}
	semanticRels := indexFrozenDynamicSemanticRels(input, phases, controlled)
	boundary.RelevantHolderPaths = make(
		map[string][]FrozenRelIdentity, len(boundary.RelevantCPUHolders))
	for _, rel := range boundary.RelevantCPUHolders {
		path, err := compileFrozenHolderPath(snapshot, childIdentities, controlled, rel)
		if err != nil {
			return FrozenBoundary{}, err
		}
		boundary.RelevantHolderPaths[rel] = path
		if !semanticRels.references(rel) {
			boundary.RetirableCPUHolders = append(boundary.RetirableCPUHolders, rel)
		}
	}
	if err := validateFrozenBoundary(boundary, snapshot); err != nil {
		return FrozenBoundary{}, err
	}
	return boundary, nil
}

func validateFrozenBoundary(boundary FrozenBoundary, snapshot *CompleteSnapshot) error {
	if boundary.Version != FrozenBoundaryVersionV1 {
		return fmt.Errorf("unsupported frozen boundary version %d", boundary.Version)
	}
	if snapshot == nil {
		return fmt.Errorf("frozen boundary requires snapshot evidence")
	}
	if len(boundary.Roots) == 0 {
		return fmt.Errorf("frozen boundary requires roots")
	}
	controlled := make(map[string]struct{}, len(boundary.ControlledRels))
	for _, rel := range boundary.ControlledRels {
		controlled[rel] = struct{}{}
		if _, ok := snapshot.Entries[rel]; !ok {
			return fmt.Errorf("frozen boundary controlled rel %q has no snapshot entry", rel)
		}
	}
	childIdentities, err := indexFrozenChildIdentities(snapshot)
	if err != nil {
		return err
	}
	for rel, children := range boundary.ShrinkChildrenByRel {
		if _, ok := snapshot.Entries[rel]; !ok {
			return fmt.Errorf("frozen boundary shrink rel %q has no snapshot entry", rel)
		}
		if !equalChildRefs(
			cloneSortedChildRefs(children),
			cloneSortedChildRefs(snapshot.Children[rel]),
		) {
			return fmt.Errorf("frozen boundary shrink children for %q do not match snapshot", rel)
		}
	}
	for _, rel := range boundary.RelevantCPUHolders {
		entry, ok := snapshot.Entries[rel]
		if !ok {
			return fmt.Errorf("frozen boundary relevant CPU holder %q has no snapshot entry", rel)
		}
		if entry.CPUs.Intersection(boundary.RelevantCPUs).IsEmpty() {
			return fmt.Errorf("frozen boundary holder %q does not hold relevant CPUs", rel)
		}
		path, ok := boundary.RelevantHolderPaths[rel]
		if !ok {
			return fmt.Errorf("frozen boundary holder %q has no controlled ancestor path", rel)
		}
		if err := validateFrozenHolderPath(
			snapshot, childIdentities, controlled, rel, path,
		); err != nil {
			return err
		}
	}
	if len(boundary.RelevantHolderPaths) != len(boundary.RelevantCPUHolders) {
		return fmt.Errorf("frozen boundary holder coverage does not match relevant holders")
	}
	relevantHolders := make(map[string]struct{}, len(boundary.RelevantCPUHolders))
	for _, rel := range boundary.RelevantCPUHolders {
		relevantHolders[rel] = struct{}{}
	}
	retirableHolders := make(map[string]struct{}, len(boundary.RetirableCPUHolders))
	for _, rel := range boundary.RetirableCPUHolders {
		if _, duplicate := retirableHolders[rel]; duplicate {
			return fmt.Errorf("frozen boundary has duplicate retirable holder %q", rel)
		}
		retirableHolders[rel] = struct{}{}
		if _, isControlled := controlled[rel]; isControlled {
			return fmt.Errorf("frozen boundary retirable holder %q is controlled", rel)
		}
		if _, ok := relevantHolders[rel]; !ok {
			return fmt.Errorf("frozen boundary retirable holder %q is not relevant", rel)
		}
	}
	return nil
}

func cloneFrozenBoundary(in FrozenBoundary) FrozenBoundary {
	out := in
	out.Roots = append([]string(nil), in.Roots...)
	out.ControlledRels = append([]string(nil), in.ControlledRels...)
	out.ShrinkChildrenByRel = make(map[string][]ChildRef, len(in.ShrinkChildrenByRel))
	for rel, children := range in.ShrinkChildrenByRel {
		out.ShrinkChildrenByRel[rel] = append([]ChildRef(nil), children...)
	}
	out.RelevantCPUHolders = append([]string(nil), in.RelevantCPUHolders...)
	out.RetirableCPUHolders = append([]string(nil), in.RetirableCPUHolders...)
	out.RelevantHolderPaths = make(map[string][]FrozenRelIdentity, len(in.RelevantHolderPaths))
	for rel, path := range in.RelevantHolderPaths {
		out.RelevantHolderPaths[rel] = append([]FrozenRelIdentity(nil), path...)
	}
	out.RelevantCPUs = in.RelevantCPUs.Clone()
	return out
}

type frozenDynamicSemanticRelIndex struct {
	exact     map[string]struct{}
	ancestors map[string]struct{}
}

func indexFrozenDynamicSemanticRels(
	input FrozenCoordinatorEvaluationInput,
	phases []CompiledPhase,
	controlled map[string]struct{},
) frozenDynamicSemanticRelIndex {
	index := frozenDynamicSemanticRelIndex{
		exact:     make(map[string]struct{}),
		ancestors: make(map[string]struct{}),
	}
	add := func(rel string) {
		if _, isControlled := controlled[rel]; isControlled {
			return
		}
		index.exact[rel] = struct{}{}
		for current := rel; current != "."; current = filepath.Dir(current) {
			index.ancestors[current] = struct{}{}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
		}
	}
	for _, rels := range []map[string]machine.CPUSet{
		input.TargetByRel,
		input.ParentSafetyTargetByRel,
		input.ExpectedByRel,
		input.RequiredByRel,
		input.DeferredByRel,
		input.PendingRequiredByRel,
	} {
		for rel := range rels {
			add(rel)
		}
	}
	for rel := range input.DeferredCleanupRels {
		add(rel)
	}
	for _, phase := range phases {
		for _, operation := range phase.Operations {
			add(operation.Rel)
		}
	}
	return index
}

func (i frozenDynamicSemanticRelIndex) references(holderRel string) bool {
	if _, semanticDescendant := i.ancestors[holderRel]; semanticDescendant {
		return true
	}
	for current := holderRel; current != "."; current = filepath.Dir(current) {
		if _, semanticAncestor := i.exact[current]; semanticAncestor {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return false
}

func compileFrozenHolderPath(
	snapshot *CompleteSnapshot,
	childIdentities map[string]map[string]CgroupIdentity,
	controlled map[string]struct{},
	holderRel string,
) ([]FrozenRelIdentity, error) {
	var reversed []FrozenRelIdentity
	currentRel := holderRel
	visited := make(map[string]struct{})
	for {
		if _, duplicate := visited[currentRel]; duplicate {
			return nil, fmt.Errorf("relevant holder %q controlled ancestor path contains a cycle", holderRel)
		}
		visited[currentRel] = struct{}{}
		entry, ok := snapshot.Entries[currentRel]
		if !ok {
			return nil, fmt.Errorf(
				"relevant holder %q controlled ancestor path is missing rel %q",
				holderRel, currentRel,
			)
		}
		reversed = append(reversed, FrozenRelIdentity{
			Rel: currentRel, Identity: entry.Identity,
		})
		if _, ok := controlled[currentRel]; ok {
			break
		}
		parentRel := filepath.Dir(currentRel)
		if parentRel == "." || parentRel == currentRel {
			return nil, fmt.Errorf(
				"relevant holder %q has no controlled ancestor", holderRel)
		}
		if err := validateFrozenHolderParentEdge(
			childIdentities, parentRel, currentRel, entry.Identity,
		); err != nil {
			return nil, fmt.Errorf(
				"relevant holder %q controlled ancestor path: %w", holderRel, err)
		}
		currentRel = parentRel
	}
	path := make([]FrozenRelIdentity, len(reversed))
	for i := range reversed {
		path[len(reversed)-1-i] = reversed[i]
	}
	return path, nil
}

func validateFrozenHolderPath(
	snapshot *CompleteSnapshot,
	childIdentities map[string]map[string]CgroupIdentity,
	controlled map[string]struct{},
	holderRel string,
	path []FrozenRelIdentity,
) error {
	if len(path) < 2 || path[len(path)-1].Rel != holderRel {
		return fmt.Errorf("frozen holder coverage for %q has invalid endpoints", holderRel)
	}
	if _, ok := controlled[path[0].Rel]; !ok {
		return fmt.Errorf(
			"frozen holder coverage for %q does not start at a controlled ancestor",
			holderRel,
		)
	}
	for i, item := range path {
		entry, ok := snapshot.Entries[item.Rel]
		if !ok || entry.Identity != item.Identity {
			return fmt.Errorf(
				"frozen holder coverage for %q identity changed at %q",
				holderRel, item.Rel,
			)
		}
		if i == 0 {
			continue
		}
		if filepath.Dir(item.Rel) != path[i-1].Rel {
			return fmt.Errorf(
				"frozen holder coverage for %q is not a direct identity path",
				holderRel,
			)
		}
		if err := validateFrozenHolderParentEdge(
			childIdentities, path[i-1].Rel, item.Rel, item.Identity,
		); err != nil {
			return fmt.Errorf("frozen holder coverage for %q: %w", holderRel, err)
		}
	}
	return nil
}

func validateFrozenHolderParentEdge(
	childIdentities map[string]map[string]CgroupIdentity,
	parentRel, childRel string,
	childIdentity CgroupIdentity,
) error {
	name := filepath.Base(childRel)
	identity, ok := childIdentities[parentRel][name]
	if !ok {
		return fmt.Errorf(
			"parent edge %q -> %q has no identity link", parentRel, childRel)
	}
	if identity != childIdentity {
		return fmt.Errorf(
			"parent edge %q -> %q identity mismatch", parentRel, childRel)
	}
	return nil
}

func indexFrozenChildIdentities(
	snapshot *CompleteSnapshot,
) (map[string]map[string]CgroupIdentity, error) {
	index := make(map[string]map[string]CgroupIdentity, len(snapshot.Children))
	for parentRel, children := range snapshot.Children {
		byName := make(map[string]CgroupIdentity, len(children))
		for _, child := range children {
			if _, duplicate := byName[child.Name]; duplicate {
				return nil, fmt.Errorf(
					"parent %q has duplicate child identity evidence for %q",
					parentRel, child.Name,
				)
			}
			byName[child.Name] = child.Identity
		}
		index[parentRel] = byName
	}
	return index, nil
}

// EvaluateFrozenBoundary is the sole owner of refreshing and checking the
// compiler-owned execution boundary. It accepts identity-scoped retirement
// only when the scanner reports typed path absence, then projects a
// root-complete snapshot whose evidence closure is revalidated. Dynamic
// descendants outside the boundary remain observable but do not invalidate a
// trace merely because they churn.
func EvaluateFrozenBoundary(
	ctx context.Context,
	driver HierarchyDriver,
	dag *TopoDAG,
	budget *BudgetTracker,
	boundary FrozenBoundary,
	expected *CompleteSnapshot,
) (FrozenBoundaryEvaluation, error) {
	if err := validateFrozenBoundary(boundary, expected); err != nil {
		return FrozenBoundaryEvaluation{}, err
	}
	fresh, err := buildCompleteSnapshot(
		ctx,
		driver,
		dag,
		SnapshotRequest{
			Purpose:                ScanForPlan,
			AffectedRels:           boundary.Roots,
			CollectDormantActivity: expected != nil && !expected.Capabilities.EmptyConfiguredCPUSet,
		},
		budget,
		nil,
		frozenBoundaryRetirementAuthorizations(boundary),
	)
	if err != nil {
		return FrozenBoundaryEvaluation{}, err
	}
	if err := evaluateFrozenBoundarySnapshot(boundary, expected, fresh); err != nil {
		return FrozenBoundaryEvaluation{Snapshot: fresh}, err
	}
	projected := projectFrozenBoundarySnapshot(boundary, expected, fresh)
	if err := validateCompleteSnapshotEvidence(projected); err != nil {
		return FrozenBoundaryEvaluation{Snapshot: projected},
			fmt.Errorf("validate frozen boundary projection: %w", err)
	}
	return FrozenBoundaryEvaluation{Snapshot: projected}, nil
}

func projectFrozenBoundarySnapshot(
	boundary FrozenBoundary,
	expected, current *CompleteSnapshot,
) *CompleteSnapshot {
	projected := CloneCompleteSnapshot(expected)
	retirableHolders, retirablePaths := indexFrozenRetirablePaths(boundary)
	retiredRoots := make(map[string]CgroupIdentity)
	for _, rel := range boundary.ControlledRels {
		projected.Entries[rel] = cloneEntryState(current.Entries[rel])
	}
	for rel := range boundary.ShrinkChildrenByRel {
		projected.Children[rel] = append([]ChildRef(nil), current.Children[rel]...)
		for _, child := range current.Children[rel] {
			childRel := filepath.Join(rel, child.Name)
			if entry, ok := current.Entries[childRel]; ok {
				projected.Entries[childRel] = cloneEntryState(entry)
			}
		}
	}
	for _, rel := range boundary.ControlledRels {
		if _, shrink := boundary.ShrinkChildrenByRel[rel]; shrink {
			continue
		}
		projectGrowChildren(projected, current, rel)
	}
	for _, rel := range boundary.RelevantCPUHolders {
		retiredPath := false
		for _, item := range boundary.RelevantHolderPaths[rel] {
			if _, present := current.Entries[item.Rel]; present {
				continue
			}
			if _, retirable := retirablePaths[item.Rel]; retirable {
				retiredRoots[item.Rel] = item.Identity
				retiredPath = true
				break
			}
		}
		if retiredPath {
			continue
		}
		if entry, ok := current.Entries[rel]; ok {
			projected.Entries[rel] = cloneEntryState(entry)
		} else if _, retirable := retirableHolders[rel]; retirable {
			retiredRoots[rel] = expected.Entries[rel].Identity
			continue
		}
		for _, item := range boundary.RelevantHolderPaths[rel] {
			if entry, ok := current.Entries[item.Rel]; ok {
				projected.Entries[item.Rel] = cloneEntryState(entry)
			}
		}
	}
	retireProjectedSubtrees(projected, retiredRoots)
	projected.DomainUnion = make(map[DomainID]machine.CPUSet)
	for rel, entry := range projected.Entries {
		domain := projected.DomainByRel[rel]
		projected.DomainUnion[domain] = projected.DomainUnion[domain].Union(entry.CPUs)
	}
	projected.ID = fingerprintSnapshot(projected)
	return projected
}

// retireProjectedSubtrees removes each minimal retired root and all of its
// descendant evidence from a projection. It also removes the matching
// identity-bearing parent edge and expanded markers so the remaining snapshot
// preserves entry, domain, child, and traversal closure.
func retireProjectedSubtrees(
	snapshot *CompleteSnapshot,
	retired map[string]CgroupIdentity,
) {
	if len(retired) == 0 {
		return
	}
	roots := make(map[string]CgroupIdentity, len(retired))
	for rel, identity := range retired {
		hasRetiredAncestor := false
		for parent := filepath.Dir(rel); parent != "." && parent != rel; parent = filepath.Dir(parent) {
			if _, ok := retired[parent]; ok {
				hasRetiredAncestor = true
				break
			}
		}
		if !hasRetiredAncestor {
			roots[rel] = identity
		}
	}
	underRetiredRoot := func(rel string) bool {
		for current := rel; current != "."; current = filepath.Dir(current) {
			if _, ok := roots[current]; ok {
				return true
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
		}
		return false
	}
	for rel := range snapshot.Entries {
		if underRetiredRoot(rel) {
			delete(snapshot.Entries, rel)
			delete(snapshot.DomainByRel, rel)
			delete(snapshot.UnavailableChildren, rel)
		}
	}
	for rel := range snapshot.UnavailableChildren {
		if underRetiredRoot(rel) {
			delete(snapshot.UnavailableChildren, rel)
		}
	}
	for parentRel, children := range snapshot.Children {
		if underRetiredRoot(parentRel) {
			delete(snapshot.Children, parentRel)
			continue
		}
		retained := children[:0]
		for _, child := range children {
			childRel := filepath.Join(parentRel, child.Name)
			rootIdentity, isRoot := roots[childRel]
			if isRoot && child.Identity == rootIdentity {
				continue
			}
			retained = append(retained, child)
		}
		snapshot.Children[parentRel] = retained
	}
	expanded := snapshot.ScanBoundary.ExpandedRels[:0]
	for _, rel := range snapshot.ScanBoundary.ExpandedRels {
		if !underRetiredRoot(rel) {
			expanded = append(expanded, rel)
		}
	}
	snapshot.ScanBoundary.ExpandedRels = expanded
}

func projectGrowChildren(projected, current *CompleteSnapshot, rel string) {
	currentChildren := make(map[ChildRef]struct{}, len(current.Children[rel]))
	for _, child := range current.Children[rel] {
		currentChildren[child] = struct{}{}
	}
	retained := make([]ChildRef, 0, len(projected.Children[rel]))
	for _, child := range projected.Children[rel] {
		if _, ok := currentChildren[child]; ok {
			retained = append(retained, child)
			continue
		}
		deleteProjectedSubtree(projected, filepath.Join(rel, child.Name))
	}
	projected.Children[rel] = retained
}

func deleteProjectedSubtree(snapshot *CompleteSnapshot, rel string) {
	prefix := rel + "/"
	underDeletedRoot := func(candidate string) bool {
		return candidate == rel || strings.HasPrefix(candidate, prefix)
	}
	for candidate := range snapshot.Entries {
		if underDeletedRoot(candidate) {
			delete(snapshot.Entries, candidate)
			delete(snapshot.Children, candidate)
			delete(snapshot.DomainByRel, candidate)
			delete(snapshot.UnavailableChildren, candidate)
		}
	}
	for candidate := range snapshot.UnavailableChildren {
		if underDeletedRoot(candidate) {
			delete(snapshot.UnavailableChildren, candidate)
		}
	}
	expanded := snapshot.ScanBoundary.ExpandedRels[:0]
	for _, candidate := range snapshot.ScanBoundary.ExpandedRels {
		if !underDeletedRoot(candidate) {
			expanded = append(expanded, candidate)
		}
	}
	snapshot.ScanBoundary.ExpandedRels = expanded
}

func cloneEntryState(entry EntryState) EntryState {
	entry.CPUs = entry.CPUs.Clone()
	entry.ConfiguredCPUs = entry.ConfiguredCPUs.Clone()
	return entry
}

func evaluateFrozenBoundarySnapshot(
	boundary FrozenBoundary,
	expected, current *CompleteSnapshot,
) error {
	if err := validateFrozenBoundary(boundary, expected); err != nil {
		return err
	}
	if current == nil {
		return frozenBoundaryStale("boundary", "<nil>", "snapshot", fmt.Errorf("fresh snapshot is nil"))
	}
	controlled := make(map[string]struct{}, len(boundary.ControlledRels))
	for _, rel := range boundary.ControlledRels {
		controlled[rel] = struct{}{}
		want, wantOK := expected.Entries[rel]
		got, gotOK := current.Entries[rel]
		if !wantOK || !gotOK {
			return frozenBoundaryStale(
				rel, fmt.Sprintf("exists=%t", gotOK), fmt.Sprintf("exists=%t", wantOK),
				fmt.Errorf("controlled relation presence changed"))
		}
		if !entryPhysicalStateEqual(got, want) {
			return frozenBoundaryStale(
				rel, frozenEntryStateString(got), frozenEntryStateString(want),
				fmt.Errorf("controlled relation state changed"))
		}
	}
	retirableHolders, retirablePaths := indexFrozenRetirablePaths(boundary)
	for rel, children := range boundary.ShrinkChildrenByRel {
		gotChildren := cloneSortedChildRefs(current.Children[rel])
		wantChildren := cloneSortedChildRefs(children)
		retainedWantChildren, err := frozenRetainedShrinkChildren(
			retirablePaths, rel, wantChildren, gotChildren)
		if err != nil {
			return frozenBoundaryStale(
				rel, fmt.Sprint(gotChildren), fmt.Sprint(wantChildren),
				fmt.Errorf("shrink relation direct children changed: %w", err))
		}
		wantCPUs, wantMems, err := frozenDirectChildUnion(
			expected, rel, retainedWantChildren)
		if err != nil {
			return err
		}
		gotCPUs, gotMems, err := frozenDirectChildUnion(current, rel, gotChildren)
		if err != nil {
			return err
		}
		if !gotCPUs.Equals(wantCPUs) {
			return frozenBoundaryStale(
				rel, gotCPUs.String(), wantCPUs.String(),
				fmt.Errorf("shrink direct child CPU union changed"))
		}
		if !gotMems.Equals(wantMems) {
			return frozenBoundaryStale(
				rel, gotMems.String(), wantMems.String(),
				fmt.Errorf("shrink direct child mems union changed"))
		}
	}

	expectedHolders := make(map[string]struct{}, len(boundary.RelevantCPUHolders))
	for _, rel := range boundary.RelevantCPUHolders {
		expectedHolders[rel] = struct{}{}
	}
	currentHolders := make(map[string]struct{}, len(expectedHolders))
	for rel, entry := range current.Entries {
		if _, isControlled := controlled[rel]; isControlled {
			continue
		}
		if !entry.CPUs.Intersection(boundary.RelevantCPUs).IsEmpty() {
			currentHolders[rel] = struct{}{}
			if _, expected := expectedHolders[rel]; !expected {
				return frozenBoundaryStale(
					"dynamic", rel, fmt.Sprint(boundary.RelevantCPUHolders),
					fmt.Errorf("relevant CPU holder set changed: new holder appeared"))
			}
		}
	}
	childIdentities, err := indexFrozenChildIdentities(current)
	if err != nil {
		return frozenBoundaryStale(
			"dynamic", err.Error(), "unique holder coverage",
			fmt.Errorf("relevant holder coverage changed"))
	}
	for _, rel := range boundary.RelevantCPUHolders {
		want, wantOK := expected.Entries[rel]
		got, gotOK := current.Entries[rel]
		if !wantOK {
			return frozenBoundaryStale(
				rel, fmt.Sprintf("exists=%t", gotOK), fmt.Sprintf("exists=%t", wantOK),
				fmt.Errorf("relevant CPU holder presence changed"))
		}
		if !gotOK {
			if _, retirable := retirableHolders[rel]; retirable {
				continue
			}
			return frozenBoundaryStale(
				rel, "exists=false", "exists=true",
				fmt.Errorf("relevant CPU holder set changed: required holder disappeared"))
		}
		if !entryPhysicalStateEqual(got, want) {
			return frozenBoundaryStale(
				rel, frozenEntryStateString(got), frozenEntryStateString(want),
				fmt.Errorf("relevant CPU holder state changed"))
		}
		if err := validateFrozenHolderPath(
			current, childIdentities, controlled, rel, boundary.RelevantHolderPaths[rel],
		); err != nil {
			return frozenBoundaryStale(
				rel, err.Error(), "stable holder coverage",
				fmt.Errorf("relevant holder coverage changed"))
		}
	}
	return nil
}

func frozenRetainedShrinkChildren(
	retirablePaths map[string]struct{},
	parentRel string,
	expected, current []ChildRef,
) ([]ChildRef, error) {
	expectedByName := make(map[string]ChildRef, len(expected))
	for _, child := range expected {
		expectedByName[child.Name] = child
	}
	currentByName := make(map[string]ChildRef, len(current))
	for _, child := range current {
		want, ok := expectedByName[child.Name]
		if !ok {
			return nil, fmt.Errorf("new child %q appeared", child.Name)
		}
		if want.Identity != child.Identity {
			return nil, fmt.Errorf("child %q identity changed", child.Name)
		}
		currentByName[child.Name] = child
	}
	retained := make([]ChildRef, 0, len(current))
	for _, child := range expected {
		if _, ok := currentByName[child.Name]; ok {
			retained = append(retained, child)
			continue
		}
		childRel := filepath.Join(parentRel, child.Name)
		if _, retirable := retirablePaths[childRel]; !retirable {
			return nil, fmt.Errorf("required child %q disappeared", child.Name)
		}
	}
	return retained, nil
}

func indexFrozenRetirablePaths(
	boundary FrozenBoundary,
) (map[string]struct{}, map[string]struct{}) {
	retirable := make(map[string]struct{}, len(boundary.RetirableCPUHolders))
	for _, holderRel := range boundary.RetirableCPUHolders {
		retirable[holderRel] = struct{}{}
	}
	paths := make(map[string]struct{})
	requiredPaths := make(map[string]struct{})
	for holderRel, path := range boundary.RelevantHolderPaths {
		_, holderRetirable := retirable[holderRel]
		for _, item := range path {
			if holderRetirable {
				paths[item.Rel] = struct{}{}
			} else {
				requiredPaths[item.Rel] = struct{}{}
			}
		}
	}
	for rel := range requiredPaths {
		delete(paths, rel)
	}
	return retirable, paths
}

// frozenBoundaryRetirementAuthorizations derives the only identity-scoped
// absences that a boundary refresh may treat as retirement. Controlled rels
// and ancestry shared with a non-retirable holder stay required, preserving
// the boundary's evidence closure.
func frozenBoundaryRetirementAuthorizations(
	boundary FrozenBoundary,
) map[string]CgroupIdentity {
	_, retirablePaths := indexFrozenRetirablePaths(boundary)
	authorizations := make(map[string]CgroupIdentity, len(retirablePaths))
	controlled := make(map[string]struct{}, len(boundary.ControlledRels))
	for _, rel := range boundary.ControlledRels {
		controlled[rel] = struct{}{}
	}
	for _, holderRel := range boundary.RetirableCPUHolders {
		for _, item := range boundary.RelevantHolderPaths[holderRel] {
			if _, ok := retirablePaths[item.Rel]; !ok {
				continue
			}
			if _, isControlled := controlled[item.Rel]; isControlled {
				continue
			}
			authorizations[item.Rel] = item.Identity
		}
	}
	return authorizations
}

func frozenDirectChildUnion(
	snapshot *CompleteSnapshot,
	parentRel string,
	children []ChildRef,
) (machine.CPUSet, machine.CPUSet, error) {
	cpus := machine.NewCPUSet()
	mems := machine.NewCPUSet()
	for _, child := range children {
		childRel := filepath.Join(parentRel, child.Name)
		entry, ok := snapshot.Entries[childRel]
		if !ok {
			if _, unavailable := snapshot.UnavailableChildren[childRel]; unavailable {
				continue
			}
			return machine.CPUSet{}, machine.CPUSet{}, frozenBoundaryStale(
				parentRel, childRel, "direct child entry",
				fmt.Errorf("shrink direct child has no snapshot evidence"))
		}
		cpus = cpus.Union(entry.CPUs)
		if entry.Mems == "" {
			continue
		}
		childMems, err := machine.Parse(entry.Mems)
		if err != nil {
			return machine.CPUSet{}, machine.CPUSet{}, fmt.Errorf(
				"parse shrink direct child %q cpuset.mems=%q: %w",
				childRel, entry.Mems, err)
		}
		mems = mems.Union(childMems)
	}
	return cpus, mems, nil
}

func entryPhysicalStateEqual(left, right EntryState) bool {
	return left.Identity == right.Identity &&
		left.ConfiguredCPUs.Equals(right.ConfiguredCPUs) &&
		left.CPUs.Equals(right.CPUs) &&
		cpusetListValuesEqual(left.ConfiguredMems, right.ConfiguredMems) &&
		cpusetListValuesEqual(left.Mems, right.Mems)
}

func frozenEntryStateString(entry EntryState) string {
	return fmt.Sprintf(
		"identity=%v configured_cpus=%s effective_cpus=%s configured_mems=%s effective_mems=%s",
		entry.Identity, entry.ConfiguredCPUs.String(), entry.CPUs.String(),
		entry.ConfiguredMems, entry.Mems)
}

func frozenBoundaryStale(rel, current, target string, cause error) error {
	return &PlanStaleError{
		Rel:       rel,
		Direction: WritePublish,
		Resource:  "frozen_boundary",
		Current:   current,
		Target:    target,
		Err:       cause,
	}
}

func cloneSortedChildRefs(in []ChildRef) []ChildRef {
	out := append([]ChildRef(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Identity.Device != out[j].Identity.Device {
			return out[i].Identity.Device < out[j].Identity.Device
		}
		return out[i].Identity.Inode < out[j].Identity.Inode
	})
	return out
}

func equalChildRefs(left, right []ChildRef) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func frozenBoundariesEqual(left, right FrozenBoundary) bool {
	return left.Version == right.Version &&
		reflect.DeepEqual(left.Roots, right.Roots) &&
		reflect.DeepEqual(left.ControlledRels, right.ControlledRels) &&
		reflect.DeepEqual(left.ShrinkChildrenByRel, right.ShrinkChildrenByRel) &&
		reflect.DeepEqual(left.RelevantCPUHolders, right.RelevantCPUHolders) &&
		reflect.DeepEqual(left.RetirableCPUHolders, right.RetirableCPUHolders) &&
		reflect.DeepEqual(left.RelevantHolderPaths, right.RelevantHolderPaths) &&
		left.RelevantCPUs.Equals(right.RelevantCPUs)
}

func writeFrozenBoundaryHash(
	hash interface{ Write([]byte) (int, error) },
	boundary FrozenBoundary,
) {
	writeHashString(hash, "frozen-boundary")
	writeHashUint64(hash, uint64(boundary.Version))
	writeStringSliceHash(hash, boundary.Roots)
	writeStringSliceHash(hash, boundary.ControlledRels)
	shrinkRels := sortedStringKeys(boundary.ShrinkChildrenByRel)
	writeHashUint64(hash, uint64(len(shrinkRels)))
	for _, rel := range shrinkRels {
		writeHashString(hash, rel)
		children := boundary.ShrinkChildrenByRel[rel]
		writeHashUint64(hash, uint64(len(children)))
		for _, child := range children {
			writeHashString(hash, child.Name)
			writeHashUint64(hash, child.Identity.Device)
			writeHashUint64(hash, child.Identity.Inode)
		}
	}
	writeStringSliceHash(hash, boundary.RelevantCPUHolders)
	writeStringSliceHash(hash, boundary.RetirableCPUHolders)
	holderRels := sortedStringKeys(boundary.RelevantHolderPaths)
	writeHashUint64(hash, uint64(len(holderRels)))
	for _, rel := range holderRels {
		writeHashString(hash, rel)
		path := boundary.RelevantHolderPaths[rel]
		writeHashUint64(hash, uint64(len(path)))
		for _, item := range path {
			writeHashString(hash, item.Rel)
			writeHashUint64(hash, item.Identity.Device)
			writeHashUint64(hash, item.Identity.Inode)
		}
	}
	writeHashString(hash, boundary.RelevantCPUs.String())
}

func writeStringSliceHash(
	hash interface{ Write([]byte) (int, error) },
	values []string,
) {
	writeHashUint64(hash, uint64(len(values)))
	for _, value := range values {
		writeHashString(hash, value)
	}
}
