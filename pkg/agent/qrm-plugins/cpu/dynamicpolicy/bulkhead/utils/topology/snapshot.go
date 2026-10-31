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
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// SnapshotID fingerprints all observed state and the exact scan boundary; it
// lives in topology/model and is re-exported by model_compat.go.

type ScanPurpose string

const (
	ScanForPlan        ScanPurpose = "plan"
	ScanForPrecheck    ScanPurpose = "precheck"
	ScanForWitness     ScanPurpose = "witness"
	ScanForAppliedView ScanPurpose = "applied_view"
)

// ScanBoundary records the minimum sufficient evidence selected for a purpose.
type ScanBoundary struct {
	Purpose      ScanPurpose
	Roots        []string
	ExpandedRels []string
}

// SnapshotRequest selects a purpose-specific hierarchy boundary.
type SnapshotRequest struct {
	Purpose                ScanPurpose
	AffectedRels           []string
	ParentRel              string
	SourceDomain           DomainID
	MismatchRels           []string
	CollectDormantActivity bool
}

// RelObservation records whether a configured rel exists and, when present,
// binds that classification to its stable directory identity.
type RelObservation struct {
	Exists   bool
	Identity CgroupIdentity
}

// ObserveConfiguredRels classifies configured paths through the same
// identity-aware hierarchy driver used by coordinator snapshots.
func ObserveConfiguredRels(
	ctx context.Context,
	cg cgroupclient.CgroupClient,
	rels []string,
) (map[string]RelObservation, error) {
	if cg == nil {
		return nil, fmt.Errorf("cgroup client is required")
	}
	driver, err := snapshotDriverForCoordinator(ctx, cg)
	if err != nil {
		return nil, err
	}
	defer driver.Close()

	out := make(map[string]RelObservation, len(rels))
	for _, rel := range normalizeRels(rels) {
		identity, err := driver.StatIdentity(ctx, rel)
		if err == nil {
			out[rel] = RelObservation{Exists: true, Identity: identity}
			continue
		}
		if isCgroupNotFoundError(err) {
			out[rel] = RelObservation{}
			continue
		}
		return nil, fmt.Errorf("stat configured rel %q: %w", rel, err)
	}
	return out, nil
}

type UnavailableChildReason string

const (
	UnavailableChildReasonControllerUnavailable UnavailableChildReason = "controller-unavailable"
)

type UnavailableChildEvidence struct {
	Identity CgroupIdentity
	Reason   UnavailableChildReason
}

// CompleteSnapshot is the only hierarchy evidence accepted by the coordinator.
// Entries always contains the physical state observed from cgroupfs.
// OwnershipByRel is an optional semantic overlay for cgroup v1 nodes whose
// physical non-empty cpuset is only a containment envelope. Consumers that
// reason about CPU ownership must use TargetProofCPUs instead of reading
// Entries directly.
type CompleteSnapshot struct {
	ID                  SnapshotID
	CapturedAt          time.Time
	Capabilities        HierarchyCapabilities
	Entries             map[string]EntryState
	Children            map[string][]ChildRef
	UnavailableChildren map[string]UnavailableChildEvidence
	DomainByRel         map[string]DomainID
	DomainUnion         map[DomainID]machine.CPUSet
	OwnershipByRel      map[string]machine.CPUSet
	ScanBoundary        ScanBoundary
	Cost                BudgetUsage
}

// SnapshotError preserves the failed operation and driver classification.
type SnapshotError struct {
	Operation HierarchyOperation
	Rel       string
	Class     HierarchyErrorClass
	Identity  CgroupIdentity
	// EvidenceID fingerprints the successfully observed portion of the failed
	// scan, including cgroup identities and configured/effective state.
	EvidenceID SnapshotID
	Err        error
}

func (e *SnapshotError) Error() string {
	return fmt.Sprintf("complete snapshot %s rel=%q class=%s: %v", e.Operation, e.Rel, e.Class, e.Err)
}

func (e *SnapshotError) Unwrap() error { return e.Err }

type snapshotBuilder struct {
	ctx           context.Context
	driver        HierarchyDriver
	budget        *BudgetTracker
	snapshot      *CompleteSnapshot
	controlled    map[string]*TopoNode
	selected      map[string]struct{}
	boundaries    map[string]struct{}
	retirable     map[string]CgroupIdentity
	activityRoots map[string]struct{}
}

