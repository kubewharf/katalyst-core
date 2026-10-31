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
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	steadyFakeNUMAFrontierWidth         = 8
	steadyFakeNUMAMaxSolveAttempts      = 32
	steadyFakeNUMAMaxMigratedCPUs       = 8
	steadyFakeNUMAQuotaStateBudget      = 16 * 1024
	steadyFakeNUMAMaxCandidateActions   = 1024
	steadyFakeNUMAStageMaxSolveAttempts = 1024
	steadyFakeNUMAStageMaxSearchActions = 64 * 1024
)

type steadyFakeNUMAPinState struct {
	pins        map[string][]machine.CPUSet
	assignments map[string]machine.CPUSet
	signature   string
}

type steadyFakeNUMAScore struct {
	wholeCores       int
	retainedOldCores int
	retainedOldCPUs  int
	migratedCPUs     int
	signature        string
}

type steadyFakeNUMASearchBudget struct {
	maxSolveAttempts    int
	maxCandidateActions int
}

type steadyFakeNUMASearchTracker struct {
	budget           steadyFakeNUMASearchBudget
	solveAttempts    int
	candidateActions int
}

type steadyFakeNUMACommittedAssignment struct {
	blockID      string
	blockIDs     []string
	componentKey string
	class        advisorBlockClass
	numaID       int
	cpus         machine.CPUSet
	eligible     machine.CPUSet
}

type steadyFakeNUMACommittedSnapshot struct {
	assignments         []steadyFakeNUMACommittedAssignment
	rawReclaimAggregate machine.CPUSet
	reclaim             machine.CPUSet
}

func (snapshot steadyFakeNUMACommittedSnapshot) clone() steadyFakeNUMACommittedSnapshot {
	cloned := steadyFakeNUMACommittedSnapshot{
		assignments:         make([]steadyFakeNUMACommittedAssignment, len(snapshot.assignments)),
		rawReclaimAggregate: snapshot.rawReclaimAggregate.Clone(),
		reclaim:             snapshot.reclaim.Clone(),
	}
	copy(cloned.assignments, snapshot.assignments)
	for i := range cloned.assignments {
		cloned.assignments[i].blockIDs = append([]string(nil), cloned.assignments[i].blockIDs...)
		cloned.assignments[i].cpus = cloned.assignments[i].cpus.Clone()
		cloned.assignments[i].eligible = cloned.assignments[i].eligible.Clone()
	}
	return cloned
}

var errSteadyFakeNUMAPinBudgetExhausted = errors.New(
	"steady fake-NUMA pin assignment budget exhausted")

var errSteadyFakeNUMASearchBudgetExhausted = errors.New(
	"steady fake-NUMA staged migration search budget exhausted")

type steadyFakeNUMAPinsForUnionFunc func(
	target machine.CPUSet,
	fakeKeys []string,
	demands map[string]partitionDemand,
	desired map[string]machine.CPUSet,
	topology *machine.CPUTopology,
	tracker *steadyFakeNUMASearchTracker,
) (map[string][]machine.CPUSet, error)

type steadyFakeNUMAProjectFunc func(
	demands []partitionDemand,
	fakeKeys []string,
	committed steadyFakeNUMACommittedSnapshot,
	desired map[string]machine.CPUSet,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
) (map[string]machine.CPUSet, error)

func newSteadyFakeNUMAPinBudgetExhaustedError(limit int) error {
	return fmt.Errorf(
		"staged fake reclaim pin assignment budget %d exhausted: %w",
		limit, errSteadyFakeNUMAPinBudgetExhausted)
}

var defaultSteadyFakeNUMASearchBudget = steadyFakeNUMASearchBudget{
	maxSolveAttempts:    steadyFakeNUMAStageMaxSolveAttempts,
	maxCandidateActions: steadyFakeNUMAStageMaxSearchActions,
}

// solveSteadyFakeNUMAWholeCore starts from the ordinary joint-flow result and
// improves the aggregate fake-NUMA reclaim assignment with exact whole-core
// pins. Search is deliberately bounded: one fast-path solve followed by an
// eight-state frontier and at most 32 total candidate solves.
func solveSteadyFakeNUMAWholeCore(
	demands []partitionDemand,
	fakeDemandKeys []string,
	topology *machine.CPUTopology,
) (map[string]machine.CPUSet, error) {
	return solveSteadyFakeNUMAWholeCoreWithFloors(
		demands, fakeDemandKeys, nil, topology)
}

func projectSteadyFakeNUMAStage(
	demands []partitionDemand,
	fakeKeys []string,
	committed steadyFakeNUMACommittedSnapshot,
	desired map[string]machine.CPUSet,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
) (map[string]machine.CPUSet, error) {
	return projectSteadyFakeNUMAStageWithBudget(
		demands, fakeKeys, committed, desired, floors, topology,
		defaultSteadyFakeNUMASearchBudget)
}

func projectSteadyFakeNUMAStageWithBudget(
	demands []partitionDemand,
	fakeKeys []string,
	committed steadyFakeNUMACommittedSnapshot,
	desired map[string]machine.CPUSet,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
	budget steadyFakeNUMASearchBudget,
) (map[string]machine.CPUSet, error) {
	return projectSteadyFakeNUMAStageWithBudgetAndPins(
		demands, fakeKeys, committed, desired, floors, topology, budget,
		steadyFakeNUMAPinsForUnionWithBudget)
}

