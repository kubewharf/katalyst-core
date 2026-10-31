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
	"errors"
	"fmt"
	"hash"
	"math"
	"path/filepath"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	ErrInvalidReclaimBucketTarget = errors.New("topology target exceeds constraint")
	ErrExpandPlanWouldShrink      = errors.New("expand plan would shrink cpuset")
	ErrConflictingWriteDirection  = errors.New("cpuset.cpus and cpuset.mems write directions conflict")
)

type EmptyTargetSource string

const (
	EmptyTargetSourceExplicitDynamic EmptyTargetSource = "explicit_dynamic"
	EmptyTargetSourceControlled      EmptyTargetSource = "controlled"
)

type UnsupportedEmptyTargetError struct {
	Rel     string
	Source  EmptyTargetSource
	Current machine.CPUSet
}

func (e *UnsupportedEmptyTargetError) Error() string {
	return fmt.Sprintf("v1 planner cannot empty non-empty cpuset: rel=%q source=%s current=%s",
		e.Rel, e.Source, e.Current.String())
}

func (e *UnsupportedEmptyTargetError) Unwrap() error {
	return ErrEmptyCPUSetUnsupported
}

type ConflictingWriteDirectionError struct {
	Rel           string
	CPUDirection  WriteDirection
	MemsDirection WriteDirection
	Current       CPUSetTarget
	Target        CPUSetTarget
}

func (e *ConflictingWriteDirectionError) Error() string {
	return fmt.Sprintf("%v: rel=%q cpu_direction=%s mems_direction=%s current_cpus=%s target_cpus=%s current_mems=%q target_mems=%q",
		ErrConflictingWriteDirection, e.Rel, e.CPUDirection, e.MemsDirection,
		e.Current.CPUs.String(), e.Target.CPUs.String(), e.Current.Mems, e.Target.Mems)
}

func (e *ConflictingWriteDirectionError) Unwrap() error {
	return ErrConflictingWriteDirection
}

type PhaseKind string

const (
	PhaseDrain  PhaseKind = "drain"
	PhaseExpand PhaseKind = "expand"
)

type WriteDirection string

const (
	WriteShrink  WriteDirection = "shrink"
	WriteGrow    WriteDirection = "grow"
	WritePublish WriteDirection = "publish"
)

type CPUSetTarget struct {
	CPUs machine.CPUSet
	Mems string
}

// ExecutionReservationCost is the exact forward-write budget of a frozen
// trace and the matching rollback capacity required to reverse every write.
type ExecutionReservationCost struct {
	Forward  PhysicalWriteCost
	Rollback PhysicalWriteCost
}

type OperationRequirement string

const (
	OperationAdmissionSourceDrain  OperationRequirement = "admission_source_drain"
	OperationAdmissionAncestorGrow OperationRequirement = "admission_ancestor_grow"
	OperationAdmissionSafetyRepair OperationRequirement = "admission_safety_repair"
	OperationDeferredLeafExact     OperationRequirement = "deferred_leaf_exact"
	OperationDeferredCleanup       OperationRequirement = "deferred_cleanup"
)

type PlanOperation struct {
	PlanID                 string
	Rel                    string
	ExpectedIdentity       CgroupIdentity
	ExpectedChildren       string
	ExpectedChildUnion     machine.CPUSet
	ParentRel              string
	ExpectedParentIdentity CgroupIdentity
	ExpectedCurrent        CPUSetTarget
	Target                 CPUSetTarget
	Direction              WriteDirection
	OwnsMems               bool
	WriteMems              bool
	Requirement            OperationRequirement
}

type AdmissionSafetyInput struct {
	PendingCPUSet        machine.CPUSet
	PendingRequiredByRel map[string]machine.CPUSet
	DeferredCPUSetByRel  map[string]machine.CPUSet
	RequiredCPUSetByRel  map[string]machine.CPUSet
}

// parentContainmentCPUs returns the observed cpuset of an operation's primary
// parent entry, or an empty CPUSet when the parent is absent from the base
// snapshot. It lets the admission split tell a true parent-containment drain
// (removed reclaim CPUs the parent does not hold) apart from an in-progress
// transfer (removed CPUs the parent already holds, which must stay deferred).
func parentContainmentCPUs(base *CompleteSnapshot, parentRel string) machine.CPUSet {
	if base == nil || parentRel == "" {
		return machine.NewCPUSet()
	}
	parent, ok := base.Entries[parentRel]
	if !ok {
		return machine.NewCPUSet()
	}
	return parent.CPUs.Clone()
}

// SplitPlanForAdmission returns an executable safety closure and a summary-only
// deferred plan. Callers must never persist or replay the deferred operations;
// retry/periodic rounds rebuild a full plan from a fresh snapshot. Pending CPUs
// are classified per destination domain: a shrink is mandatory only when it
// removes pending CPUs still owned by the source domain and no pending
// requirement targets that same domain. Missing ownership evidence falls back
// to the conservative global rule.
func SplitPlanForAdmission(plan *PhasePlan, in AdmissionSafetyInput) (required, deferred *PhasePlan, err error) {
	if plan == nil {
		return nil, nil, errors.New("cannot split nil admission plan")
	}
	requiredPlan, deferredPlan := *plan, *plan
	requiredPlan.Operations = nil
	deferredPlan.Operations = nil
	type operationClass struct {
		required    bool
		requirement OperationRequirement
	}
	classes := make([]operationClass, len(plan.Operations))
	outgoingCPUsBySource := make(map[DomainID]machine.CPUSet, len(plan.TransferGraph))
	incomingCPUsByDestination := make(map[DomainID]machine.CPUSet, len(plan.TransferGraph))
	pendingCPUsByDestination := make(map[DomainID]machine.CPUSet)
	for source, destinations := range plan.TransferGraph {
		for destination, cpus := range destinations {
			outgoingCPUsBySource[source] = outgoingCPUsBySource[source].Union(cpus)
			incomingCPUsByDestination[destination] = incomingCPUsByDestination[destination].Union(cpus)
		}
	}
	if plan.Base != nil {
		for rel, cpus := range in.PendingRequiredByRel {
			domain := plan.Base.DomainByRel[rel]
			if domain == "" {
				continue
			}
			pendingCPUsByDestination[domain] = pendingCPUsByDestination[domain].
				Union(cpus.Intersection(in.PendingCPUSet))
		}
	}
	for i, operation := range plan.Operations {
		deferredTarget, explicitlyDeferred := in.DeferredCPUSetByRel[operation.Rel]
		removedCPUs := operation.ExpectedCurrent.CPUs.Difference(operation.Target.CPUs)
		addedCPUs := operation.Target.CPUs.Difference(operation.ExpectedCurrent.CPUs)
		operationDomain := DomainID("")
		if plan.Base != nil {
			operationDomain = plan.Base.DomainByRel[operation.Rel]
		}
		removedPending := removedCPUs.Intersection(in.PendingCPUSet)
		sourceOwnedPendingDrain := !removedPending.IsEmpty()
		if plan.Base != nil && operationDomain != "" {
			if !pendingCPUsByDestination[operationDomain].IsEmpty() {
				sourceOwnedPendingDrain = false
			} else if owned, ok := plan.Base.DomainUnion[operationDomain]; ok {
				sourceOwnedPendingDrain = !removedPending.Intersection(owned).IsEmpty()
			}
		}
		switch {
		case operation.Direction == WriteShrink &&
			sourceOwnedPendingDrain:
			classes[i] = operationClass{required: true, requirement: OperationAdmissionSourceDrain}
		case operation.Direction == WriteShrink &&
			!removedCPUs.Intersection(outgoingCPUsBySource[operationDomain]).IsEmpty():
			classes[i] = operationClass{required: true, requirement: OperationAdmissionSourceDrain}
		case operation.Direction == WriteShrink &&
			operationDomain == DomainPrimary &&
			plan.Base != nil &&
			!operation.Target.CPUs.IsEmpty() &&
			!removedCPUs.Intersection(plan.Base.DomainUnion[DomainReclaim]).IsEmpty() &&
			removedCPUs.Intersection(parentContainmentCPUs(plan.Base, operation.ParentRel)).IsEmpty():
			// A primary entry currently holds CPUs that already belong to the
			// reclaim union AND that its primary parent does not contain, while
			// the entry itself still converges to a non-empty target. The
			// entry's observed cpuset is therefore not contained by its parent
			// (parent_not_containing_child / primary-reclaim overlap).
			// walkTransferEdges only credits a shrink as an outgoing transfer
			// when a destination domain is net *entering* these CPUs; here the
			// reclaim bucket already enforces them, so there is no transfer edge
			// and the shrink would otherwise fall through to
			// OperationDeferredCleanup. That deferral leaves the containment
			// violation in place forever and stalls the fixed-point loop as a
			// bare ErrNoProgress. The removed CPUs being absent from the parent
			// entry is what makes this a mandatory source drain rather than an
			// in-progress transfer (where the parent already holds the CPU and
			// the shrink must stay deferred/absorbed). A non-empty target keeps
			// this from reaching a freshly-churned dynamic holder that should be
			// absorbed, not drained to empty.
			classes[i] = operationClass{required: true, requirement: OperationAdmissionSourceDrain}
		case operation.Direction == WriteGrow &&
			!operation.Target.CPUs.Difference(operation.ExpectedCurrent.CPUs).
				Intersection(in.PendingRequiredByRel[operation.Rel]).IsEmpty():
			classes[i] = operationClass{required: true, requirement: OperationAdmissionAncestorGrow}
		case operation.Direction == WriteGrow &&
			!addedCPUs.Intersection(incomingCPUsByDestination[operationDomain]).IsEmpty():
			classes[i] = operationClass{required: true, requirement: OperationAdmissionSafetyRepair}
		case operation.Direction == WriteGrow &&
			!addedCPUs.Intersection(in.RequiredCPUSetByRel[operation.Rel]).IsEmpty():
			classes[i] = operationClass{required: true, requirement: OperationAdmissionSafetyRepair}
		case explicitlyDeferred &&
			operation.Direction == WriteShrink &&
			operation.Target.CPUs.Equals(deferredTarget) &&
			deferredTarget.IsSubsetOf(operation.ExpectedCurrent.CPUs):
			classes[i] = operationClass{requirement: OperationDeferredLeafExact}
		case operation.Direction == WriteShrink || operation.Direction == WriteGrow:
			classes[i] = operationClass{requirement: OperationDeferredCleanup}
		default:
			classes[i] = operationClass{required: true, requirement: OperationAdmissionSafetyRepair}
		}
	}

	growOperationByRel := make(map[string]int, len(plan.Operations))
	for i, operation := range plan.Operations {
		if operation.Direction == WriteGrow {
			growOperationByRel[operation.Rel] = i
		}
	}
	for i, operation := range plan.Operations {
		if !classes[i].required || operation.Direction != WriteGrow {
			continue
		}
		requiredTarget := operation.Target.CPUs
		parentRel := operation.ParentRel
		for parentRel != "" {
			if parentIndex, ok := growOperationByRel[parentRel]; ok {
				parentOperation := plan.Operations[parentIndex]
				if !requiredTarget.IsSubsetOf(parentOperation.Target.CPUs) {
					return nil, nil, fmt.Errorf(
						"admission child grow target %q for %q is not covered by planned parent target %q for %q",
						requiredTarget.String(), operation.Rel,
						parentOperation.Target.CPUs.String(), parentRel)
				}
				if !classes[parentIndex].required {
					classes[parentIndex] = operationClass{
						required:    true,
						requirement: OperationAdmissionAncestorGrow,
					}
				}
				requiredTarget = parentOperation.Target.CPUs
				parentRel = parentOperation.ParentRel
				continue
			}
			if plan.Base == nil {
				return nil, nil, fmt.Errorf(
					"admission child grow %q requires parent %q but plan has no base snapshot",
					operation.Rel, parentRel)
			}
			parentEntry, ok := plan.Base.Entries[parentRel]
			if !ok {
				return nil, nil, fmt.Errorf(
					"admission child grow %q requires missing parent %q",
					operation.Rel, parentRel)
			}
			if !requiredTarget.IsSubsetOf(parentEntry.CPUs) {
				return nil, nil, fmt.Errorf(
					"admission child grow target %q for %q is not covered by current parent %q target %q",
					requiredTarget.String(), operation.Rel, parentRel, parentEntry.CPUs.String())
			}
			break
		}
	}

	for growIndex, growOperation := range plan.Operations {
		if !classes[growIndex].required || growOperation.Direction != WriteGrow {
			continue
		}
		for shrinkIndex := growIndex - 1; shrinkIndex >= 0; shrinkIndex-- {
			shrinkOperation := plan.Operations[shrinkIndex]
			if shrinkOperation.Rel != growOperation.Rel {
				continue
			}
			if shrinkOperation.Direction != WriteShrink ||
				!shrinkOperation.Target.CPUs.Equals(growOperation.ExpectedCurrent.CPUs) ||
				shrinkOperation.Target.Mems != growOperation.ExpectedCurrent.Mems {
				break
			}
			if !classes[shrinkIndex].required {
				classes[shrinkIndex] = operationClass{
					required:    true,
					requirement: OperationAdmissionSafetyRepair,
				}
			}
			break
		}
	}

	for i, operation := range plan.Operations {
		operation.Requirement = classes[i].requirement
		if classes[i].required {
			requiredPlan.Operations = append(requiredPlan.Operations, operation)
		} else {
			deferredPlan.Operations = append(deferredPlan.Operations, operation)
		}
	}
	if err := validateAdmissionGrowAncestorClosure(&requiredPlan); err != nil {
		return nil, nil, err
	}
	finalizeSplitPlan := func(split *PhasePlan) {
		split.CostUpperBound.Operations = len(split.Operations)
		split.PlanID = canonicalExecutionPlanID(*split)
		for i := range split.Operations {
			split.Operations[i].PlanID = split.PlanID
		}
	}
	finalizeSplitPlan(&requiredPlan)
	finalizeSplitPlan(&deferredPlan)
	return &requiredPlan, &deferredPlan, nil
}