// BuildCompleteSnapshot returns either complete purpose-scoped evidence or a
// nil snapshot and a typed error. It never publishes partially collected state.
func BuildCompleteSnapshot(
	ctx context.Context,
	driver HierarchyDriver,
	dag *TopoDAG,
	request SnapshotRequest,
	budget *BudgetTracker,
) (*CompleteSnapshot, error) {
	return buildCompleteSnapshot(ctx, driver, dag, request, budget, nil, nil)
}

func buildCompleteSnapshot(
	ctx context.Context,
	driver HierarchyDriver,
	dag *TopoDAG,
	request SnapshotRequest,
	budget *BudgetTracker,
	boundaries map[string]struct{},
	retirable map[string]CgroupIdentity,
) (*CompleteSnapshot, error) {
	if driver == nil || dag == nil || budget == nil {
		return nil, &SnapshotError{Operation: HierarchyOperationRead, Class: HierarchyErrorInvalid, Err: fmt.Errorf("driver, dag and budget are required")}
	}
	if !driver.Capabilities().StableIdentity {
		return nil, &SnapshotError{
			Operation: HierarchyOperationStat,
			Class:     HierarchyErrorInvalid,
			Err:       fmt.Errorf("stable hierarchy identity is required"),
		}
	}
	if wrapped, ok := driver.(*budgetedHierarchyDriver); !ok || wrapped.budget != budget {
		driver = NewBudgetedHierarchyDriver(driver, budget)
	}
	boundary, _, expand, err := selectSnapshotBoundary(dag, request)
	if err != nil {
		return nil, &SnapshotError{Operation: HierarchyOperationRead, Class: HierarchyErrorInvalid, Err: err}
	}
	return buildCompleteSnapshotWithBoundary(
		ctx, driver, dag, boundary, expand, budget, boundaries, retirable,
		request.CollectDormantActivity,
	)
}

func buildCompleteSnapshotWithBoundary(
	ctx context.Context,
	driver HierarchyDriver,
	dag *TopoDAG,
	boundary ScanBoundary,
	expand map[string]bool,
	budget *BudgetTracker,
	boundaries map[string]struct{},
	retirable map[string]CgroupIdentity,
	collectDormantActivity bool,
) (*CompleteSnapshot, error) {
	builder := &snapshotBuilder{
		ctx:           ctx,
		driver:        driver,
		budget:        budget,
		controlled:    make(map[string]*TopoNode, len(dag.index)),
		selected:      make(map[string]struct{}, len(boundary.Roots)),
		boundaries:    boundaries,
		retirable:     retirable,
		activityRoots: make(map[string]struct{}),
		snapshot: &CompleteSnapshot{
			CapturedAt:          time.Now(),
			Capabilities:        driver.Capabilities(),
			Entries:             make(map[string]EntryState),
			Children:            make(map[string][]ChildRef),
			UnavailableChildren: make(map[string]UnavailableChildEvidence),
			DomainByRel:         make(map[string]DomainID),
			DomainUnion:         make(map[DomainID]machine.CPUSet),
			ScanBoundary: ScanBoundary{
				Purpose: boundary.Purpose,
				Roots:   append([]string(nil), boundary.Roots...),
			},
		},
	}
	for rel, node := range dag.index {
		builder.controlled[rel] = node
		if collectDormantActivity &&
			!builder.snapshot.Capabilities.EmptyConfiguredCPUSet &&
			node.Role == TopoNodeRoleReclaimNUMABucket {
			builder.activityRoots[rel] = struct{}{}
		}
	}
	for _, rel := range boundary.Roots {
		builder.selected[rel] = struct{}{}
	}
	for _, rel := range boundary.Roots {
		node := dag.index[rel]
		domain := node.Domain
		if _, err := builder.scan(rel, domain, 1, CgroupIdentity{}, expand[rel], boundary.Purpose == ScanForPrecheck); err != nil {
			return nil, err
		}
	}
	builder.rebuildDomainUnion()
	builder.snapshot.Cost = budget.Usage()
	if err := validateCompleteSnapshotEvidence(builder.snapshot); err != nil {
		return nil, &SnapshotError{
			Operation:  HierarchyOperationRead,
			Class:      HierarchyErrorInvalid,
			EvidenceID: fingerprintSnapshot(builder.snapshot),
			Err:        fmt.Errorf("validate snapshot closure: %w", err),
		}
	}
	builder.snapshot.ID = fingerprintSnapshot(builder.snapshot)
	return builder.snapshot, nil
}

