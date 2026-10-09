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

package dynamicpolicy

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type hardReclaimPartitionDonor struct {
	key             string
	groupKey        string
	cpus            machine.CPUSet
	requestQuantity float64

	// target-driven reclaim source provenance (runtime-config gated). When
	// targetDriven is true the donor's retained floor is the frozen advisor
	// sourceTarget (not ceil(requestQuantity)), and the CPUs it may donate for
	// reclaim are capped by reclaimQuota. requestQuantity is retained for
	// diagnostics only. When targetDriven is false (legacy) the legacy
	// requestGroup floor path is unchanged.
	targetDriven bool
	sourceTarget int
	reclaimQuota int
}

type hardReclaimPartitionInput struct {
	topology        *machine.CPUTopology
	targetByNUMA    map[int]int
	currentReclaim  machine.CPUSet
	free            machine.CPUSet
	reclaimEligible machine.CPUSet
	donors          []hardReclaimPartitionDonor
	// targetDrivenReclaimNUMAs marks which NUMA reclaim targets are target-driven.
	// A target-driven reclaim target draws only on targetDriven donors; a legacy
	// reclaim target draws on all donors regardless of NUMA.
	targetDrivenReclaimNUMAs map[int]struct{}
}

type hardReclaimPartitionPlan struct {
	reclaim   machine.CPUSet
	donorCPUs map[string]machine.CPUSet
}

const (
	hardReclaimCoreSelectionFrontierWidth = 64
	hardReclaimCoreSelectionMaxStates     = 4096

	hardReclaimReplacementMaxCandidateStates = 100_000
	hardReclaimReplacementMaxTerminalSolves  = 4096
)

type hardReclaimSelectionFailureReason string

const (
	hardReclaimFailureInsufficientWholeCore hardReclaimSelectionFailureReason = "insufficient_whole_core"
	hardReclaimFailureDonorFloor            hardReclaimSelectionFailureReason = "donor_floor"
	hardReclaimFailureSearchBudget          hardReclaimSelectionFailureReason = "search_budget"
)

type hardReclaimSelectionError struct {
	reason  hardReclaimSelectionFailureReason
	numaID  int
	deficit int
	cause   error
}

func (e *hardReclaimSelectionError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return fmt.Sprintf("%s: NUMA %d deficit %d", e.reason, e.numaID, e.deficit)
}

func (e *hardReclaimSelectionError) Unwrap() error {
	return e.cause
}

type hardReclaimReplacementOptions struct {
	maxCandidateStates          int
	maxTerminalSolves           int
	maxPartitionAssignmentEdges int
	maxPartitionFlowOperations  int
	residualCacheLimits         partitionResidualCacheLimits
}

var hardReclaimReplacementOptionsOverrides = struct {
	mu         sync.RWMutex
	byTopology map[*machine.CPUTopology]hardReclaimReplacementOptions
}{
	byTopology: make(map[*machine.CPUTopology]hardReclaimReplacementOptions),
}

type hardReclaimReplacementProof struct {
	reclaimBefore machine.CPUSet
	reclaimAfter  machine.CPUSet

	dedicatedBeforeByGroup map[string]machine.CPUSet
	dedicatedAfterByGroup  map[string]machine.CPUSet
	partialBeforeCores     int

	// target-driven reclaim contract. targetDrivenByGroupNUMA records, per
	// [group][NUMA], whether that group's footprint on that NUMA is target-driven
	// (frozen target floor) or legacy (before==after). A group may be target-driven
	// on one NUMA and legacy on another; the flags are per-NUMA so a legacy NUMA in a
	// mixed group is never freed by a group-level OR.
	dedicatedTargetDrivenByGroupNUMA map[string]map[int]bool
	// per-[group][numa] frozen target for exact NUMA-scope proof.
	dedicatedTargetByGroupNUMA map[string]map[int]int
}

func defaultHardReclaimReplacementOptions() hardReclaimReplacementOptions {
	return hardReclaimReplacementOptions{
		maxCandidateStates:          hardReclaimReplacementMaxCandidateStates,
		maxTerminalSolves:           hardReclaimReplacementMaxTerminalSolves,
		maxPartitionAssignmentEdges: partitionAssignmentEdgeBudget,
		maxPartitionFlowOperations:  partitionFlowOperationBudget,
		residualCacheLimits: partitionResidualCacheLimits{
			maxPreparationGraphs: partitionResidualMaxPreparationGraphs,
			maxCanonicalWork:     partitionResidualMaxCanonicalWork,
			maxRetainedBytes:     partitionResidualMaxRetainedBytes,
			maxEntries:           partitionResidualMaxEntries,
			maxResultUnits:       partitionResidualMaxResultUnits,
		},
	}
}

func hardReclaimReplacementOptionsForTopology(
	topology *machine.CPUTopology,
) hardReclaimReplacementOptions {
	hardReclaimReplacementOptionsOverrides.mu.RLock()
	options, ok := hardReclaimReplacementOptionsOverrides.byTopology[topology]
	hardReclaimReplacementOptionsOverrides.mu.RUnlock()
	if ok {
		return options
	}
	return defaultHardReclaimReplacementOptions()
}

type hardReclaimCoreSelectionState struct {
	selected       machine.CPUSet
	selectedByNUMA []int
	donations      []int
	retained       int
	donated        int
}

type hardReclaimCoreSelectionCandidate struct {
	coreAlignedCandidate
	numaIndex int
}

type hardReclaimReplacementTerminal struct {
	cpus               machine.CPUSet
	partialBeforeCores int
	retained           int
}

type hardReclaimReplacementResult struct {
	proof *hardReclaimReplacementProof
}

type hardReclaimReplacementGlobalState struct {
	reclaimAfter    machine.CPUSet
	reclaimRetained int
}

