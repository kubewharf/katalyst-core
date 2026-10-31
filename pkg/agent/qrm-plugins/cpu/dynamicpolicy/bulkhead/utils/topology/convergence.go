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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type ConvergenceReport struct {
	FullyConverged        bool
	NonConvergedTargets   []RelConvergence
	PendingToPrimary      machine.CPUSet
	PendingToReclaim      machine.CPUSet
	CleanupPendingPrimary machine.CPUSet
	CleanupPendingReclaim machine.CPUSet
}

type RelConvergence struct {
	Rel          string
	Observed     machine.CPUSet
	Target       machine.CPUSet
	ObservedMems string
	TargetMems   string
	Reason       string
}

type ParentSafetyReport struct {
	Safe                   bool
	PendingOutsidePrimary  machine.CPUSet
	PendingInsideReclaim   machine.CPUSet
	PrimaryReclaimOverlap  machine.CPUSet
	PendingScopeDeficit    map[string]machine.CPUSet
	RequiredFloorDeficit   map[string]machine.CPUSet
	UnsafeRequiredRels     []RelConvergence
	DeferredLeafMismatches []RelConvergence
}

type coordinatorSnapshotEvaluation struct {
	Report       ConvergenceReport
	ParentSafety ParentSafetyReport
}

func evaluateCoordinatorSnapshot(
	snapshot *CompleteSnapshot,
	dag *TopoDAG,
	targetByRel map[string]machine.CPUSet,
	semanticTargetByRel map[string]machine.CPUSet,
	targetMemsByRel map[string]string,
	desired map[DomainID]machine.CPUSet,
	allowedCPUs machine.CPUSet,
	expectedByRel map[string]machine.CPUSet,
	requiredByRel map[string]machine.CPUSet,
	deferredByRel map[string]machine.CPUSet,
	deferredMismatchRels map[string]struct{},
	protectedPending machine.CPUSet,
	pendingRequiredByRel map[string]machine.CPUSet,
	capabilities HierarchyCapabilities,
	allowEmptyTarget bool,
) (coordinatorSnapshotEvaluation, error) {
	report, err := buildConvergenceReport(
		snapshot, dag, targetByRel, targetMemsByRel, desired,
		allowedCPUs, capabilities, allowEmptyTarget,
	)
	if err != nil {
		return coordinatorSnapshotEvaluation{}, err
	}
	includeMaterializedDynamicConvergence(&report, snapshot, expectedByRel, capabilities)
	includeMaterializedDynamicConvergence(&report, snapshot, deferredByRel, capabilities)
	return coordinatorSnapshotEvaluation{
		Report: report,
		ParentSafety: buildParentSafetyReportWithScopedPending(
			snapshot, dag, semanticTargetByRel, report, protectedPending,
			pendingRequiredByRel, requiredByRel, deferredByRel, deferredMismatchRels, capabilities,
		),
	}, nil
}

func buildParentSafetyReport(
	snapshot *CompleteSnapshot,
	dag *TopoDAG,
	semanticTargetByRel map[string]machine.CPUSet,
	convergence ConvergenceReport,
	protectedPending machine.CPUSet,
	deferredByRel map[string]machine.CPUSet,
	deferredMismatchRels map[string]struct{},
	capabilities HierarchyCapabilities,
) ParentSafetyReport {
	return buildParentSafetyReportWithRequired(
		snapshot, dag, semanticTargetByRel, convergence, protectedPending,
		nil, deferredByRel, deferredMismatchRels, capabilities,
	)
}

func buildParentSafetyReportWithRequired(
	snapshot *CompleteSnapshot,
	dag *TopoDAG,
	targetByRel map[string]machine.CPUSet,
	convergence ConvergenceReport,
	protectedPending machine.CPUSet,
	requiredByRel map[string]machine.CPUSet,
	deferredByRel map[string]machine.CPUSet,
	deferredMismatchRels map[string]struct{},
	capabilities HierarchyCapabilities,
) ParentSafetyReport {
	return buildParentSafetyReportWithScopedPending(
		snapshot, dag, targetByRel, convergence, protectedPending,
		nil, requiredByRel, deferredByRel, deferredMismatchRels, capabilities,
	)
}