func projectSteadyFakeNUMAStageWithBudgetAndPins(
	demands []partitionDemand,
	fakeKeys []string,
	committed steadyFakeNUMACommittedSnapshot,
	desired map[string]machine.CPUSet,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
	budget steadyFakeNUMASearchBudget,
	pinsForUnion steadyFakeNUMAPinsForUnionFunc,
) (map[string]machine.CPUSet, error) {
	if topology == nil {
		return nil, fmt.Errorf("cannot project steady fake-NUMA migration with nil topology")
	}
	if pinsForUnion == nil {
		return nil, fmt.Errorf("cannot project steady fake-NUMA migration with nil pin allocator")
	}
	if budget.maxSolveAttempts <= 0 || budget.maxCandidateActions <= 0 {
		return nil, fmt.Errorf(
			"invalid steady fake-NUMA search budget: solves=%d candidates=%d",
			budget.maxSolveAttempts, budget.maxCandidateActions)
	}
	if _, err := validateSteadyFakeNUMAFinal(
		demands, fakeKeys, desired, topology, nil, false,
	); err != nil {
		return nil, fmt.Errorf("invalid desired steady fake-NUMA assignment: %w", err)
	}
	desiredFake := unionPartitionAssignments(desired, fakeKeys)
	if err := assertCoreAligned(desiredFake, topology); err != nil {
		return nil, fmt.Errorf("invalid desired steady fake-NUMA assignment: %w", err)
	}
	if outside := committed.reclaim.Difference(topology.CPUDetails.CPUs()); !outside.IsEmpty() {
		return nil, fmt.Errorf(
			"invalid committed reclaim outside machine topology: %s", outside.String())
	}
	committedErr := validateCommittedSteadyFakeNUMASnapshot(committed, floors, topology)
	if committedErr != nil {
		if committed.reclaim.IsEmpty() {
			return cloneSteadyFakeNUMAAssignments(desired), nil
		}
		fakeSet := make(map[string]struct{}, len(fakeKeys))
		for _, key := range fakeKeys {
			fakeSet[key] = struct{}{}
		}
		repairDemands := append([]partitionDemand(nil), demands...)
		repairQuantity := 0
		for i := range repairDemands {
			if _, fake := fakeSet[repairDemands[i].key]; !fake {
				continue
			}
			preferred := committed.reclaim.Intersection(repairDemands[i].eligible)
			repairDemands[i].quantity = preferred.Size()
			repairDemands[i].preferred = preferred
			repairQuantity += preferred.Size()
		}
		if repairQuantity == committed.reclaim.Size() {
			baseline, solveErr := solveDisjointPartitionsWithCoreFloors(
				repairDemands, floors, topology)
			if solveErr == nil {
				repair, repairErr := solveSteadyFakeNUMADesiredWholeCore(
					repairDemands, fakeKeys, floors, topology, baseline)
				if repairErr == nil {
					contractErr := validateSteadyFakeNUMARepairBlockQuantities(
						demands, fakeKeys, repair)
					if contractErr == nil &&
						steadyFakeNUMAMigrationChurn(
							committed.reclaim, unionPartitionAssignments(repair, fakeKeys),
						) <= steadyFakeNUMAMaxMigratedCPUs {
						return repair, nil
					}
				}
			}
		}
		return cloneSteadyFakeNUMAAssignments(desired), nil
	}
	migrationLimit := steadyFakeNUMAMaxMigratedCPUs
	if steadyFakeNUMAMigrationChurn(committed.reclaim, desiredFake) <= migrationLimit {
		general.InfoS("steady fake NUMA migration reaches frozen target",
			"currentCPUSet", committed.reclaim.String(),
			"frozenTargetCPUSet", desiredFake.String(),
			"stageCPUSet", desiredFake.String(),
			"currentDistance", steadyFakeNUMAMigrationChurn(committed.reclaim, desiredFake),
			"nextDistance", 0,
			"stageChurn", steadyFakeNUMAMigrationChurn(committed.reclaim, desiredFake))
		return cloneSteadyFakeNUMAAssignments(desired), nil
	}

	cpusPerCore := topology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return nil, fmt.Errorf(
			"cannot project steady fake-NUMA migration with non-positive cpus per core %d",
			cpusPerCore)
	}
	fakeSet := make(map[string]struct{}, len(fakeKeys))
	for _, key := range fakeKeys {
		fakeSet[key] = struct{}{}
	}
	donorPreferred := machine.NewCPUSet()
	for _, demand := range demands {
		if _, fake := fakeSet[demand.key]; !fake {
			donorPreferred = donorPreferred.Union(demand.preferred)
		}
	}
	desiredOther := unionAssignmentsExcept(desired, fakeKeys)
	currentOnlySet := committed.reclaim.Difference(desiredFake)
	desiredOnlySet := desiredFake.Difference(committed.reclaim)
	currentOnly := steadyFakeNUMACoreSetsByPreference(
		currentOnlySet, currentOnlySet.Difference(desiredOther), topology)
	desiredOnly := steadyFakeNUMACoreSetsByPreference(
		desiredOnlySet, desiredOnlySet.Difference(donorPreferred), topology)
	common := committed.reclaim.Intersection(desiredFake)
	if len(currentOnly)*cpusPerCore != committed.reclaim.Difference(desiredFake).Size() ||
		len(desiredOnly)*cpusPerCore != desiredFake.Difference(committed.reclaim).Size() {
		return nil, fmt.Errorf("steady fake-NUMA migration contains fragmented core differences")
	}

	replacementBudget := migrationLimit / (2 * cpusPerCore)
	replacementPairs := len(currentOnly)
	if len(desiredOnly) < replacementPairs {
		replacementPairs = len(desiredOnly)
	}
	if replacementPairs <= replacementBudget {
		return cloneSteadyFakeNUMAAssignments(desired), nil
	}
	selectedAddedCount := len(desiredOnly) - (replacementPairs - replacementBudget)
	retainedCurrentCount := desiredFake.Size()/cpusPerCore -
		common.Size()/cpusPerCore - selectedAddedCount
	if selectedAddedCount < 0 || retainedCurrentCount < 0 ||
		selectedAddedCount > len(desiredOnly) || retainedCurrentCount > len(currentOnly) {
		return nil, fmt.Errorf("cannot derive bounded steady fake-NUMA migration cardinality")
	}

	var lastErr error
	currentDistance := steadyFakeNUMAMigrationChurn(committed.reclaim, desiredFake)
	selectionLimit := budget.maxCandidateActions + 1
	for replacementCoreCount := replacementBudget; replacementCoreCount >= 1; replacementCoreCount-- {
		selectedAddedCount = len(desiredOnly) - (replacementPairs - replacementCoreCount)
		retainedCurrentCount = desiredFake.Size()/cpusPerCore -
			common.Size()/cpusPerCore - selectedAddedCount
		if selectedAddedCount < 0 || retainedCurrentCount < 0 ||
			selectedAddedCount > len(desiredOnly) || retainedCurrentCount > len(currentOnly) {
			continue
		}

		addedSelections := steadyFakeNUMACoreSelections(
			desiredOnly, selectedAddedCount, selectionLimit)
		currentSelections := steadyFakeNUMACoreSelections(
			currentOnly, retainedCurrentCount, selectionLimit)
		tracker := &steadyFakeNUMASearchTracker{budget: budget}
		var best map[string]machine.CPUSet
		batchExhausted := false

	batchSearch:
		for _, selectedAdded := range addedSelections {
			for _, retainedCurrent := range currentSelections {
				if budgetErr := tracker.consumeCandidateAction(); budgetErr != nil {
					lastErr = budgetErr
					batchExhausted = true
					break batchSearch
				}
				target := common
				for _, core := range selectedAdded {
					target = target.Union(core)
				}
				for _, core := range retainedCurrent {
					target = target.Union(core)
				}
				stageDemands, stageDemandByKey, stageErr := steadyFakeNUMAStageDemands(
					demands, fakeSet, target, desired, topology)
				if stageErr != nil {
					lastErr = stageErr
					continue
				}
				pins, pinErr := pinsForUnion(
					target, fakeKeys, stageDemandByKey, desired, topology, tracker)
				if pinErr != nil {
					if errors.Is(pinErr, errSteadyFakeNUMAPinBudgetExhausted) {
						return nil, pinErr
					}
					lastErr = pinErr
					if errors.Is(pinErr, errSteadyFakeNUMASearchBudgetExhausted) {
						batchExhausted = true
						break batchSearch
					}
					continue
				}
				if budgetErr := tracker.consumeCandidateAction(); budgetErr != nil {
					lastErr = budgetErr
					batchExhausted = true
					break batchSearch
				}
				assignments, solveErr := solveSteadyFakeNUMAWithPinsBudget(
					stageDemands, fakeKeys, floors, pins, topology, &tracker.solveAttempts,
					tracker.budget.maxSolveAttempts)
				if solveErr != nil {
					lastErr = solveErr
					if tracker.solveAttempts >= budget.maxSolveAttempts {
						lastErr = fmt.Errorf(
							"steady fake-NUMA staged migration solve budget %d exhausted: %w",
							budget.maxSolveAttempts, solveErr)
						batchExhausted = true
						break batchSearch
					}
					continue
				}
				if _, validateErr := validateSteadyFakeNUMAFinal(
					stageDemands, fakeKeys, assignments, topology, nil, false,
				); validateErr != nil {
					lastErr = validateErr
					continue
				}
				nextFake := unionPartitionAssignments(assignments, fakeKeys)
				if alignErr := assertCoreAligned(nextFake, topology); alignErr != nil {
					lastErr = alignErr
					continue
				}
				if churn := steadyFakeNUMAMigrationChurn(committed.reclaim, nextFake); churn >
					migrationLimit {
					lastErr = fmt.Errorf("projected migration churn %d exceeds limit", churn)
					continue
				}
				if distance := steadyFakeNUMAMigrationChurn(nextFake, desiredFake); distance >=
					currentDistance {
					lastErr = fmt.Errorf(
						"projected migration target distance %d does not improve current distance %d",
						distance, currentDistance)
					continue
				}
				if best == nil || steadyFakeNUMAStageAssignmentLess(
					best, assignments, stageDemands, fakeKeys, desired, topology) {
					best = assignments
				}
			}
		}
		if len(addedSelections) >= selectionLimit || len(currentSelections) >= selectionLimit {
			lastErr = fmt.Errorf(
				"steady fake-NUMA staged migration search budget %d exhausted",
				budget.maxCandidateActions)
			batchExhausted = true
		}
		if best != nil && !batchExhausted {
			nextFake := unionPartitionAssignments(best, fakeKeys)
			general.InfoS("steady fake NUMA migration stage planned",
				"currentCPUSet", committed.reclaim.String(),
				"frozenTargetCPUSet", desiredFake.String(),
				"stageCPUSet", nextFake.String(),
				"currentDistance", currentDistance,
				"nextDistance", steadyFakeNUMAMigrationChurn(nextFake, desiredFake),
				"stageChurn", steadyFakeNUMAMigrationChurn(committed.reclaim, nextFake))
			return best, nil
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf(
			"no legal staged reclaim migration within %d changed CPU IDs: %w",
			migrationLimit, lastErr)
	}
	return nil, fmt.Errorf(
		"no legal staged reclaim migration within %d changed CPU IDs",
		migrationLimit)
}

func validateSteadyFakeNUMARepairBlockQuantities(
	demands []partitionDemand,
	fakeKeys []string,
	assignments map[string]machine.CPUSet,
) error {
	demandByKey := make(map[string]partitionDemand, len(demands))
	for _, demand := range demands {
		demandByKey[demand.key] = demand
	}

	requestedByBlock := make(map[string]int)
	assignedByBlock := make(map[string]int)
	for _, key := range fakeKeys {
		demand, found := demandByKey[key]
		if !found {
			return fmt.Errorf("repair contract demand %q is missing", key)
		}
		assigned, found := assignments[key]
		if !found {
			return fmt.Errorf("repair contract assignment %q is missing", key)
		}
		blockKey := steadyFakeNUMARepairBlockKey(key)
		requestedByBlock[blockKey] += demand.quantity
		assignedByBlock[blockKey] += assigned.Size()
	}
	for blockKey, requested := range requestedByBlock {
		if assigned := assignedByBlock[blockKey]; assigned != requested {
			return fmt.Errorf(
				"repair block %q assigned quantity %d, want %d",
				blockKey, assigned, requested)
		}
	}
	return nil
}

func steadyFakeNUMARepairBlockKey(demandKey string) string {
	const numaMarker = "\x00numa\x00"
	if index := strings.LastIndex(demandKey, numaMarker); index >= 0 {
		return demandKey[:index]
	}
	return demandKey
}