func planHardReclaimPartition(in hardReclaimPartitionInput) (*hardReclaimPartitionPlan, error) {
	if in.topology == nil {
		return nil, fmt.Errorf("hard reclaim partition topology is nil")
	}

	plan := &hardReclaimPartitionPlan{
		reclaim:   machine.NewCPUSet(),
		donorCPUs: make(map[string]machine.CPUSet, len(in.donors)),
	}
	donorByKey := make(map[string]hardReclaimPartitionDonor, len(in.donors))
	groupMinimum := make(map[string]int)
	groupCPUs := make(map[string]machine.CPUSet)
	groupByCPU := make(map[int]string)
	// groupReclaimQuota accumulates the frozen reclaim quota declared by targetDriven
	// donors in each group. It caps how many CPUs the group may donate for reclaim;
	// legacy groups leave this zero and are bounded only by the floor.
	groupReclaimQuota := make(map[string]int)
	groupTargetDriven := make(map[string]bool)
	// targetDrivenDonorCPUs collects only targetDriven donor CPUs. A targetDriven
	// reclaim target must never draw on legacy donor capacity: legacy capacity is
	// never donated to satisfy a targetDriven reclaim target.
	targetDrivenDonorCPUs := machine.NewCPUSet()
	allDonorCPUs := machine.NewCPUSet()
	for _, donor := range in.donors {
		if donor.key == "" {
			return nil, fmt.Errorf("hard reclaim partition donor has empty key")
		}
		if _, found := donorByKey[donor.key]; found {
			return nil, fmt.Errorf("hard reclaim partition has duplicate donor %q", donor.key)
		}
		if math.IsNaN(donor.requestQuantity) || math.IsInf(donor.requestQuantity, 0) || donor.requestQuantity < 0 {
			return nil, fmt.Errorf("hard reclaim partition donor %q has invalid request quantity %v",
				donor.key, donor.requestQuantity)
		}
		groupKey := donor.groupKey
		if groupKey == "" {
			groupKey = donor.key
		}
		donorByKey[donor.key] = donor
		if donor.targetDriven {
			// The frozen advisor source target is the retained floor; requestQuantity
			// is diagnostic only. The group floor is the SUM of every source's frozen
			// target (per-source ownership is independent), matching the replacement
			// proof which sums the same targets.
			groupMinimum[groupKey] += donor.sourceTarget
			groupReclaimQuota[groupKey] += donor.reclaimQuota
			groupTargetDriven[groupKey] = true
			// Per-source quota: a targetDriven source whose frozen reclaim quota is
			// zero has nothing to lend and must never be a donation candidate, even
			// when another source in the same group/NUMA has spare quota.
			if donor.reclaimQuota > 0 {
				targetDrivenDonorCPUs = targetDrivenDonorCPUs.Union(donor.cpus)
			}
		} else {
			groupMinimum[groupKey] = general.Max(groupMinimum[groupKey], int(math.Ceil(donor.requestQuantity)))
		}
		plan.donorCPUs[donor.key] = donor.cpus.Clone()
		for _, cpu := range donor.cpus.ToSliceInt() {
			if existing, found := groupByCPU[cpu]; found && existing != groupKey {
				return nil, fmt.Errorf(
					"hard reclaim partition has overlapping donor ownership on CPU %d: %q and %q",
					cpu, existing, groupKey)
			}
			groupByCPU[cpu] = groupKey
		}
		groupCPUs[groupKey] = groupCPUs[groupKey].Union(donor.cpus)
		allDonorCPUs = allDonorCPUs.Union(donor.cpus)
	}
	groupDonationLimit := make(map[string]int, len(groupCPUs))
	for groupKey, cpus := range groupCPUs {
		groupDonationLimit[groupKey] = cpus.Size() - groupMinimum[groupKey]
		if groupTargetDriven[groupKey] && groupReclaimQuota[groupKey] < groupDonationLimit[groupKey] {
			// The frozen reclaim quota is the binding cap: a targetDriven source may
			// only lend what the advisor froze, never the whole excess. In a purely
			// targetDriven group this comparison is mathematically never the binding
			// constraint, because groupMinimum already equals the frozen sourceTarget
			// (so the excess equals the reclaim quota by construction); it only truly
			// bites when the same group mixes in a legacy donor whose ceil(requestQuantity)
			// floor would otherwise inflate groupMinimum. The actual per-reclaim-target
			// isolation of targetDriven donors from legacy ones is enforced upstream by
			// the targetDrivenDonorCPUs candidate pool filter (G2), not by this cap.
			groupDonationLimit[groupKey] = groupReclaimQuota[groupKey]
		}
		if groupDonationLimit[groupKey] < 0 {
			groupDonationLimit[groupKey] = 0
		}
	}

	numaIDs := make([]int, 0, len(in.targetByNUMA))
	for numaID := range in.targetByNUMA {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)

	candidates := make([]hardReclaimCoreSelectionCandidate, 0)
	targets := make([]int, len(numaIDs))
	for numaIndex, numaID := range numaIDs {
		target := in.targetByNUMA[numaID]
		targets[numaIndex] = target
		if target < 0 {
			return nil, fmt.Errorf("NUMA %d has negative hard reclaim target %d", numaID, target)
		}
		// G1: a frozen reclaim target must be whole-core representable on this
		// topology; an odd target is a typed whole_core_infeasible and is never
		// silently rounded to a neighboring pairing.
		cpusPerCore := in.topology.CPUsPerCore()
		if cpusPerCore <= 0 {
			cpusPerCore = 1
		}
		if err := ensureReclaimTargetWholeCoreRepresentable(target, cpusPerCore); err != nil {
			return nil, err
		}
		numaCPUs := in.topology.CPUDetails.CPUsInNUMANodes(numaID)
		if numaCPUs.IsEmpty() {
			return nil, fmt.Errorf("hard reclaim target references unknown NUMA %d", numaID)
		}
		eligible := in.reclaimEligible.Intersection(numaCPUs)
		if eligible.Size() < target {
			return nil, fmt.Errorf("NUMA %d reclaim eligibility %d is smaller than target %d",
				numaID, eligible.Size(), target)
		}

		donorPool := allDonorCPUs
		if _, isTargetDriven := in.targetDrivenReclaimNUMAs[numaID]; isTargetDriven {
			// G2: a targetDriven reclaim target must never draw on legacy donor
			// capacity. Restrict the donor candidate pool to targetDriven donors only.
			// The switch is keyed on the reclaim target's own target-driven-ness, not
			// on the NUMA, so a legacy reclaim target on a targetDriven NUMA is
			// unaffected.
			donorPool = targetDrivenDonorCPUs
		}
		source := in.currentReclaim.Union(in.free).Union(donorPool).Intersection(eligible)
		for _, candidate := range coreAlignedCandidates(in.topology, source, in.currentReclaim) {
			candidates = append(candidates, hardReclaimCoreSelectionCandidate{
				coreAlignedCandidate: candidate,
				numaIndex:            numaIndex,
			})
		}
	}
	selected, err := selectHardReclaimCoresByNUMAWithFrontier(
		candidates, numaIDs, targets, in.currentReclaim, groupCPUs, groupDonationLimit)
	if err != nil {
		// Structured diagnostic: classify the failure into a stable outcome token.
		// No high-cardinality cpuset/source ids are logged here.
		general.InfoS("hard reclaim solve finished",
			"outcome", classifyHardReclaimSolveOutcome(err),
			"numaCount", len(numaIDs))
		return nil, err
	}
	general.InfoS("hard reclaim solve finished",
		"outcome", hardReclaimOutcomeSelected,
		"numaCount", len(numaIDs), "reclaimCPUs", selected.Size())
	plan.reclaim = selected

	if err := assertCoreAligned(plan.reclaim, in.topology); err != nil {
		return nil, fmt.Errorf("hard reclaim partition plan violated core alignment: %w", err)
	}
	for key, donor := range donorByKey {
		plan.donorCPUs[key] = donor.cpus.Difference(plan.reclaim)
	}
	return plan, nil
}

func selectHardReclaimCoresWithFrontier(
	candidates []coreAlignedCandidate,
	target int,
	currentReclaim machine.CPUSet,
	groupCPUs map[string]machine.CPUSet,
	groupDonationLimit map[string]int,
) (machine.CPUSet, error) {
	candidatesByNUMA := make([]hardReclaimCoreSelectionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidatesByNUMA = append(candidatesByNUMA, hardReclaimCoreSelectionCandidate{
			coreAlignedCandidate: candidate,
		})
	}
	return selectHardReclaimCoresByNUMAWithFrontier(
		candidatesByNUMA, []int{0}, []int{target}, currentReclaim, groupCPUs, groupDonationLimit)
}

func selectHardReclaimCoresByNUMAWithFrontier(
	candidates []hardReclaimCoreSelectionCandidate,
	numaIDs []int,
	targets []int,
	currentReclaim machine.CPUSet,
	groupCPUs map[string]machine.CPUSet,
	groupDonationLimit map[string]int,
) (machine.CPUSet, error) {
	groupKeys := make([]string, 0, len(groupCPUs))
	for groupKey := range groupCPUs {
		groupKeys = append(groupKeys, groupKey)
	}
	sort.Strings(groupKeys)

	states := []hardReclaimCoreSelectionState{{
		selected:       machine.NewCPUSet(),
		selectedByNUMA: make([]int, len(targets)),
		donations:      make([]int, len(groupKeys)),
	}}
	frontierTruncated := false
	for _, candidate := range candidates {
		nextByKey := make(map[string]hardReclaimCoreSelectionState, len(states)*2)
		for _, state := range states {
			addHardReclaimCoreSelectionState(nextByKey, state)
			if state.selectedByNUMA[candidate.numaIndex]+candidate.cpus.Size() >
				targets[candidate.numaIndex] {
				continue
			}
			next := hardReclaimCoreSelectionState{
				selected:       state.selected.Union(candidate.cpus),
				selectedByNUMA: append([]int(nil), state.selectedByNUMA...),
				donations:      append([]int(nil), state.donations...),
				retained:       state.retained + candidate.cpus.Intersection(currentReclaim).Size(),
				donated:        state.donated,
			}
			next.selectedByNUMA[candidate.numaIndex] += candidate.cpus.Size()
			allowed := true
			for i, groupKey := range groupKeys {
				donated := candidate.cpus.Intersection(groupCPUs[groupKey]).Size()
				next.donations[i] += donated
				next.donated += donated
				if next.donations[i] > groupDonationLimit[groupKey] {
					allowed = false
					break
				}
			}
			if allowed {
				addHardReclaimCoreSelectionState(nextByKey, next)
			}
		}
		var truncated bool
		states, truncated = pruneHardReclaimCoreSelectionStates(nextByKey)
		frontierTruncated = frontierTruncated || truncated
	}
	// Always scan for a feasible terminal, regardless of whether the frontier
	// was truncated. Truncation only means we cannot prove global optimality;
	// the retained states may still contain a valid solution that satisfies all
	// hard constraints (NUMA targets, donation limits, whole-core alignment).
	best := findBestFeasibleHardReclaimTerminal(states, targets)
	if best != nil {
		if frontierTruncated {
			general.InfoS("hard reclaim accepted best-feasible from truncated frontier",
				"candidateCount", len(candidates),
				"stateCount", len(states),
				"frontierWidth", hardReclaimCoreSelectionFrontierWidth,
				"quality", "best_feasible",
				"retained", best.retained,
				"donated", best.donated)
		}
		return best.selected, nil
	}

	if frontierTruncated {
		cause := fmt.Errorf(
			"search frontier truncated at width %d and no feasible terminal retained",
			hardReclaimCoreSelectionFrontierWidth)
		return machine.NewCPUSet(), &hardReclaimSelectionError{
			reason: hardReclaimFailureSearchBudget,
			cause:  cause,
		}
	}

	{
		var bestPartial *hardReclaimCoreSelectionState
		for _, state := range states {
			if bestPartial == nil || state.selected.Size() > bestPartial.selected.Size() ||
				(state.selected.Size() == bestPartial.selected.Size() &&
					hardReclaimCoreSelectionStateLess(state, *bestPartial)) {
				candidate := state
				bestPartial = &candidate
			}
		}
		for i, target := range targets {
			if bestPartial.selectedByNUMA[i] < target {
				deficit := target - bestPartial.selectedByNUMA[i]
				reason := hardReclaimFailureDonorFloor
				wholeCoreCapacity := 0
				for _, candidate := range candidates {
					if candidate.numaIndex == i {
						wholeCoreCapacity += candidate.cpus.Size()
					}
				}
				if wholeCoreCapacity < target {
					reason = hardReclaimFailureInsufficientWholeCore
				}
				cause := fmt.Errorf("NUMA %d needs %d more reclaim CPUs", numaIDs[i], deficit)
				return machine.NewCPUSet(), &hardReclaimSelectionError{
					reason:  reason,
					numaID:  numaIDs[i],
					deficit: deficit,
					cause:   cause,
				}
			}
		}
		cause := fmt.Errorf("no feasible hard reclaim selection")
		return machine.NewCPUSet(), &hardReclaimSelectionError{
			reason: hardReclaimFailureDonorFloor,
			cause:  cause,
		}
	}
}

