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
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// The deadlock result types (deadlock.ProbeCompleteness, deadlock.DrainAtom, deadlock.DrainAtomClass,
// deadlock.DeadlockAnalysis, deadlock.DeadlockProbeStats, deadlock.StructuralV1NonEmptyDeadlock and
// deadlock.ErrIncompleteRequiredCoreReleaseWitness) live in topology/deadlock; the parent package
// imports that subpackage directly. This file
// keeps the planner-coupled probe entry point and its helpers, which still
// operate on parent-package types (PhasePlanInput, CompleteSnapshot, BudgetTracker).
func analyzeV1Deadlock(in PhasePlanInput) (analysis deadlock.DeadlockAnalysis, err error) {
	start := time.Now()
	defer func() {
		stats := analysis.ProbeStats
		general.Infof("cpuset_topology: deadlock probe finished duration=%s err=%v completeness=%s atoms=%d blockers=%d snapshot_entries=%d snapshot_child_edges=%d protected_rels=%d protected_pending_cpus=%d probe_operations=%d probe_limit=%d auto_budget=%t context_phase=%s atom_index=%d",
			time.Since(start), err, analysis.Completeness, len(analysis.Atoms), len(analysis.EmptyBlockers),
			stats.SnapshotEntries, stats.SnapshotChildEdges, stats.ProtectedRels, stats.ProtectedPendingCPUs,
			stats.ProbeOperations, stats.ProbeLimit, stats.AutoBudget, stats.ContextPhase, stats.AtomIndex)
	}()

	analysis = deadlock.DeadlockAnalysis{
		Completeness:  deadlock.ProbeComplete,
		EmptyBlockers: make(map[string]machine.CPUSet),
		ProbeStats: deadlock.DeadlockProbeStats{
			AtomIndex: -1,
		},
	}
	if in.AllowEmptyTarget || in.Kind != PhaseDrain || in.DAG == nil || in.Snapshot == nil {
		return analysis, nil
	}
	analysis.ProbeStats.SnapshotEntries = len(in.Snapshot.Entries)
	analysis.ProbeStats.SnapshotChildEdges = snapshotChildEdgeCount(in.Snapshot)
	analysis.ProbeStats.ProtectedRels = len(in.ProtectedByRel)
	analysis.ProbeStats.ProtectedPendingCPUs = in.ProtectedPending.Size()
	analysis.ProbeStats.SnapshotID = in.Snapshot.ID
	desiredByDomain := desiredDomainUnions(in.DAG, semanticTargetsForPlan(in))
	domains := sortedDomains(in.Snapshot.DomainUnion, desiredByDomain)
	// Ownership decisions must read semantic ownership, not the physical
	// DomainUnion which retains dormant v1 containment envelopes; otherwise the
	// probe fabricates reclaim->primary edges the drain projection cannot execute.
	observedOwnership := semanticOwnershipUnionByDomain(in.Snapshot, in.Snapshot.DomainByRel)
	graph := buildTransferGraph(domains, observedOwnership, desiredByDomain, nil)
	if !isNarrowPrimaryReclaimCycle(graph) {
		return analysis, nil
	}
	analysis.SafeGrowAnchor = verifiedUnownedFinalCPUs(
		in.AllowedCPUs, observedOwnership, desiredByDomain,
	)
	if !analysis.SafeGrowAnchor.IsEmpty() {
		return analysis, nil
	}
	for _, source := range []DomainID{DomainPrimary, DomainReclaim} {
		destinations := make([]DomainID, 0, len(graph[source]))
		for destination := range graph[source] {
			destinations = append(destinations, destination)
		}
		sort.Slice(destinations, func(i, j int) bool { return destinations[i] < destinations[j] })
		for _, destination := range destinations {
			for _, cpus := range drainAtomCPUSetGroups(
				graph[source][destination], in.CPUDetails) {
				analysis.Atoms = append(analysis.Atoms, deadlock.DrainAtom{
					Source: source, Destination: destination, CPUs: cpus,
				})
			}
		}
	}
	analysis.ProbeStats.Atoms = len(analysis.Atoms)
	if in.Budget != nil {
		analysis.ProbeStats.AutoBudget = in.Budget.AutoDeadlockProbeOperations()
		protectedPreaggregationOperations := protectedAncestorPreaggregationOperations(in.Snapshot, in.ProtectedByRel)
		in.Budget.EnsureDeadlockProbeCapacity(deadlockProbeCapacityUpperBound(
			analysis.ProbeStats.SnapshotEntries,
			analysis.ProbeStats.SnapshotChildEdges,
			analysis.ProbeStats.Atoms,
			analysis.ProbeStats.ProtectedPendingCPUs,
			protectedPreaggregationOperations,
		))
		analysis.ProbeStats.ProbeLimit = in.Budget.DeadlockProbeLimit()
	}
	ctx := in.Context
	if ctx == nil {
		ctx = context.Background()
	}
	limit := in.DeadlockProbeBudget
	if limit == 0 && in.Budget == nil {
		limit = deadlock.DefaultDeadlockProbeBudget
	}
	probeCost := 0
	protectedByDomain := protectedCPUSetByDomain(in.ProtectedByRel, in.ProtectedPending, in.DAG)
	depthByRel, relOrder := buildSnapshotDepthByRel(in.Snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(in.Snapshot, in.DAG, depthByRel, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	projectionContext, err := buildDrainProjectionContext(ctx, in.Snapshot, in.ProtectedByRel, in.Budget)
	if err != nil {
		if errors.Is(err, ErrDeadlockProbeBudgetExceeded) {
			analysis.Completeness = deadlock.ProbeIndeterminate
			if projectionContext != nil {
				copyProjectionContextStats(&analysis.ProbeStats, projectionContext)
			}
			analysis.ProbeStats.ProbeOperations = deadlockProbeOperations(in.Budget, probeCost)
			return analysis, wrapDeadlockProbeError(err, analysis.ProbeStats)
		}
		return deadlock.DeadlockAnalysis{}, err
	}
	probeCost = projectionContext.cost
	copyProjectionContextStats(&analysis.ProbeStats, projectionContext)
	analysis.ProbeStats.ProbeOperations = deadlockProbeOperations(in.Budget, probeCost)
	leavingByDomain := make(map[DomainID]machine.CPUSet, len(domains))
	for _, domain := range domains {
		leavingByDomain[domain] = observedOwnership[domain].Difference(desiredByDomain[domain])
	}
	transferCPUs := machine.NewCPUSet()
	for _, atom := range analysis.Atoms {
		transferCPUs = transferCPUs.Union(atom.CPUs)
	}
	projectionInput := DrainProjectionInput{
		PlanInput: in, LeavingByDomain: leavingByDomain,
		DomainByRel: domainByRel, ParentByRel: parentByRel, DepthByRel: depthByRel,
		Context: projectionContext, TransferCPUs: transferCPUs,
		ProbeContext: ctx, ProbeBudget: in.Budget,
	}
	if err := prepareIncrementalDrainProjectionContext(projectionInput, projectionContext); err != nil {
		if errors.Is(err, ErrDeadlockProbeBudgetExceeded) {
			analysis.Completeness = deadlock.ProbeIndeterminate
			copyProjectionContextStats(&analysis.ProbeStats, projectionContext)
			analysis.ProbeStats.ProbeOperations = deadlockProbeOperations(in.Budget, projectionContext.cost)
			return analysis, wrapDeadlockProbeError(err, analysis.ProbeStats)
		}
		return deadlock.DeadlockAnalysis{}, err
	}
	probeCost = projectionContext.cost
	copyProjectionContextStats(&analysis.ProbeStats, projectionContext)
	analysis.ProbeStats.ProbeOperations = deadlockProbeOperations(in.Budget, probeCost)
	for i := range analysis.Atoms {
		atom := analysis.Atoms[i]
		analysis.ProbeStats.AtomIndex = i
		analysis.ProbeStats.AtomSource = atom.Source
		analysis.ProbeStats.AtomDestination = atom.Destination
		analysis.ProbeStats.ProbeOperations = deadlockProbeOperations(in.Budget, probeCost)
		if limit > 0 && probeCost >= limit {
			analysis.Completeness = deadlock.ProbeIndeterminate
			err := fmt.Errorf("%w: limit=%d used=%d",
				ErrDeadlockProbeBudgetExceeded, limit, probeCost)
			return analysis, wrapDeadlockProbeError(err, analysis.ProbeStats)
		}
		if !atom.CPUs.Intersection(protectedByDomain[atom.Source]).IsEmpty() {
			analysis.Protected = analysis.Protected.Union(atom.CPUs)
			analysis.AtomClasses = append(analysis.AtomClasses, deadlock.DrainAtomClassProtected)
			continue
		}
		drainBatch := map[DomainID]machine.CPUSet{
			DomainPrimary: machine.NewCPUSet(),
			DomainReclaim: machine.NewCPUSet(),
			atom.Source:   atom.CPUs,
		}
		atomInput := projectionInput
		atomInput.DrainBatch = drainBatch
		projection, err := projectSingleAtomIncremental(atomInput, atom)
		if err != nil {
			if errors.Is(err, ErrDeadlockProbeBudgetExceeded) {
				analysis.Completeness = deadlock.ProbeIndeterminate
				analysis.ProbeStats.ProbeOperations = deadlockProbeOperations(in.Budget, probeCost)
				return analysis, wrapDeadlockProbeError(err, analysis.ProbeStats)
			}
			return deadlock.DeadlockAnalysis{}, err
		}
		projectionCost := projection.Cost.Total()
		if limit > 0 && projectionCost > limit-probeCost {
			analysis.Completeness = deadlock.ProbeIndeterminate
			err := fmt.Errorf("%w: limit=%d used=%d requested=%d",
				ErrDeadlockProbeBudgetExceeded, limit, probeCost, projectionCost)
			analysis.ProbeStats.ProbeOperations = deadlockProbeOperations(in.Budget, probeCost)
			return analysis, wrapDeadlockProbeError(err, analysis.ProbeStats)
		}
		probeCost += projectionCost
		analysis.ProbeStats.AtomOperations += projectionCost
		analysis.ProbeStats.ProbeOperations = deadlockProbeOperations(in.Budget, probeCost)
		for rel, cpus := range projection.EmptyBlockers {
			analysis.EmptyBlockers[rel] = analysis.EmptyBlockers[rel].Union(cpus)
		}
		if projection.DomainUnion[atom.Source].Intersection(atom.CPUs).IsEmpty() {
			analysis.AtomClasses = append(analysis.AtomClasses, deadlock.DrainAtomClassReleasable)
			seed := atom
			analysis.SafeSeed = &seed
			return analysis, nil
		}
		if atom.CPUs.Size() > 1 {
			return analysis, fmt.Errorf(
				"%w: source=%s destination=%s core=%s",
				deadlock.ErrIncompleteRequiredCoreReleaseWitness,
				atom.Source, atom.Destination, atom.CPUs.String())
		}
		emptyBlocked := machine.NewCPUSet()
		for _, cpus := range projection.EmptyBlockers {
			emptyBlocked = emptyBlocked.Union(cpus)
		}
		if atom.CPUs.IsSubsetOf(emptyBlocked) {
			analysis.AtomClasses = append(analysis.AtomClasses, deadlock.DrainAtomClassV1Empty)
		} else {
			analysis.AtomClasses = append(analysis.AtomClasses, deadlock.DrainAtomClassHeld)
		}
	}
	return analysis, nil
}

type drainAtomCoreKey struct {
	numaID   int
	socketID int
	coreID   int
}

func drainAtomCPUSetGroups(
	cpus machine.CPUSet,
	details machine.CPUDetails,
) []machine.CPUSet {
	if len(details) == 0 {
		groups := make([]machine.CPUSet, 0, cpus.Size())
		for _, cpu := range cpus.ToSliceInt() {
			groups = append(groups, machine.NewCPUSet(cpu))
		}
		return groups
	}
	byCore := make(map[drainAtomCoreKey]machine.CPUSet)
	for _, cpu := range cpus.ToSliceInt() {
		detail, ok := details[cpu]
		if !ok {
			byCore[drainAtomCoreKey{coreID: cpu}] = machine.NewCPUSet(cpu)
			continue
		}
		key := drainAtomCoreKey{
			numaID: detail.NUMANodeID, socketID: detail.SocketID, coreID: detail.CoreID,
		}
		byCore[key] = byCore[key].Union(machine.NewCPUSet(cpu))
	}
	keys := make([]drainAtomCoreKey, 0, len(byCore))
	for key := range byCore {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].numaID != keys[j].numaID {
			return keys[i].numaID < keys[j].numaID
		}
		if keys[i].socketID != keys[j].socketID {
			return keys[i].socketID < keys[j].socketID
		}
		return keys[i].coreID < keys[j].coreID
	})
	groups := make([]machine.CPUSet, 0, len(keys))
	for _, key := range keys {
		groups = append(groups, byCore[key])
	}
	return groups
}