func steadyFakeNUMAStageDemands(
	demands []partitionDemand,
	fakeSet map[string]struct{},
	target machine.CPUSet,
	desired map[string]machine.CPUSet,
	topology *machine.CPUTopology,
) ([]partitionDemand, map[string]partitionDemand, error) {
	stageDemands := make([]partitionDemand, len(demands))
	stageDemandByKey := make(map[string]partitionDemand, len(demands))
	fakeDemandByKey := make(map[string]partitionDemand, len(fakeSet))
	for i, demand := range demands {
		stageDemand := demand
		if _, fake := fakeSet[demand.key]; fake {
			stageDemand.quantity = 0
			fakeDemandByKey[demand.key] = demand
		}
		stageDemands[i] = stageDemand
	}
	for _, core := range steadyFakeNUMACoreSets(target, topology) {
		keys := make([]string, 0, len(fakeDemandByKey))
		for key, demand := range fakeDemandByKey {
			if core.IsSubsetOf(demand.eligible) {
				keys = append(keys, key)
			}
		}
		if len(keys) == 0 {
			return nil, nil, fmt.Errorf(
				"steady fake-NUMA stage core %s has no eligible demand", core.String())
		}
		sort.Slice(keys, func(i, j int) bool {
			left, right := fakeDemandByKey[keys[i]], fakeDemandByKey[keys[j]]
			leftDesired := core.IsSubsetOf(desired[keys[i]])
			rightDesired := core.IsSubsetOf(desired[keys[j]])
			if leftDesired != rightDesired {
				return leftDesired
			}
			leftPreferred := core.IsSubsetOf(left.preferred)
			rightPreferred := core.IsSubsetOf(right.preferred)
			if leftPreferred != rightPreferred {
				return leftPreferred
			}
			return keys[i] < keys[j]
		})
		selected := keys[0]
		for i := range stageDemands {
			if stageDemands[i].key == selected {
				stageDemands[i].quantity += core.Size()
				break
			}
		}
	}
	assignedFakeQuantity := 0
	for _, stageDemand := range stageDemands {
		stageDemandByKey[stageDemand.key] = stageDemand
		if _, fake := fakeSet[stageDemand.key]; fake {
			assignedFakeQuantity += stageDemand.quantity
		}
	}
	if assignedFakeQuantity != target.Size() {
		return nil, nil, fmt.Errorf(
			"steady fake-NUMA stage target quantity %d differs from assigned quantity %d",
			target.Size(), assignedFakeQuantity)
	}
	return stageDemands, stageDemandByKey, nil
}

func (b *steadyFakeNUMASearchTracker) consumeCandidateAction() error {
	if b == nil || b.candidateActions >= b.budget.maxCandidateActions {
		limit := 0
		if b != nil {
			limit = b.budget.maxCandidateActions
		}
		return fmt.Errorf("steady fake-NUMA staged migration search budget %d exhausted: %w",
			limit, errSteadyFakeNUMASearchBudgetExhausted)
	}
	b.candidateActions++
	return nil
}

func buildSteadyFakeNUMACommittedSnapshot(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
) (steadyFakeNUMACommittedSnapshot, error) {
	snapshot := steadyFakeNUMACommittedSnapshot{
		assignments:         make([]steadyFakeNUMACommittedAssignment, 0, len(descriptors)),
		rawReclaimAggregate: machine.NewCPUSet(),
		reclaim:             machine.NewCPUSet(),
	}
	seenBlockIDs := make(map[string]struct{}, len(descriptors))
	realMandatory := machine.NewCPUSet()
	rawAggregateComponent := ""
	for _, descriptor := range descriptors {
		if descriptor.BlockID == "" {
			return steadyFakeNUMACommittedSnapshot{}, fmt.Errorf("committed descriptor has empty block ID")
		}
		if descriptor.ComponentKey == "" {
			return steadyFakeNUMACommittedSnapshot{}, fmt.Errorf(
				"committed block %q has empty component key", descriptor.BlockID)
		}
		if _, duplicate := seenBlockIDs[descriptor.BlockID]; duplicate {
			return steadyFakeNUMACommittedSnapshot{}, fmt.Errorf(
				"duplicate committed block ID %q", descriptor.BlockID)
		}
		seenBlockIDs[descriptor.BlockID] = struct{}{}
		if descriptor.Class == advisorBlockClassMandatoryReclaim &&
			descriptor.NUMAID != commonstate.FakedNUMAID {
			realMandatory = realMandatory.Union(descriptor.Committed)
		}
		if descriptor.Class == advisorBlockClassMandatoryReclaim &&
			descriptor.NUMAID == commonstate.FakedNUMAID {
			if rawAggregateComponent != "" && rawAggregateComponent != descriptor.ComponentKey {
				return steadyFakeNUMACommittedSnapshot{}, fmt.Errorf(
					"multiple committed fake-NUMA reclaim aggregates")
			}
			rawAggregateComponent = descriptor.ComponentKey
			snapshot.rawReclaimAggregate = snapshot.rawReclaimAggregate.Union(descriptor.Committed)
		}
	}
	assignmentByComponent := make(map[string]int, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.Class == advisorBlockClassReclaimOverlap ||
			descriptor.Class == advisorBlockClassStatic {
			continue
		}
		committed := descriptor.Committed.Clone()
		if descriptor.Class == advisorBlockClassMandatoryReclaim &&
			descriptor.NUMAID == commonstate.FakedNUMAID {
			committed = committed.Difference(realMandatory)
		}
		index, found := assignmentByComponent[descriptor.ComponentKey]
		if found {
			assignment := &snapshot.assignments[index]
			if assignment.class != descriptor.Class || assignment.numaID != descriptor.NUMAID {
				return steadyFakeNUMACommittedSnapshot{}, fmt.Errorf(
					"committed component %q has inconsistent class or NUMA", descriptor.ComponentKey)
			}
			assignment.blockIDs = append(assignment.blockIDs, descriptor.BlockID)
			if descriptor.BlockID < assignment.blockID {
				assignment.blockID = descriptor.BlockID
			}
			assignment.cpus = assignment.cpus.Union(committed)
			assignment.eligible = assignment.eligible.Intersection(
				descriptor.Eligible.Intersection(available))
			continue
		}
		assignment := steadyFakeNUMACommittedAssignment{
			blockID:      descriptor.BlockID,
			blockIDs:     []string{descriptor.BlockID},
			componentKey: descriptor.ComponentKey,
			class:        descriptor.Class,
			numaID:       descriptor.NUMAID,
			cpus:         committed.Clone(),
			eligible:     descriptor.Eligible.Intersection(available),
		}
		assignmentByComponent[descriptor.ComponentKey] = len(snapshot.assignments)
		snapshot.assignments = append(snapshot.assignments, assignment)
	}
	for i := range snapshot.assignments {
		sort.Strings(snapshot.assignments[i].blockIDs)
		if snapshot.assignments[i].class == advisorBlockClassMandatoryReclaim {
			snapshot.reclaim = snapshot.reclaim.Union(snapshot.assignments[i].cpus)
		}
	}
	sort.Slice(snapshot.assignments, func(i, j int) bool {
		return snapshot.assignments[i].blockID < snapshot.assignments[j].blockID
	})
	return snapshot, nil
}