// findBestFeasibleHardReclaimTerminal scans all states for one where every NUMA
// exactly hits its target. It returns the highest-scoring such state (by
// retained-donated-lexicographic order) or nil if no terminal exists.
func findBestFeasibleHardReclaimTerminal(
	states []hardReclaimCoreSelectionState,
	targets []int,
) *hardReclaimCoreSelectionState {
	var best *hardReclaimCoreSelectionState
	for i := range states {
		if !intSlicesEqual(states[i].selectedByNUMA, targets) {
			continue
		}
		if best == nil || hardReclaimCoreSelectionStateLess(states[i], *best) {
			cp := states[i]
			best = &cp
		}
	}
	return best
}

func intSlicesEqual(left, right []int) bool {
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

func addHardReclaimCoreSelectionState(
	states map[string]hardReclaimCoreSelectionState,
	candidate hardReclaimCoreSelectionState,
) {
	key := fmt.Sprintf("%v/%v", candidate.selectedByNUMA, candidate.donations)
	current, found := states[key]
	if !found || hardReclaimCoreSelectionStateLess(candidate, current) {
		states[key] = candidate
	}
}

func pruneHardReclaimCoreSelectionStates(
	states map[string]hardReclaimCoreSelectionState,
) ([]hardReclaimCoreSelectionState, bool) {
	byProgress := make(map[string][]hardReclaimCoreSelectionState)
	for _, state := range states {
		progress := fmt.Sprint(state.selectedByNUMA)
		byProgress[progress] = append(byProgress[progress], state)
	}
	progresses := make([]string, 0, len(byProgress))
	for progress := range byProgress {
		progresses = append(progresses, progress)
	}
	sort.Strings(progresses)

	result := make([]hardReclaimCoreSelectionState, 0, len(states))
	truncated := false
	for _, progress := range progresses {
		bucket := byProgress[progress]
		sort.Slice(bucket, func(i, j int) bool {
			return hardReclaimCoreSelectionStateLess(bucket[i], bucket[j])
		})
		if len(bucket) > hardReclaimCoreSelectionFrontierWidth {
			truncated = true
			bucket = bucket[:hardReclaimCoreSelectionFrontierWidth]
		}
		result = append(result, bucket...)
	}
	if len(result) > hardReclaimCoreSelectionMaxStates {
		truncated = true
		sort.Slice(result, func(i, j int) bool {
			if result[i].selected.Size() != result[j].selected.Size() {
				return result[i].selected.Size() > result[j].selected.Size()
			}
			for k := range result[i].selectedByNUMA {
				if result[i].selectedByNUMA[k] != result[j].selectedByNUMA[k] {
					return result[i].selectedByNUMA[k] < result[j].selectedByNUMA[k]
				}
			}
			return hardReclaimCoreSelectionStateLess(result[i], result[j])
		})
		result = result[:hardReclaimCoreSelectionMaxStates]
	}
	return result, truncated
}

func hardReclaimCoreSelectionStateLess(
	left, right hardReclaimCoreSelectionState,
) bool {
	if left.retained != right.retained {
		return left.retained > right.retained
	}
	if left.donated != right.donated {
		return left.donated < right.donated
	}
	leftCPUs, rightCPUs := left.selected.ToSliceInt(), right.selected.ToSliceInt()
	for i := range leftCPUs {
		if leftCPUs[i] != rightCPUs[i] {
			return leftCPUs[i] < rightCPUs[i]
		}
	}
	return false
}

func validateHardReclaimReplacement(
	demands []partitionDemand,
	assignments map[string]machine.CPUSet,
	topology *machine.CPUTopology,
	targetByNUMA map[int]int,
) (*hardReclaimReplacementProof, error) {
	if topology == nil {
		return nil, fmt.Errorf("hard reclaim replacement topology is nil")
	}

	sortedDemands := sortedByKey(demands, func(d partitionDemand) string { return d.key })
	proof := &hardReclaimReplacementProof{
		reclaimBefore:                    machine.NewCPUSet(),
		reclaimAfter:                     machine.NewCPUSet(),
		dedicatedBeforeByGroup:           make(map[string]machine.CPUSet),
		dedicatedAfterByGroup:            make(map[string]machine.CPUSet),
		dedicatedTargetDrivenByGroupNUMA: make(map[string]map[int]bool),
		dedicatedTargetByGroupNUMA:       make(map[string]map[int]int),
	}
	assigned := machine.NewCPUSet()
	seenKeys := make(map[string]struct{}, len(sortedDemands))
	for _, demand := range sortedDemands {
		if demand.key == "" {
			return nil, fmt.Errorf("hard reclaim replacement demand has empty key")
		}
		if _, found := seenKeys[demand.key]; found {
			return nil, fmt.Errorf("hard reclaim replacement has duplicate demand %q", demand.key)
		}
		seenKeys[demand.key] = struct{}{}
		for _, cpu := range demand.preferred.ToSliceInt() {
			if _, found := topology.CPUDetails[cpu]; !found {
				return nil, fmt.Errorf(
					"hard reclaim replacement demand %q preferred CPU %d is missing from topology",
					demand.key, cpu)
			}
		}

		assignment, found := assignments[demand.key]
		if !found {
			return nil, fmt.Errorf("hard reclaim replacement is missing assignment %q", demand.key)
		}
		if assignment.Size() != demand.quantity {
			return nil, fmt.Errorf(
				"hard reclaim replacement assignment %q has size %d, want %d",
				demand.key, assignment.Size(), demand.quantity)
		}
		if !assignment.IsSubsetOf(demand.eligible) {
			return nil, fmt.Errorf(
				"hard reclaim replacement assignment %q is outside eligibility", demand.key)
		}
		if !assignment.Intersection(assigned).IsEmpty() {
			return nil, fmt.Errorf(
				"hard reclaim replacement assignment %q overlaps another assignment", demand.key)
		}
		for _, cpu := range assignment.ToSliceInt() {
			if _, found := topology.CPUDetails[cpu]; !found {
				return nil, fmt.Errorf(
					"hard reclaim replacement assignment %q references CPU %d missing from topology",
					demand.key, cpu)
			}
		}
		assigned = assigned.Union(assignment)

		switch demand.class {
		case advisorBlockClassMandatoryReclaim:
			proof.reclaimBefore = proof.reclaimBefore.Union(demand.preferred)
			proof.reclaimAfter = proof.reclaimAfter.Union(assignment)
		case advisorBlockClassDedicated:
			groupKey := demand.requestGroupKey
			if groupKey == "" {
				groupKey = demand.key
			}
			if math.IsNaN(demand.requestQuantity) ||
				math.IsInf(demand.requestQuantity, 0) ||
				demand.requestQuantity < 0 {
				return nil, fmt.Errorf(
					"hard reclaim replacement demand %q has invalid request quantity %v",
					demand.key, demand.requestQuantity)
			}
			proof.dedicatedBeforeByGroup[groupKey] = proof.dedicatedBeforeByGroup[groupKey].Union(demand.preferred)
			proof.dedicatedAfterByGroup[groupKey] = proof.dedicatedAfterByGroup[groupKey].Union(assignment)
			if demand.targetDriven {
				if proof.dedicatedTargetDrivenByGroupNUMA[groupKey] == nil {
					proof.dedicatedTargetDrivenByGroupNUMA[groupKey] = make(map[int]bool)
				}
				proof.dedicatedTargetDrivenByGroupNUMA[groupKey][demand.numaID] = true
				if proof.dedicatedTargetByGroupNUMA[groupKey] == nil {
					proof.dedicatedTargetByGroupNUMA[groupKey] = make(map[int]int)
				}
				proof.dedicatedTargetByGroupNUMA[groupKey][demand.numaID] += demand.sourceTarget
				// per-source frozen floor check. A group-level sum check alone lets one
				// targetDriven donor be over-drawn below its own frozen sourceTarget as
				// long as the group total balances. Each donor must keep at least its
				// frozen target CPUs after replacement.
				if demand.sourceTarget > 0 && assignment.Size() < demand.sourceTarget {
					return nil, fmt.Errorf(
						"targetDriven donor %q after size %d below frozen target %d",
						demand.key, assignment.Size(), demand.sourceTarget)
				}
			}
		case advisorBlockClassShared:
		default:
			return nil, fmt.Errorf(
				"hard reclaim replacement demand %q has unsupported class %q",
				demand.key, demand.class)
		}
	}
	if len(assignments) != len(seenKeys) {
		for key := range assignments {
			if _, found := seenKeys[key]; !found {
				return nil, fmt.Errorf("hard reclaim replacement has unexpected assignment %q", key)
			}
		}
	}

	if err := assertCoreAligned(proof.reclaimAfter, topology); err != nil {
		return nil, fmt.Errorf("hard reclaim replacement final reclaim is not core-aligned: %w", err)
	}
	for _, candidate := range coreAlignedCandidates(topology, proof.reclaimAfter, proof.reclaimBefore) {
		retained := candidate.cpus.Intersection(proof.reclaimBefore).Size()
		if retained > 0 && retained < candidate.cpus.Size() {
			proof.partialBeforeCores++
		}
	}
	actualByNUMA := make(map[int]int)
	for _, cpu := range proof.reclaimAfter.ToSliceInt() {
		actualByNUMA[topology.CPUDetails[cpu].NUMANodeID]++
	}
	numaIDs := make(map[int]struct{}, len(targetByNUMA)+len(actualByNUMA))
	for numaID := range targetByNUMA {
		numaIDs[numaID] = struct{}{}
	}
	for numaID := range actualByNUMA {
		numaIDs[numaID] = struct{}{}
	}
	for _, numaID := range sortedHardReclaimNUMAIDs(numaIDs) {
		if actualByNUMA[numaID] != targetByNUMA[numaID] {
			return nil, fmt.Errorf(
				"hard reclaim replacement NUMA %d has reclaim size %d, want %d",
				numaID, actualByNUMA[numaID], targetByNUMA[numaID])
		}
	}

	groupKeys := make([]string, 0, len(proof.dedicatedBeforeByGroup))
	for groupKey := range proof.dedicatedBeforeByGroup {
		groupKeys = append(groupKeys, groupKey)
	}
	sort.Strings(groupKeys)
	for _, groupKey := range groupKeys {
		before := proof.dedicatedBeforeByGroup[groupKey]
		after := proof.dedicatedAfterByGroup[groupKey]
		// Collect the NUMAs this group touches: its before footprint plus any NUMA
		// recorded as target-driven for this group. The decision is per-NUMA, so a
		// group can be frozen-target on one NUMA and before==after on another without
		// either contaminating the other.
		groupNUMAs := map[int]struct{}{}
		targetDrivenNUMAs := proof.dedicatedTargetDrivenByGroupNUMA[groupKey]
		for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
			numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
			if !before.Intersection(numaCPUs).IsEmpty() {
				groupNUMAs[numaID] = struct{}{}
			}
			if targetDrivenNUMAs[numaID] {
				groupNUMAs[numaID] = struct{}{}
			}
		}
		for numaID := range groupNUMAs {
			numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
			oldOwned := before.Intersection(numaCPUs).Size()
			newOwned := after.Intersection(numaCPUs).Size()
			if targetDrivenNUMAs[numaID] {
				// target-driven NUMA: the after footprint must equal the frozen
				// target on this NUMA. It may shrink from before, but never below
				// the frozen target, and must not be rebalanced from another NUMA.
				//
				// Extension point: this exact equality relies on the gate's
				// "single target-driven source per NUMA" guarantee (see
				// identifyAmbiguousTargetDrivenNUMAs, which degrades any NUMA with
				// more than one dedicated source to legacy). If that guarantee is
				// ever relaxed to allow multiple target-driven sources on one NUMA,
				// this check must become per-source: newOwned >= frozenTarget per
				// source, plus an explicit reclaim-target<->donor pairing, rather
				// than a single group-NUMA exact equality.
				frozenTarget := proof.dedicatedTargetByGroupNUMA[groupKey][numaID]
				if newOwned != frozenTarget {
					return nil, fmt.Errorf(
						"dedicated group %q NUMA %d after %d != frozen target %d",
						groupKey, numaID, newOwned, frozenTarget)
				}
				continue
			}
			// legacy NUMA: preserve the old before==after ownership.
			if oldOwned != newOwned {
				return nil, fmt.Errorf(
					"dedicated group %q replacement changed NUMA %d ownership from %d to %d",
					groupKey, numaID, oldOwned, newOwned)
			}
		}
		// Group-total safety net: the retained footprint must not fall below the sum
		// of frozen targets across this group's target-driven NUMAs.
		frozenTotal := 0
		for _, t := range proof.dedicatedTargetByGroupNUMA[groupKey] {
			frozenTotal += t
		}
		if len(targetDrivenNUMAs) > 0 && after.Size() < frozenTotal {
			return nil, fmt.Errorf(
				"dedicated group %q after-dedicated %d is below frozen target %d",
				groupKey, after.Size(), frozenTotal)
		}
	}
	return proof, nil
}