func snapshotChildEdgeCount(snapshot *CompleteSnapshot) int {
	if snapshot == nil {
		return 0
	}
	count := 0
	for _, children := range snapshot.Children {
		count += len(children)
	}
	return count
}

func deadlockProbeOperations(budget *BudgetTracker, fallback int) int {
	if budget == nil {
		return fallback
	}
	return budget.Usage().DeadlockProbeOperations
}

func deadlockProbeCapacityUpperBound(entries, childEdges, atoms, protectedPendingCPUs, protectedPreaggregationOperations int) int {
	snapshotWork := saturatingAdd(entries, childEdges)
	contextWork := saturatingMultiply(snapshotWork, 3)
	pendingWork := saturatingMultiply(protectedPendingCPUs, snapshotWork)
	perAtomWork := saturatingAdd(snapshotWork, 1)
	return saturatingAdd(
		deadlock.DefaultDeadlockProbeBudget,
		saturatingAdd(
			saturatingAdd(saturatingAdd(contextWork, pendingWork), protectedPreaggregationOperations),
			saturatingMultiply(atoms, perAtomWork),
		),
	)
}

// protectedAncestorPreaggregationOperations returns the exact number of
// budgeted iterations required to preaggregate protected CPUs into each
// materialized ancestor in the snapshot.
func protectedAncestorPreaggregationOperations(snapshot *CompleteSnapshot, protectedByRel map[string]machine.CPUSet) int {
	if snapshot == nil || len(protectedByRel) == 0 {
		return 0
	}

	operations := 0
	for originalRel := range protectedByRel {
		for rel := filepath.Clean(originalRel); rel != "." && rel != ""; rel = filepath.Dir(rel) {
			if _, ok := snapshot.Entries[rel]; ok {
				operations = saturatingAdd(operations, 1)
			}
		}
	}
	return operations
}