func validateCommittedSteadyFakeNUMASnapshot(
	snapshot steadyFakeNUMACommittedSnapshot,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
) error {
	if topology == nil {
		return fmt.Errorf("topology is nil")
	}
	if outside := snapshot.reclaim.Difference(topology.CPUDetails.CPUs()); !outside.IsEmpty() {
		return fmt.Errorf("committed reclaim outside machine topology: %s", outside.String())
	}
	assignmentByBlockID := make(map[string]steadyFakeNUMACommittedAssignment, len(snapshot.assignments))
	assignmentByComponent := make(map[string]struct{}, len(snapshot.assignments))
	mandatoryReclaim := machine.NewCPUSet()
	for _, assignment := range snapshot.assignments {
		if assignment.blockID == "" {
			return fmt.Errorf("committed assignment has empty block ID")
		}
		componentKey := assignment.componentKey
		if componentKey == "" {
			componentKey = assignment.blockID
		}
		if _, duplicate := assignmentByComponent[componentKey]; duplicate {
			return fmt.Errorf("duplicate committed component %q", componentKey)
		}
		assignmentByComponent[componentKey] = struct{}{}
		blockIDs := append([]string{assignment.blockID}, assignment.blockIDs...)
		seenAssignmentBlockIDs := make(map[string]struct{}, len(blockIDs))
		for _, blockID := range blockIDs {
			if blockID == "" {
				return fmt.Errorf("committed assignment has empty block ID")
			}
			if _, duplicate := seenAssignmentBlockIDs[blockID]; duplicate {
				continue
			}
			seenAssignmentBlockIDs[blockID] = struct{}{}
			if _, duplicate := assignmentByBlockID[blockID]; duplicate {
				return fmt.Errorf("duplicate committed block ID %q", blockID)
			}
			assignmentByBlockID[blockID] = assignment
		}
		if outside := assignment.cpus.Difference(topology.CPUDetails.CPUs()); !outside.IsEmpty() {
			return fmt.Errorf(
				"committed block %q has CPUs outside machine topology: %s",
				assignment.blockID, outside.String())
		}
		if outside := assignment.cpus.Difference(assignment.eligible); !outside.IsEmpty() {
			return fmt.Errorf(
				"committed block %q is outside eligibility: %s",
				assignment.blockID, outside.String())
		}
		if assignment.class == advisorBlockClassMandatoryReclaim {
			mandatoryReclaim = mandatoryReclaim.Union(assignment.cpus)
		}
	}
	if !mandatoryReclaim.Equals(snapshot.reclaim) {
		return fmt.Errorf(
			"committed mandatory reclaim union %s does not match snapshot reclaim %s",
			mandatoryReclaim.String(), snapshot.reclaim.String())
	}
	if !snapshot.rawReclaimAggregate.Equals(snapshot.reclaim) {
		return fmt.Errorf(
			"raw committed reclaim aggregate %s does not match normalized reclaim leaves %s",
			snapshot.rawReclaimAggregate.String(), snapshot.reclaim.String())
	}
	if err := assertCoreAligned(snapshot.reclaim, topology); err != nil {
		return fmt.Errorf("committed reclaim is not core-aligned: %w", err)
	}
	for _, floor := range floors {
		assignment, found := assignmentByBlockID[floor.committedBlockID]
		if !found {
			return fmt.Errorf("committed core floor block %q is missing", floor.committedBlockID)
		}
		if assignment.class != advisorBlockClassMandatoryReclaim ||
			assignment.numaID == commonstate.FakedNUMAID {
			return fmt.Errorf(
				"committed floor block %q is not a real-NUMA mandatory reclaim block",
				floor.committedBlockID)
		}
		if len(coreAlignedCandidates(topology, assignment.cpus, assignment.cpus)) == 0 {
			return fmt.Errorf(
				"committed block %q violates its NUMA/core floor", floor.committedBlockID)
		}
	}
	for _, assignment := range snapshot.assignments {
		if assignment.class != advisorBlockClassMandatoryReclaim ||
			assignment.numaID != commonstate.FakedNUMAID {
			continue
		}
		if err := assertCoreAligned(assignment.cpus, topology); err != nil {
			return fmt.Errorf(
				"committed fake-NUMA block %q is not core-aligned: %w",
				assignment.blockID, err)
		}
	}
	used := machine.NewCPUSet()
	usedByBlock := make(map[int]string)
	for _, assignment := range snapshot.assignments {
		if overlap := used.Intersection(assignment.cpus); !overlap.IsEmpty() {
			other := ""
			for _, cpu := range overlap.ToSliceInt() {
				other = usedByBlock[cpu]
				if other != "" {
					break
				}
			}
			return fmt.Errorf(
				"committed blocks %q and %q overlap: %s",
				minString(other, assignment.blockID),
				maxString(other, assignment.blockID),
				overlap.String())
		}
		for _, cpu := range assignment.cpus.ToSliceInt() {
			usedByBlock[cpu] = assignment.blockID
		}
		used = used.Union(assignment.cpus)
	}
	return nil
}

func minString(left, right string) string {
	if left < right {
		return left
	}
	return right
}

func maxString(left, right string) string {
	if left > right {
		return left
	}
	return right
}

func steadyFakeNUMAStageAssignmentLess(
	left, right map[string]machine.CPUSet,
	demands []partitionDemand,
	fakeKeys []string,
	desired map[string]machine.CPUSet,
	topology *machine.CPUTopology,
) bool {
	leftDistance := steadyFakeNUMAQuotaDistance(left, desired, fakeKeys, topology)
	rightDistance := steadyFakeNUMAQuotaDistance(right, desired, fakeKeys, topology)
	if leftDistance != rightDistance {
		return leftDistance > rightDistance
	}
	leftCost, leftErr := steadyFakeNUMAStageAssignmentCost(left, demands, topology)
	rightCost, rightErr := steadyFakeNUMAStageAssignmentCost(right, demands, topology)
	if leftErr == nil && rightErr == nil && leftCost != rightCost {
		return leftCost > rightCost
	}
	return steadyFakeNUMAAssignmentSignature(left) > steadyFakeNUMAAssignmentSignature(right)
}

func steadyFakeNUMAQuotaDistance(
	assignments, desired map[string]machine.CPUSet,
	fakeKeys []string,
	topology *machine.CPUTopology,
) int {
	currentFake := unionPartitionAssignments(assignments, fakeKeys)
	desiredFake := unionPartitionAssignments(desired, fakeKeys)
	distance := 0
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		distance += absInt(
			currentFake.Intersection(numaCPUs).Size() -
				desiredFake.Intersection(numaCPUs).Size())
	}
	return distance
}

func steadyFakeNUMAStageAssignmentCost(
	assignments map[string]machine.CPUSet,
	demands []partitionDemand,
	topology *machine.CPUTopology,
) (int64, error) {
	sortedDemands, cpus, total, err := validatePartitionDemands(demands, topology)
	if err != nil {
		return 0, err
	}
	oldWeight, reclaimWeight, topologyWeight, err := partitionCostWeights(total, len(cpus), len(sortedDemands))
	if err != nil {
		return 0, err
	}
	dedicatedEligible := machine.NewCPUSet()
	for _, demand := range sortedDemands {
		if demand.class == advisorBlockClassDedicated {
			dedicatedEligible = dedicatedEligible.Union(demand.eligible)
		}
	}
	cpuRank := make(map[int]int, len(cpus))
	for rank, cpu := range cpus {
		cpuRank[cpu] = rank
	}
	includeTie := total < len(cpus)
	var totalCost int64
	for demandRank, demand := range sortedDemands {
		for _, cpu := range assignments[demand.key].ToSliceInt() {
			cost, costErr := partitionEdgeCost(
				cpu, cpuRank[cpu], demandRank, demand, dedicatedEligible, topology,
				oldWeight, reclaimWeight, topologyWeight, len(sortedDemands), includeTie)
			if costErr != nil {
				return 0, costErr
			}
			totalCost, err = checkedPartitionCostAdd(totalCost, cost)
			if err != nil {
				return 0, err
			}
		}
	}
	return totalCost, nil
}

func steadyFakeNUMAAssignmentSignature(assignments map[string]machine.CPUSet) string {
	keys := make([]string, 0, len(assignments))
	for key := range assignments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(assignments[key].String())
		builder.WriteByte('|')
	}
	return builder.String()
}

func solveSteadyFakeNUMAWholeCoreWithFloors(
	demands []partitionDemand,
	fakeDemandKeys []string,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
) (map[string]machine.CPUSet, error) {
	if topology == nil {
		return nil, fmt.Errorf("steady fake-NUMA whole-core topology is nil")
	}
	cpusPerCore := topology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return nil, fmt.Errorf(
			"steady fake-NUMA whole-core has non-positive cpus per core %d", cpusPerCore)
	}
	demandByKey := make(map[string]partitionDemand, len(demands))
	for _, demand := range demands {
		demandByKey[demand.key] = demand
	}
	requestedFakeQuantity := totalFakeQuantity(demandByKey, fakeDemandKeys)
	if requestedFakeQuantity%cpusPerCore != 0 {
		return nil, fmt.Errorf(
			"steady fake-NUMA quantity %d is not a whole-core multiple of %d",
			requestedFakeQuantity, cpusPerCore)
	}

	baseline, err := solveDisjointPartitionsWithCoreFloors(demands, floors, topology)
	if err != nil {
		return nil, fmt.Errorf("steady fake-NUMA baseline: %w", err)
	}
	fakeKeys, err := validateSteadyFakeNUMAFinal(
		demands, fakeDemandKeys, baseline, topology, nil, false)
	if err != nil {
		return nil, fmt.Errorf("steady fake-NUMA baseline: %w", err)
	}
	if len(fakeKeys) == 0 {
		return baseline, nil
	}
	return solveSteadyFakeNUMADesiredWholeCore(
		demands, fakeKeys, floors, topology, baseline)
}

func solveSteadyFakeNUMAWholeCoreWithFloorsAndProject(
	demands []partitionDemand,
	fakeDemandKeys []string,
	committed steadyFakeNUMACommittedSnapshot,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
	project steadyFakeNUMAProjectFunc,
) (map[string]machine.CPUSet, error) {
	if topology == nil {
		return nil, fmt.Errorf("steady fake-NUMA whole-core topology is nil")
	}
	if project == nil {
		return nil, fmt.Errorf("steady fake-NUMA whole-core projector is nil")
	}
	cpusPerCore := topology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return nil, fmt.Errorf(
			"steady fake-NUMA whole-core has non-positive cpus per core %d", cpusPerCore)
	}
	demandByKey := make(map[string]partitionDemand, len(demands))
	for _, demand := range demands {
		demandByKey[demand.key] = demand
	}
	requestedFakeQuantity := totalFakeQuantity(demandByKey, fakeDemandKeys)
	if requestedFakeQuantity%cpusPerCore != 0 {
		return nil, fmt.Errorf(
			"steady fake-NUMA quantity %d is not a whole-core multiple of %d",
			requestedFakeQuantity, cpusPerCore)
	}
	baseline, err := solveDisjointPartitionsWithCoreFloors(demands, floors, topology)
	if err != nil {
		return nil, fmt.Errorf("steady fake-NUMA baseline: %w", err)
	}
	fakeKeys, err := validateSteadyFakeNUMAFinal(
		demands, fakeDemandKeys, baseline, topology, nil, false)
	if err != nil {
		return nil, fmt.Errorf("steady fake-NUMA baseline: %w", err)
	}
	if len(fakeKeys) == 0 {
		return baseline, nil
	}
	desired, err := solveSteadyFakeNUMADesiredWholeCore(
		demands, fakeKeys, floors, topology, baseline)
	if err != nil {
		return nil, err
	}
	return project(demands, fakeKeys, committed.clone(), desired, floors, topology)
}