func validateAdmissionGrowAncestorClosure(plan *PhasePlan) error {
	if plan == nil {
		return errors.New("cannot validate nil admission plan")
	}
	requiredGrowByRel := make(map[string]PlanOperation, len(plan.Operations))
	for _, operation := range plan.Operations {
		if operation.Direction == WriteGrow {
			requiredGrowByRel[operation.Rel] = operation
		}
	}
	for _, operation := range plan.Operations {
		if operation.Direction != WriteGrow || operation.ParentRel == "" {
			continue
		}
		var parentTarget machine.CPUSet
		if parentOperation, ok := requiredGrowByRel[operation.ParentRel]; ok {
			parentTarget = parentOperation.Target.CPUs
		} else {
			if plan.Base == nil {
				return fmt.Errorf(
					"admission grow closure for %q requires parent %q but plan has no base snapshot",
					operation.Rel, operation.ParentRel)
			}
			parentEntry, ok := plan.Base.Entries[operation.ParentRel]
			if !ok {
				return fmt.Errorf(
					"admission grow closure for %q requires missing parent %q",
					operation.Rel, operation.ParentRel)
			}
			parentTarget = parentEntry.CPUs
		}
		if !operation.Target.CPUs.IsSubsetOf(parentTarget) {
			return fmt.Errorf(
				"admission grow closure violated: parent %q target %q does not cover child %q target %q",
				operation.ParentRel, parentTarget.String(),
				operation.Rel, operation.Target.CPUs.String())
		}
	}
	return nil
}

type DrainSelectionPolicy struct {
	MaxCPUsDrainRatio         float64
	GroupByNUMA               bool
	RequirePairedSwapProgress bool
}

type PhasePlan struct {
	ConvergenceID    string
	PlanID           string
	Base             *CompleteSnapshot
	FailClosedRoots  []string
	Kind             PhaseKind
	AllowEmptyTarget bool
	Capabilities     HierarchyCapabilities
	ControlledRels   []string
	DormantRels      map[string]struct{}
	Witnesses        []ReleaseWitness
	TransferGraph    map[DomainID]map[DomainID]machine.CPUSet
	// CanonicalTargetByRel is the immutable final target for the convergence
	// attempt. TargetByRel remains the executable target for this phase.
	CanonicalTargetByRel map[string]CPUSetTarget
	TargetByRel          map[string]CPUSetTarget
	AllowedEntering      map[DomainID]machine.CPUSet
	DrainBatch           map[DomainID]machine.CPUSet
	Operations           []PlanOperation
	CostUpperBound       BudgetUsage
}

func finalAdmissionTarget(plan *PhasePlan) map[string]CPUSetTarget {
	if plan == nil {
		return nil
	}
	targets := make(map[string]CPUSetTarget, len(plan.TargetByRel)+len(plan.CanonicalTargetByRel))
	for rel, target := range plan.TargetByRel {
		target.CPUs = target.CPUs.Clone()
		targets[rel] = target
	}
	for rel, target := range plan.CanonicalTargetByRel {
		target.CPUs = target.CPUs.Clone()
		targets[rel] = target
	}
	return targets
}

type RoundStatus string

const (
	RoundStatusProgress  RoundStatus = "progress"
	RoundStatusStale     RoundStatus = "stale"
	RoundStatusBlocked   RoundStatus = "blocked"
	RoundStatusConverged RoundStatus = "converged"
)

type RoundOutcome struct {
	Status      RoundStatus
	Snapshot    *CompleteSnapshot
	Witnesses   []ReleaseWitness
	Blocker     error
	Journal     []AppliedPlanOperation
	ChangedRels []string
	Progress    ProgressMeasure
	Cost        BudgetUsage
}

type ProgressMeasure struct {
	DrainChangedRels int
	VerifiedWrites   int
}

func (m ProgressMeasure) MadeProgress() bool {
	return m.DrainChangedRels > 0 || m.VerifiedWrites > 0
}

type PhasePlanInput struct {
	Context  context.Context
	Kind     PhaseKind
	DAG      *TopoDAG
	Snapshot *CompleteSnapshot
	// DesiredByRel is the physical writer/convergence target. SemanticByRel
	// remains the ownership authority for domain transfer decisions.
	DesiredByRel     map[string]machine.CPUSet
	SemanticByRel    map[string]machine.CPUSet
	DynamicByRel     map[string]machine.CPUSet
	DesiredMemsByRel map[string]string
	DormantRels      map[string]struct{}
	AllowedCPUs      machine.CPUSet
	// AllowEmptyTarget records whether the backing cgroup version accepts an
	// explicitly empty cpuset.cpus target.
	AllowEmptyTarget bool
	// Capabilities fix the proof semantics for this hierarchy generation; planner, writer, and rebase
	// must use the same capability set rather than infer the cgroup version independently.
	Capabilities     HierarchyCapabilities
	Witnesses        []ReleaseWitness
	ProtectedPending machine.CPUSet
	ProtectedByRel   map[string]machine.CPUSet
	CPUDetails       machine.CPUDetails
	Selection        DrainSelectionPolicy
	// DeadlockProbeBudget bounds canonical drain-atom projections. Zero uses a
	// defensive default. Exhaustion is indeterminate and never structural.
	DeadlockProbeBudget int
	Budget              *BudgetTracker
}

func BuildPhasePlan(in PhasePlanInput) (PhasePlan, error) {
	return buildPhasePlanWithStats(in, nil)
}

func semanticTargetsForPlan(in PhasePlanInput) map[string]machine.CPUSet {
	if in.SemanticByRel != nil {
		return in.SemanticByRel
	}
	return in.DesiredByRel
}

type plannerBuildStats struct {
	DomainEntries          int
	TransferEdgesCounted   int
	TransferEdgesAllocated int
	PlanOperationsCounted  int
	SortKeys               int
	DepthNodes             int
	DepthEdges             int
}