func copyProjectionContextStats(stats *deadlock.DeadlockProbeStats, projectionContext *drainProjectionContext) {
	if stats == nil || projectionContext == nil {
		return
	}
	stats.ContextOperations = projectionContext.cost
	stats.ContextPhase = projectionContext.phase
	stats.BaseOperations = projectionContext.baseCost
	stats.ProtectedOperations = projectionContext.protectedOperations
	stats.RelIndexOperations = projectionContext.relIndexOperations
	stats.ChildIndexOps = projectionContext.childIndexOperations
	stats.ChildMembershipsScanned = projectionContext.childMembershipsScanned
	stats.FrontierIndexOps = projectionContext.frontierIndexOperations
	stats.FrontierMembershipsScanned = projectionContext.frontierMembershipsScanned
	stats.AncestorClosureOps = projectionContext.ancestorClosureOperations
}

func wrapDeadlockProbeError(err error, stats deadlock.DeadlockProbeStats) error {
	return fmt.Errorf(
		"%w: atoms=%d atom_index=%d atom_source=%s atom_destination=%s "+
			"snapshot_entries=%d snapshot_child_edges=%d protected_rels=%d "+
			"protected_pending_cpus=%d probe_operations=%d probe_limit=%d auto_budget=%t context_operations=%d "+
			"context_phase=%s base_operations=%d protected_operations=%d "+
			"rel_index_operations=%d child_index_operations=%d "+
			"child_memberships_scanned=%d "+
			"frontier_index_operations=%d frontier_memberships_scanned=%d "+
			"ancestor_closure_operations=%d "+
			"atom_operations=%d snapshot_id=%x",
		err,
		stats.Atoms,
		stats.AtomIndex,
		stats.AtomSource,
		stats.AtomDestination,
		stats.SnapshotEntries,
		stats.SnapshotChildEdges,
		stats.ProtectedRels,
		stats.ProtectedPendingCPUs,
		stats.ProbeOperations,
		stats.ProbeLimit,
		stats.AutoBudget,
		stats.ContextOperations,
		stats.ContextPhase,
		stats.BaseOperations,
		stats.ProtectedOperations,
		stats.RelIndexOperations,
		stats.ChildIndexOps,
		stats.ChildMembershipsScanned,
		stats.FrontierIndexOps,
		stats.FrontierMembershipsScanned,
		stats.AncestorClosureOps,
		stats.AtomOperations,
		stats.SnapshotID,
	)
}