func solveSteadyFakeNUMADesiredWholeCore(
	demands []partitionDemand,
	fakeDemandKeys []string,
	floors []partitionCoreFloorConstraint,
	topology *machine.CPUTopology,
	baseline map[string]machine.CPUSet,
) (map[string]machine.CPUSet, error) {
	if topology == nil {
		return nil, fmt.Errorf("steady fake-NUMA desired whole-core topology is nil")
	}
	cpusPerCore := topology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return nil, fmt.Errorf(
			"steady fake-NUMA desired whole-core has non-positive cpus per core %d", cpusPerCore)
	}
	fakeKeys, err := validateSteadyFakeNUMAFinal(
		demands, fakeDemandKeys, baseline, topology, nil, false)
	if err != nil {
		return nil, fmt.Errorf("steady fake-NUMA desired baseline: %w", err)
	}
	demandByKey := make(map[string]partitionDemand, len(demands))
	for _, demand := range demands {
		demandByKey[demand.key] = demand
	}
	baselineFake := unionPartitionAssignments(baseline, fakeKeys)
	targetWhole := 0
	for _, key := range fakeKeys {
		targetWhole += demandByKey[key].quantity / cpusPerCore
	}
	baselineWhole := wholeCoreCount(baselineFake, topology)
	if baselineWhole >= targetWhole && fragmentedLogicalCPUCount(baselineFake, topology) == 0 {
		if _, validateErr := validateSteadyFakeNUMAFinal(
			demands, fakeKeys, baseline, topology, baseline, true,
		); validateErr == nil {
			return baseline, nil
		}
	}

	candidates := make(map[string][]coreAlignedCandidate, len(fakeKeys))
	baselineOther := unionAssignmentsExcept(baseline, fakeKeys)
	for _, key := range fakeKeys {
		demand := demandByKey[key]
		items := coreAlignedCandidates(topology, demand.eligible, demand.preferred)
		sort.SliceStable(items, func(i, j int) bool {
			iComplete := boolRank(items[i].cpus.IsSubsetOf(baseline[key]))
			jComplete := boolRank(items[j].cpus.IsSubsetOf(baseline[key]))
			if iComplete != jComplete {
				return iComplete > jComplete
			}
			iOld := items[i].cpus.Intersection(demand.preferred).Size()
			jOld := items[j].cpus.Intersection(demand.preferred).Size()
			if iOld != jOld {
				return iOld > jOld
			}
			iConflicts := items[i].cpus.Intersection(baselineOther).Size()
			jConflicts := items[j].cpus.Intersection(baselineOther).Size()
			if iConflicts != jConflicts {
				return iConflicts < jConflicts
			}
			return items[i].coreID < items[j].coreID
		})
		candidates[key] = items
	}

	initial := &steadyFakeNUMAPinState{
		pins:        baselineCompletePins(fakeKeys, baseline, candidates, demandByKey, cpusPerCore),
		assignments: baseline,
	}
	initial.signature = steadyFakeNUMAPinSignature(initial.pins)
	best := initial
	attempts := 0

	requestedFakeQuantity := targetWhole * cpusPerCore
	aggregateTarget := steadyFakeNUMAAggregateTargetByNUMA(
		fakeKeys, demandByKey, baselineFake, topology)
	if aggregateTarget.Size() == requestedFakeQuantity {
		if aggregatePins, pinErr := steadyFakeNUMAPinsForUnion(
			aggregateTarget, fakeKeys, demandByKey, baseline, topology,
		); pinErr == nil {
			if aggregate, solveErr := solveSteadyFakeNUMAWithPins(
				demands, fakeKeys, floors, aggregatePins, topology, &attempts,
			); solveErr == nil {
				if _, validateErr := validateSteadyFakeNUMAFinal(
					demands, fakeKeys, aggregate, topology, baseline, true,
				); validateErr == nil {
					return aggregate, nil
				}
			}
		}
	}

	fastPins := make(map[string][]machine.CPUSet, len(fakeKeys))
	for _, key := range fakeKeys {
		want := demandByKey[key].quantity / cpusPerCore
		for i := 0; i < want && i < len(candidates[key]); i++ {
			fastPins[key] = append(fastPins[key], candidates[key][i].cpus)
		}
	}
	if fast, solveErr := solveSteadyFakeNUMAWithPins(
		demands, fakeKeys, floors, fastPins, topology, &attempts,
	); solveErr == nil {
		if _, validateErr := validateSteadyFakeNUMAFinal(
			demands, fakeKeys, fast, topology, baseline, true,
		); validateErr == nil &&
			wholeCoreCount(unionPartitionAssignments(fast, fakeKeys), topology) >= targetWhole {
			return fast, nil
		}
	}

	frontierSeed := initial
	migrationPins := cloneSteadyFakeNUMAPins(initial.pins)
	for _, key := range fakeKeys {
		maxPins := demandByKey[key].quantity / cpusPerCore
		selected := make(map[string]struct{}, len(migrationPins[key]))
		for _, cpus := range migrationPins[key] {
			selected[cpus.String()] = struct{}{}
		}
		for _, candidate := range candidates[key] {
			if len(migrationPins[key]) >= maxPins {
				break
			}
			if _, found := selected[candidate.cpus.String()]; found {
				continue
			}
			migrationPins[key] = append(migrationPins[key], candidate.cpus)
			selected[candidate.cpus.String()] = struct{}{}
		}
		sortCPUSetSlice(migrationPins[key])
	}
	migrationSignature := steadyFakeNUMAPinSignature(migrationPins)
	if migrationSignature != initial.signature {
		if migrated, solveErr := solveSteadyFakeNUMAWithPins(
			demands, fakeKeys, floors, migrationPins, topology, &attempts,
		); solveErr == nil {
			if _, validateErr := validateSteadyFakeNUMAFinal(
				demands, fakeKeys, migrated, topology, baseline, true,
			); validateErr == nil {
				frontierSeed = &steadyFakeNUMAPinState{
					pins:        migrationPins,
					assignments: migrated,
					signature:   migrationSignature,
				}
				if steadyFakeNUMAStateLess(best, frontierSeed, fakeKeys, demandByKey, topology) {
					best = frontierSeed
				}
			}
		}
	}

	frontier := []*steadyFakeNUMAPinState{frontierSeed}
	seen := map[string]struct{}{initial.signature: {}}
	seen[frontierSeed.signature] = struct{}{}
	actions := 0
	for len(frontier) > 0 && attempts < steadyFakeNUMAMaxSolveAttempts {
		next := make([]*steadyFakeNUMAPinState, 0, steadyFakeNUMAFrontierWidth)
		for _, state := range frontier {
			for _, key := range fakeKeys {
				maxPins := demandByKey[key].quantity / cpusPerCore
				neighbors := steadyFakeNUMAPinNeighbors(
					state.pins, key, candidates[key], maxPins, &actions)
				for _, pins := range neighbors {
					signature := steadyFakeNUMAPinSignature(pins)
					if _, found := seen[signature]; found {
						continue
					}
					seen[signature] = struct{}{}
					assignments, solveErr := solveSteadyFakeNUMAWithPins(
						demands, fakeKeys, floors, pins, topology, &attempts)
					if solveErr != nil {
						if attempts >= steadyFakeNUMAMaxSolveAttempts {
							break
						}
						continue
					}
					if _, validateErr := validateSteadyFakeNUMAFinal(
						demands, fakeKeys, assignments, topology, baseline, true,
					); validateErr != nil {
						continue
					}
					candidate := &steadyFakeNUMAPinState{
						pins: pins, assignments: assignments, signature: signature,
					}
					if steadyFakeNUMAStateLess(best, candidate, fakeKeys, demandByKey, topology) {
						best = candidate
					}
					next = append(next, candidate)
					if attempts >= steadyFakeNUMAMaxSolveAttempts ||
						actions >= steadyFakeNUMAMaxCandidateActions {
						break
					}
				}
				if attempts >= steadyFakeNUMAMaxSolveAttempts ||
					actions >= steadyFakeNUMAMaxCandidateActions {
					break
				}
			}
			if attempts >= steadyFakeNUMAMaxSolveAttempts ||
				actions >= steadyFakeNUMAMaxCandidateActions {
				break
			}
		}
		sort.Slice(next, func(i, j int) bool {
			return steadyFakeNUMAStateLess(next[j], next[i], fakeKeys, demandByKey, topology)
		})
		if len(next) > steadyFakeNUMAFrontierWidth {
			next = next[:steadyFakeNUMAFrontierWidth]
		}
		frontier = next
		if wholeCoreCount(unionPartitionAssignments(best.assignments, fakeKeys), topology) >= targetWhole {
			break
		}
	}

	if _, err := validateSteadyFakeNUMAFinal(
		demands, fakeKeys, best.assignments, topology, baseline, true,
	); err != nil {
		return nil, fmt.Errorf("steady fake-NUMA final validation: %w", err)
	}
	return best.assignments, nil
}

