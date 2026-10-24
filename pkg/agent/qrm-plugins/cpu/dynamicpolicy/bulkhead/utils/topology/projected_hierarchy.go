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
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ErrProjectedParentContainment reports that a projected grow would place a
// child's configured cpuset outside its parent's effective set, which the
// kernel would reject. The projection rejects it before mutating the clone so a
// compiled trace never freezes an operation the live hierarchy would refuse.
var ErrProjectedParentContainment = errors.New("projected target exceeds parent effective cpuset")

// ErrProjectedExternalInheritanceUnknown reports that a snapshot root uses
// cgroup v2 inheritance but the snapshot does not contain enough initial
// effective state to model its parent outside the captured hierarchy.
var ErrProjectedExternalInheritanceUnknown = errors.New("projected external inheritance is unknown")

// ErrProjectedFrontierDependency reports that one projected frontier contains
// an ancestor and descendant. Such operations depend on intermediate effective
// state and therefore cannot share one evidence settlement.
var ErrProjectedFrontierDependency = errors.New("projected frontier contains dependent operations")

type projectedExternalInheritance struct {
	CPUs    machine.CPUSet
	Mems    string
	hasCPUs bool
	hasMems bool
}

// projectedHierarchy applies ordered plan operations to a cloned snapshot using
// the exact cgroup-version semantics of the backend the trace targets. It never
// touches a live driver: every mutation lands on the clone, so the fixed-point
// compiler can model a complete Drain->Expand trace and prove its end state
// without disturbing captured evidence or the real hierarchy.
//
// Parent/child relationships are resolved by stable cgroup identity through the
// snapshot's Children map, not by path strings, so inheritance follows the same
// edges the driver observed.
type projectedHierarchy struct {
	snapshot         *CompleteSnapshot
	capabilities     HierarchyCapabilities
	parentByRel      map[string]string
	childrenByRel    map[string][]string
	externalByRel    map[string]projectedExternalInheritance
	evidenceRebuilds int
}

// newProjectedHierarchy clones base and indexes its parent/child edges by
// identity so projection can re-derive effective state after every operation.
func newProjectedHierarchy(
	base *CompleteSnapshot,
	capabilities HierarchyCapabilities,
) (*projectedHierarchy, error) {
	if base == nil {
		return nil, fmt.Errorf("projected hierarchy requires a base snapshot")
	}
	snapshot := CloneCompleteSnapshot(base)
	snapshot.Capabilities = capabilities
	if err := validateUnavailableChildEvidence(snapshot); err != nil {
		return nil, fmt.Errorf("projected hierarchy has invalid unavailable-child evidence: %w", err)
	}

	identityToRel := make(map[CgroupIdentity]string, len(snapshot.Entries))
	for rel, entry := range snapshot.Entries {
		identityToRel[entry.Identity] = rel
	}
	parentByRel := make(map[string]string, len(snapshot.Entries))
	childrenByRel := make(map[string][]string, len(snapshot.Children))
	for parentRel, refs := range snapshot.Children {
		for _, ref := range refs {
			childRel, ok := identityToRel[ref.Identity]
			if !ok {
				continue
			}
			parentByRel[childRel] = parentRel
			childrenByRel[parentRel] = append(childrenByRel[parentRel], childRel)
		}
	}
	snapshotRoots := make(map[string]struct{}, len(snapshot.ScanBoundary.Roots))
	for _, rel := range snapshot.ScanBoundary.Roots {
		snapshotRoots[rel] = struct{}{}
	}
	externalByRel := make(map[string]projectedExternalInheritance)
	for rel, entry := range snapshot.Entries {
		if parentByRel[rel] != "" {
			continue
		}
		if !capabilities.EmptyConfiguredCPUSet {
			continue
		}
		if _, isSnapshotRoot := snapshotRoots[rel]; !isSnapshotRoot {
			return nil, fmt.Errorf("%w: rel %q has neither an internal parent nor a snapshot-root boundary",
				ErrProjectedExternalInheritanceUnknown, rel)
		}
		boundary := projectedExternalInheritance{}
		if entry.ConfiguredCPUs.IsEmpty() {
			if entry.CPUs.IsEmpty() {
				return nil, fmt.Errorf("%w: root %q has no effective CPU evidence",
					ErrProjectedExternalInheritanceUnknown, rel)
			}
			boundary.CPUs = entry.CPUs.Clone()
			boundary.hasCPUs = true
		}
		if entry.ConfiguredMems == "" {
			effectiveMems, err := machine.Parse(entry.Mems)
			if err != nil || effectiveMems.IsEmpty() {
				return nil, fmt.Errorf("%w: root %q has no effective mems evidence",
					ErrProjectedExternalInheritanceUnknown, rel)
			}
			boundary.Mems = entry.Mems
			boundary.hasMems = true
		}
		externalByRel[rel] = boundary
	}
	return &projectedHierarchy{
		snapshot:      snapshot,
		capabilities:  capabilities,
		parentByRel:   parentByRel,
		childrenByRel: childrenByRel,
		externalByRel: externalByRel,
	}, nil
}