// TargetProofCPUs returns the state that proves a target under the snapshot's
// hierarchy semantics. Semantic ownership overlays take precedence over
// physical cgroup state. Empty cgroup v2 targets are otherwise proved by
// configured state; every other target is proved by effective state. Callers
// must not bypass this method and read Entries for ownership decisions: a
// dormant v1 envelope is intentionally present in Entries while absent from
// semantic ownership.
func (s *CompleteSnapshot) TargetProofCPUs(rel string, target machine.CPUSet) (machine.CPUSet, bool) {
	if s == nil {
		return machine.NewCPUSet(), false
	}
	if cpus, ok := s.OwnershipByRel[rel]; ok {
		return cpus.Clone(), true
	}
	entry, ok := s.Entries[rel]
	if !ok {
		return machine.NewCPUSet(), false
	}
	return observedCPUsForTargetProof(entry, target, s.Capabilities).Clone(), true
}

// semanticOwnershipUnionByDomain recomputes the per-domain current ownership
// union from semantic ownership evidence. DomainUnion is the closed physical
// evidence union (it may contain dormant v1 containment envelopes), so ownership
// decisions such as which CPUs are "leaving" a domain must not read it directly.
// When no dormant overlay exists OwnershipByRel is empty and this reduces to the
// physical union of Entries, preserving prior behavior.
func semanticOwnershipUnionByDomain(snapshot *CompleteSnapshot, domainByRel map[string]DomainID) map[DomainID]machine.CPUSet {
	out := make(map[DomainID]machine.CPUSet)
	if snapshot == nil {
		return out
	}
	// Without a dormant overlay, physical evidence and semantic ownership agree,
	// and DomainUnion is already the correct per-domain union.
	if len(snapshot.OwnershipByRel) == 0 {
		for domain, cpus := range snapshot.DomainUnion {
			out[domain] = cpus.Clone()
		}
		return out
	}
	for rel, entry := range snapshot.Entries {
		owned := entry.CPUs
		if override, ok := snapshot.OwnershipByRel[rel]; ok {
			owned = override
		}
		domain := domainByRel[rel]
		out[domain] = out[domain].Union(owned)
	}
	return out
}

func newCompleteSnapshotSource(
	driver HierarchyDriver,
	dag *TopoDAG,
	budget *BudgetTracker,
	boundarySets ...map[string]struct{},
) func(context.Context) (*CompleteSnapshot, error) {
	return newCompleteSnapshotSourceWithActivity(driver, dag, budget, false, boundarySets...)
}

func newDormantCompleteSnapshotSource(
	driver HierarchyDriver,
	dag *TopoDAG,
	budget *BudgetTracker,
	boundarySets ...map[string]struct{},
) func(context.Context) (*CompleteSnapshot, error) {
	return newCompleteSnapshotSourceWithActivity(driver, dag, budget, true, boundarySets...)
}

func newCompleteSnapshotSourceWithActivity(
	driver HierarchyDriver,
	dag *TopoDAG,
	budget *BudgetTracker,
	collectDormantActivity bool,
	boundarySets ...map[string]struct{},
) func(context.Context) (*CompleteSnapshot, error) {
	affected := make([]string, 0, len(dag.index))
	for rel, node := range dag.index {
		if node.Domain == "" {
			continue
		}
		affected = append(affected, rel)
	}
	sort.Strings(affected)
	var boundaries map[string]struct{}
	if len(boundarySets) > 0 {
		boundaries = boundarySets[0]
	}
	return func(ctx context.Context) (*CompleteSnapshot, error) {
		return buildCompleteSnapshot(ctx, driver, dag, SnapshotRequest{
			Purpose:                ScanForPlan,
			AffectedRels:           affected,
			CollectDormantActivity: collectDormantActivity,
		}, budget, boundaries, nil)
	}
}