func steadyFakeNUMAAggregateTargetByNUMA(
	fakeKeys []string,
	demands map[string]partitionDemand,
	baselineFake machine.CPUSet,
	topology *machine.CPUTopology,
) machine.CPUSet {
	quantityByNUMA := make(map[int]int)
	eligibleByNUMA := make(map[int]machine.CPUSet)
	preferredByNUMA := make(map[int]machine.CPUSet)
	globalQuantity := 0
	globalEligible := machine.NewCPUSet()
	globalPreferred := machine.NewCPUSet()
	for _, key := range fakeKeys {
		demand := demands[key]
		numaIDs := topology.CPUDetails.KeepOnly(demand.eligible).NUMANodes().ToSliceInt()
		if len(numaIDs) == 1 {
			numaID := numaIDs[0]
			quantityByNUMA[numaID] += demand.quantity
			eligibleByNUMA[numaID] = eligibleByNUMA[numaID].Union(demand.eligible)
			preferredByNUMA[numaID] = preferredByNUMA[numaID].Union(demand.preferred)
			continue
		}
		globalQuantity += demand.quantity
		globalEligible = globalEligible.Union(demand.eligible)
		globalPreferred = globalPreferred.Union(demand.preferred)
	}

	target := machine.NewCPUSet()
	for numaID, quantity := range quantityByNUMA {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		target = target.Union(takeCoreAlignedCPUSetByTiers(
			topology,
			eligibleByNUMA[numaID].Intersection(numaCPUs),
			[]machine.CPUSet{
				baselineFake.Intersection(numaCPUs),
				preferredByNUMA[numaID].Intersection(numaCPUs),
			},
			quantity,
		))
	}
	if globalQuantity > 0 {
		target = target.Union(takeCoreAlignedCPUSetByTiers(
			topology,
			globalEligible.Difference(target),
			[]machine.CPUSet{
				baselineFake.Difference(target),
				globalPreferred.Difference(target),
			},
			globalQuantity,
		))
	}
	return target
}

func steadyFakeNUMACoreSets(
	cpus machine.CPUSet,
	topology *machine.CPUTopology,
) []machine.CPUSet {
	return steadyFakeNUMACoreSetsByPreference(cpus, cpus, topology)
}

func steadyFakeNUMACoreSetsByPreference(
	cpus machine.CPUSet,
	preferred machine.CPUSet,
	topology *machine.CPUTopology,
) []machine.CPUSet {
	candidates := coreAlignedCandidates(topology, cpus, preferred)
	result := make([]machine.CPUSet, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.cpus)
	}
	return result
}

func steadyFakeNUMACoreSelections(
	values []machine.CPUSet,
	want int,
	limit int,
) [][]machine.CPUSet {
	if want < 0 || want > len(values) || limit <= 0 {
		return nil
	}
	result := make([][]machine.CPUSet, 0)
	selected := make([]machine.CPUSet, 0, want)
	var visit func(int)
	visit = func(start int) {
		if len(result) >= limit {
			return
		}
		if len(selected) == want {
			result = append(result, append([]machine.CPUSet(nil), selected...))
			return
		}
		remaining := want - len(selected)
		for index := start; index <= len(values)-remaining; index++ {
			selected = append(selected, values[index])
			visit(index + 1)
			selected = selected[:len(selected)-1]
			if len(result) >= limit {
				return
			}
		}
	}
	visit(0)
	return result
}

func steadyFakeNUMAPinsForUnion(
	target machine.CPUSet,
	fakeKeys []string,
	demands map[string]partitionDemand,
	desired map[string]machine.CPUSet,
	topology *machine.CPUTopology,
) (map[string][]machine.CPUSet, error) {
	tracker := &steadyFakeNUMASearchTracker{budget: steadyFakeNUMASearchBudget{
		maxCandidateActions: steadyFakeNUMAMaxCandidateActions,
	}}
	pins, err := steadyFakeNUMAPinsForUnionWithBudget(
		target, fakeKeys, demands, desired, topology, tracker)
	if errors.Is(err, errSteadyFakeNUMASearchBudgetExhausted) {
		return nil, newSteadyFakeNUMAPinBudgetExhaustedError(
			steadyFakeNUMAMaxCandidateActions)
	}
	return pins, err
}

func steadyFakeNUMAPinsForUnionWithBudget(
	target machine.CPUSet,
	fakeKeys []string,
	demands map[string]partitionDemand,
	desired map[string]machine.CPUSet,
	topology *machine.CPUTopology,
	tracker *steadyFakeNUMASearchTracker,
) (map[string][]machine.CPUSet, error) {
	keys := append([]string(nil), fakeKeys...)
	sort.Strings(keys)
	capacity := make(map[string]int, len(keys))
	for _, key := range keys {
		demand, found := demands[key]
		if !found {
			return nil, fmt.Errorf("fake demand key %q is missing", key)
		}
		if demand.quantity%topology.CPUsPerCore() != 0 {
			return nil, fmt.Errorf("fake demand %q quantity is not core aligned", key)
		}
		capacity[key] = demand.quantity / topology.CPUsPerCore()
	}
	cores := steadyFakeNUMACoreSets(target, topology)
	if len(cores)*topology.CPUsPerCore() != target.Size() {
		return nil, fmt.Errorf("staged fake reclaim target is not core aligned")
	}
	eligibleKeysByCore := make([][]string, len(cores))
	suffixEligibleCounts := make([]map[string]int, len(cores)+1)
	suffixEligibleCounts[len(cores)] = make(map[string]int, len(keys))
	for index := len(cores) - 1; index >= 0; index-- {
		suffixEligibleCounts[index] = cloneStringIntMap(suffixEligibleCounts[index+1])
		for _, key := range keys {
			if !cores[index].IsSubsetOf(demands[key].eligible) {
				continue
			}
			eligibleKeysByCore[index] = append(eligibleKeysByCore[index], key)
			suffixEligibleCounts[index][key]++
		}
		if len(eligibleKeysByCore[index]) == 0 {
			return nil, fmt.Errorf("staged fake reclaim target core %s cannot be assigned to any fake demand",
				cores[index].String())
		}
	}
	for _, key := range keys {
		if suffixEligibleCounts[0][key] < capacity[key] {
			return nil, fmt.Errorf("staged fake reclaim target has %d assignable cores for demand %q, want %d",
				suffixEligibleCounts[0][key], key, capacity[key])
		}
	}

	pins := make(map[string][]machine.CPUSet, len(keys))
	var assign func(int) bool
	assign = func(index int) bool {
		for _, key := range keys {
			if len(pins[key]) > capacity[key] ||
				len(pins[key])+suffixEligibleCounts[index][key] < capacity[key] {
				return false
			}
		}
		if tracker.consumeCandidateAction() != nil {
			return false
		}
		if index == len(cores) {
			for _, key := range keys {
				if len(pins[key]) != capacity[key] {
					return false
				}
			}
			return true
		}
		core := cores[index]
		candidateKeys := append([]string(nil), eligibleKeysByCore[index]...)
		sort.SliceStable(candidateKeys, func(i, j int) bool {
			leftDesired := boolRank(core.IsSubsetOf(desired[candidateKeys[i]]))
			rightDesired := boolRank(core.IsSubsetOf(desired[candidateKeys[j]]))
			if leftDesired != rightDesired {
				return leftDesired > rightDesired
			}
			leftOld := boolRank(core.IsSubsetOf(demands[candidateKeys[i]].preferred))
			rightOld := boolRank(core.IsSubsetOf(demands[candidateKeys[j]].preferred))
			return leftOld > rightOld
		})
		for _, key := range candidateKeys {
			if len(pins[key]) >= capacity[key] {
				continue
			}
			pins[key] = append(pins[key], core)
			if assign(index + 1) {
				return true
			}
			pins[key] = pins[key][:len(pins[key])-1]
			if tracker.candidateActions >= tracker.budget.maxCandidateActions {
				return false
			}
		}
		return false
	}
	if !assign(0) {
		if tracker.candidateActions >= tracker.budget.maxCandidateActions {
			return nil, fmt.Errorf("steady fake-NUMA staged migration search budget %d exhausted: %w",
				tracker.budget.maxCandidateActions, errSteadyFakeNUMASearchBudgetExhausted)
		}
		return nil, fmt.Errorf("staged fake reclaim target cannot be assigned to fake demands")
	}
	for _, key := range keys {
		sortCPUSetSlice(pins[key])
	}
	return pins, nil
}

func cloneSteadyFakeNUMAAssignments(
	source map[string]machine.CPUSet,
) map[string]machine.CPUSet {
	result := make(map[string]machine.CPUSet, len(source))
	for key, cpus := range source {
		result[key] = cpus.Clone()
	}
	return result
}