func sortedHardReclaimNUMAIDs(values map[int]struct{}) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}

func solveHardReclaimWithReplacement(
	demands []partitionDemand,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	options hardReclaimReplacementOptions,
) (map[string]machine.CPUSet, *hardReclaimReplacementProof, error) {
	result, err := solveHardReclaimWithReplacementDiagnosed(
		demands, available, topology, options)
	if err != nil {
		return nil, nil, err
	}
	return result.assignments, result.proof, nil
}

func solveHardReclaimWithReplacementDiagnosed(
	demands []partitionDemand,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	options hardReclaimReplacementOptions,
) (result hardReclaimDiagnosedResult, err error) {
	var diagnostics hardReclaimSearchDiagnostics
	searchBudget := hardReclaimPartitionSearchBudget(options)
	var residualCache *partitionResidualSolveCache
	finish := func(complete bool) hardReclaimSearchDiagnostics {
		diagnostics.Complete = complete
		diagnostics.FlowOperations = searchBudget.flowOperations
		return cloneHardReclaimSearchDiagnostics(diagnostics)
	}
	defer func() {
		if err == nil {
			result.diagnostics = finish(true)
			return
		}
		if fromError, ok := hardReclaimDiagnosticsFromError(err); ok {
			result.diagnostics = fromError
			return
		}
		result.diagnostics = finish(false)
	}()
	fail := func(err error) (hardReclaimDiagnosedResult, error) {
		if kind, ok := hardReclaimBudgetKindFromError(err); ok {
			var selectionErr *hardReclaimSelectionError
			if !errors.As(err, &selectionErr) {
				err = hardReclaimBudgetError(err)
			}
			return hardReclaimDiagnosedResult{}, hardReclaimBudgetExceeded(
				kind, finish(false), err)
		}
		var selectionErr *hardReclaimSelectionError
		if errors.As(err, &selectionErr) &&
			(selectionErr.reason == hardReclaimFailureInsufficientWholeCore ||
				selectionErr.reason == hardReclaimFailureDonorFloor) {
			return hardReclaimDiagnosedResult{}, hardReclaimNoFeasible(
				finish(true), err)
		}
		return hardReclaimDiagnosedResult{}, err
	}
	if topology == nil {
		return hardReclaimDiagnosedResult{}, fmt.Errorf("hard reclaim replacement topology is nil")
	}
	if options.maxCandidateStates <= 0 {
		return fail(fmt.Errorf("%w: limit must be positive",
			errHardReclaimCandidateStateBudget))
	}
	if options.maxTerminalSolves <= 0 {
		return fail(fmt.Errorf("%w: limit must be positive",
			errHardReclaimTerminalSolveBudget))
	}
	if _, _, _, err := validatePartitionDemands(demands, topology); err != nil {
		if errors.Is(err, errPartitionNoFeasibleAssignment) {
			return fail(&hardReclaimSelectionError{
				reason: hardReclaimFailureDonorFloor,
				cause:  err,
			})
		}
		return hardReclaimDiagnosedResult{}, err
	}
	residualCache, err = newPartitionResidualSolveCache(topology)
	if err != nil {
		return hardReclaimDiagnosedResult{}, err
	}
	applyPartitionResidualCacheLimits(residualCache, options.residualCacheLimits)

	targetByNUMA := make(map[int]int)
	activeNUMAs := make(map[int]struct{})
	for _, demand := range demands {
		if demand.class != advisorBlockClassMandatoryReclaim {
			continue
		}
		numaIDs := topology.CPUDetails.KeepOnly(demand.eligible).NUMANodes().ToSliceInt()
		if len(numaIDs) != 1 {
			return hardReclaimDiagnosedResult{}, fmt.Errorf(
				"hard reclaim replacement demand %q must belong to exactly one NUMA", demand.key)
		}
		activeNUMAs[numaIDs[0]] = struct{}{}
		targetByNUMA[numaIDs[0]] += demand.quantity
	}
	if len(targetByNUMA) == 0 {
		graphBudget := hardReclaimPartitionGraphBudget(options)
		diagnostics.TerminalStates++
		assignments, hit, graphBudget, err := solvePartitionResidualCached(
			intersectDemandEligibility(demands, available),
			residualCache,
			graphBudget,
			&searchBudget,
		)
		if hit {
			diagnostics.ResidualCacheHits++
		} else {
			diagnostics.ResidualCacheMisses++
		}
		diagnostics.MaxAssignmentEdgesInGraph = general.Max(
			diagnostics.MaxAssignmentEdgesInGraph, graphBudget.assignmentEdges)
		if err != nil {
			if isPartitionSolverBudgetError(err) ||
				errors.Is(err, errPartitionResidualPreparationWorkBudget) ||
				errors.Is(err, errPartitionResidualRetainedMemoryBudget) {
				return fail(err)
			}
			if errors.Is(err, errPartitionNoFeasibleAssignment) {
				return fail(&hardReclaimSelectionError{
					reason: hardReclaimFailureDonorFloor,
					cause:  err,
				})
			}
			return hardReclaimDiagnosedResult{}, err
		}
		proof, err := validateHardReclaimReplacement(demands, assignments, topology, targetByNUMA)
		if err != nil {
			return fail(&hardReclaimSelectionError{
				reason: hardReclaimFailureDonorFloor,
				cause:  err,
			})
		}
		diagnostics = hardReclaimSelectedDiagnostics(diagnostics, proof)
		return hardReclaimDiagnosedResult{
			assignments: assignments, proof: proof, diagnostics: diagnostics,
		}, nil
	}

	states := []hardReclaimReplacementGlobalState{{
		reclaimAfter: machine.NewCPUSet(),
	}}
	for _, numaID := range sortedHardReclaimNUMAIDs(activeNUMAs) {
		localResults, err := enumerateHardReclaimReplacementInNUMADiagnosed(
			demands, available, topology, numaID, targetByNUMA[numaID], options,
			&diagnostics)
		if err != nil {
			return fail(err)
		}
		nextByKey := make(map[string]hardReclaimReplacementGlobalState)
		combinations := 0
		for _, state := range states {
			for _, local := range localResults {
				combinations++
				diagnostics.GeneratedCandidateStates++
				if combinations > options.maxCandidateStates {
					return fail(fmt.Errorf(
						"%w: hard reclaim replacement global merge exceeded state budget %d",
						errHardReclaimCandidateStateBudget, options.maxCandidateStates))
				}
				next := mergeHardReclaimReplacementState(state, local)
				key := hardReclaimGlobalStateKey(next)
				current, found := nextByKey[key]
				if !found || hardReclaimGlobalStateLess(next, current) {
					nextByKey[key] = next
				}
			}
		}
		if len(nextByKey) > options.maxCandidateStates {
			return fail(fmt.Errorf(
				"%w: hard reclaim replacement global frontier exceeded state budget %d",
				errHardReclaimCandidateStateBudget, options.maxCandidateStates))
		}
		diagnostics.DeduplicatedCandidateStates += len(nextByKey)
		states = states[:0]
		for _, state := range nextByKey {
			states = append(states, state)
		}
	}

	bestAssignments, bestProof, err := solveHardReclaimResidualCandidatesDiagnosed(
		states, demands, available, topology, targetByNUMA, options,
		residualCache, &searchBudget, &diagnostics)
	if err != nil {
		return fail(err)
	}
	if bestAssignments == nil {
		return fail(&hardReclaimSelectionError{
			reason: hardReclaimFailureDonorFloor,
			cause:  fmt.Errorf("no global hard reclaim replacement is feasible"),
		})
	}
	diagnostics = hardReclaimSelectedDiagnostics(diagnostics, bestProof)
	return hardReclaimDiagnosedResult{
		assignments: bestAssignments,
		proof:       bestProof,
		diagnostics: diagnostics,
	}, nil
}