func verifiedUnownedFinalCPUs(
	allowed machine.CPUSet,
	observed, desired map[DomainID]machine.CPUSet,
) machine.CPUSet {
	owned := machine.NewCPUSet()
	for _, cpus := range observed {
		owned = owned.Union(cpus)
	}
	unowned := allowed.Difference(owned)
	anchors := machine.NewCPUSet()
	for _, cpu := range unowned.ToSliceInt() {
		owners := 0
		for _, cpus := range desired {
			if cpus.Contains(cpu) {
				owners++
			}
		}
		if owners == 1 {
			anchors = anchors.Union(machine.NewCPUSet(cpu))
		}
	}
	return anchors
}

func isNarrowPrimaryReclaimCycle(graph map[DomainID]map[DomainID]machine.CPUSet) bool {
	if len(graph) != 2 {
		return false
	}
	primaryToReclaim := graph[DomainPrimary][DomainReclaim]
	reclaimToPrimary := graph[DomainReclaim][DomainPrimary]
	if primaryToReclaim.IsEmpty() || reclaimToPrimary.IsEmpty() {
		return false
	}
	for source, destinations := range graph {
		for destination := range destinations {
			if !((source == DomainPrimary && destination == DomainReclaim) ||
				(source == DomainReclaim && destination == DomainPrimary)) {
				return false
			}
		}
	}
	return true
}

func structuralV1Deadlock(analysis deadlock.DeadlockAnalysis) bool {
	if analysis.Completeness != deadlock.ProbeComplete ||
		len(analysis.Atoms) == 0 ||
		len(analysis.AtomClasses) != len(analysis.Atoms) {
		return false
	}
	for _, class := range analysis.AtomClasses {
		if class != deadlock.DrainAtomClassV1Empty {
			return false
		}
	}
	return true
}