// applyOperation projects one standalone operation and settles its evidence.
// Frontier projection uses applyConfiguredOperation for every independent
// operation and settles once after the complete frontier.
func (h *projectedHierarchy) applyOperation(operation PlanOperation) error {
	if err := h.applyConfiguredOperation(operation); err != nil {
		return err
	}
	return h.settleEvidence()
}

func (h *projectedHierarchy) applyConfiguredOperation(operation PlanOperation) error {
	entry, ok := h.snapshot.Entries[operation.Rel]
	if !ok {
		return fmt.Errorf("projected hierarchy has no entry for rel %q", operation.Rel)
	}
	stale := func(resource, current, expected string, err error) error {
		return &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: resource,
			Current: current, Target: expected, Err: err,
		}
	}
	if entry.Identity != operation.ExpectedIdentity {
		return stale(
			"identity", fmt.Sprint(entry.Identity), fmt.Sprint(operation.ExpectedIdentity),
			fmt.Errorf("%w: projected operation identity changed", ErrCgroupIdentityChanged),
		)
	}
	currentCPUs := observedCPUsForTargetProof(entry, operation.Target.CPUs, h.capabilities)
	if !currentCPUs.Equals(operation.ExpectedCurrent.CPUs) {
		return stale(
			"cpuset.cpus", currentCPUs.String(), operation.ExpectedCurrent.CPUs.String(),
			fmt.Errorf("projected predecessor differs from expected current"),
		)
	}
	if entry.Mems != operation.ExpectedCurrent.Mems {
		return stale(
			"cpuset.mems", entry.Mems, operation.ExpectedCurrent.Mems,
			fmt.Errorf("projected predecessor differs from expected current"),
		)
	}
	if operation.ParentRel != "" {
		parent, exists := h.snapshot.Entries[operation.ParentRel]
		if !exists || parent.Identity != operation.ExpectedParentIdentity {
			current := "<missing>"
			if exists {
				current = fmt.Sprint(parent.Identity)
			}
			return stale(
				"parent_identity", current, fmt.Sprint(operation.ExpectedParentIdentity),
				fmt.Errorf("%w: projected parent identity changed", ErrCgroupIdentityChanged),
			)
		}
	}
	if operation.Direction == WriteShrink {
		currentChildren := ChildrenFingerprint(h.snapshot.Children[operation.Rel])
		if currentChildren != operation.ExpectedChildren {
			return stale(
				"children", currentChildren, operation.ExpectedChildren,
				fmt.Errorf("projected shrink children changed"),
			)
		}
	}
	if err := h.precheckOperationChildren(operation); err != nil {
		return err
	}
	if err := h.precheckExternalInheritance(operation, entry); err != nil {
		return err
	}

	target := operation.Target.CPUs
	writeCPUs := !operation.ExpectedCurrent.CPUs.Equals(target)
	if writeCPUs && target.IsEmpty() {
		// An empty configured cpuset means "inherit the parent" only where the
		// backend supports it; v1 has no such representation and must be refused.
		if !h.capabilities.EmptyConfiguredCPUSet {
			return fmt.Errorf("%w", ErrEmptyCPUSetUnsupported)
		}
		entry.ConfiguredCPUs = machine.NewCPUSet()
	} else if writeCPUs {
		if parentRel := h.parentByRel[operation.Rel]; parentRel != "" {
			parentEffective := h.snapshot.Entries[parentRel].CPUs
			if !target.IsSubsetOf(parentEffective) {
				return fmt.Errorf("%w: rel %q target %s exceeds parent %q effective %s",
					ErrProjectedParentContainment, operation.Rel,
					target.String(), parentRel, parentEffective.String())
			}
		}
		entry.ConfiguredCPUs = target.Clone()
	}
	if operation.Direction == WriteGrow && operation.WriteMems {
		if parentRel := h.parentByRel[operation.Rel]; parentRel != "" {
			parentMemsValue := h.snapshot.Entries[parentRel].Mems
			parentMems, parentErr := machine.Parse(parentMemsValue)
			targetMems, targetErr := machine.Parse(operation.Target.Mems)
			if parentErr != nil || targetErr != nil || !targetMems.IsSubsetOf(parentMems) {
				return stale(
					"parent_cpuset.mems", parentMemsValue, operation.Target.Mems,
					fmt.Errorf("projected mems grow target outside parent %q: parent_parse=%v target_parse=%v",
						parentRel, parentErr, targetErr),
				)
			}
		}
	}
	if operation.WriteMems {
		entry.ConfiguredMems = operation.Target.Mems
	}
	h.snapshot.Entries[operation.Rel] = entry

	if err := h.recomputeEffectiveSubtree(operation.Rel); err != nil {
		return err
	}
	return nil
}