func solveHardReclaimResidualCandidates(
	states []hardReclaimReplacementGlobalState,
	demands []partitionDemand,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	targetByNUMA map[int]int,
	options hardReclaimReplacementOptions,
	residualCache *partitionResidualSolveCache,
	searchBudget *partitionSearchBudget,
) (map[string]machine.CPUSet, *hardReclaimReplacementProof, error) {
	var diagnostics hardReclaimSearchDiagnostics
	return solveHardReclaimResidualCandidatesDiagnosed(
		states, demands, available, topology, targetByNUMA, options,
		residualCache, searchBudget, &diagnostics)
}

func solveHardReclaimResidualCandidatesDiagnosed(
	states []hardReclaimReplacementGlobalState,
	demands []partitionDemand,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	targetByNUMA map[int]int,
	options hardReclaimReplacementOptions,
	residualCache *partitionResidualSolveCache,
	searchBudget *partitionSearchBudget,
	diagnostics *hardReclaimSearchDiagnostics,
) (map[string]machine.CPUSet, *hardReclaimReplacementProof, error) {
	orderedStates := append([]hardReclaimReplacementGlobalState(nil), states...)
	sort.Slice(orderedStates, func(i, j int) bool {
		return hardReclaimGlobalStateKey(orderedStates[i]) <
			hardReclaimGlobalStateKey(orderedStates[j])
	})
	if err := preparePartitionResidualCacheLazy(
		residualCache,
		len(orderedStates),
		func(i int) []partitionDemand {
			return hardReclaimResidualDemands(
				demands, available, orderedStates[i].reclaimAfter)
		},
	); err != nil {
		return nil, nil, err
	}

	var bestAssignments map[string]machine.CPUSet
	var bestProof *hardReclaimReplacementProof
	for _, state := range orderedStates {
		diagnostics.TerminalStates++
		residualDemands := hardReclaimResidualDemands(
			demands, available, state.reclaimAfter)
		graphBudget := hardReclaimPartitionGraphBudget(options)
		assignments, hit, graphBudget, solveErr := solvePartitionResidualCached(
			residualDemands, residualCache, graphBudget, searchBudget)
		if hit {
			diagnostics.ResidualCacheHits++
		} else {
			diagnostics.ResidualCacheMisses++
		}
		diagnostics.MaxAssignmentEdgesInGraph = general.Max(
			diagnostics.MaxAssignmentEdgesInGraph, graphBudget.assignmentEdges)
		if solveErr != nil {
			if isPartitionSolverBudgetError(solveErr) {
				return nil, nil, hardReclaimBudgetError(solveErr)
			}
			if errors.Is(solveErr, errPartitionNoFeasibleAssignment) {
				continue
			}
			return nil, nil, solveErr
		}

		proof, validationErr := validateHardReclaimReplacement(
			demands, assignments, topology, targetByNUMA)
		if validationErr != nil {
			continue
		}
		if !proof.reclaimAfter.Equals(state.reclaimAfter) {
			continue
		}
		if bestAssignments == nil || hardReclaimReplacementResultLess(
			assignments, proof, bestAssignments, bestProof) {
			bestAssignments = clonePartitionAssignments(assignments)
			bestProof = proof
		}
	}
	return bestAssignments, bestProof, nil
}

func hardReclaimBudgetError(cause error) error {
	return &hardReclaimSelectionError{
		reason: hardReclaimFailureSearchBudget,
		cause:  cause,
	}
}

func isPartitionSolverBudgetError(err error) bool {
	return errors.Is(err, errPartitionAssignmentEdgeBudget) ||
		errors.Is(err, errPartitionFlowOperationBudget) ||
		errors.Is(err, errPartitionResidualPreparationWorkBudget) ||
		errors.Is(err, errPartitionResidualRetainedMemoryBudget)
}

func enumerateHardReclaimReplacementInNUMA(
	demands []partitionDemand,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	numaID, target int,
	options hardReclaimReplacementOptions,
) ([]hardReclaimReplacementResult, error) {
	var diagnostics hardReclaimSearchDiagnostics
	return enumerateHardReclaimReplacementInNUMADiagnosed(
		demands, available, topology, numaID, target, options, &diagnostics)
}