func buildParentSafetyReportWithScopedPending(
	snapshot *CompleteSnapshot,
	dag *TopoDAG,
	targetByRel map[string]machine.CPUSet,
	convergence ConvergenceReport,
	protectedPending machine.CPUSet,
	pendingRequiredByRel map[string]machine.CPUSet,
	requiredByRel map[string]machine.CPUSet,
	deferredByRel map[string]machine.CPUSet,
	deferredMismatchRels map[string]struct{},
	capabilities HierarchyCapabilities,
) ParentSafetyReport {
	report := ParentSafetyReport{
		PendingOutsidePrimary: protectedPending.Clone(),
		PendingScopeDeficit:   make(map[string]machine.CPUSet),
		RequiredFloorDeficit:  make(map[string]machine.CPUSet),
	}
	if snapshot == nil || dag == nil {
		return report
	}
	primary := snapshot.DomainUnion[DomainPrimary]
	reclaim := snapshot.DomainUnion[DomainReclaim]
	if targetByRel != nil {
		desiredByDomain := desiredDomainUnions(dag, targetByRel)
		primary = desiredByDomain[DomainPrimary]
		reclaim = desiredByDomain[DomainReclaim]
	}
	report.PendingOutsidePrimary = protectedPending.Difference(primary)
	report.PendingInsideReclaim = protectedPending.Intersection(reclaim)
	report.PrimaryReclaimOverlap = primary.Intersection(reclaim)
	for _, mismatch := range convergence.NonConvergedTargets {
		if _, required := requiredByRel[mismatch.Rel]; required {
			continue
		}
		_, deferredLeaf := deferredByRel[mismatch.Rel]
		_, deferredCleanup := deferredMismatchRels[mismatch.Rel]
		if deferredLeaf || deferredCleanup {
			report.DeferredLeafMismatches = append(report.DeferredLeafMismatches, mismatch)
			continue
		}
		report.UnsafeRequiredRels = append(report.UnsafeRequiredRels, mismatch)
	}
	for rel, expected := range deferredByRel {
		entry, ok := snapshot.Entries[rel]
		if !ok {
			report.UnsafeRequiredRels = append(report.UnsafeRequiredRels, RelConvergence{
				Rel: rel, Target: expected.Clone(), Reason: convergenceReasonReadError,
			})
			continue
		}
		observed := observedCPUsForTargetProof(entry, expected, capabilities)
		if !observed.Intersection(reclaim).IsEmpty() || !expected.Intersection(reclaim).IsEmpty() {
			report.UnsafeRequiredRels = append(report.UnsafeRequiredRels, RelConvergence{
				Rel: rel, Observed: observed.Clone(), Target: expected.Clone(), Reason: "unsafe_deferred_leaf",
			})
		}
	}
	for rel, required := range requiredByRel {
		entry, ok := snapshot.Entries[rel]
		if !ok {
			report.RequiredFloorDeficit[rel] = required.Clone()
			continue
		}
		observed := observedCPUsForTargetProof(entry, required, capabilities)
		if deficit := required.Difference(observed); !deficit.IsEmpty() {
			report.RequiredFloorDeficit[rel] = deficit
		}
	}
	for rel, required := range pendingRequiredByRel {
		entry, ok := snapshot.Entries[rel]
		if !ok {
			report.PendingScopeDeficit[rel] = required.Clone()
			continue
		}
		if deficit := required.Difference(entry.CPUs); !deficit.IsEmpty() {
			report.PendingScopeDeficit[rel] = deficit
		}
	}
	for parentRel, children := range snapshot.Children {
		parent, ok := snapshot.Entries[parentRel]
		if !ok {
			continue
		}
		for _, child := range children {
			childRel := child.Name
			if parentRel != "" {
				childRel = parentRel + "/" + child.Name
			}
			childEntry, exists := snapshot.Entries[childRel]
			if exists && !childEntry.CPUs.IsSubsetOf(parent.CPUs) {
				report.UnsafeRequiredRels = append(report.UnsafeRequiredRels, RelConvergence{
					Rel: childRel, Observed: childEntry.CPUs.Clone(), Target: parent.CPUs.Clone(), Reason: "parent_not_containing_child",
				})
			}
		}
	}
	report.Safe = report.PendingOutsidePrimary.IsEmpty() &&
		report.PendingInsideReclaim.IsEmpty() &&
		report.PrimaryReclaimOverlap.IsEmpty() &&
		len(report.PendingScopeDeficit) == 0 &&
		len(report.RequiredFloorDeficit) == 0 &&
		len(report.UnsafeRequiredRels) == 0
	return report
}