func (h *projectedHierarchy) settleEvidence() error {
	return h.recomputeEvidence()
}

func validateProjectedFrontierIndependence(
	hierarchy *projectedHierarchy,
	operations []PlanOperation,
) error {
	if hierarchy == nil {
		return fmt.Errorf("projected frontier requires a hierarchy")
	}
	rels := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		rels[operation.Rel] = struct{}{}
	}
	for _, operation := range operations {
		for parent := hierarchy.parentByRel[operation.Rel]; parent != ""; parent = hierarchy.parentByRel[parent] {
			if _, dependent := rels[parent]; dependent {
				return fmt.Errorf("%w: ancestor=%q descendant=%q",
					ErrProjectedFrontierDependency, parent, operation.Rel)
			}
		}
	}
	return nil
}

func (h *projectedHierarchy) precheckExternalInheritance(operation PlanOperation, entry EntryState) error {
	if !h.capabilities.EmptyConfiguredCPUSet || h.parentByRel[operation.Rel] != "" {
		return nil
	}
	boundary, ok := h.externalByRel[operation.Rel]
	if !ok {
		return fmt.Errorf("%w: rel %q has no captured boundary", ErrProjectedExternalInheritanceUnknown, operation.Rel)
	}
	targetCPUs := entry.ConfiguredCPUs
	if !operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs) {
		targetCPUs = operation.Target.CPUs
	}
	if targetCPUs.IsEmpty() && !boundary.hasCPUs {
		return fmt.Errorf("%w: root %q has no CPU boundary", ErrProjectedExternalInheritanceUnknown, operation.Rel)
	}
	targetMems := entry.ConfiguredMems
	if operation.WriteMems {
		targetMems = operation.Target.Mems
	}
	if targetMems == "" && !boundary.hasMems {
		return fmt.Errorf("%w: root %q has no mems boundary", ErrProjectedExternalInheritanceUnknown, operation.Rel)
	}
	return nil
}