func solveSteadyFakeNUMAWithPins(
	demands []partitionDemand,
	fakeKeys []string,
	floors []partitionCoreFloorConstraint,
	pins map[string][]machine.CPUSet,
	topology *machine.CPUTopology,
	attempts *int,
) (map[string]machine.CPUSet, error) {
	return solveSteadyFakeNUMAWithPinsBudget(
		demands, fakeKeys, floors, pins, topology, attempts,
		steadyFakeNUMAMaxSolveAttempts)
}

func solveSteadyFakeNUMAWithPinsBudget(
	demands []partitionDemand,
	fakeKeys []string,
	floors []partitionCoreFloorConstraint,
	pins map[string][]machine.CPUSet,
	topology *machine.CPUTopology,
	attempts *int,
	maxAttempts int,
) (map[string]machine.CPUSet, error) {
	if *attempts >= maxAttempts {
		return nil, fmt.Errorf(
			"steady fake-NUMA solve budget %d exhausted", maxAttempts)
	}
	*attempts++

	fakeSet := make(map[string]struct{}, len(fakeKeys))
	for _, key := range fakeKeys {
		fakeSet[key] = struct{}{}
	}
	expanded := make([]partitionDemand, 0, len(demands)+16)
	pinParent := make(map[string]string)
	pinnedParents := make(map[string]struct{}, len(fakeKeys))
	for _, demand := range demands {
		if _, fake := fakeSet[demand.key]; !fake {
			expanded = append(expanded, demand)
			continue
		}
		residual := demand.quantity
		for i, cpus := range pins[demand.key] {
			if cpus.Size() != topology.CPUsPerCore() || !cpus.IsSubsetOf(demand.eligible) {
				return nil, fmt.Errorf(
					"steady fake-NUMA pin %s[%d]=%s is invalid", demand.key, i, cpus.String())
			}
			key := fmt.Sprintf("%s\x00whole-core\x00%06d", demand.key, i)
			expanded = append(expanded, partitionDemand{
				key:       key,
				quantity:  cpus.Size(),
				eligible:  cpus,
				preferred: cpus,
				class:     advisorBlockClassMandatoryReclaim,
			})
			pinParent[key] = demand.key
			pinnedParents[demand.key] = struct{}{}
			residual -= cpus.Size()
		}
		if residual < 0 {
			return nil, fmt.Errorf("steady fake-NUMA pins exceed demand %q quantity", demand.key)
		}
		if residual > 0 {
			demand.quantity = residual
			expanded = append(expanded, demand)
		}
	}
	effectiveFloors := make([]partitionCoreFloorConstraint, 0, len(floors))
	for _, floor := range floors {
		if _, pinned := pinnedParents[floor.demandKey]; !pinned {
			effectiveFloors = append(effectiveFloors, floor)
		}
	}
	raw, err := solveDisjointPartitionsWithCoreFloors(expanded, effectiveFloors, topology)
	if err != nil {
		return nil, err
	}
	result := make(map[string]machine.CPUSet, len(demands))
	for _, demand := range demands {
		result[demand.key] = machine.NewCPUSet()
	}
	for key, cpus := range raw {
		parent := pinParent[key]
		if parent == "" {
			parent = key
		}
		result[parent] = result[parent].Union(cpus)
	}
	return result, nil
}

func steadyFakeNUMAPinNeighbors(
	current map[string][]machine.CPUSet,
	key string,
	candidates []coreAlignedCandidate,
	maxPins int,
	actions *int,
) []map[string][]machine.CPUSet {
	result := make([]map[string][]machine.CPUSet, 0)
	selected := current[key]
	selectedSignatures := make(map[string]struct{}, len(selected))
	for _, cpus := range selected {
		selectedSignatures[cpus.String()] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, found := selectedSignatures[candidate.cpus.String()]; found {
			continue
		}
		if *actions >= steadyFakeNUMAMaxCandidateActions {
			break
		}
		if len(selected) < maxPins {
			next := cloneSteadyFakeNUMAPins(current)
			next[key] = append(next[key], candidate.cpus)
			sortCPUSetSlice(next[key])
			result = append(result, next)
			*actions++
		}
		for replace := range selected {
			if *actions >= steadyFakeNUMAMaxCandidateActions {
				break
			}
			next := cloneSteadyFakeNUMAPins(current)
			next[key][replace] = candidate.cpus
			sortCPUSetSlice(next[key])
			result = append(result, next)
			*actions++
		}
	}
	return result
}

func baselineCompletePins(
	fakeKeys []string,
	baseline map[string]machine.CPUSet,
	candidates map[string][]coreAlignedCandidate,
	demands map[string]partitionDemand,
	cpusPerCore int,
) map[string][]machine.CPUSet {
	result := make(map[string][]machine.CPUSet, len(fakeKeys))
	for _, key := range fakeKeys {
		limit := demands[key].quantity / cpusPerCore
		for _, candidate := range candidates[key] {
			if len(result[key]) >= limit {
				break
			}
			if candidate.cpus.IsSubsetOf(baseline[key]) {
				result[key] = append(result[key], candidate.cpus)
			}
		}
		sortCPUSetSlice(result[key])
	}
	return result
}

func validateSteadyFakeNUMAFinal(
	demands []partitionDemand,
	fakeDemandKeys []string,
	assignments map[string]machine.CPUSet,
	topology *machine.CPUTopology,
	baseline map[string]machine.CPUSet,
	enforceImprovement bool,
) ([]string, error) {
	if topology == nil {
		return nil, fmt.Errorf("topology is nil")
	}
	demandByKey := make(map[string]partitionDemand, len(demands))
	for _, demand := range demands {
		demandByKey[demand.key] = demand
	}
	fakeKeys := append([]string(nil), fakeDemandKeys...)
	sort.Strings(fakeKeys)
	seenFake := make(map[string]struct{}, len(fakeKeys))
	for _, key := range fakeKeys {
		if _, duplicate := seenFake[key]; duplicate {
			return nil, fmt.Errorf("duplicate fake demand key %q", key)
		}
		seenFake[key] = struct{}{}
		if _, found := demandByKey[key]; !found {
			return nil, fmt.Errorf("fake demand key %q is missing", key)
		}
	}

	used := machine.NewCPUSet()
	for _, demand := range demands {
		cpus, found := assignments[demand.key]
		if !found {
			return nil, fmt.Errorf("demand %q has no assignment", demand.key)
		}
		if cpus.Size() != demand.quantity {
			return nil, fmt.Errorf(
				"demand %q assigned quantity %d, want %d", demand.key, cpus.Size(), demand.quantity)
		}
		if !cpus.IsSubsetOf(demand.eligible) {
			return nil, fmt.Errorf("demand %q assignment is outside eligibility", demand.key)
		}
		if !used.Intersection(cpus).IsEmpty() {
			return nil, fmt.Errorf("demand %q overlaps an earlier assignment", demand.key)
		}
		used = used.Union(cpus)
	}
	if !enforceImprovement || baseline == nil {
		return fakeKeys, nil
	}

	baseFake := unionPartitionAssignments(baseline, fakeKeys)
	finalFake := unionPartitionAssignments(assignments, fakeKeys)
	if finalFake.Size() != baseFake.Size() {
		return nil, fmt.Errorf(
			"fake reclaim quantity changed from %d to %d", baseFake.Size(), finalFake.Size())
	}
	if wholeCoreCount(finalFake, topology) < wholeCoreCount(baseFake, topology) {
		return nil, fmt.Errorf("fake reclaim whole-core count regressed")
	}
	if err := assertCoreAligned(finalFake, topology); err != nil {
		return nil, fmt.Errorf(
			"no whole-core repair found within migration limit %d: %w",
			steadyFakeNUMAMaxMigratedCPUs, err)
	}
	return fakeKeys, nil
}

func planSteadyFakeNUMACoreCapacityQuotas(
	quantity int,
	oldPreferred, eligible machine.CPUSet,
	topology *machine.CPUTopology,
	skipNUMAs sets.Int,
	minimumByNUMA map[int]int,
) (map[int]int, error) {
	return planSteadyFakeNUMACoreCapacityQuotasWithLimits(
		quantity, oldPreferred, eligible, topology, skipNUMAs, minimumByNUMA, nil, nil)
}