func enumerateHardReclaimReplacementInNUMADiagnosed(
	demands []partitionDemand,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	numaID, target int,
	options hardReclaimReplacementOptions,
	diagnostics *hardReclaimSearchDiagnostics,
) ([]hardReclaimReplacementResult, error) {
	numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
	localAvailable := available.Intersection(numaCPUs)
	reclaimBefore := machine.NewCPUSet()
	reclaimEligible := machine.NewCPUSet()
	dedicatedBefore := machine.NewCPUSet()
	allCurrentlyOwned := machine.NewCPUSet()
	for _, demand := range demands {
		preferred := demand.preferred.Intersection(numaCPUs)
		allCurrentlyOwned = allCurrentlyOwned.Union(preferred)
		switch demand.class {
		case advisorBlockClassMandatoryReclaim:
			reclaimBefore = reclaimBefore.Union(preferred)
			reclaimEligible = reclaimEligible.Union(demand.eligible.Intersection(localAvailable))
		case advisorBlockClassDedicated:
			dedicatedBefore = dedicatedBefore.Union(preferred)
		}
	}

	terminals := []machine.CPUSet{machine.NewCPUSet()}
	if target > 0 {
		free := localAvailable.Difference(allCurrentlyOwned)
		source := reclaimBefore.Union(free).Union(dedicatedBefore).
			Intersection(reclaimEligible)
		candidates := coreAlignedCandidates(topology, source, reclaimBefore)
		var candidateBudgetExceeded, terminalTruncated bool
		var generated int
		terminals, candidateBudgetExceeded, terminalTruncated, generated = enumerateHardReclaimReplacementNUMATerminalsDiagnosed(
			candidates, target, options.maxCandidateStates, options.maxTerminalSolves)
		diagnostics.GeneratedCandidateStates += generated
		if candidateBudgetExceeded {
			return nil, fmt.Errorf(
				"%w: hard reclaim replacement NUMA %d search exceeded candidate state budget %d",
				errHardReclaimCandidateStateBudget, numaID, options.maxCandidateStates)
		}
		if terminalTruncated {
			return nil, fmt.Errorf(
				"%w: hard reclaim replacement NUMA %d exceeded terminal solve budget %d",
				errHardReclaimTerminalSolveBudget, numaID, options.maxTerminalSolves)
		}
		if len(terminals) == 0 {
			return nil, &hardReclaimSelectionError{
				reason:  hardReclaimFailureInsufficientWholeCore,
				numaID:  numaID,
				deficit: target,
				cause: fmt.Errorf(
					"no complete-core reclaim replacement satisfies NUMA %d target %d",
					numaID, target),
			}
		}
	}

	resultsBySignature := make(map[string]hardReclaimReplacementResult)
	for _, terminal := range terminals {
		result := hardReclaimReplacementResult{
			proof: &hardReclaimReplacementProof{
				reclaimBefore: reclaimBefore,
				reclaimAfter:  terminal,
			},
		}
		signature := hardReclaimTerminalSignature(terminal, demands, topology)
		current, found := resultsBySignature[signature]
		if !found || hardReclaimLocalFrontierLess(result, current) {
			resultsBySignature[signature] = result
		}
	}
	results := make([]hardReclaimReplacementResult, 0, len(resultsBySignature))
	for _, result := range resultsBySignature {
		results = append(results, result)
	}
	diagnostics.DeduplicatedCandidateStates += len(results)
	if len(results) == 0 {
		return nil, &hardReclaimSelectionError{
			reason: hardReclaimFailureDonorFloor,
			numaID: numaID,
			cause:  fmt.Errorf("no reclaim frontier satisfies NUMA %d target %d", numaID, target),
		}
	}
	sort.Slice(results, func(i, j int) bool {
		left, right := results[i].proof, results[j].proof
		leftRetained := left.reclaimAfter.Intersection(left.reclaimBefore).Size()
		rightRetained := right.reclaimAfter.Intersection(right.reclaimBefore).Size()
		if leftRetained != rightRetained {
			return leftRetained > rightRetained
		}
		return compareCPUSetLexicographically(left.reclaimAfter, right.reclaimAfter) < 0
	})
	return results, nil
}

func hardReclaimTerminalSignature(
	terminal machine.CPUSet,
	demands []partitionDemand,
	topology *machine.CPUTopology,
) string {
	sortedDemands := sortedByKey(demands, func(d partitionDemand) string { return d.key })
	classCounts := make(map[string]int)
	for _, cpu := range terminal.ToSliceInt() {
		var signature strings.Builder
		for _, demand := range sortedDemands {
			if demand.eligible.Contains(cpu) {
				signature.WriteByte('e')
			} else {
				signature.WriteByte('-')
			}
			if demand.preferred.Contains(cpu) {
				signature.WriteByte('p')
			} else {
				signature.WriteByte('-')
			}
			signature.WriteByte(byte('0' + partitionTopologyDistance(cpu, demand.preferred, topology)))
		}
		classCounts[signature.String()]++
	}
	classes := make([]string, 0, len(classCounts))
	for class := range classCounts {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	var result strings.Builder
	for _, class := range classes {
		fmt.Fprintf(&result, "%s=%d;", class, classCounts[class])
	}
	return result.String()
}

func hardReclaimLocalFrontierLess(
	left, right hardReclaimReplacementResult,
) bool {
	leftRetained := left.proof.reclaimAfter.Intersection(left.proof.reclaimBefore).Size()
	rightRetained := right.proof.reclaimAfter.Intersection(right.proof.reclaimBefore).Size()
	if leftRetained != rightRetained {
		return leftRetained > rightRetained
	}
	return compareCPUSetLexicographically(left.proof.reclaimAfter, right.proof.reclaimAfter) < 0
}

func hardReclaimPartitionGraphBudget(
	options hardReclaimReplacementOptions,
) partitionGraphBudget {
	budget := defaultPartitionGraphBudget()
	if options.maxPartitionAssignmentEdges > 0 {
		budget.maxAssignmentEdges = options.maxPartitionAssignmentEdges
	}
	return budget
}

func hardReclaimPartitionSearchBudget(
	options hardReclaimReplacementOptions,
) partitionSearchBudget {
	budget := defaultPartitionSearchBudget()
	if options.maxPartitionFlowOperations > 0 {
		budget.maxFlowOperations = options.maxPartitionFlowOperations
	}
	return budget
}

func mergeHardReclaimReplacementState(
	state hardReclaimReplacementGlobalState,
	local hardReclaimReplacementResult,
) hardReclaimReplacementGlobalState {
	next := hardReclaimReplacementGlobalState{
		reclaimRetained: state.reclaimRetained +
			local.proof.reclaimAfter.Intersection(local.proof.reclaimBefore).Size(),
		reclaimAfter: state.reclaimAfter.Union(local.proof.reclaimAfter),
	}
	return next
}

func hardReclaimGlobalStateKey(state hardReclaimReplacementGlobalState) string {
	return fmt.Sprintf("%d/%s",
		state.reclaimRetained,
		state.reclaimAfter.String())
}

func hardReclaimGlobalStateLess(
	left, right hardReclaimReplacementGlobalState,
) bool {
	if comparison := compareCPUSetLexicographically(
		left.reclaimAfter, right.reclaimAfter); comparison != 0 {
		return comparison < 0
	}
	return false
}

func hardReclaimResidualDemands(
	demands []partitionDemand,
	available, reclaim machine.CPUSet,
) []partitionDemand {
	result := append([]partitionDemand(nil), demands...)
	for i := range result {
		result[i].eligible = result[i].eligible.Intersection(available)
		if result[i].class == advisorBlockClassMandatoryReclaim {
			result[i].eligible = result[i].eligible.Intersection(reclaim)
		} else {
			result[i].eligible = result[i].eligible.Difference(reclaim)
		}
	}
	return result
}

func intersectDemandEligibility(
	demands []partitionDemand,
	available machine.CPUSet,
) []partitionDemand {
	result := append([]partitionDemand(nil), demands...)
	for i := range result {
		result[i].eligible = result[i].eligible.Intersection(available)
	}
	return result
}

func enumerateHardReclaimReplacementNUMATerminalsDiagnosed(
	candidates []coreAlignedCandidate,
	target int,
	maxCandidateStates, maxTerminalSolves int,
) ([]machine.CPUSet, bool, bool, int) {
	visited := 0
	budgetExceeded := false
	frontier := make([]hardReclaimReplacementTerminal, 0)
	var visit func(int, int, hardReclaimReplacementTerminal)
	visit = func(index, selected int, terminal hardReclaimReplacementTerminal) {
		if budgetExceeded {
			return
		}
		visited++
		if visited > maxCandidateStates {
			budgetExceeded = true
			return
		}
		if selected == target {
			frontier = append(frontier, terminal)
			return
		}
		if index == len(candidates) {
			return
		}

		candidate := candidates[index]
		if selected+candidate.cpus.Size() <= target {
			next := hardReclaimReplacementTerminal{
				cpus:               terminal.cpus.Union(candidate.cpus),
				partialBeforeCores: terminal.partialBeforeCores,
				retained:           terminal.retained + candidate.preferredHit,
			}
			if candidate.preferredHit > 0 && candidate.preferredHit < candidate.cpus.Size() {
				next.partialBeforeCores++
			}
			visit(index+1, selected+candidate.cpus.Size(), next)
		}
		visit(index+1, selected, terminal)
	}
	visit(0, 0, hardReclaimReplacementTerminal{cpus: machine.NewCPUSet()})
	if budgetExceeded {
		return nil, true, false, visited
	}
	if len(frontier) == 0 {
		return nil, false, false, visited
	}
	sort.Slice(frontier, func(i, j int) bool {
		return hardReclaimReplacementTerminalLess(frontier[i], frontier[j])
	})
	truncated := len(frontier) > maxTerminalSolves
	if truncated {
		frontier = frontier[:maxTerminalSolves]
	}
	terminals := make([]machine.CPUSet, len(frontier))
	for i := range frontier {
		terminals[i] = frontier[i].cpus
	}
	return terminals, false, truncated, visited
}

func hardReclaimReplacementTerminalLess(
	left, right hardReclaimReplacementTerminal,
) bool {
	if left.retained != right.retained {
		return left.retained > right.retained
	}
	if left.partialBeforeCores != right.partialBeforeCores {
		return left.partialBeforeCores < right.partialBeforeCores
	}
	return compareCPUSetLexicographically(left.cpus, right.cpus) < 0
}

func hardReclaimReplacementResultLess(
	leftAssignments map[string]machine.CPUSet,
	leftProof *hardReclaimReplacementProof,
	rightAssignments map[string]machine.CPUSet,
	rightProof *hardReclaimReplacementProof,
) bool {
	leftReclaimRetained := leftProof.reclaimAfter.Intersection(leftProof.reclaimBefore).Size()
	rightReclaimRetained := rightProof.reclaimAfter.Intersection(rightProof.reclaimBefore).Size()
	if leftReclaimRetained != rightReclaimRetained {
		return leftReclaimRetained > rightReclaimRetained
	}
	if leftProof.partialBeforeCores != rightProof.partialBeforeCores {
		return leftProof.partialBeforeCores < rightProof.partialBeforeCores
	}

	leftDedicatedRetained, rightDedicatedRetained := 0, 0
	leftTouched, rightTouched := 0, 0
	groupKeys := make([]string, 0, len(leftProof.dedicatedBeforeByGroup))
	for groupKey := range leftProof.dedicatedBeforeByGroup {
		groupKeys = append(groupKeys, groupKey)
	}
	sort.Strings(groupKeys)
	for _, groupKey := range groupKeys {
		leftBefore := leftProof.dedicatedBeforeByGroup[groupKey]
		leftAfter := leftProof.dedicatedAfterByGroup[groupKey]
		rightBefore := rightProof.dedicatedBeforeByGroup[groupKey]
		rightAfter := rightProof.dedicatedAfterByGroup[groupKey]
		leftDedicatedRetained += leftBefore.Intersection(leftAfter).Size()
		rightDedicatedRetained += rightBefore.Intersection(rightAfter).Size()
		if !leftBefore.Equals(leftAfter) {
			leftTouched++
		}
		if !rightBefore.Equals(rightAfter) {
			rightTouched++
		}
	}
	if leftDedicatedRetained != rightDedicatedRetained {
		return leftDedicatedRetained > rightDedicatedRetained
	}
	if leftTouched != rightTouched {
		return leftTouched < rightTouched
	}
	if comparison := compareCPUSetLexicographically(
		leftProof.reclaimAfter, rightProof.reclaimAfter); comparison != 0 {
		return comparison < 0
	}

	keys := make([]string, 0, len(leftAssignments))
	for key := range leftAssignments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if comparison := compareCPUSetLexicographically(
			leftAssignments[key], rightAssignments[key]); comparison != 0 {
			return comparison < 0
		}
	}
	return false
}