// scan owns construction of one identity-fenced subtree in the snapshot
// evidence closure. A successful return leaves every retained child backed by
// exactly one entry or typed-unavailable proof; a scan error rejects the whole
// builder result, so callers never consume its partial mutations. Authorized
// disappearance is retirement only for the frozen identity and typed path
// absence, after which the complete retired subtree is removed.
func (b *snapshotBuilder) scan(
	rel string,
	domain DomainID,
	depth int,
	expected CgroupIdentity,
	expand, immediateOnly bool,
) (bool, error) {
	if _, done := b.snapshot.Entries[rel]; done {
		return false, nil
	}
	before, err := b.driver.StatIdentity(b.ctx, rel)
	if err != nil {
		if b.authorizedRetirement(rel, expected, err) {
			return b.confirmEarlyRetirement(rel, expected, err)
		}
		return false, b.fail(HierarchyOperationStat, rel, expected, err)
	}
	if expected != (CgroupIdentity{}) && before != expected {
		return false, b.fail(HierarchyOperationStat, rel, before, fmt.Errorf("%w: listed=%v stat=%v", ErrCgroupIdentityChanged, expected, before))
	}
	if err := b.budget.VisitNode(rel, before, depth); err != nil {
		return false, b.fail(HierarchyOperationStat, rel, before, err)
	}
	entry, err := b.readEntry(rel, before)
	if err != nil {
		if b.authorizedRetirement(rel, before, err) {
			return b.confirmEarlyRetirement(rel, before, err)
		}
		if b.shouldSkipUnavailableController(rel, depth, err) {
			b.snapshot.UnavailableChildren[rel] = UnavailableChildEvidence{
				Identity: before,
				Reason:   UnavailableChildReasonControllerUnavailable,
			}
			return false, nil
		}
		return false, b.fail(HierarchyOperationRead, rel, before, err)
	}
	after, err := b.driver.StatIdentity(b.ctx, rel)
	if err != nil {
		if b.authorizedRetirement(rel, before, err) {
			return b.confirmEarlyRetirement(rel, before, err)
		}
		return false, b.fail(HierarchyOperationStat, rel, before, err)
	}
	if before != after || entry.Identity != before {
		return false, b.fail(HierarchyOperationStat, rel, after, fmt.Errorf("%w: before=%v read=%v after=%v", ErrCgroupIdentityChanged, before, entry.Identity, after))
	}
	entry.Rel = rel
	entry.CPUs = entry.CPUs.Clone()
	entry.ConfiguredCPUs = entry.ConfiguredCPUs.Clone()
	b.snapshot.Entries[rel] = entry
	b.snapshot.DomainByRel[rel] = domain

	if !expand && !immediateOnly && !b.hasSelectedControlledChild(rel) {
		return false, nil
	}
	children, err := b.driver.ListChildren(b.ctx, rel)
	if err != nil {
		if isCgroupPathAbsent(err) {
			retired, confirmErr := b.confirmRetirement(rel, entry.Identity)
			if confirmErr != nil {
				return false, confirmErr
			}
			if retired {
				return true, nil
			}
		}
		return false, b.fail(HierarchyOperationList, rel, entry.Identity, err)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
	retainedChildren := make([]ChildRef, 0, len(children))
	scannedChildren := make([]ChildRef, 0, len(children))
	childRetired := false
	b.snapshot.ScanBoundary.ExpandedRels = append(b.snapshot.ScanBoundary.ExpandedRels, rel)
	for _, child := range children {
		childRel := filepath.Join(rel, child.Name)
		if withinTraversalBoundary(childRel, b.boundaries) {
			continue
		}
		childNode, isControlled := b.controlled[childRel]
		if isControlled && !immediateOnly {
			if _, selected := b.selected[childRel]; selected {
				retainedChildren = append(retainedChildren, child)
			}
			continue
		}
		if !expand && !immediateOnly {
			continue
		}
		childDomain := domain
		if isControlled {
			childDomain = childNode.Domain
		}
		childExpand := expand && !immediateOnly
		retiredChild, scanErr := b.scan(childRel, childDomain, depth+1, child.Identity, childExpand, false)
		if scanErr != nil {
			if !isCgroupPathAbsent(scanErr) {
				return false, scanErr
			}
			retired, confirmErr := b.confirmRetirement(rel, entry.Identity)
			if confirmErr != nil {
				return false, confirmErr
			}
			if retired {
				return true, nil
			}
			return false, scanErr
		}
		if !retiredChild {
			retainedChildren = append(retainedChildren, child)
			scannedChildren = append(scannedChildren, child)
		} else {
			childRetired = true
		}
	}
	if childRetired {
		currentChildren, listErr := b.driver.ListChildren(b.ctx, rel)
		if listErr != nil {
			if isCgroupPathAbsent(listErr) {
				retired, confirmErr := b.confirmRetirement(rel, entry.Identity)
				if confirmErr != nil {
					return false, confirmErr
				}
				if retired {
					return true, nil
				}
			}
			return false, b.fail(HierarchyOperationList, rel, entry.Identity, listErr)
		}
		currentChildren = b.filterRecursivelyScannedChildren(
			rel, currentChildren, expand, immediateOnly)
		sort.Slice(currentChildren, func(i, j int) bool {
			return currentChildren[i].Name < currentChildren[j].Name
		})
		if !equalChildRefs(scannedChildren, currentChildren) {
			return false, b.fail(HierarchyOperationList, rel, entry.Identity, fmt.Errorf(
				"%w: children changed after retirement: expected=%v current=%v",
				ErrCgroupIdentityChanged, scannedChildren, currentChildren))
		}
	}
	b.snapshot.Children[rel] = retainedChildren
	sort.Strings(b.snapshot.ScanBoundary.ExpandedRels)
	retired, err := b.confirmRetirement(rel, entry.Identity)
	if err != nil {
		return false, err
	}
	return retired, nil
}

func (b *snapshotBuilder) filterRecursivelyScannedChildren(
	parentRel string,
	children []ChildRef,
	expand bool,
	immediateOnly bool,
) []ChildRef {
	inScope := make([]ChildRef, 0, len(children))
	for _, child := range children {
		childRel := filepath.Join(parentRel, child.Name)
		if withinTraversalBoundary(childRel, b.boundaries) {
			continue
		}
		if _, isControlled := b.controlled[childRel]; isControlled && !immediateOnly {
			continue
		}
		if !expand && !immediateOnly {
			continue
		}
		inScope = append(inScope, child)
	}
	return inScope
}

func (b *snapshotBuilder) hasSelectedControlledChild(rel string) bool {
	for selectedRel := range b.selected {
		node := b.controlled[selectedRel]
		if node != nil && node.parent != nil && node.parent.Rel == rel {
			return true
		}
	}
	return false
}

// authorizedRetirement recognizes the only absence that may close a frozen
// retirement window: typed cgroup-path absence for the exact authorized
// identity. Zero identities, replacements, controller unavailability, and
// untyped errors remain failures.
func (b *snapshotBuilder) authorizedRetirement(rel string, identity CgroupIdentity, err error) bool {
	return identity != (CgroupIdentity{}) &&
		b.retirable[rel] == identity &&
		isCgroupPathAbsent(err)
}

func (b *snapshotBuilder) confirmEarlyRetirement(
	rel string,
	identity CgroupIdentity,
	absenceErr error,
) (bool, error) {
	current, err := b.driver.StatIdentity(b.ctx, rel)
	if err != nil {
		if b.authorizedRetirement(rel, identity, err) {
			b.retireSubtree(rel)
			return true, nil
		}
		return false, b.fail(HierarchyOperationStat, rel, identity, err)
	}
	return false, b.fail(HierarchyOperationStat, rel, current, fmt.Errorf(
		"%w: rel=%q appeared after absence (%v): expected=%v current=%v",
		ErrCgroupIdentityChanged, rel, absenceErr, identity, current))
}

func (b *snapshotBuilder) confirmRetirement(rel string, identity CgroupIdentity) (bool, error) {
	current, err := b.driver.StatIdentity(b.ctx, rel)
	if err != nil {
		if b.authorizedRetirement(rel, identity, err) {
			b.retireSubtree(rel)
			return true, nil
		}
		return false, b.fail(HierarchyOperationStat, rel, identity, err)
	}
	if current != identity {
		return false, b.fail(HierarchyOperationStat, rel, current, fmt.Errorf(
			"%w: read=%v recursive-fence=%v", ErrCgroupIdentityChanged, identity, current))
	}
	return false, nil
}

// retireSubtree removes a retired rel and its complete descendant evidence,
// including domain ownership, unavailable proofs, parent edges, and expanded
// markers, so no dangling evidence escapes in an otherwise valid snapshot.
func (b *snapshotBuilder) retireSubtree(rel string) {
	prefix := rel + "/"
	underRetiredRoot := func(candidate string) bool {
		return candidate == rel || strings.HasPrefix(candidate, prefix)
	}
	for candidate := range b.snapshot.Entries {
		if underRetiredRoot(candidate) {
			delete(b.snapshot.Entries, candidate)
			delete(b.snapshot.DomainByRel, candidate)
			delete(b.snapshot.UnavailableChildren, candidate)
			delete(b.snapshot.OwnershipByRel, candidate)
		}
	}
	for candidate := range b.snapshot.UnavailableChildren {
		if underRetiredRoot(candidate) {
			delete(b.snapshot.UnavailableChildren, candidate)
		}
	}
	for parent, children := range b.snapshot.Children {
		if underRetiredRoot(parent) {
			delete(b.snapshot.Children, parent)
			continue
		}
		retained := children[:0]
		for _, child := range children {
			if !underRetiredRoot(filepath.Join(parent, child.Name)) {
				retained = append(retained, child)
			}
		}
		b.snapshot.Children[parent] = retained
	}
	expanded := b.snapshot.ScanBoundary.ExpandedRels[:0]
	for _, candidate := range b.snapshot.ScanBoundary.ExpandedRels {
		if !underRetiredRoot(candidate) {
			expanded = append(expanded, candidate)
		}
	}
	b.snapshot.ScanBoundary.ExpandedRels = expanded
}

func (b *snapshotBuilder) rebuildDomainUnion() {
	b.snapshot.DomainUnion = make(map[DomainID]machine.CPUSet)
	for rel, entry := range b.snapshot.Entries {
		domain := b.snapshot.DomainByRel[rel]
		b.snapshot.DomainUnion[domain] = b.snapshot.DomainUnion[domain].Union(entry.CPUs)
	}
}

func isCgroupPathAbsent(err error) bool {
	if err == nil || errors.Is(err, ErrCgroupControllerUnavailable) {
		return false
	}
	return errors.Is(err, syscall.ENOENT)
}

func (b *snapshotBuilder) readEntry(rel string, expected CgroupIdentity) (EntryState, error) {
	if _, collectActivity := b.activityRoots[rel]; b.snapshot.Capabilities.EmptyConfiguredCPUSet || !collectActivity {
		return b.driver.ReadEntry(b.ctx, rel)
	}
	reader, ok := b.driver.(hierarchyActivityReader)
	if !ok {
		return EntryState{}, fmt.Errorf("cgroup v1 activity proof is unavailable for rel %q", rel)
	}
	return reader.ReadEntryWithActivity(b.ctx, rel, expected)
}

func (b *snapshotBuilder) shouldSkipUnavailableController(rel string, depth int, err error) bool {
	if !errors.Is(err, ErrCgroupControllerUnavailable) {
		return false
	}
	if !b.snapshot.Capabilities.EffectiveCPUSet {
		return false
	}
	if depth <= 1 {
		return false
	}
	if _, controlled := b.controlled[rel]; controlled {
		return false
	}
	return true
}

func (b *snapshotBuilder) fail(op HierarchyOperation, rel string, identity CgroupIdentity, err error) error {
	return &SnapshotError{
		Operation:  op,
		Rel:        rel,
		Class:      b.driver.Classify(err, op),
		Identity:   identity,
		EvidenceID: fingerprintSnapshot(b.snapshot),
		Err:        err,
	}
}

func selectSnapshotBoundary(dag *TopoDAG, request SnapshotRequest) (ScanBoundary, map[string]struct{}, map[string]bool, error) {
	selected := make(map[string]struct{})
	expand := make(map[string]bool)
	add := func(rel string, shouldExpand bool) error {
		node := dag.index[rel]
		if node == nil {
			return fmt.Errorf("snapshot boundary rel %q is not controlled", rel)
		}
		if node.Domain == "" {
			return fmt.Errorf("snapshot boundary rel %q has no explicit domain", rel)
		}
		selected[rel] = struct{}{}
		expand[rel] = expand[rel] || shouldExpand
		return nil
	}

	switch request.Purpose {
	case ScanForPlan:
		if len(request.AffectedRels) == 0 {
			return ScanBoundary{}, nil, nil, fmt.Errorf("plan snapshot requires affected rels")
		}
		for _, rel := range request.AffectedRels {
			if err := add(rel, true); err != nil {
				return ScanBoundary{}, nil, nil, err
			}
			for node := dag.index[rel].parent; node != nil; node = node.parent {
				if err := add(node.Rel, false); err != nil {
					return ScanBoundary{}, nil, nil, err
				}
			}
		}
	case ScanForPrecheck:
		if err := add(request.ParentRel, true); err != nil {
			return ScanBoundary{}, nil, nil, err
		}
	case ScanForWitness:
		if request.SourceDomain == "" {
			return ScanBoundary{}, nil, nil, fmt.Errorf("witness snapshot requires source domain")
		}
		for _, node := range dag.Nodes() {
			if node.Domain == request.SourceDomain && node.TrustAnchor {
				if err := add(node.Rel, false); err != nil {
					return ScanBoundary{}, nil, nil, err
				}
			}
		}
		if len(selected) == 0 {
			return ScanBoundary{}, nil, nil, fmt.Errorf("source domain %q has no trust anchors", request.SourceDomain)
		}
	case ScanForAppliedView:
		for _, node := range dag.Nodes() {
			if node.ControlledRoot || node.TrustAnchor || len(node.children) == 0 {
				if err := add(node.Rel, false); err != nil {
					return ScanBoundary{}, nil, nil, err
				}
			}
		}
		for _, rel := range request.MismatchRels {
			if err := add(rel, true); err != nil {
				return ScanBoundary{}, nil, nil, err
			}
		}
	default:
		return ScanBoundary{}, nil, nil, fmt.Errorf("unsupported scan purpose %q", request.Purpose)
	}

	roots := make([]string, 0, len(selected))
	for rel := range selected {
		roots = append(roots, rel)
	}
	sort.Strings(roots)
	return ScanBoundary{Purpose: request.Purpose, Roots: roots}, selected, expand, nil
}

func fingerprintSnapshot(snapshot *CompleteSnapshot) SnapshotID {
	hash := sha256.New()
	writeHashString(hash, "bulkhead-complete-snapshot-v2")
	writeHashString(hash, "purpose")
	writeHashString(hash, string(snapshot.ScanBoundary.Purpose))
	writeHashString(hash, "capabilities")
	writeHashUint64(hash, hierarchyCapabilitiesBits(snapshot.Capabilities))
	roots := append([]string(nil), snapshot.ScanBoundary.Roots...)
	sort.Strings(roots)
	writeHashString(hash, "roots")
	writeHashUint64(hash, uint64(len(roots)))
	for _, rel := range roots {
		writeHashString(hash, rel)
	}
	domains := make([]string, 0, len(snapshot.DomainUnion))
	for domain := range snapshot.DomainUnion {
		domains = append(domains, string(domain))
	}
	sort.Strings(domains)
	writeHashString(hash, "domain-union")
	writeHashUint64(hash, uint64(len(domains)))
	for _, domain := range domains {
		writeHashString(hash, domain)
		writeHashString(hash, snapshot.DomainUnion[DomainID(domain)].String())
	}
	rels := make([]string, 0, len(snapshot.Entries))
	for rel := range snapshot.Entries {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	writeHashString(hash, "entries")
	writeHashUint64(hash, uint64(len(rels)))
	for _, rel := range rels {
		entry := snapshot.Entries[rel]
		writeHashString(hash, rel)
		writeHashUint64(hash, entry.Identity.Device)
		writeHashUint64(hash, entry.Identity.Inode)
		writeHashString(hash, entry.CPUs.String())
		writeHashString(hash, entry.Mems)
		writeHashString(hash, entry.ConfiguredCPUs.String())
		writeHashString(hash, entry.ConfiguredMems)
		if entry.Activity.TasksEmpty {
			writeHashUint64(hash, 1)
		} else {
			writeHashUint64(hash, 0)
		}
		if entry.Activity.CgroupProcsEmpty {
			writeHashUint64(hash, 1)
		} else {
			writeHashUint64(hash, 0)
		}
		if entry.Activity.Childless {
			writeHashUint64(hash, 1)
		} else {
			writeHashUint64(hash, 0)
		}
	}
	domainRels := sortedStringKeys(snapshot.DomainByRel)
	writeHashString(hash, "domain-by-rel")
	writeHashUint64(hash, uint64(len(domainRels)))
	for _, rel := range domainRels {
		writeHashString(hash, rel)
		writeHashString(hash, string(snapshot.DomainByRel[rel]))
	}
	childParents := sortedStringKeys(snapshot.Children)
	writeHashString(hash, "children")
	writeHashUint64(hash, uint64(len(childParents)))
	for _, rel := range childParents {
		writeHashString(hash, rel)
		children := append([]ChildRef(nil), snapshot.Children[rel]...)
		sort.Slice(children, func(i, j int) bool {
			if children[i].Name != children[j].Name {
				return children[i].Name < children[j].Name
			}
			if children[i].Identity.Device != children[j].Identity.Device {
				return children[i].Identity.Device < children[j].Identity.Device
			}
			return children[i].Identity.Inode < children[j].Identity.Inode
		})
		writeHashUint64(hash, uint64(len(children)))
		for _, child := range children {
			writeHashString(hash, child.Name)
			writeHashUint64(hash, child.Identity.Device)
			writeHashUint64(hash, child.Identity.Inode)
		}
	}
	unavailableRels := sortedStringKeys(snapshot.UnavailableChildren)
	writeHashString(hash, "unavailable-children")
	writeHashUint64(hash, uint64(len(unavailableRels)))
	for _, rel := range unavailableRels {
		evidence := snapshot.UnavailableChildren[rel]
		writeHashString(hash, rel)
		writeHashUint64(hash, evidence.Identity.Device)
		writeHashUint64(hash, evidence.Identity.Inode)
		writeHashString(hash, string(evidence.Reason))
	}
	var id SnapshotID
	copy(id[:], hash.Sum(nil))
	return id
}

func hierarchyCapabilitiesBits(capabilities HierarchyCapabilities) uint64 {
	var bits uint64
	for index, enabled := range []bool{
		capabilities.StableIdentity,
		capabilities.EmptyConfiguredCPUSet,
		capabilities.EffectiveCPUSet,
		capabilities.KernelParentContainment,
		capabilities.PartitionRoots,
	} {
		if enabled {
			bits |= 1 << index
		}
	}
	return bits
}

// stringBytesNoAlloc converts a string to []byte without copying.
// hash.Write only reads the bytes and does not retain the slice, so this
// is safe for sha256 and similar digest implementations.
func stringBytesNoAlloc(s string) []byte {
	type stringHeader struct {
		Data unsafe.Pointer
		Len  int
	}
	type sliceHeader struct {
		Data unsafe.Pointer
		Len  int
		Cap  int
	}
	sh := (*stringHeader)(unsafe.Pointer(&s))
	bh := sliceHeader{Data: sh.Data, Len: sh.Len, Cap: sh.Len}
	return *(*[]byte)(unsafe.Pointer(&bh))
}

func writeHashString(hash interface{ Write([]byte) (int, error) }, value string) {
	writeHashUint64(hash, uint64(len(value)))
	if len(value) > 0 {
		_, _ = hash.Write(stringBytesNoAlloc(value))
	}
}

// hashUint64ScratchPool reuses the 8-byte little-endian encoding buffer. A local
// [8]byte passed through the opaque io.Writer interface escapes to the heap on
// every call (microbenchmark: 8 B/op, 1 allocs/op; a pre-allocated *[8]byte is 0).
// Pooling a fixed buffer removes that allocation; PutUint64 overwrites all eight
// bytes on every use, so no stale bytes can leak into the digest.
var hashUint64ScratchPool = sync.Pool{
	New: func() any {
		b := new([8]byte)
		return b
	},
}

func writeHashUint64(hash interface{ Write([]byte) (int, error) }, value uint64) {
	buf := hashUint64ScratchPool.Get().(*[8]byte)
	binary.LittleEndian.PutUint64(buf[:], value)
	_, _ = hash.Write(buf[:])
	hashUint64ScratchPool.Put(buf)
}

func cloneUnavailableChildEvidenceMap(
	in map[string]UnavailableChildEvidence,
) map[string]UnavailableChildEvidence {
	out := make(map[string]UnavailableChildEvidence, len(in))
	for rel, evidence := range in {
		out[rel] = evidence
	}
	return out
}

// CloneCompleteSnapshot returns a deep copy that shares no mutable map, slice,
// or CPUSet state with the input. Compilation and projection rely on this
// isolation to model a fixed point without disturbing captured evidence.
func CloneCompleteSnapshot(in *CompleteSnapshot) *CompleteSnapshot {
	if in == nil {
		return nil
	}
	out := *in
	out.Entries = make(map[string]EntryState, len(in.Entries))
	for rel, entry := range in.Entries {
		cloned := entry
		cloned.CPUs = entry.CPUs.Clone()
		cloned.ConfiguredCPUs = entry.ConfiguredCPUs.Clone()
		out.Entries[rel] = cloned
	}
	out.Children = make(map[string][]ChildRef, len(in.Children))
	for rel, refs := range in.Children {
		out.Children[rel] = append([]ChildRef(nil), refs...)
	}
	out.UnavailableChildren = cloneUnavailableChildEvidenceMap(in.UnavailableChildren)
	out.DomainByRel = cloneDomainByRel(in.DomainByRel)
	out.DomainUnion = cloneDomainUnion(in.DomainUnion)
	out.OwnershipByRel = model.CloneCPUSetMap(in.OwnershipByRel)
	out.ScanBoundary = cloneScanBoundary(in.ScanBoundary)
	return &out
}

func cloneDomainByRel(in map[string]DomainID) map[string]DomainID {
	if in == nil {
		return nil
	}
	out := make(map[string]DomainID, len(in))
	for rel, domain := range in {
		out[rel] = domain
	}
	return out
}

func cloneDomainUnion(in map[DomainID]machine.CPUSet) map[DomainID]machine.CPUSet {
	if in == nil {
		return nil
	}
	out := make(map[DomainID]machine.CPUSet, len(in))
	for domain, cpus := range in {
		out[domain] = cpus.Clone()
	}
	return out
}

func cloneScanBoundary(in ScanBoundary) ScanBoundary {
	out := in
	out.Roots = append([]string(nil), in.Roots...)
	out.ExpandedRels = append([]string(nil), in.ExpandedRels...)
	return out
}