func (h *projectedHierarchy) precheckOperationChildren(operation PlanOperation) error {
	if operation.Direction != WriteShrink {
		return nil
	}
	checkCPUs := !operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs)
	skipCPUCheck := isConfiguredInheritanceClear(operation, h.capabilities) ||
		h.capabilities.EmptyConfiguredCPUSet
	if checkCPUs && !skipCPUCheck && !operation.ExpectedChildUnion.IsSubsetOf(operation.Target.CPUs) {
		return &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "expected_child_union",
			Current: operation.ExpectedChildUnion.String(), Target: operation.Target.CPUs.String(),
			Err: fmt.Errorf("expected child union is outside shrink target"),
		}
	}

	childCPUs := machine.NewCPUSet()
	childMems := machine.NewCPUSet()
	expectedChildIdentities := make(map[CgroupIdentity]struct{}, len(h.snapshot.Children[operation.Rel]))
	for _, child := range h.snapshot.Children[operation.Rel] {
		childRel := child.Name
		if operation.Rel != "" {
			childRel = operation.Rel + "/" + child.Name
		}
		if evidence, unavailable := h.snapshot.UnavailableChildren[childRel]; unavailable {
			if evidence.Reason != UnavailableChildReasonControllerUnavailable {
				return &PlanStaleError{
					Rel: operation.Rel, Direction: operation.Direction, Resource: "child_unavailable_reason",
					Current: string(evidence.Reason),
					Target:  string(UnavailableChildReasonControllerUnavailable),
					Err:     fmt.Errorf("projected unavailable-child skip reason changed"),
				}
			}
			if evidence.Identity != child.Identity {
				return &PlanStaleError{
					Rel: operation.Rel, Direction: operation.Direction, Resource: "child_identity",
					Current: fmt.Sprint(evidence.Identity), Target: fmt.Sprint(child.Identity),
					Err: fmt.Errorf("projected unavailable-child identity changed"),
				}
			}
			continue
		}
		expectedChildIdentities[child.Identity] = struct{}{}
	}
	for _, childRel := range h.childrenByRel[operation.Rel] {
		entry, ok := h.snapshot.Entries[childRel]
		_, identityMatches := expectedChildIdentities[entry.Identity]
		if !ok || !identityMatches {
			current := "<missing>"
			if ok {
				current = fmt.Sprint(entry.Identity)
			}
			return &PlanStaleError{
				Rel: operation.Rel, Direction: operation.Direction, Resource: "child_identity",
				Current: current, Target: operation.ExpectedChildren,
				Err: fmt.Errorf("projected child identity changed"),
			}
		}
		delete(expectedChildIdentities, entry.Identity)
		childCPUs = childCPUs.Union(entry.CPUs)
		if operation.WriteMems {
			mems, err := machine.Parse(entry.Mems)
			if err != nil {
				return fmt.Errorf("parse projected child %q cpuset.mems=%q: %w", childRel, entry.Mems, err)
			}
			childMems = childMems.Union(mems)
		}
	}
	if checkCPUs && operation.ExpectedChildUnion.Initialed &&
		!childCPUs.Equals(operation.ExpectedChildUnion) {
		return &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "expected_child_union",
			Current: childCPUs.String(), Target: operation.ExpectedChildUnion.String(),
			Err: fmt.Errorf("projected child union differs from frozen evidence"),
		}
	}
	if len(expectedChildIdentities) != 0 {
		return &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "child_identity",
			Current: "missing", Target: operation.ExpectedChildren,
			Err: fmt.Errorf("projected child entry is missing"),
		}
	}
	if checkCPUs && h.capabilities.EmptyConfiguredCPUSet {
		for _, childRel := range h.childrenByRel[operation.Rel] {
			if err := h.validateProjectedChildCPUContainment(
				operation, childRel, operation.Target.CPUs,
			); err != nil {
				return err
			}
		}
	}
	if checkCPUs && !skipCPUCheck && !childCPUs.IsSubsetOf(operation.Target.CPUs) {
		return &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "child_union",
			Current: childCPUs.String(), Target: operation.Target.CPUs.String(),
			Err: fmt.Errorf("projected child union is outside shrink target"),
		}
	}
	if operation.WriteMems {
		targetMems, err := machine.Parse(operation.Target.Mems)
		if err != nil || !childMems.IsSubsetOf(targetMems) {
			return &PlanStaleError{
				Rel: operation.Rel, Direction: operation.Direction, Resource: "child_union_cpuset.mems",
				Current: childMems.String(), Target: operation.Target.Mems,
				Err: fmt.Errorf("projected child mems union is outside shrink target: target_parse=%v", err),
			}
		}
	}
	return nil
}