func compareCPUSetLexicographically(left, right machine.CPUSet) int {
	leftCPUs, rightCPUs := left.ToSliceInt(), right.ToSliceInt()
	limit := general.Min(len(leftCPUs), len(rightCPUs))
	for i := 0; i < limit; i++ {
		if leftCPUs[i] < rightCPUs[i] {
			return -1
		}
		if leftCPUs[i] > rightCPUs[i] {
			return 1
		}
	}
	switch {
	case len(leftCPUs) < len(rightCPUs):
		return -1
	case len(leftCPUs) > len(rightCPUs):
		return 1
	default:
		return 0
	}
}

func pinPartitionDemandsToAssignments(
	demands []partitionDemand,
	assignments map[string]machine.CPUSet,
) ([]partitionDemand, error) {
	pinned := append([]partitionDemand(nil), demands...)
	for i := range pinned {
		assignment, ok := assignments[pinned[i].key]
		if !ok {
			return nil, fmt.Errorf(
				"missing exact assignment for partition demand %q", pinned[i].key)
		}
		if assignment.Size() != pinned[i].quantity {
			return nil, fmt.Errorf(
				"exact assignment %q has size %d, want %d",
				pinned[i].key, assignment.Size(), pinned[i].quantity)
		}
		pinned[i].eligible = assignment.Clone()
		pinned[i].preferred = assignment.Clone()
	}
	return pinned, nil
}

func pinHardReclaimPartitionDemands(
	demands []partitionDemand,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	allowCommittedFallback bool,
) ([]partitionDemand, error) {
	targetByNUMA := make(map[int]int)
	currentReclaim := machine.NewCPUSet()
	reclaimEligible := machine.NewCPUSet()
	allCurrentlyOwned := machine.NewCPUSet()
	donors := make([]hardReclaimPartitionDonor, 0)
	// targetDrivenDonorNUMAs collects the NUMA of every dedicated source the advisor
	// marked target-driven. The mandatory reclaim demand itself never carries the
	// provenance flag (expandHardPartitionReclaimPhase creates it without it), so the
	// donor side is the only reliable source of truth.
	targetDrivenDonorNUMAs := make(map[int]struct{})

	for _, demand := range demands {
		allCurrentlyOwned = allCurrentlyOwned.Union(demand.preferred)
		switch demand.class {
		case advisorBlockClassMandatoryReclaim:
			numaIDs := topology.CPUDetails.KeepOnly(demand.eligible).NUMANodes().ToSliceInt()
			if len(numaIDs) != 1 {
				return nil, fmt.Errorf("hard reclaim demand %q must belong to exactly one NUMA", demand.key)
			}
			targetByNUMA[numaIDs[0]] += demand.quantity
			currentReclaim = currentReclaim.Union(demand.preferred)
			reclaimEligible = reclaimEligible.Union(demand.eligible)
		case advisorBlockClassDedicated:
			requestQuantity := demand.requestQuantity
			if requestQuantity <= 0 {
				requestQuantity = float64(demand.quantity)
			}
			donors = append(donors, hardReclaimPartitionDonor{
				key:             demand.key,
				groupKey:        demand.requestGroupKey,
				cpus:            demand.preferred,
				requestQuantity: requestQuantity,
				targetDriven:    demand.targetDriven,
				sourceTarget:    demand.sourceTarget,
				reclaimQuota:    demand.reclaimQuota,
			})
			if demand.targetDriven {
				targetDrivenDonorNUMAs[demand.numaID] = struct{}{}
			}
		}
	}
	if len(targetByNUMA) == 0 {
		return append([]partitionDemand(nil), demands...), nil
	}

	// A reclaim target is itself target-driven only when its NUMA hosts both a
	// mandatory reclaim target and a target-driven dedicated donor. This is what
	// connects the fast-path donor-pool isolation to the production chain: before,
	// targetDrivenReclaimNUMAs was derived from the mandatory demand's (always empty)
	// targetDriven field and so the isolation never engaged.
	targetDrivenReclaimNUMAs := make(map[int]struct{}, len(targetDrivenDonorNUMAs))
	for numaID := range targetDrivenDonorNUMAs {
		if _, hasTarget := targetByNUMA[numaID]; hasTarget {
			targetDrivenReclaimNUMAs[numaID] = struct{}{}
		}
	}
	// Log the derived provenance explicitly: this set is what connects the fast-path
	// donor-pool isolation to the production chain, and a silent empty result (as when
	// it was derived from the mandatory demand's always-empty targetDriven field) is
	// exactly the failure mode this derivation guards against.
	general.InfoS("hard reclaim target-driven NUMA provenance derived",
		"targetDrivenDonorNUMAs", sortedIntKeys(targetDrivenDonorNUMAs),
		"targetDrivenReclaimNUMAs", sortedIntKeys(targetDrivenReclaimNUMAs))

	input := hardReclaimPartitionInput{
		topology:                 topology,
		targetByNUMA:             targetByNUMA,
		currentReclaim:           currentReclaim,
		free:                     available.Difference(allCurrentlyOwned),
		reclaimEligible:          reclaimEligible,
		donors:                   donors,
		targetDrivenReclaimNUMAs: targetDrivenReclaimNUMAs,
	}
	runReplacement := func(currentErr error) ([]partitionDemand, error) {
		var selectionErr *hardReclaimSelectionError
		if !errors.As(currentErr, &selectionErr) ||
			(selectionErr.reason != hardReclaimFailureInsufficientWholeCore &&
				selectionErr.reason != hardReclaimFailureDonorFloor) {
			return nil, currentErr
		}

		replacement, replacementErr := solveHardReclaimWithReplacementDiagnosed(
			demands, available, topology,
			hardReclaimReplacementOptionsForTopology(topology))
		diagnostics := replacement.diagnostics
		if fromError, ok := hardReclaimDiagnosticsFromError(replacementErr); ok {
			diagnostics = fromError
		}
		general.InfoS(
			"finished hard reclaim replacement search",
			"complete", diagnostics.Complete,
			"generatedCandidateStates", diagnostics.GeneratedCandidateStates,
			"deduplicatedCandidateStates", diagnostics.DeduplicatedCandidateStates,
			"terminalStates", diagnostics.TerminalStates,
			"residualCacheHits", diagnostics.ResidualCacheHits,
			"residualCacheMisses", diagnostics.ResidualCacheMisses,
			"maxAssignmentEdgesInGraph", diagnostics.MaxAssignmentEdgesInGraph,
			"flowOperations", diagnostics.FlowOperations,
			"selectedRetainedReclaimCPUs", diagnostics.SelectedRetainedReclaimCPUs,
			"selectedTouchedDonorGroupCount", len(diagnostics.SelectedTouchedDonorGroups),
		)
		if replacementErr != nil {
			return nil, fmt.Errorf(
				"hard reclaim fast path failed: %v; replacement failed: %w",
				currentErr, replacementErr)
		}
		return pinPartitionDemandsToAssignments(demands, replacement.assignments)
	}

	plan, err := planHardReclaimPartition(input)
	originalPlanErr := err
	committedFallbackApplied := false
	var committedByNUMA map[int]int
	if err != nil && allowCommittedFallback {
		// The committed whole-core fallback is exclusive to the steady real-NUMA
		// path: when the fast path cannot grow reclaim beyond the committed whole
		// core (a protected dedicated group cannot donate), retry anchored on the
		// committed reclaim size before falling through to the general replacement
		// search. Ramp-up/hard path keeps its original error semantics.
		if committedPlan, anchor, fallbackErr := pnhCommittedFallback(input, err); fallbackErr == nil {
			plan, committedByNUMA = committedPlan, anchor
			committedFallbackApplied = true
		} else {
			err = fallbackErr
		}
	}
	if err != nil {
		return runReplacement(err)
	}

	pinned := append([]partitionDemand(nil), demands...)
	for i := range pinned {
		switch pinned[i].class {
		case advisorBlockClassMandatoryReclaim:
			pinned[i].eligible = pinned[i].eligible.Intersection(plan.reclaim)
			pinned[i].preferred = pinned[i].preferred.Intersection(plan.reclaim)
		case advisorBlockClassDedicated:
			pinned[i].eligible = pinned[i].eligible.Difference(plan.reclaim)
			pinned[i].preferred = plan.donorCPUs[pinned[i].key].Intersection(pinned[i].eligible)
		}
	}

	if committedFallbackApplied {
		// Anchoring the reclaim target down to the committed whole core shrinks the
		// mandatory demand's pinned eligible below its original quantity. The pinning
		// loop only recomputes eligible/preferred; it does not rewrite quantity. The
		// downstream solver enforces eligible.Size() >= quantity and the exact-
		// assignment path enforces assignment.Size() == quantity, so reconcile the
		// quantities now: shrink the mandatory demand to the committed anchor and
		// return the released CPUs to the dedicated demands on the same NUMA. If the
		// dedicated side cannot absorb the released CPUs, the fallback plan is not
		// internally consistent and the general replacement search must own it.
		if reconcileErr := reconcileCommittedFallbackQuantities(
			pinned, targetByNUMA, committedByNUMA, topology); reconcileErr != nil {
			general.InfoS("committed whole-core fallback could not reconcile demand quantities; deferring to replacement",
				"error", reconcileErr.Error())
			return runReplacement(originalPlanErr)
		}
	}
	return pinned, nil
}