func mergeCPUSetMaps(left, right map[string]machine.CPUSet) map[string]machine.CPUSet {
	out := model.CloneCPUSetMap(left)
	for rel, cpus := range right {
		out[rel] = cpus.Clone()
	}
	return out
}

const (
	convergenceReasonTargetMismatch = "target_mismatch"
	convergenceReasonReadError      = "read_error"
)

func buildConvergenceReport(
	snapshot *CompleteSnapshot,
	dag *TopoDAG,
	targetByRel map[string]machine.CPUSet,
	targetMemsByRel map[string]string,
	desired map[DomainID]machine.CPUSet,
	allowedCPUs machine.CPUSet,
	capabilities HierarchyCapabilities,
	allowEmptyTarget bool,
) (ConvergenceReport, error) {
	gate, err := NewDomainGate("convergence-report", snapshot, desired, allowedCPUs, nil)
	if err != nil {
		return ConvergenceReport{}, err
	}
	// Successful writes alone do not prove convergence: runtime descendants may
	// appear between writes. Compare the current hierarchy with targets to expose
	// incomplete convergence.
	report := ConvergenceReport{
		PendingToPrimary:      gate.pending[DomainPrimary].Clone(),
		PendingToReclaim:      gate.pending[DomainReclaim].Clone(),
		CleanupPendingPrimary: gate.cleanupPending[DomainPrimary].Clone(),
		CleanupPendingReclaim: gate.cleanupPending[DomainReclaim].Clone(),
	}
	for _, node := range dag.Nodes() {
		target := targetByRel[node.Rel]
		if target.IsEmpty() && !allowEmptyTarget {
			continue
		}
		entry, ok := snapshot.Entries[node.Rel]
		if !ok {
			report.NonConvergedTargets = append(report.NonConvergedTargets, RelConvergence{
				Rel:    node.Rel,
				Target: target.Clone(),
				Reason: convergenceReasonReadError,
			})
			continue
		}
		targetMems := targetMemsByRel[node.Rel]
		memsMatch := true
		if targetMems != "" {
			observedSet, observedErr := machine.Parse(entry.Mems)
			targetSet, targetErr := machine.Parse(targetMems)
			memsMatch = observedErr == nil && targetErr == nil && observedSet.Equals(targetSet)
		}
		observedCPUs := observedCPUsForTargetProof(entry, target, capabilities)
		if !observedCPUs.Equals(target) || !memsMatch {
			report.NonConvergedTargets = append(report.NonConvergedTargets, RelConvergence{
				Rel:          node.Rel,
				Observed:     observedCPUs.Clone(),
				Target:       target.Clone(),
				ObservedMems: entry.Mems,
				TargetMems:   targetMems,
				Reason:       convergenceReasonTargetMismatch,
			})
		}
	}
	report.FullyConverged = len(report.NonConvergedTargets) == 0 &&
		report.PendingToPrimary.IsEmpty() &&
		report.PendingToReclaim.IsEmpty() &&
		report.CleanupPendingPrimary.IsEmpty() &&
		report.CleanupPendingReclaim.IsEmpty()
	return report, nil
}