func (h *projectedHierarchy) validateProjectedChildCPUContainment(
	operation PlanOperation,
	rel string,
	parentTarget machine.CPUSet,
) error {
	entry, ok := h.snapshot.Entries[rel]
	if !ok {
		return &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "child_identity",
			Current: "missing", Target: rel,
			Err: fmt.Errorf("projected child entry is missing"),
		}
	}
	effectiveTarget := parentTarget
	if !entry.ConfiguredCPUs.IsEmpty() {
		if !entry.ConfiguredCPUs.IsSubsetOf(parentTarget) {
			return &PlanStaleError{
				Rel: operation.Rel, Direction: operation.Direction, Resource: "child_configured_cpuset",
				Current: entry.ConfiguredCPUs.String(), Target: parentTarget.String(),
				Err: fmt.Errorf("projected configured child %q is outside parent shrink target", rel),
			}
		}
		effectiveTarget = entry.ConfiguredCPUs
	}
	for _, childRel := range h.childrenByRel[rel] {
		if err := h.validateProjectedChildCPUContainment(operation, childRel, effectiveTarget); err != nil {
			return err
		}
	}
	return nil
}

// recomputeEffectiveSubtree re-derives effective CPU and memory state for rel
// and every descendant, top-down, so a parent grow or shrink re-projects every
// inheriting child before the child itself is evaluated.
func (h *projectedHierarchy) recomputeEffectiveSubtree(rel string) error {
	if _, ok := h.snapshot.Entries[rel]; !ok {
		return fmt.Errorf("projected hierarchy has no entry for rel %q", rel)
	}
	if err := h.projectEntry(rel); err != nil {
		return err
	}
	for _, child := range h.childrenByRel[rel] {
		if err := h.recomputeEffectiveSubtree(child); err != nil {
			return err
		}
	}
	return nil
}

// projectEntry derives one entry's effective CPUs and mems from its configured
// state and its parent's current effective state, applying the backend's
// inheritance and kernel-containment semantics.
func (h *projectedHierarchy) projectEntry(rel string) error {
	entry := h.snapshot.Entries[rel]
	parent, hasParent := h.parentEntry(rel)

	switch {
	case h.capabilities.EmptyConfiguredCPUSet && entry.ConfiguredCPUs.IsEmpty():
		// Empty configured means inherit the parent's effective set.
		if hasParent {
			entry.CPUs = parent.CPUs.Clone()
		} else {
			boundary, ok := h.externalByRel[rel]
			if !ok || !boundary.hasCPUs {
				return fmt.Errorf("%w: root %q has no CPU boundary",
					ErrProjectedExternalInheritanceUnknown, rel)
			}
			entry.CPUs = boundary.CPUs.Clone()
		}
	case hasParent:
		// A configured child is clamped into its parent by kernel containment.
		entry.CPUs = entry.ConfiguredCPUs.Intersection(parent.CPUs)
	default:
		entry.CPUs = entry.ConfiguredCPUs.Clone()
	}

	switch {
	case h.capabilities.EmptyConfiguredCPUSet && entry.ConfiguredMems == "":
		if hasParent {
			entry.Mems = parent.Mems
		} else {
			boundary, ok := h.externalByRel[rel]
			if !ok || !boundary.hasMems {
				return fmt.Errorf("%w: root %q has no mems boundary",
					ErrProjectedExternalInheritanceUnknown, rel)
			}
			entry.Mems = boundary.Mems
		}
	default:
		entry.Mems = entry.ConfiguredMems
	}

	h.snapshot.Entries[rel] = entry
	return nil
}

// parentEntry returns the identity-resolved parent entry and whether one exists.
func (h *projectedHierarchy) parentEntry(rel string) (EntryState, bool) {
	parentRel := h.parentByRel[rel]
	if parentRel == "" {
		return EntryState{}, false
	}
	parent, ok := h.snapshot.Entries[parentRel]
	return parent, ok
}

// recomputeEvidence rebuilds the domain unions from the projected effective sets
// and refreshes the snapshot fingerprint so downstream proofs bind to the exact
// projected end state rather than the pre-operation snapshot.
func (h *projectedHierarchy) recomputeEvidence() error {
	h.evidenceRebuilds++
	unions := make(map[DomainID]machine.CPUSet, len(h.snapshot.DomainUnion))
	for rel, entry := range h.snapshot.Entries {
		domain, ok := h.snapshot.DomainByRel[rel]
		if !ok {
			continue
		}
		existing, ok := unions[domain]
		if !ok {
			existing = machine.NewCPUSet()
		}
		unions[domain] = existing.Union(entry.CPUs)
	}
	h.snapshot.DomainUnion = unions
	h.snapshot.ID = fingerprintSnapshot(h.snapshot)
	return nil
}