// wholeCoreInfeasibleError is the typed error returned when a frozen reclaim
// target cannot be expressed in whole physical cores.
type wholeCoreInfeasibleError struct {
	Target      int
	CPUsPerCore int
}

func (e *wholeCoreInfeasibleError) Error() string {
	return fmt.Sprintf("reclaim target %d not whole-core representable at CPUsPerCore %d", e.Target, e.CPUsPerCore)
}

// ensureReclaimTargetWholeCoreRepresentable validates that a frozen reclaim
// target can be expressed in whole physical cores. A zero target (release the
// whole core) is always representable.
func ensureReclaimTargetWholeCoreRepresentable(target, cpusPerCore int) error {
	if cpusPerCore <= 0 {
		return fmt.Errorf("invalid cpusPerCore %d", cpusPerCore)
	}
	if target < 0 {
		return fmt.Errorf("negative reclaim target %d", target)
	}
	if target%cpusPerCore != 0 {
		return &wholeCoreInfeasibleError{Target: target, CPUsPerCore: cpusPerCore}
	}
	return nil
}

// pnhCommittedFallback is the narrow committed whole-core fallback for the steady
// real-NUMA fast path. It only engages when the fast path could not assemble the
// requested reclaim (a hardReclaimSelectionError with insufficient_whole_core) and
// the already-committed reclaim on each NUMA is itself a legal whole-core target.
// It retries the plan anchored on the committed reclaim size per NUMA, which needs
// no new donation and therefore no cgroup churn. Any other failure, a committed
// anchor below the reclaim floor or not whole-core aligned, or a fallback that also
// fails, returns the original error so unrelated infeasibility is never swallowed
// and the general replacement search still runs.
func pnhCommittedFallback(
	input hardReclaimPartitionInput,
	planningErr error,
) (*hardReclaimPartitionPlan, map[int]int, error) {
	var selectionErr *hardReclaimSelectionError
	if !errors.As(planningErr, &selectionErr) ||
		selectionErr.reason != hardReclaimFailureInsufficientWholeCore {
		return nil, nil, planningErr
	}

	w := input.topology.CPUsPerCore()
	floorReclaim := minimumHardReclaimCoresPerNUMA * w
	committedByNUMA := make(map[int]int, len(input.targetByNUMA))
	anyChanged := false
	for numaID := range input.targetByNUMA {
		committed := input.currentReclaim.
			Intersection(input.topology.CPUDetails.CPUsInNUMANodes(numaID)).Size()
		committedByNUMA[numaID] = committed
		if input.targetByNUMA[numaID] == committed {
			continue
		}
		// A committed anchor below the per-NUMA reclaim floor cannot be a safe
		// no-op target: anchoring on it would silently drop reclaim below the
		// invariant minimum. Keep the original planning error.
		if committed < floorReclaim {
			return nil, nil, planningErr
		}
		// A committed anchor that is itself not whole-core aligned is an invariant
		// break, not a retry candidate; surface nothing and let replacement run.
		if w > 1 && committed%w != 0 {
			return nil, nil, planningErr
		}
		anyChanged = true
	}
	if !anyChanged {
		// The requested target already equals the committed anchor on every NUMA;
		// the shortage is not recoverable by anchoring on committed.
		return nil, nil, planningErr
	}

	fallbackInput := input
	fallbackInput.targetByNUMA = committedByNUMA
	plan, err := planHardReclaimPartition(fallbackInput)
	if err != nil {
		// fallback did not resolve it; keep the original typed planning error so
		// the general replacement search still runs.
		general.InfoS("committed whole-core reclaim fallback did not resolve planning error",
			"originalError", planningErr.Error(),
			"fallbackError", err.Error())
		return nil, nil, planningErr
	}
	general.InfoS("steady real-NUMA reclaim fell back to committed whole-core target",
		"committedTargetByNUMA", committedByNUMA)
	return plan, committedByNUMA, nil
}

// reconcileCommittedFallbackQuantities rewrites demand quantities after the steady
// committed whole-core fallback anchored the reclaim target down to the committed
// size. The pinning loop recomputes eligible/preferred but leaves quantity untouched,
// so the mandatory demand would keep its original (larger) target while its pinned
// eligible shrank to the committed anchor. That breaks the downstream
// eligible.Size() >= quantity invariant and the exact-assignment size == quantity
// check. For every affected NUMA:
//
//   - the mandatory reclaim demand is shrunk to its pinned eligible (committed) size;
//   - the released CPUs (original target - committed) are returned to the dedicated
//     demands on the same NUMA in stable key order, each capped by its pinned
//     eligible headroom;
//   - if the dedicated side cannot fully absorb the released CPUs the fallback plan is
//     not internally consistent and an error is returned so the general replacement
//     search owns the NUMA.
func reconcileCommittedFallbackQuantities(
	pinned []partitionDemand,
	originalTargetByNUMA map[int]int,
	committedByNUMA map[int]int,
	topology *machine.CPUTopology,
) error {
	for numaID, committed := range committedByNUMA {
		delta := originalTargetByNUMA[numaID] - committed
		if delta <= 0 {
			continue
		}

		// Shrink every mandatory reclaim demand on this NUMA to its pinned (committed)
		// eligible.
		for i := range pinned {
			if pinned[i].class != advisorBlockClassMandatoryReclaim {
				continue
			}
			numaIDs := topology.CPUDetails.KeepOnly(pinned[i].eligible).NUMANodes().ToSliceInt()
			if len(numaIDs) == 1 && numaIDs[0] == numaID {
				pinned[i].quantity = pinned[i].eligible.Size()
			}
		}

		// Collect the dedicated demands on this NUMA in stable order and distribute the
		// released CPUs across them, capped by each one's pinned eligible headroom.
		dedicatedIdx := make([]int, 0, len(pinned))
		for i := range pinned {
			if pinned[i].class == advisorBlockClassDedicated && pinned[i].numaID == numaID {
				dedicatedIdx = append(dedicatedIdx, i)
			}
		}
		sort.Slice(dedicatedIdx, func(a, b int) bool {
			return pinned[dedicatedIdx[a]].key < pinned[dedicatedIdx[b]].key
		})

		remaining := delta
		for _, di := range dedicatedIdx {
			if remaining == 0 {
				break
			}
			headroom := pinned[di].eligible.Size() - pinned[di].quantity
			if headroom <= 0 {
				continue
			}
			take := headroom
			if take > remaining {
				take = remaining
			}
			pinned[di].quantity += take
			remaining -= take
		}
		if remaining > 0 {
			return fmt.Errorf(
				"committed fallback released %d reclaim CPUs on NUMA %d but dedicated demands cannot absorb them",
				remaining, numaID)
		}
	}
	return nil
}