func buildPhasePlanWithStats(in PhasePlanInput, stats *plannerBuildStats) (PhasePlan, error) {
	if in.DAG == nil || in.Snapshot == nil || in.Budget == nil {
		return PhasePlan{}, fmt.Errorf("phase planner requires DAG, complete snapshot and budget")
	}
	if in.Kind != PhaseDrain && in.Kind != PhaseExpand {
		return PhasePlan{}, fmt.Errorf("unsupported phase kind %q", in.Kind)
	}
	if in.AllowedCPUs.IsEmpty() {
		return PhasePlan{}, fmt.Errorf("phase planner requires explicit non-empty AllowedCPUs")
	}
	if len(in.DormantRels) > 0 && in.SemanticByRel == nil {
		return PhasePlan{}, fmt.Errorf("phase planner requires semantic targets for dormant relations")
	}
	if err := validateFinalTargets(in); err != nil {
		return PhasePlan{}, err
	}
	if err := validateExecutableEmptyTargets(in); err != nil {
		return PhasePlan{}, err
	}
	if err := validateTopologyConstraints(in); err != nil {
		return PhasePlan{}, err
	}
	semanticByRel := semanticTargetsForPlan(in)
	// Observed domain ownership must follow semantic evidence (OwnershipByRel),
	// not the physical DomainUnion which retains dormant v1 containment envelopes.
	observedOwnership := semanticOwnershipUnionByDomain(in.Snapshot, in.Snapshot.DomainByRel)
	domains, desiredByDomain, err := collectPlannerDomains(in.DAG, observedOwnership, semanticByRel, in.Budget)
	if err != nil {
		return PhasePlan{}, err
	}
	depthStats := &depthBuildStats{}
	depthByRel, relOrder := buildSnapshotDepthByRel(in.Snapshot, depthStats)
	domainByRel, parentByRel := buildPlannerRelations(in.Snapshot, in.DAG, depthByRel, relOrder.relsAsc, relOrder.childRelsByRel, stats)
	if stats != nil {
		stats.DepthNodes = depthStats.NodesInitialized
		stats.DepthEdges = depthStats.EdgesVisited
	}

	edgeCount := countTransferEdges(domains, observedOwnership, desiredByDomain, stats)
	if err := in.Budget.ConsumeTransferEdges(edgeCount); err != nil {
		return PhasePlan{}, err
	}
	graph := buildTransferGraph(domains, observedOwnership, desiredByDomain, stats)
	controlledRels := make([]string, 0, len(in.DAG.index))
	for _, node := range in.DAG.Nodes() {
		controlledRels = append(controlledRels, node.Rel)
	}

	plan := PhasePlan{
		ConvergenceID:        canonicalConvergenceID(in),
		Base:                 in.Snapshot,
		FailClosedRoots:      normalizeRels(in.Snapshot.ScanBoundary.Roots),
		Kind:                 in.Kind,
		AllowEmptyTarget:     in.AllowEmptyTarget,
		Capabilities:         in.Capabilities,
		ControlledRels:       controlledRels,
		DormantRels:          cloneRelSet(in.DormantRels),
		Witnesses:            append([]ReleaseWitness(nil), in.Witnesses...),
		TransferGraph:        graph,
		CanonicalTargetByRel: canonicalPhaseTargets(in),
		TargetByRel:          make(map[string]CPUSetTarget, len(in.Snapshot.Entries)),
		AllowedEntering:      make(map[DomainID]machine.CPUSet, len(domains)),
		DrainBatch:           make(map[DomainID]machine.CPUSet, len(domains)),
	}
	switch in.Kind {
	case PhaseDrain:
		analysis, err := analyzeV1Deadlock(in)
		if err != nil {
			return PhasePlan{}, err
		}
		if structuralV1Deadlock(analysis) {
			return PhasePlan{}, &deadlock.StructuralV1NonEmptyDeadlock{Analysis: analysis}
		}
		if err := buildDrainTargets(&plan, in, domains, desiredByDomain, domainByRel, parentByRel, depthByRel, analysis.SafeSeed); err != nil {
			return PhasePlan{}, err
		}
	case PhaseExpand:
		if err := buildExpandTargets(&plan, in, domains, desiredByDomain, domainByRel, parentByRel, depthByRel, relOrder); err != nil {
			return PhasePlan{}, err
		}
	}
	for rel := range plan.TargetByRel {
		if relInDormantSubtree(rel, in.DormantRels) {
			delete(plan.TargetByRel, rel)
		}
	}
	postProcessPhaseOperationTargets(in.Kind, in.AllowEmptyTarget, in.Capabilities, plan.TargetByRel, in.Snapshot)
	if err := propagatePhaseTargetEnvelope(plan.TargetByRel, parentByRel, depthByRel, relOrder.relsDesc); err != nil {
		return PhasePlan{}, err
	}
	if !in.AllowEmptyTarget {
		if err := propagateControlledPhaseTargetEnvelope(plan.TargetByRel, in.DAG); err != nil {
			return PhasePlan{}, err
		}
		clampReclaimNUMABucketPhaseMems(plan.TargetByRel, in.DAG, in.DesiredMemsByRel)
	}
	if err := validatePhaseTargets(in, plan.TargetByRel); err != nil {
		return PhasePlan{}, err
	}
	operationCount, err := countPlanOperations(in.Kind, in.Capabilities, plan.TargetByRel, in.Snapshot, in.DAG, stats)
	if err != nil {
		return PhasePlan{}, err
	}
	if err := in.Budget.ConsumePlanOperations(operationCount); err != nil {
		return PhasePlan{}, err
	}
	operations := buildPlanOperations(
		in.Kind,
		in.AllowEmptyTarget,
		in.Capabilities,
		plan.TargetByRel,
		in.Snapshot,
		depthByRel,
		domainByRel,
		parentByRel,
		in.DAG,
		operationCount,
		stats,
		relOrder.childRelsByRel,
	)
	plan.Operations = operations
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	plan.CostUpperBound = BudgetUsage{Domains: len(domains), Edges: edgeCount, Operations: len(operations)}
	return plan, nil
}

func canonicalPhaseTargets(in PhasePlanInput) map[string]CPUSetTarget {
	targets := make(map[string]CPUSetTarget, len(in.DAG.index))
	for _, node := range in.DAG.Nodes() {
		cpus := node.CPUs.Clone()
		if desired, ok := in.DesiredByRel[node.Rel]; ok {
			cpus = desired.Clone()
		}
		mems := node.Mems
		if desiredMems := in.DesiredMemsByRel[node.Rel]; desiredMems != "" {
			mems = desiredMems
		} else if mems == "" {
			if current, ok := in.Snapshot.Entries[node.Rel]; ok {
				mems = current.Mems
			}
		}
		targets[node.Rel] = CPUSetTarget{CPUs: cpus, Mems: mems}
	}
	return targets
}