func planSteadyFakeNUMACoreCapacityQuotasWithLimits(
	quantity int,
	oldPreferred, eligible machine.CPUSet,
	topology *machine.CPUTopology,
	skipNUMAs sets.Int,
	minimumByNUMA, maximumByNUMA, mandatoryByNUMA map[int]int,
) (map[int]int, error) {
	if topology == nil {
		return nil, fmt.Errorf("cannot plan steady fake reclaim quotas with nil CPU topology")
	}
	cpusPerCore := topology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return nil, fmt.Errorf(
			"cannot plan steady fake reclaim quotas with non-positive cpus per core %d", cpusPerCore)
	}
	if quantity < 0 {
		return nil, fmt.Errorf("steady fake reclaim has negative quantity %d", quantity)
	}
	mandatoryQuantity := 0
	for _, quantity := range mandatoryByNUMA {
		mandatoryQuantity += quantity
	}
	if (quantity+mandatoryQuantity)%cpusPerCore != 0 {
		return nil, fmt.Errorf(
			"steady mandatory reclaim quantity %d is not a whole-core multiple of %d",
			quantity+mandatoryQuantity, cpusPerCore)
	}

	numaIDs := topology.CPUDetails.KeepOnly(eligible).NUMANodes().ToSliceInt()
	sort.Ints(numaIDs)
	type quotaOption struct {
		quantity int
		whole    int
		distance int
	}
	options := make(map[int][]quotaOption, len(numaIDs))
	required := 0
	for _, numaID := range numaIDs {
		if skipNUMAs.Has(numaID) {
			continue
		}
		numaEligible := eligible.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		completeCapacity := len(coreAlignedCandidates(
			topology, numaEligible, oldPreferred.Intersection(numaEligible))) * cpusPerCore
		minimum := minimumByNUMA[numaID]
		if minimum < 0 {
			return nil, fmt.Errorf("steady fake reclaim NUMA %d has negative minimum %d", numaID, minimum)
		}
		if minimum > numaEligible.Size() {
			return nil, fmt.Errorf(
				"steady fake reclaim NUMA %d minimum %d exceeds eligible capacity %d",
				numaID, minimum, numaEligible.Size())
		}
		mandatory := mandatoryByNUMA[numaID]
		if (minimum+mandatory)%cpusPerCore != 0 {
			return nil, fmt.Errorf(
				"steady mandatory reclaim NUMA %d minimum %d plus real mandatory %d is not a whole-core multiple of %d",
				numaID, minimum, mandatory, cpusPerCore)
		}
		required += minimum
		oldCount := oldPreferred.Intersection(numaEligible).Size()
		maximum := numaEligible.Size()
		if configured, found := maximumByNUMA[numaID]; found && configured < maximum {
			maximum = configured
		}
		if mandatory == 0 && completeCapacity < maximum {
			maximum = completeCapacity
		}
		maximum -= (maximum + mandatory) % cpusPerCore
		if maximum < minimum {
			return nil, fmt.Errorf(
				"steady fake reclaim NUMA %d maximum %d is smaller than minimum %d",
				numaID, maximum, minimum)
		}
		for quota := minimum; quota <= maximum && quota <= quantity; quota += cpusPerCore {
			whole := (quota + mandatory) / cpusPerCore
			if mandatory == 0 && whole*cpusPerCore > completeCapacity {
				whole = completeCapacity / cpusPerCore
			}
			options[numaID] = append(options[numaID], quotaOption{
				quantity: quota,
				whole:    whole,
				distance: absInt(quota - oldCount),
			})
		}
	}
	if quantity < required {
		return nil, fmt.Errorf(
			"steady fake reclaim quantity %d is smaller than required steady minimum %d",
			quantity, required)
	}

	type quotaState struct {
		quotas   map[int]int
		whole    int
		distance int
	}
	states := map[int]quotaState{0: {quotas: make(map[int]int)}}
	visited := 1
	for _, numaID := range numaIDs {
		if skipNUMAs.Has(numaID) {
			continue
		}
		next := make(map[int]quotaState)
		for total, state := range states {
			for _, option := range options[numaID] {
				newTotal := total + option.quantity
				if newTotal > quantity {
					continue
				}
				candidate := quotaState{
					quotas:   cloneIntMap(state.quotas),
					whole:    state.whole + option.whole,
					distance: state.distance + option.distance,
				}
				candidate.quotas[numaID] = option.quantity
				current, found := next[newTotal]
				if !found || quotaStateLess(current, candidate, numaIDs) {
					if !found {
						visited++
						if visited > steadyFakeNUMAQuotaStateBudget {
							return nil, fmt.Errorf(
								"steady fake reclaim quota state budget %d exhausted",
								steadyFakeNUMAQuotaStateBudget)
						}
					}
					next[newTotal] = candidate
				}
			}
		}
		states = next
	}
	selected, found := states[quantity]
	if !found {
		return nil, fmt.Errorf(
			"steady fake reclaim has insufficient core-capacity quota for quantity %d", quantity)
	}
	return selected.quotas, nil
}

func quotaStateLess(left, right struct {
	quotas   map[int]int
	whole    int
	distance int
}, numaIDs []int,
) bool {
	if left.whole != right.whole {
		return left.whole < right.whole
	}
	if left.distance != right.distance {
		return left.distance > right.distance
	}
	for _, numaID := range numaIDs {
		if left.quotas[numaID] != right.quotas[numaID] {
			return left.quotas[numaID] > right.quotas[numaID]
		}
	}
	return false
}

func steadyFakeNUMAStateLess(
	left, right *steadyFakeNUMAPinState,
	fakeKeys []string,
	demands map[string]partitionDemand,
	topology *machine.CPUTopology,
) bool {
	leftScore := steadyFakeNUMAAssignmentScore(left.assignments, fakeKeys, demands, topology, left.signature)
	rightScore := steadyFakeNUMAAssignmentScore(right.assignments, fakeKeys, demands, topology, right.signature)
	if leftScore.wholeCores != rightScore.wholeCores {
		return leftScore.wholeCores < rightScore.wholeCores
	}
	if leftScore.retainedOldCores != rightScore.retainedOldCores {
		return leftScore.retainedOldCores < rightScore.retainedOldCores
	}
	if leftScore.retainedOldCPUs != rightScore.retainedOldCPUs {
		return leftScore.retainedOldCPUs < rightScore.retainedOldCPUs
	}
	if leftScore.migratedCPUs != rightScore.migratedCPUs {
		return leftScore.migratedCPUs > rightScore.migratedCPUs
	}
	return leftScore.signature > rightScore.signature
}

func steadyFakeNUMAAssignmentScore(
	assignments map[string]machine.CPUSet,
	fakeKeys []string,
	demands map[string]partitionDemand,
	topology *machine.CPUTopology,
	signature string,
) steadyFakeNUMAScore {
	fake := unionPartitionAssignments(assignments, fakeKeys)
	old := machine.NewCPUSet()
	for _, key := range fakeKeys {
		old = old.Union(demands[key].preferred)
	}
	return steadyFakeNUMAScore{
		wholeCores:       wholeCoreCount(fake, topology),
		retainedOldCores: wholeCoreCount(fake.Intersection(old), topology),
		retainedOldCPUs:  fake.Intersection(old).Size(),
		migratedCPUs:     steadyFakeNUMAMigrationChurn(old, fake),
		signature:        signature,
	}
}

func steadyFakeNUMAMigrationChurn(old, current machine.CPUSet) int {
	removed := old.Difference(current).Size()
	added := current.Difference(old).Size()
	if removed < added {
		return 2 * removed
	}
	return 2 * added
}

func wholeCoreCount(cpus machine.CPUSet, topology *machine.CPUTopology) int {
	if topology == nil {
		return 0
	}
	return len(coreAlignedCandidates(topology, cpus, machine.NewCPUSet()))
}

func fragmentedLogicalCPUCount(cpus machine.CPUSet, topology *machine.CPUTopology) int {
	if topology == nil {
		return cpus.Size()
	}
	complete := machine.NewCPUSet()
	for _, candidate := range coreAlignedCandidates(topology, cpus, machine.NewCPUSet()) {
		complete = complete.Union(candidate.cpus)
	}
	return cpus.Difference(complete).Size()
}

func unionPartitionAssignments(assignments map[string]machine.CPUSet, keys []string) machine.CPUSet {
	result := machine.NewCPUSet()
	for _, key := range keys {
		result = result.Union(assignments[key])
	}
	return result
}

func unionAssignmentsExcept(assignments map[string]machine.CPUSet, excluded []string) machine.CPUSet {
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, key := range excluded {
		excludedSet[key] = struct{}{}
	}
	result := machine.NewCPUSet()
	for key, cpus := range assignments {
		if _, found := excludedSet[key]; !found {
			result = result.Union(cpus)
		}
	}
	return result
}

func totalFakeQuantity(demands map[string]partitionDemand, keys []string) int {
	total := 0
	for _, key := range keys {
		total += demands[key].quantity
	}
	return total
}

func cloneSteadyFakeNUMAPins(source map[string][]machine.CPUSet) map[string][]machine.CPUSet {
	result := make(map[string][]machine.CPUSet, len(source))
	for key, cpus := range source {
		result[key] = append([]machine.CPUSet(nil), cpus...)
	}
	return result
}

func steadyFakeNUMAPinSignature(pins map[string][]machine.CPUSet) string {
	keys := make([]string, 0, len(pins))
	for key := range pins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		values := append([]machine.CPUSet(nil), pins[key]...)
		sortCPUSetSlice(values)
		for _, cpus := range values {
			builder.WriteString(cpus.String())
			builder.WriteByte(';')
		}
		builder.WriteByte('|')
	}
	return builder.String()
}

func sortCPUSetSlice(values []machine.CPUSet) {
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i].ToSliceInt(), values[j].ToSliceInt()
		for index := 0; index < len(left) && index < len(right); index++ {
			if left[index] != right[index] {
				return left[index] < right[index]
			}
		}
		return len(left) < len(right)
	})
}

func boolRank(value bool) int {
	if value {
		return 1
	}
	return 0
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func cloneIntMap(source map[int]int) map[int]int {
	result := make(map[int]int, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneStringIntMap(source map[string]int) map[string]int {
	result := make(map[string]int, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}