func canonicalConvergenceID(in PhasePlanInput) string {
	hash := sha256.New()
	writeHashString(hash, "bulkhead-cpuset-convergence-intent-v1")
	writeHashString(hash, "phase-envelope")
	writeHashString(hash, in.AllowedCPUs.String())
	writeHashUint64(hash, boolUint64(in.AllowEmptyTarget))
	writeHierarchyCapabilitiesHash(hash, in.Capabilities)

	writeCPUSetMap := func(values map[string]machine.CPUSet) {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		writeHashUint64(hash, uint64(len(keys)))
		for _, key := range keys {
			writeHashString(hash, key)
			writeHashString(hash, values[key].String())
		}
	}
	writeStringMap := func(values map[string]string) {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		writeHashUint64(hash, uint64(len(keys)))
		for _, key := range keys {
			writeHashString(hash, key)
			writeHashString(hash, values[key])
		}
	}
	writeHashString(hash, "desired-cpus")
	writeCPUSetMap(in.DesiredByRel)
	writeCPUSetMap(in.SemanticByRel)
	writeHashString(hash, "dynamic-cpus")
	writeCPUSetMap(in.DynamicByRel)
	writeHashString(hash, "desired-mems")
	writeStringMap(in.DesiredMemsByRel)
	writeHashString(hash, "protected-pending")
	writeHashString(hash, in.ProtectedPending.String())
	writeHashString(hash, "protected-by-rel")
	writeCPUSetMap(in.ProtectedByRel)
	writeHashString(hash, "selection-policy")
	writeHashUint64(hash, math.Float64bits(in.Selection.MaxCPUsDrainRatio))
	writeHashUint64(hash, boolUint64(in.Selection.GroupByNUMA))
	writeHashUint64(hash, boolUint64(in.Selection.RequirePairedSwapProgress))

	writeHashString(hash, "cpu-details")
	cpuIDs := make([]int, 0, len(in.CPUDetails))
	for cpu := range in.CPUDetails {
		cpuIDs = append(cpuIDs, cpu)
	}
	sort.Ints(cpuIDs)
	writeHashUint64(hash, uint64(len(cpuIDs)))
	for _, cpu := range cpuIDs {
		info := in.CPUDetails[cpu]
		writeHashUint64(hash, uint64(cpu))
		writeHashUint64(hash, uint64(info.NUMANodeID))
		writeHashUint64(hash, uint64(info.SocketID))
		writeHashUint64(hash, uint64(info.CoreID))
		writeHashUint64(hash, uint64(info.L3CacheID))
	}

	writeHashString(hash, "dag")
	if in.DAG != nil {
		nodes := in.DAG.Nodes()
		writeHashUint64(hash, uint64(len(nodes)))
		for _, node := range nodes {
			writeHashString(hash, node.Rel)
			writeHashString(hash, string(node.Role))
			writeHashString(hash, string(node.Domain))
			writeHashString(hash, node.CPUs.String())
			writeHashString(hash, node.Mems)
			if node.parent != nil {
				writeHashString(hash, node.parent.Rel)
			} else {
				writeHashString(hash, "")
			}
			writeHashUint64(hash, boolUint64(node.ControlledRoot))
			writeHashUint64(hash, boolUint64(node.TrustAnchor))
			writeHashString(hash, node.Constraint.CPUUpperBound.String())
			writeHashString(hash, node.Constraint.MemUpperBound.String())
			writeHashString(hash, string(node.Constraint.Scope))
			writeStringMap(node.Metadata)
		}
	} else {
		writeHashUint64(hash, 0)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func canonicalExecutionPlanID(plan PhasePlan) string {
	hash := sha256.New()
	writeHashString(hash, "bulkhead-cpuset-execution-plan-v1")
	writeHashString(hash, plan.ConvergenceID)
	writeHashString(hash, string(plan.Kind))
	writeHashUint64(hash, boolUint64(plan.AllowEmptyTarget))
	writeHierarchyCapabilitiesHash(hash, plan.Capabilities)
	if plan.Base != nil {
		_, _ = hash.Write(plan.Base.ID[:])
	} else {
		_, _ = hash.Write(make([]byte, len(SnapshotID{})))
	}
	failClosedRoots := normalizeRels(plan.FailClosedRoots)
	writeHashUint64(hash, uint64(len(failClosedRoots)))
	for _, rel := range failClosedRoots {
		writeHashString(hash, rel)
	}

	witnesses := append([]ReleaseWitness(nil), plan.Witnesses...)
	sort.Slice(witnesses, func(i, j int) bool {
		left, right := witnesses[i], witnesses[j]
		if left.ConvergenceID != right.ConvergenceID {
			return left.ConvergenceID < right.ConvergenceID
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Destination != right.Destination {
			return left.Destination < right.Destination
		}
		if left.CPUs.String() != right.CPUs.String() {
			return left.CPUs.String() < right.CPUs.String()
		}
		if left.SourceEvidenceID != right.SourceEvidenceID {
			return string(left.SourceEvidenceID[:]) < string(right.SourceEvidenceID[:])
		}
		return left.SourceBoundaryFingerprint < right.SourceBoundaryFingerprint
	})
	writeHashUint64(hash, uint64(len(witnesses)))
	for _, witness := range witnesses {
		writeHashString(hash, witness.ConvergenceID)
		writeHashString(hash, string(witness.Source))
		writeHashString(hash, string(witness.Destination))
		writeHashString(hash, witness.CPUs.String())
		_, _ = hash.Write(witness.SourceEvidenceID[:])
		writeHashString(hash, witness.SourceBoundaryFingerprint)
	}

	controlledRels := append([]string(nil), plan.ControlledRels...)
	sort.Strings(controlledRels)
	writeHashUint64(hash, uint64(len(controlledRels)))
	for _, rel := range controlledRels {
		writeHashString(hash, rel)
	}
	dormantRels := make([]string, 0, len(plan.DormantRels))
	for rel := range plan.DormantRels {
		dormantRels = append(dormantRels, rel)
	}
	sort.Strings(dormantRels)
	writeHashUint64(hash, uint64(len(dormantRels)))
	for _, rel := range dormantRels {
		writeHashString(hash, rel)
	}

	canonicalRels := make([]string, 0, len(plan.CanonicalTargetByRel))
	for rel := range plan.CanonicalTargetByRel {
		canonicalRels = append(canonicalRels, rel)
	}
	sort.Strings(canonicalRels)
	writeHashUint64(hash, uint64(len(canonicalRels)))
	for _, rel := range canonicalRels {
		target := plan.CanonicalTargetByRel[rel]
		writeHashString(hash, rel)
		writeHashString(hash, target.CPUs.String())
		writeHashString(hash, target.Mems)
	}

	writeHashUint64(hash, uint64(len(plan.Operations)))
	for _, operation := range plan.Operations {
		writeHashString(hash, operation.Rel)
		writeHashUint64(hash, operation.ExpectedIdentity.Device)
		writeHashUint64(hash, operation.ExpectedIdentity.Inode)
		writeHashString(hash, operation.ExpectedChildren)
		writeHashString(hash, operation.ExpectedChildUnion.String())
		writeHashString(hash, operation.ParentRel)
		writeHashUint64(hash, operation.ExpectedParentIdentity.Device)
		writeHashUint64(hash, operation.ExpectedParentIdentity.Inode)
		writeHashString(hash, operation.ExpectedCurrent.CPUs.String())
		writeHashString(hash, operation.ExpectedCurrent.Mems)
		writeHashString(hash, operation.Target.CPUs.String())
		writeHashString(hash, operation.Target.Mems)
		writeHashString(hash, string(operation.Direction))
		writeHashUint64(hash, boolUint64(operation.OwnsMems))
		writeHashUint64(hash, boolUint64(operation.WriteMems))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func normalizeRels(rels []string) []string {
	normalized := append([]string(nil), rels...)
	sort.Strings(normalized)
	out := normalized[:0]
	for _, rel := range normalized {
		if len(out) == 0 || out[len(out)-1] != rel {
			out = append(out, rel)
		}
	}
	return out
}

func writeHierarchyCapabilitiesHash(hash hash.Hash, capabilities HierarchyCapabilities) {
	writeHashUint64(hash, boolUint64(capabilities.StableIdentity))
	writeHashUint64(hash, boolUint64(capabilities.EmptyConfiguredCPUSet))
	writeHashUint64(hash, boolUint64(capabilities.EffectiveCPUSet))
	writeHashUint64(hash, boolUint64(capabilities.KernelParentContainment))
	writeHashUint64(hash, boolUint64(capabilities.PartitionRoots))
}

func boolUint64(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func collectPlannerDomains(
	dag *TopoDAG,
	observed map[DomainID]machine.CPUSet,
	desiredByRel map[string]machine.CPUSet,
	budget *BudgetTracker,
) ([]DomainID, map[DomainID]machine.CPUSet, error) {
	seen := make(map[DomainID]struct{})
	domains := make([]DomainID, 0)
	desired := make(map[DomainID]machine.CPUSet)
	add := func(domain DomainID) error {
		if domain == "" {
			return nil
		}
		if _, ok := seen[domain]; ok {
			return nil
		}
		if err := budget.ConsumeDomains(1); err != nil {
			return err
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
		return nil
	}
	for domain := range observed {
		if err := add(domain); err != nil {
			return nil, nil, err
		}
	}
	for _, node := range dag.Nodes() {
		if err := add(node.Domain); err != nil {
			return nil, nil, err
		}
		desired[node.Domain] = desired[node.Domain].Union(desiredByRel[node.Rel])
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i] < domains[j] })
	return domains, desired, nil
}

func walkTransferEdges(
	domains []DomainID,
	observed, desired map[DomainID]machine.CPUSet,
	visit func(source, destination DomainID, cpus machine.CPUSet),
) {
	claimedOutgoing := machine.NewCPUSet()
	for _, source := range domains {
		leaving := observed[source].Difference(desired[source]).Difference(claimedOutgoing)
		for _, destination := range domains {
			if source == destination {
				continue
			}
			entering := desired[destination].Difference(observed[destination])
			edge := leaving.Intersection(entering)
			if edge.IsEmpty() {
				continue
			}
			visit(source, destination, edge)
			leaving = leaving.Difference(edge)
			claimedOutgoing = claimedOutgoing.Union(edge)
		}
	}
}

func countTransferEdges(
	domains []DomainID,
	observed, desired map[DomainID]machine.CPUSet,
	stats *plannerBuildStats,
) int {
	count := 0
	walkTransferEdges(domains, observed, desired, func(_, _ DomainID, _ machine.CPUSet) {
		count++
		if stats != nil {
			stats.TransferEdgesCounted++
		}
	})
	return count
}

func buildTransferGraph(
	domains []DomainID,
	observed, desired map[DomainID]machine.CPUSet,
	stats *plannerBuildStats,
) map[DomainID]map[DomainID]machine.CPUSet {
	graph := make(map[DomainID]map[DomainID]machine.CPUSet)
	walkTransferEdges(domains, observed, desired, func(source, destination DomainID, edge machine.CPUSet) {
		if graph[source] == nil {
			graph[source] = make(map[DomainID]machine.CPUSet)
		}
		graph[source][destination] = edge
		if stats != nil {
			stats.TransferEdgesAllocated++
		}
	})
	return graph
}

func buildDrainTargets(
	plan *PhasePlan,
	in PhasePlanInput,
	domains []DomainID,
	desiredByDomain map[DomainID]machine.CPUSet,
	domainByRel map[string]DomainID,
	parentByRel map[string]string,
	depthByRel map[string]int,
	safeSeed *deadlock.DrainAtom,
) error {
	protectedByDomain := protectedCPUSetByDomain(in.ProtectedByRel, in.ProtectedPending, in.DAG)
	currentOwnershipByDomain := semanticOwnershipUnionByDomain(in.Snapshot, domainByRel)
	leavingByDomain := make(map[DomainID]machine.CPUSet, len(domains))
	eligibleByDomain := make(map[DomainID]machine.CPUSet, len(domains))
	for _, domain := range domains {
		leaving := currentOwnershipByDomain[domain].Difference(desiredByDomain[domain])
		leavingByDomain[domain] = leaving
		eligibleByDomain[domain] = leaving.Difference(protectedByDomain[domain])
		plan.DrainBatch[domain] = stableDrainBatch(
			eligibleByDomain[domain],
			in.CPUDetails,
			in.Selection,
		)
	}
	if in.Selection.RequirePairedSwapProgress {
		selectPairedCycleProgress(
			plan.TransferGraph,
			eligibleByDomain,
			plan.DrainBatch,
			in.CPUDetails,
			in.Selection,
		)
	}
	if safeSeed != nil {
		plan.DrainBatch[safeSeed.Source] = safeSeed.CPUs.Clone()
	}
	return buildBottomUpDrainTargets(plan, in, leavingByDomain, domainByRel, parentByRel, depthByRel)
}

func protectedCPUSetByDomain(
	protectedByRel map[string]machine.CPUSet,
	protectedPending machine.CPUSet,
	dag *TopoDAG,
) map[DomainID]machine.CPUSet {
	protectedByDomain := make(map[DomainID]machine.CPUSet, len(protectedByRel))
	protectedByDomain[DomainPrimary] = protectedPending.Clone()
	for rel, cpus := range protectedByRel {
		if cpus.IsEmpty() || dag == nil {
			continue
		}
		node := dag.index[rel]
		for node == nil && rel != "" {
			rel = filepath.Dir(rel)
			if rel == "." || rel == "/" {
				rel = ""
			}
			node = dag.index[rel]
		}
		if node == nil || node.Domain == "" {
			continue
		}
		protectedByDomain[node.Domain] = protectedByDomain[node.Domain].Union(cpus)
	}
	return protectedByDomain
}

func buildExpandTargets(
	plan *PhasePlan,
	in PhasePlanInput,
	domains []DomainID,
	desiredByDomain map[DomainID]machine.CPUSet,
	domainByRel map[string]DomainID,
	parentByRel map[string]string,
	depthByRel map[string]int,
	relOrder relDepthOrder,
) error {
	gate, err := NewDomainGate(plan.ConvergenceID, in.Snapshot, desiredByDomain, in.AllowedCPUs, in.Witnesses)
	if err != nil {
		return err
	}
	for _, domain := range domains {
		plan.AllowedEntering[domain] = gate.AllowedEntering(domain)
	}
	semanticByRel := semanticTargetsForPlan(in)
	for rel, entry := range in.Snapshot.Entries {
		if relInDormantSubtree(rel, in.DormantRels) {
			continue
		}
		node := in.DAG.index[rel]
		target := entry.CPUs.Clone()
		if node != nil {
			desired := in.DesiredByRel[rel]
			available := plan.AllowedEntering[node.Domain].Union(in.Snapshot.DomainUnion[node.Domain])
			// desired.Difference(semantic) is empty whenever desired is already a
			// subset of the semantic target; skip the Difference+Union allocations.
			if !desired.IsSubsetOf(semanticByRel[rel]) {
				available = available.Union(desired.Difference(semanticByRel[rel]))
			}
			target, err = buildPhaseTransition(PhaseExpand, RelTransition{
				Current:            entry.CPUs,
				Final:              desired,
				AuthorizedEntering: available,
				AllowEmptyTarget:   in.AllowEmptyTarget,
			})
			if err != nil {
				return err
			}
		}
		mems := entry.Mems
		if node != nil {
			if desiredMems := in.DesiredMemsByRel[rel]; desiredMems != "" {
				mems = desiredMems
			} else if node.Mems != "" {
				mems = node.Mems
			}
		}
		plan.TargetByRel[rel] = CPUSetTarget{CPUs: target, Mems: mems}
	}
	if err := closeExpandDynamicDescendantTargets(
		plan.TargetByRel,
		in.Snapshot,
		in.DAG,
		parentByRel,
		depthByRel,
		in.DynamicByRel,
		in.DesiredByRel,
		plan.AllowedEntering,
		domainByRel,
		in.AllowEmptyTarget,
	); err != nil {
		return err
	}
	closeExpandTargetsOverImmediateEdges(plan.TargetByRel, in.Snapshot, depthByRel, relOrder.relsDesc, relOrder.childRelsByRel)
	return nil
}

func closeExpandDynamicDescendantTargets(
	targets map[string]CPUSetTarget,
	snapshot *CompleteSnapshot,
	dag *TopoDAG,
	parentByRel map[string]string,
	depthByRel map[string]int,
	dynamicByRel map[string]machine.CPUSet,
	desiredByRel map[string]machine.CPUSet,
	allowedEntering map[DomainID]machine.CPUSet,
	domainByRel map[string]DomainID,
	allowEmptyTarget bool,
) error {
	rels := sortedSnapshotRels(snapshot, depthByRel)
	for _, rel := range rels {
		if dag.index[rel] != nil {
			continue
		}
		entry := snapshot.Entries[rel]
		final := finalCPUSetForRel(rel, dag, parentByRel, dynamicByRel, desiredByRel)
		domain := domainByRel[rel]
		available := allowedEntering[domain].Union(snapshot.DomainUnion[domain])
		target, err := buildPhaseTransition(PhaseExpand, RelTransition{
			Current:            entry.CPUs,
			Final:              final,
			AuthorizedEntering: available,
			AllowEmptyTarget:   allowEmptyTarget,
		})
		if err != nil {
			return err
		}
		targets[rel] = CPUSetTarget{CPUs: target, Mems: entry.Mems}
	}
	return nil
}

func buildBottomUpDrainTargets(
	plan *PhasePlan,
	in PhasePlanInput,
	leavingByDomain map[DomainID]machine.CPUSet,
	domainByRel map[string]DomainID,
	parentByRel map[string]string,
	depthByRel map[string]int,
) error {
	projection, err := projectDrainTargets(DrainProjectionInput{
		PlanInput: in, DrainBatch: plan.DrainBatch, LeavingByDomain: leavingByDomain,
		DomainByRel: domainByRel, ParentByRel: parentByRel, DepthByRel: depthByRel,
	})
	if err != nil {
		return err
	}
	if in.Budget != nil {
		if err := in.Budget.ConsumePlanOperations(projection.Cost.Total()); err != nil {
			return err
		}
	}
	plan.TargetByRel = projection.TargetByRel
	return nil
}

func finalCPUSetForRel(
	rel string,
	dag *TopoDAG,
	parentByRel map[string]string,
	dynamicByRel map[string]machine.CPUSet,
	desiredByRel map[string]machine.CPUSet,
) machine.CPUSet {
	if explicit, ok := dynamicByRel[rel]; ok {
		return explicit
	}
	for current := rel; current != ""; current = parentByRel[current] {
		if node := dag.index[current]; node != nil {
			return desiredByRel[node.Rel]
		}
	}
	return machine.NewCPUSet()
}

func sortedSnapshotRels(snapshot *CompleteSnapshot, depthByRel map[string]int) []string {
	// Rebuild the sorted order here; this is called from a secondary path
	// (dynamic descendant closure) that does not have access to relOrder.
	// The sort uses a local depth-keyed struct to avoid string map lookups.
	type rd struct {
		rel   string
		depth int
	}
	all := make([]rd, 0, len(snapshot.Entries))
	for rel := range snapshot.Entries {
		all = append(all, rd{rel: rel, depth: depthByRel[rel]})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].depth != all[j].depth {
			return all[i].depth < all[j].depth
		}
		return all[i].rel < all[j].rel
	})
	rels := make([]string, len(all))
	for i, rd := range all {
		rels[i] = rd.rel
	}
	return rels
}

func selectPairedCycleProgress(
	graph map[DomainID]map[DomainID]machine.CPUSet,
	eligible map[DomainID]machine.CPUSet,
	drainBatch map[DomainID]machine.CPUSet,
	details machine.CPUDetails,
	policy DrainSelectionPolicy,
) {
	// A ratio-limited topology selection can repeatedly choose an outgoing edge
	// that does not participate in the source's SCC. Select one executable cycle
	// per SCC instead: every selected transfer can produce its normal release
	// witness, so the existing gate authorizes the paired expands without any
	// special-case bypass. Each successful round removes at least one pending CPU
	// from every edge in the chosen cycle.
	for _, component := range transferStronglyConnectedComponents(graph) {
		if len(component) < 2 {
			continue
		}
		cycle := executableTransferCycle(component, graph, eligible)
		for _, domain := range component {
			drainBatch[domain] = machine.NewCPUSet()
		}
		for source, destination := range cycle {
			candidates := graph[source][destination].Intersection(eligible[source])
			drainBatch[source] = pairedProgressDrainBatch(
				candidates,
				eligible[source],
				details,
				policy,
			)
		}
	}
}

func pairedProgressDrainBatch(
	cycleCandidates machine.CPUSet,
	allCandidates machine.CPUSet,
	details machine.CPUDetails,
	policy DrainSelectionPolicy,
) machine.CPUSet {
	limit := maxCPUsPerDrainRound(len(details), policy.MaxCPUsDrainRatio)
	if limit == 0 || allCandidates.Size() <= limit {
		return allCandidates.Clone()
	}
	selected := stableDrainBatch(cycleCandidates, details, policy)
	remaining := limit - selected.Size()
	if remaining <= 0 {
		return selected
	}
	extras := allCandidates.Difference(selected)
	if extras.Size() <= remaining {
		return selected.Union(extras)
	}
	if len(details) == 0 {
		return selected.Union(machine.NewCPUSet(extras.ToSliceInt()[:remaining]...))
	}
	topology := cpuTopologyFromDetails(details)
	fill, err := selectDrainCPUsByTopology(topology, extras, remaining, policy.GroupByNUMA)
	if err != nil {
		fill = machine.NewCPUSet(extras.ToSliceInt()[:remaining]...)
	}
	return selected.Union(fill)
}

func transferStronglyConnectedComponents(
	graph map[DomainID]map[DomainID]machine.CPUSet,
) [][]DomainID {
	index := 0
	indices := make(map[DomainID]int)
	lowlink := make(map[DomainID]int)
	onStack := make(map[DomainID]bool)
	stack := make([]DomainID, 0)
	components := make([][]DomainID, 0)

	var visit func(DomainID)
	visit = func(domain DomainID) {
		indices[domain] = index
		lowlink[domain] = index
		index++
		stack = append(stack, domain)
		onStack[domain] = true

		destinations := make([]DomainID, 0, len(graph[domain]))
		for next := range graph[domain] {
			destinations = append(destinations, next)
		}
		sort.Slice(destinations, func(i, j int) bool { return destinations[i] < destinations[j] })
		for _, next := range destinations {
			if _, seen := indices[next]; !seen {
				visit(next)
				if lowlink[next] < lowlink[domain] {
					lowlink[domain] = lowlink[next]
				}
			} else if onStack[next] && indices[next] < lowlink[domain] {
				lowlink[domain] = indices[next]
			}
		}
		if lowlink[domain] != indices[domain] {
			return
		}

		component := make([]DomainID, 0)
		for {
			last := len(stack) - 1
			member := stack[last]
			stack = stack[:last]
			onStack[member] = false
			component = append(component, member)
			if member == domain {
				break
			}
		}
		sort.Slice(component, func(i, j int) bool { return component[i] < component[j] })
		components = append(components, component)
	}

	domains := make(map[DomainID]struct{})
	for source, destinations := range graph {
		domains[source] = struct{}{}
		for destination := range destinations {
			domains[destination] = struct{}{}
		}
	}
	ordered := make([]DomainID, 0, len(domains))
	for domain := range domains {
		ordered = append(ordered, domain)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, domain := range ordered {
		if _, seen := indices[domain]; !seen {
			visit(domain)
		}
	}
	return components
}

func executableTransferCycle(
	component []DomainID,
	graph map[DomainID]map[DomainID]machine.CPUSet,
	eligible map[DomainID]machine.CPUSet,
) map[DomainID]DomainID {
	members := make(map[DomainID]struct{}, len(component))
	for _, domain := range component {
		members[domain] = struct{}{}
	}
	color := make(map[DomainID]uint8, len(component))
	parent := make(map[DomainID]DomainID, len(component))
	var cycle map[DomainID]DomainID
	var visit func(DomainID) bool
	visit = func(source DomainID) bool {
		color[source] = 1
		destinations := make([]DomainID, 0, len(graph[source]))
		for destination, cpus := range graph[source] {
			if _, internal := members[destination]; internal &&
				!cpus.Intersection(eligible[source]).IsEmpty() {
				destinations = append(destinations, destination)
			}
		}
		sort.Slice(destinations, func(i, j int) bool { return destinations[i] < destinations[j] })
		for _, destination := range destinations {
			switch color[destination] {
			case 0:
				parent[destination] = source
				if visit(destination) {
					return true
				}
			case 1:
				cycle = map[DomainID]DomainID{source: destination}
				for node := source; node != destination; {
					previous := parent[node]
					cycle[previous] = node
					node = previous
				}
				return true
			}
		}
		color[source] = 2
		return false
	}
	for _, domain := range component {
		if color[domain] == 0 && visit(domain) {
			return cycle
		}
	}
	return nil
}

func closeExpandTargetsOverImmediateEdges(targets map[string]CPUSetTarget, snapshot *CompleteSnapshot, depthByRel map[string]int, sortedRelsDesc []string, childRelsByRel map[string][]string) {
	if snapshot == nil {
		return
	}
	for _, parent := range sortedRelsDesc {
		parentTarget, ok := targets[parent]
		if !ok {
			continue
		}
		for _, childRel := range childRelsByRel[parent] {
			childTarget, exists := targets[childRel]
			if !exists {
				continue
			}
			parentTarget.CPUs = parentTarget.CPUs.Union(childTarget.CPUs)
		}
		targets[parent] = parentTarget
	}
}

func desiredDomainUnions(dag *TopoDAG, desired map[string]machine.CPUSet) map[DomainID]machine.CPUSet {
	out := make(map[DomainID]machine.CPUSet)
	for _, node := range dag.Nodes() {
		out[node.Domain] = out[node.Domain].Union(desired[node.Rel])
	}
	return out
}

func sortedDomains(sets ...map[DomainID]machine.CPUSet) []DomainID {
	seen := make(map[DomainID]struct{})
	for _, set := range sets {
		for domain := range set {
			if domain != "" {
				seen[domain] = struct{}{}
			}
		}
	}
	out := make([]DomainID, 0, len(seen))
	for domain := range seen {
		out = append(out, domain)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func stableDrainBatch(
	candidates machine.CPUSet,
	details machine.CPUDetails,
	policy DrainSelectionPolicy,
) machine.CPUSet {
	if candidates.IsEmpty() {
		return candidates.Clone()
	}
	limit := maxCPUsPerDrainRound(len(details), policy.MaxCPUsDrainRatio)
	if limit == 0 || candidates.Size() <= limit {
		return candidates.Clone()
	}
	if limit > candidates.Size() {
		limit = candidates.Size()
	}
	if len(details) == 0 {
		return machine.NewCPUSet(candidates.ToSliceInt()[:limit]...)
	}
	topology := cpuTopologyFromDetails(details)
	selected, err := selectDrainCPUsByTopology(topology, candidates, limit, policy.GroupByNUMA)
	if err != nil {
		return machine.NewCPUSet(candidates.ToSliceInt()[:limit]...)
	}
	return selected
}

func cpuTopologyFromDetails(details machine.CPUDetails) *machine.CPUTopology {
	return &machine.CPUTopology{
		NumCPUs:      len(details),
		NumCores:     details.Cores().Size(),
		NumSockets:   details.Sockets().Size(),
		NumNUMANodes: details.NUMANodes().Size(),
		CPUDetails:   details,
	}
}

func selectDrainCPUsByTopology(
	topology *machine.CPUTopology,
	candidates machine.CPUSet,
	limit int,
	groupByNUMA bool,
) (machine.CPUSet, error) {
	if !groupByNUMA {
		return calculator.SelectCPUsByTopology(topology, candidates, limit, false)
	}
	// Keep the shared selector's socket/core/thread preference within each NUMA
	// group, while preserving the original first-NUMA-first grouping contract.
	const unknownNUMA = int(^uint(0) >> 1)
	byNUMA := make(map[int]machine.CPUSet)
	for _, cpu := range candidates.ToSliceInt() {
		numa := unknownNUMA
		if info, ok := topology.CPUDetails[cpu]; ok {
			numa = info.NUMANodeID
		}
		byNUMA[numa] = byNUMA[numa].Union(machine.NewCPUSet(cpu))
	}
	numas := make([]int, 0, len(byNUMA))
	for numa := range byNUMA {
		numas = append(numas, numa)
	}
	sort.Ints(numas)
	selected := machine.NewCPUSet()
	for _, numa := range numas {
		remaining := limit - selected.Size()
		if remaining == 0 {
			break
		}
		group := byNUMA[numa]
		take := remaining
		if group.Size() < take {
			take = group.Size()
		}
		fromGroup, err := calculator.SelectCPUsByTopology(topology, group, take, false)
		if err != nil {
			return machine.NewCPUSet(), err
		}
		selected = selected.Union(fromGroup)
	}
	return selected, nil
}

func maxCPUsPerDrainRound(total int, ratio float64) int {
	if ratio == 0 {
		return 0
	}
	limit := int(math.Floor(float64(total) * ratio))
	limit -= limit % 2
	if limit < 2 {
		return 2
	}
	return limit
}

func countPlanOperations(
	kind PhaseKind,
	capabilities HierarchyCapabilities,
	targets map[string]CPUSetTarget,
	snapshot *CompleteSnapshot,
	dag *TopoDAG,
	stats *plannerBuildStats,
) (int, error) {
	count := 0
	for rel, target := range targets {
		entry := snapshot.Entries[rel]
		currentCPUs := observedCPUsForTargetProof(entry, target.CPUs, capabilities)
		ownsMems := dag != nil && dag.index[rel] != nil
		if target.CPUs.Equals(currentCPUs) && (!ownsMems || target.Mems == entry.Mems) {
			continue
		}
		removes := !currentCPUs.IsSubsetOf(target.CPUs)
		if kind == PhaseExpand && removes {
			return 0, fmt.Errorf("%w: rel=%q observed=%s target=%s",
				ErrExpandPlanWouldShrink, rel, currentCPUs.String(), target.CPUs.String())
		}
		adds := !target.CPUs.IsSubsetOf(currentCPUs)
		current := CPUSetTarget{CPUs: currentCPUs, Mems: entry.Mems}
		if _, _, err := combinedWriteDirection(rel, current, target, ownsMems, removes && adds); err != nil {
			return 0, err
		}
		if removes && adds {
			count += 2
		} else {
			count++
		}
	}
	if stats != nil {
		stats.PlanOperationsCounted = count
	}
	return count, nil
}

func buildPlanOperations(
	kind PhaseKind,
	allowEmptyTarget bool,
	capabilities HierarchyCapabilities,
	targets map[string]CPUSetTarget,
	snapshot *CompleteSnapshot,
	depthByRel map[string]int,
	domainByRel map[string]DomainID,
	parentByRel map[string]string,
	dag *TopoDAG,
	operationCount int,
	stats *plannerBuildStats,
	childRelsByRel map[string][]string,
) []PlanOperation {
	type operationSortKey struct {
		rel       string
		depth     int
		domain    DomainID
		direction WriteDirection
		current   CPUSetTarget
		target    CPUSetTarget
		ownsMems  bool
	}
	keys := make([]operationSortKey, 0, operationCount)
	appendKey := func(rel string, direction WriteDirection, current, target CPUSetTarget, ownsMems bool) {
		keys = append(keys, operationSortKey{
			rel: rel, depth: depthByRel[rel], domain: domainByRel[rel],
			direction: direction, current: current, target: target, ownsMems: ownsMems,
		})
		if stats != nil {
			stats.SortKeys++
		}
	}
	for rel, target := range targets {
		entry := snapshot.Entries[rel]
		current := CPUSetTarget{
			CPUs: observedCPUsForTargetProof(entry, target.CPUs, capabilities),
			Mems: entry.Mems,
		}
		target = phaseOperationTarget(kind, allowEmptyTarget, current, target)
		ownsMems := dag != nil && dag.index[rel] != nil
		if !ownsMems {
			target.Mems = entry.Mems
		}
		if target.CPUs.Equals(current.CPUs) && target.Mems == entry.Mems {
			continue
		}
		removes := !current.CPUs.IsSubsetOf(target.CPUs)
		adds := !target.CPUs.IsSubsetOf(current.CPUs)
		direction, memsDirection, err := combinedWriteDirection(
			rel, current, target, ownsMems, removes && adds,
		)
		if err != nil {
			// countPlanOperations performs the same pre-construction check; if the caller violates this precondition,
			// return an empty plan instead of guessing the direction, preserving fail-closed behavior.
			return nil
		}
		if removes && adds {
			intermediate := CPUSetTarget{
				CPUs: entry.CPUs.Intersection(target.CPUs), Mems: entry.Mems,
			}
			// Split CPU replacement into shrink+grow; merge mems only into the step with the same direction,
			// avoiding opposing hierarchy write orders within one operation.
			if memsDirection == WriteShrink {
				intermediate.Mems = target.Mems
			}
			appendKey(rel, WriteShrink, current, intermediate, ownsMems)
			appendKey(rel, WriteGrow, intermediate, target, ownsMems)
		} else {
			appendKey(rel, direction, current, target, ownsMems)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].direction != keys[j].direction {
			return keys[i].direction == WriteShrink
		}
		if keys[i].depth != keys[j].depth {
			if keys[i].direction == WriteShrink {
				return keys[i].depth > keys[j].depth
			}
			return keys[i].depth < keys[j].depth
		}
		if keys[i].domain != keys[j].domain {
			return keys[i].domain > keys[j].domain
		}
		return keys[i].rel < keys[j].rel
	})
	operations := make([]PlanOperation, 0, len(keys))
	for _, key := range keys {
		rel := key.rel
		entry := snapshot.Entries[rel]
		parentRel := parentByRel[rel]
		parentIdentity := CgroupIdentity{}
		if parentRel != "" {
			parentIdentity = snapshot.Entries[parentRel].Identity
		}
		childRefs := snapshot.Children[key.rel]
		childUnion := machine.NewCPUSet()
		if childRelsByRel != nil {
			for _, childRel := range childRelsByRel[key.rel] {
				if entry, ok := snapshot.Entries[childRel]; ok {
					childUnion = childUnion.Union(entry.CPUs)
				}
			}
		} else {
			for _, child := range childRefs {
				childRel := filepath.Join(key.rel, child.Name)
				if entry, ok := snapshot.Entries[childRel]; ok {
					childUnion = childUnion.Union(entry.CPUs)
				}
			}
		}
		operations = append(operations, PlanOperation{
			Rel: rel, ExpectedIdentity: entry.Identity,
			ExpectedChildren: ChildrenFingerprint(childRefs), ExpectedChildUnion: childUnion,
			ParentRel: parentRel, ExpectedParentIdentity: parentIdentity,
			ExpectedCurrent: key.current, Target: key.target, Direction: key.direction,
			OwnsMems:  key.ownsMems,
			WriteMems: key.ownsMems && key.target.Mems != entry.Mems,
		})
	}
	return operations
}

func combinedWriteDirection(
	rel string,
	current, target CPUSetTarget,
	ownsMems bool,
	cpuReplacement bool,
) (WriteDirection, WriteDirection, error) {
	cpuDirection, _, _ := classifySetDirection(current.CPUs, target.CPUs)
	var memsDirection WriteDirection
	if ownsMems && current.Mems != target.Mems {
		currentMems, currentErr := machine.Parse(current.Mems)
		targetMems, targetErr := machine.Parse(target.Mems)
		if currentErr != nil || targetErr != nil {
			return "", "", fmt.Errorf("classify cpuset.mems direction for rel=%q: parse current/target mems: current=%v target=%v",
				rel, currentErr, targetErr)
		}
		var monotonic bool
		memsDirection, _, monotonic = classifySetDirection(currentMems, targetMems)
		if !monotonic {
			return "", "", fmt.Errorf("classify cpuset.mems direction for rel=%q: non-monotonic transition current=%q target=%q",
				rel, current.Mems, target.Mems)
		}
	}

	// CPU replacement splits into shrink+grow, with mems merged into the step sharing its direction;
	// a monotonic CPU change must match the mems direction to satisfy parent/child hierarchy write ordering.
	if cpuReplacement {
		return "", memsDirection, nil
	}
	switch {
	case cpuDirection == "":
		return memsDirection, memsDirection, nil
	case memsDirection == "":
		return cpuDirection, memsDirection, nil
	case cpuDirection == memsDirection:
		return cpuDirection, memsDirection, nil
	default:
		return "", "", &ConflictingWriteDirectionError{
			Rel: rel, CPUDirection: cpuDirection, MemsDirection: memsDirection,
			Current: current, Target: target,
		}
	}
}

func classifySetDirection(current, target machine.CPUSet) (WriteDirection, bool, bool) {
	switch {
	case current.Equals(target):
		return "", false, true
	case target.IsSubsetOf(current):
		return WriteShrink, true, true
	case current.IsSubsetOf(target):
		return WriteGrow, true, true
	default:
		return "", true, false
	}
}

func postProcessPhaseOperationTargets(
	kind PhaseKind,
	allowEmptyTarget bool,
	capabilities HierarchyCapabilities,
	targets map[string]CPUSetTarget,
	snapshot *CompleteSnapshot,
) {
	for rel, target := range targets {
		entry, ok := snapshot.Entries[rel]
		if !ok {
			continue
		}
		originalCPUs := target.CPUs.Clone()
		adjusted := phaseOperationTarget(
			kind,
			allowEmptyTarget,
			CPUSetTarget{
				CPUs: observedCPUsForTargetProof(entry, target.CPUs, capabilities),
				Mems: entry.Mems,
			},
			target,
		)
		if allowEmptyTarget && capabilities.EmptyConfiguredCPUSet &&
			entry.ConfiguredCPUs.IsEmpty() && originalCPUs.Equals(entry.CPUs) {
			adjusted.CPUs = machine.NewCPUSet()
		}
		targets[rel] = adjusted
	}
}

func propagatePhaseTargetEnvelope(
	targets map[string]CPUSetTarget,
	parentByRel map[string]string,
	depthByRel map[string]int,
	sortedRelsDesc []string,
) error {
	for _, rel := range sortedRelsDesc {
		parentRel := parentByRel[rel]
		if parentRel == "" {
			continue
		}
		parent, ok := targets[parentRel]
		if !ok {
			continue
		}
		child := targets[rel]
		parent.CPUs = parent.CPUs.Union(child.CPUs)
		mems, err := unionPhaseMemsEnvelope(parent.Mems, child.Mems)
		if err != nil {
			return fmt.Errorf("propagate phase mems envelope from %q toward controlled ancestor through %q: %w",
				rel, parentRel, err)
		}
		parent.Mems = mems
		targets[parentRel] = parent
	}
	return nil
}

func clampReclaimNUMABucketPhaseMems(
	targets map[string]CPUSetTarget,
	dag *TopoDAG,
	desiredMemsByRel map[string]string,
) {
	if dag == nil {
		return
	}
	for _, node := range dag.Nodes() {
		if node == nil || node.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		target, ok := targets[node.Rel]
		if !ok {
			continue
		}
		mems := desiredMemsByRel[node.Rel]
		if mems == "" {
			mems = node.Mems
		}
		if mems == "" || node.Constraint.MemUpperBound.IsEmpty() {
			continue
		}
		parsed, err := machine.Parse(mems)
		if err != nil || !parsed.IsSubsetOf(node.Constraint.MemUpperBound) {
			continue
		}
		target.Mems = mems
		targets[node.Rel] = target
	}
}

func propagateControlledPhaseTargetEnvelope(targets map[string]CPUSetTarget, dag *TopoDAG) error {
	if dag == nil {
		return nil
	}
	nodes := dag.Nodes()
	// Compute all depths in a single top-down DFS (O(N)) instead of walking
	// the parent chain per node (O(N²) on deep chains).
	depthOf := make(map[*TopoNode]int, len(nodes))
	maxDepth := 0
	var assignDepth func(node *TopoNode, d int)
	assignDepth = func(node *TopoNode, d int) {
		depthOf[node] = d
		if d > maxDepth {
			maxDepth = d
		}
		for _, child := range node.children {
			assignDepth(child, d+1)
		}
	}
	for _, root := range dag.topLevel {
		assignDepth(root, 0)
	}
	// Bucket nodes by depth; iterate deepest-first. Within each depth bucket,
	// sort by Rel to preserve deterministic tie-breaking.
	buckets := make([][]*TopoNode, maxDepth+1)
	for _, node := range nodes {
		d := depthOf[node]
		buckets[d] = append(buckets[d], node)
	}
	for d := maxDepth; d >= 0; d-- {
		bucket := buckets[d]
		if len(bucket) > 1 {
			sort.Slice(bucket, func(i, j int) bool { return bucket[i].Rel < bucket[j].Rel })
		}
		for _, node := range bucket {
			if node == nil || node.parent == nil {
				continue
			}
			parent, ok := targets[node.parent.Rel]
			if !ok {
				continue
			}
			child := targets[node.Rel]
			parent.CPUs = parent.CPUs.Union(child.CPUs)
			mems, err := unionPhaseMemsEnvelope(parent.Mems, child.Mems)
			if err != nil {
				return fmt.Errorf("propagate controlled phase mems envelope from %q toward %q: %w",
					node.Rel, node.parent.Rel, err)
			}
			parent.Mems = mems
			targets[node.parent.Rel] = parent
		}
	}
	return nil
}

func unionPhaseMemsEnvelope(parent, child string) (string, error) {
	if child == "" {
		return parent, nil
	}
	if parent == "" {
		return child, nil
	}
	// Identical mem strings union to themselves; skip the Parse+Union+String
	// round-trip (hot path: most controlled nodes share the same mem envelope).
	if parent == child {
		return parent, nil
	}
	parentSet, err := machine.Parse(parent)
	if err != nil {
		return "", fmt.Errorf("parse parent mems %q: %w", parent, err)
	}
	childSet, err := machine.Parse(child)
	if err != nil {
		return "", fmt.Errorf("parse child mems %q: %w", child, err)
	}
	return parentSet.Union(childSet).String(), nil
}

func phaseOperationTarget(
	kind PhaseKind,
	allowEmptyTarget bool,
	current CPUSetTarget,
	target CPUSetTarget,
) CPUSetTarget {
	target.Mems = phaseMemsTarget(kind, current.Mems, target.Mems)
	if kind == PhaseDrain {
		drainTarget := target.CPUs.Intersection(current.CPUs)
		if !allowEmptyTarget && drainTarget.IsEmpty() &&
			!current.CPUs.IsEmpty() {
			drainTarget = current.CPUs.Clone()
		}
		target.CPUs = drainTarget
	}
	return target
}

func phaseMemsTarget(kind PhaseKind, current, target string) string {
	if current == "" || target == "" || current == target {
		return target
	}
	currentSet, currentErr := machine.Parse(current)
	targetSet, targetErr := machine.Parse(target)
	if currentErr != nil || targetErr != nil {
		return target
	}
	// Keep each phase monotonic per resource: drain performs only mems shrink,
	// while expand performs only mems growth. The other direction is deferred.
	switch kind {
	case PhaseDrain:
		if targetSet.IsSubsetOf(currentSet) {
			return target
		}
		return current
	case PhaseExpand:
		if currentSet.IsSubsetOf(targetSet) {
			return target
		}
		return current
	default:
		return target
	}
}

func validateExecutableEmptyTargets(in PhasePlanInput) error {
	if in.AllowEmptyTarget || in.Snapshot == nil {
		return nil
	}
	for rel, current := range in.Snapshot.Entries {
		if current.CPUs.IsEmpty() {
			continue
		}
		if target, explicit := in.DynamicByRel[rel]; explicit && target.IsEmpty() &&
			len(in.Snapshot.Children[rel]) == 0 {
			return &UnsupportedEmptyTargetError{
				Rel: rel, Source: EmptyTargetSourceExplicitDynamic, Current: current.CPUs.Clone(),
			}
		}
		if in.DAG != nil && in.DAG.index[rel] != nil && in.DesiredByRel[rel].IsEmpty() {
			return &UnsupportedEmptyTargetError{
				Rel: rel, Source: EmptyTargetSourceControlled, Current: current.CPUs.Clone(),
			}
		}
	}
	return nil
}

func buildPlannerRelations(
	snapshot *CompleteSnapshot,
	dag *TopoDAG,
	depthByRel map[string]int,
	sortedRelsAsc []string,
	childRelsByRel map[string][]string,
	stats *plannerBuildStats,
) (map[string]DomainID, map[string]string) {
	domainByRel := make(map[string]DomainID, len(snapshot.Entries))
	parentByRel := make(map[string]string, len(snapshot.Entries))
	for parent, childRels := range childRelsByRel {
		for _, childRel := range childRels {
			parentByRel[childRel] = parent
		}
	}
	for _, rel := range sortedRelsAsc {
		domain := snapshot.DomainByRel[rel]
		if domain == "" {
			if node := dag.index[rel]; node != nil {
				domain = node.Domain
			} else {
				domain = domainByRel[parentByRel[rel]]
			}
		}
		domainByRel[rel] = domain
		if stats != nil {
			stats.DomainEntries++
		}
	}
	return domainByRel, parentByRel
}

type depthBuildStats struct {
	NodesInitialized int
	EdgesVisited     int
}

// relDepthOrder pairs a rel with its precomputed depth to avoid map lookups
// during sort comparisons (O(string-key-hash) per comparison).
type relDepthOrder struct {
	relsAsc        []string            // sorted by (depth ASC, rel ASC)
	relsDesc       []string            // sorted by (depth DESC, rel ASC)
	childRelsByRel map[string][]string // precomputed child rel paths per parent
}

// buildSnapshotDepthByRel computes all snapshot depths once in O(N+E) and
// returns both the depth map and pre-sorted rel orderings so callers do not
// repeat sort.Slice with string-key map-lookup comparators.
func buildSnapshotDepthByRel(snapshot *CompleteSnapshot, stats *depthBuildStats) (map[string]int, relDepthOrder) {
	if snapshot == nil {
		return nil, relDepthOrder{}
	}
	depthByRel := make(map[string]int, len(snapshot.Entries))
	indegree := make(map[string]int, len(snapshot.Entries))
	childrenByRel := make(map[string][]string, len(snapshot.Children))
	for rel := range snapshot.Entries {
		depthByRel[rel] = 0
		if stats != nil {
			stats.NodesInitialized++
		}
	}
	for parent, children := range snapshot.Children {
		if _, ok := snapshot.Entries[parent]; !ok {
			continue
		}
		for _, child := range children {
			childRel := filepath.Join(parent, child.Name)
			if _, ok := snapshot.Entries[childRel]; !ok {
				continue
			}
			childrenByRel[parent] = append(childrenByRel[parent], childRel)
			indegree[childRel]++
			if stats != nil {
				stats.EdgesVisited++
			}
		}
	}
	queue := make([]string, 0, len(snapshot.Entries))
	for rel := range snapshot.Entries {
		if indegree[rel] == 0 {
			queue = append(queue, rel)
		}
	}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range childrenByRel[parent] {
			if next := depthByRel[parent] + 1; next > depthByRel[child] {
				depthByRel[child] = next
			}
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	// Build sorted rel orderings once to avoid repeated sort.Slice calls
	// with string-key map-lookup comparators in downstream consumers.
	type rd struct {
		rel   string
		depth int
	}
	all := make([]rd, 0, len(depthByRel))
	for rel, d := range depthByRel {
		all = append(all, rd{rel: rel, depth: d})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].depth != all[j].depth {
			return all[i].depth < all[j].depth
		}
		return all[i].rel < all[j].rel
	})
	order := relDepthOrder{
		relsAsc:        make([]string, len(all)),
		relsDesc:       make([]string, len(all)),
		childRelsByRel: childrenByRel,
	}
	for i, rd := range all {
		order.relsAsc[i] = rd.rel
		order.relsDesc[len(all)-1-i] = rd.rel
	}
	return depthByRel, order
}

func validateTopologyConstraints(in PhasePlanInput) error {
	semanticByRel := semanticTargetsForPlan(in)
	for _, node := range in.DAG.Nodes() {
		constraint := node.Constraint
		desired := semanticByRel[node.Rel]
		if parent := node.parent; parent != nil {
			parentCPUs := semanticByRel[parent.Rel]
			if !desired.IsSubsetOf(parentCPUs) {
				return fmt.Errorf("%w: controlled child=%q CPUs=%s outside parent %q=%s",
					ErrInvalidReclaimBucketTarget, node.Rel, desired.String(), parent.Rel, parentCPUs.String())
			}
			childMems := desiredMemsForNode(in, node)
			parentMems := desiredMemsForNode(in, parent)
			if err := validateMemsSubset(childMems, parentMems); err != nil {
				return fmt.Errorf("%w: controlled child=%q mems=%q outside parent %q=%q",
					ErrInvalidReclaimBucketTarget, node.Rel, childMems, parent.Rel, parentMems)
			}
		}
		if !constraint.CPUUpperBound.IsEmpty() && !desired.IsSubsetOf(constraint.CPUUpperBound) {
			return fmt.Errorf("%w: rel=%q desired CPUs=%s upper=%s", ErrInvalidReclaimBucketTarget, node.Rel, desired.String(), constraint.CPUUpperBound.String())
		}
		if constraint.MemUpperBound.IsEmpty() {
			if node.Role == TopoNodeRoleReclaimNUMABucket {
				if err := validateBucketHierarchyEnvelope(in, semanticByRel, node, desired, in.DesiredMemsByRel[node.Rel]); err != nil {
					return err
				}
			}
			continue
		}
		mems := in.DesiredMemsByRel[node.Rel]
		if mems == "" {
			mems = node.Mems
		}
		parsed, err := machine.Parse(mems)
		if err != nil || !parsed.IsSubsetOf(constraint.MemUpperBound) {
			return fmt.Errorf("%w: rel=%q desired mems=%q upper=%s", ErrInvalidReclaimBucketTarget, node.Rel, mems, constraint.MemUpperBound.String())
		}
		if node.Role == TopoNodeRoleReclaimNUMABucket {
			if err := validateBucketHierarchyEnvelope(in, semanticByRel, node, desired, mems); err != nil {
				return err
			}
		}
	}
	return nil
}

func desiredMemsForNode(in PhasePlanInput, node *TopoNode) string {
	if node == nil {
		return ""
	}
	if mems := in.DesiredMemsByRel[node.Rel]; mems != "" {
		return mems
	}
	return node.Mems
}

func validateMemsSubset(child, parent string) error {
	if child == "" {
		return nil
	}
	if parent == "" {
		return fmt.Errorf("non-empty child mems with empty parent")
	}
	// Identical mem strings are trivially a subset; skip the double Parse.
	if child == parent {
		return nil
	}
	childSet, childErr := machine.Parse(child)
	parentSet, parentErr := machine.Parse(parent)
	if childErr != nil {
		return childErr
	}
	if parentErr != nil {
		return parentErr
	}
	if !childSet.IsSubsetOf(parentSet) {
		return fmt.Errorf("child mems not subset of parent")
	}
	return nil
}

func validateBucketHierarchyEnvelope(
	in PhasePlanInput,
	semanticByRel map[string]machine.CPUSet,
	node *TopoNode,
	cpus machine.CPUSet,
	mems string,
) error {
	for parent := node.parent; parent != nil; parent = parent.parent {
		parentCPUs := semanticByRel[parent.Rel]
		if !cpus.IsSubsetOf(parentCPUs) {
			return fmt.Errorf("%w: bucket=%q CPUs=%s outside parent/domain envelope %q=%s",
				ErrInvalidReclaimBucketTarget, node.Rel, cpus.String(), parent.Rel, parentCPUs.String())
		}
		parentMems := in.DesiredMemsByRel[parent.Rel]
		if parentMems == "" {
			parentMems = parent.Mems
		}
		if mems != "" && parentMems != "" {
			childSet, childErr := machine.Parse(mems)
			parentSet, parentErr := machine.Parse(parentMems)
			if childErr != nil || parentErr != nil || !childSet.IsSubsetOf(parentSet) {
				return fmt.Errorf("%w: bucket=%q mems=%q outside parent/domain envelope %q=%q",
					ErrInvalidReclaimBucketTarget, node.Rel, mems, parent.Rel, parentMems)
			}
		}
	}
	return nil
}

func validatePhaseTargets(in PhasePlanInput, targets map[string]CPUSetTarget) error {
	semanticByRel := semanticTargetsForPlan(in)
	seenReclaimBucketCPUsByRoot := make(map[string]machine.CPUSet)
	for rel, target := range targets {
		node := in.DAG.index[rel]
		if node == nil {
			continue
		}
		parent := ""
		if node.parent != nil {
			parent = node.parent.Rel
		}
		if parent != "" && !target.CPUs.IsSubsetOf(targets[parent].CPUs) {
			return fmt.Errorf("%w: controlled child=%q phase CPUs=%s outside parent target %q=%s",
				ErrInvalidReclaimBucketTarget, rel, target.CPUs.String(), parent, targets[parent].CPUs.String())
		}
		if parent != "" {
			if err := validateMemsSubset(target.Mems, targets[parent].Mems); err != nil {
				return fmt.Errorf("%w: controlled child=%q phase mems=%q outside parent target %q=%q",
					ErrInvalidReclaimBucketTarget, rel, target.Mems, parent, targets[parent].Mems)
			}
		}
		if node.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		desired := semanticByRel[rel]
		if !node.Constraint.CPUUpperBound.IsEmpty() && !desired.IsSubsetOf(node.Constraint.CPUUpperBound) {
			return fmt.Errorf("%w: bucket=%q desired CPUs=%s upper=%s",
				ErrInvalidReclaimBucketTarget, rel, desired.String(), node.Constraint.CPUUpperBound.String())
		}
		current := in.Snapshot.Entries[rel].CPUs
		entering := target.CPUs.Difference(current)
		if !node.Constraint.CPUUpperBound.IsEmpty() && !entering.IsSubsetOf(node.Constraint.CPUUpperBound) {
			return fmt.Errorf("%w: bucket=%q entering CPUs=%s upper=%s",
				ErrInvalidReclaimBucketTarget, rel, entering.String(), node.Constraint.CPUUpperBound.String())
		}
		root := reclaimValidationGroup(node)
		overlap := seenReclaimBucketCPUsByRoot[root].Intersection(desired)
		if !overlap.IsEmpty() {
			return fmt.Errorf("%w: reclaim NUMA bucket=%q overlaps sibling final CPUs=%s",
				ErrInvalidReclaimBucketTarget, rel, overlap.String())
		}
		seenReclaimBucketCPUsByRoot[root] = seenReclaimBucketCPUsByRoot[root].Union(desired)
		if !node.Constraint.MemUpperBound.IsEmpty() {
			mems, err := machine.Parse(target.Mems)
			if err != nil || !mems.IsSubsetOf(node.Constraint.MemUpperBound) {
				return fmt.Errorf("%w: bucket=%q phase mems=%q upper=%s",
					ErrInvalidReclaimBucketTarget, rel, target.Mems, node.Constraint.MemUpperBound.String())
			}
		}
	}
	return nil
}
