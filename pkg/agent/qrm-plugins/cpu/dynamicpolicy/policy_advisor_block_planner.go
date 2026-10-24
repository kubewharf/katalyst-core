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
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type advisorBlockClass string

const (
	advisorBlockClassStatic           advisorBlockClass = "static"
	advisorBlockClassMandatoryReclaim advisorBlockClass = "mandatory-reclaim"
	advisorBlockClassDedicated        advisorBlockClass = "dedicated"
	advisorBlockClassShared           advisorBlockClass = "shared"
	advisorBlockClassReclaimOverlap   advisorBlockClass = "reclaim-overlap"

	// minimumHardReclaimCoresPerNUMA is the immutable minimum reclaim floor a
	// single eligible NUMA always keeps on the hard-partition path, expressed in
	// complete physical cores. The CPU magnitude is derived from CPUsPerCore() so
	// the guard is core-granular on any topology (no hard-coded SMT factor).
	minimumHardReclaimCoresPerNUMA = 1
)

type advisorBlockDescriptor struct {
	BlockID      string
	Owners       []string
	Class        advisorBlockClass
	NUMAID       int
	Quantity     int
	ComponentKey string
	Eligible     machine.CPUSet
	Committed    machine.CPUSet
	OldPreferred machine.CPUSet

	// target-driven reclaim provenance (zero until the source-pool config gate enables it).
	TargetDriven bool
	SourceTarget int
	ReclaimQuota int
}

type advisorBlockDescriptorBuilder struct {
	advisorBlockDescriptor
	ownerSeen          map[string]struct{}
	resourcePackage    string
	resourcePackageSet bool
}

func buildAdvisorBlockDescriptors(
	resp *advisorapi.ListAndWatchResponse,
	cpuDetails machine.CPUDetails,
	podEntries state.PodEntries,
	rpPinnedCPUSet map[string]machine.CPUSet,
	nonReclaimableCPUSet machine.CPUSet,
) ([]advisorBlockDescriptor, error) {
	if resp == nil {
		return nil, fmt.Errorf("got nil advisor response")
	}

	// Isolation pods own exclusive whole cores that the bulkhead domain model
	// folds into the primary (NonReclaimPool) side; they are never releasable to
	// reclaim. Fold their committed CPUs into the non-reclaimable set so
	// mandatory-reclaim eligibility (numaCPUs.Difference(nonReclaimableCPUSet))
	// never offers an isolation core's SMT sibling as a whole-core reclaim donor.
	// Skipping this lets whole-core alignment select the sibling and later fail
	// the bulkhead core-release witness (source=primary destination=reclaim).
	nonReclaimableCPUSet = nonReclaimableCPUSet.Union(
		state.GetUnitedPoolsCPUs(podEntries, commonstate.IsIsolationPool))

	allCPUs := cpuDetails.CPUs()
	allPinnedCPUs := machine.NewCPUSet()
	for _, cpus := range rpPinnedCPUSet {
		allPinnedCPUs = allPinnedCPUs.Union(cpus)
	}

	builders := make(map[string]*advisorBlockDescriptorBuilder)
	for entryName, calculationEntries := range resp.Entries {
		if calculationEntries == nil {
			return nil, fmt.Errorf("entry %q has nil calculation entries", entryName)
		}
		for subEntryName, calculationInfo := range calculationEntries.Entries {
			if calculationInfo == nil {
				return nil, fmt.Errorf("entry %q sub-entry %q has nil calculation info", entryName, subEntryName)
			}

			ownerPoolName, ownerResourcePackage := resourcepackage.UnwrapOwnerPoolName(calculationInfo.OwnerPoolName)
			ownerKey := canonicalAdvisorBlockOwner(ownerPoolName, entryName, subEntryName, ownerResourcePackage)
			for numaID64, result := range calculationInfo.CalculationResultsByNumas {
				if result == nil {
					return nil, fmt.Errorf("entry %q sub-entry %q NUMA %d has nil result", entryName, subEntryName, numaID64)
				}
				numaID := int(numaID64)
				if int64(numaID) != numaID64 {
					return nil, fmt.Errorf("NUMA id %d overflows int", numaID64)
				}

				numaCPUs, err := advisorBlockNUMACPUSet(allCPUs, cpuDetails, numaID)
				if err != nil {
					return nil, err
				}
				for _, block := range result.Blocks {
					if block == nil {
						return nil, fmt.Errorf("entry %q sub-entry %q NUMA %d has nil block", entryName, subEntryName, numaID)
					}
					if block.BlockId == "" {
						return nil, fmt.Errorf("entry %q sub-entry %q NUMA %d has empty block id", entryName, subEntryName, numaID)
					}
					quantity, err := uint64ToAdvisorBlockQuantity(block.Result)
					if err != nil {
						return nil, fmt.Errorf("block %q: %w", block.BlockId, err)
					}
					ownerClass, err := classifyAdvisorBlockOwner(ownerPoolName, block.OverlapTargets)
					if err != nil {
						return nil, fmt.Errorf("block %q: %w", block.BlockId, err)
					}
					ownerEligible := advisorBlockOwnerEligible(
						ownerPoolName,
						ownerResourcePackage,
						numaCPUs,
						allPinnedCPUs,
						rpPinnedCPUSet,
						nonReclaimableCPUSet,
					)
					ownerResourcePackage, ownerResourcePackageApplies := advisorBlockOwnerResourcePackageDomain(
						ownerPoolName, ownerResourcePackage,
					)

					builder, found := builders[block.BlockId]
					if !found {
						builder = &advisorBlockDescriptorBuilder{
							advisorBlockDescriptor: advisorBlockDescriptor{
								BlockID:      block.BlockId,
								Class:        ownerClass,
								NUMAID:       numaID,
								Quantity:     quantity,
								Eligible:     ownerEligible.Clone(),
								OldPreferred: machine.NewCPUSet(),
								// provenance gating happens at the source pool based on runtime config.
								TargetDriven: false,
								SourceTarget: 0,
								ReclaimQuota: 0,
							},
							ownerSeen: make(map[string]struct{}),
						}
						if ownerResourcePackageApplies {
							builder.resourcePackage = ownerResourcePackage
							builder.resourcePackageSet = true
						}
						builders[block.BlockId] = builder
					} else {
						if builder.NUMAID != numaID || builder.Quantity != quantity {
							return nil, fmt.Errorf("block %q aliases disagree on NUMA or quantity", block.BlockId)
						}
						if builder.Class != ownerClass {
							return nil, fmt.Errorf("block %q aliases have incompatible owner classes", block.BlockId)
						}
						if ownerResourcePackageApplies {
							if builder.resourcePackageSet && builder.resourcePackage != ownerResourcePackage {
								return nil, fmt.Errorf("block %q aliases have incompatible resource packages", block.BlockId)
							}
							builder.resourcePackage = ownerResourcePackage
							builder.resourcePackageSet = true
						}
						builder.Eligible = builder.Eligible.Intersection(ownerEligible)
					}

					if _, found := builder.ownerSeen[ownerKey]; !found {
						builder.ownerSeen[ownerKey] = struct{}{}
						builder.Owners = append(builder.Owners, ownerKey)
					}
					committed := advisorBlockOwnerOldPreferred(
						podEntries, entryName, subEntryName, numaID, numaCPUs)
					builder.Committed = builder.Committed.Union(committed)
					builder.OldPreferred = builder.OldPreferred.Union(committed)
				}
			}
		}
	}

	descriptors := make([]advisorBlockDescriptor, 0, len(builders))
	for _, builder := range builders {
		sort.Strings(builder.Owners)
		builder.ComponentKey = fmt.Sprintf("%s|%s|%d",
			builder.Class, strings.Join(builder.Owners, "\x1f"), builder.NUMAID)
		if builder.Eligible.Size() < builder.Quantity {
			return nil, fmt.Errorf("block %q eligible capacity %d is smaller than quantity %d",
				builder.BlockID, builder.Eligible.Size(), builder.Quantity)
		}
		builder.OldPreferred = builder.OldPreferred.Intersection(builder.Eligible)
		descriptors = append(descriptors, builder.advisorBlockDescriptor)
	}

	sort.Slice(descriptors, func(i, j int) bool {
		return advisorBlockDescriptorLess(descriptors[i], descriptors[j])
	})
	for _, descriptor := range descriptors {
		general.InfoS("advisor block descriptor built",
			"blockID", descriptor.BlockID,
			"class", descriptor.Class,
			"numaID", descriptor.NUMAID,
			"quantity", descriptor.Quantity,
			"componentKey", descriptor.ComponentKey,
			"owners", descriptor.Owners,
			"eligibleSize", descriptor.Eligible.Size(),
			"eligible", descriptor.Eligible.String(),
			"committedSize", descriptor.Committed.Size(),
			"committed", descriptor.Committed.String(),
			"oldPreferredSize", descriptor.OldPreferred.Size(),
			"oldPreferred", descriptor.OldPreferred.String())
	}
	return descriptors, nil
}

func advisorBlockDescriptorLess(left, right advisorBlockDescriptor) bool {
	if left.NUMAID != right.NUMAID {
		if left.NUMAID == commonstate.FakedNUMAID {
			return false
		}
		if right.NUMAID == commonstate.FakedNUMAID {
			return true
		}
		return left.NUMAID < right.NUMAID
	}
	if left.Class != right.Class {
		return advisorBlockClassRank(left.Class) < advisorBlockClassRank(right.Class)
	}
	if left.ComponentKey != right.ComponentKey {
		return left.ComponentKey < right.ComponentKey
	}
	if left.Quantity != right.Quantity {
		return left.Quantity < right.Quantity
	}
	leftAliases, rightAliases := strings.Join(left.Owners, "\x1f"), strings.Join(right.Owners, "\x1f")
	if leftAliases != rightAliases {
		return leftAliases < rightAliases
	}
	return left.BlockID < right.BlockID
}

func classifyAdvisorBlockOwner(
	poolName string,
	overlapTargets []*advisorapi.OverlapTarget,
) (advisorBlockClass, error) {
	if poolName == "" {
		return "", fmt.Errorf("cannot classify empty owner pool")
	}
	for _, target := range overlapTargets {
		if target == nil {
			return "", fmt.Errorf("cannot classify nil overlap target")
		}
	}

	poolType := commonstate.GetPoolType(poolName)
	switch poolType {
	case commonstate.PoolNameReserve, commonstate.PoolNamePrefixSystem,
		commonstate.PoolNameInterrupt, commonstate.PoolNameFallback:
		if len(overlapTargets) != 0 {
			return "", fmt.Errorf("cannot classify static owner pool %q with overlap targets", poolName)
		}
		return advisorBlockClassStatic, nil
	case commonstate.PoolNameDedicated:
		return advisorBlockClassDedicated, nil
	case commonstate.PoolNameReclaim:
		if len(overlapTargets) == 0 {
			return advisorBlockClassMandatoryReclaim, nil
		}
		return advisorBlockClassReclaimOverlap, nil
	default:
		if len(overlapTargets) != 0 {
			return advisorBlockClassReclaimOverlap, nil
		}
		return advisorBlockClassShared, nil
	}
}

func canonicalAdvisorBlockOwner(poolName, entryName, subEntryName, resourcePackageName string) string {
	return poolName + "\x00" + entryName + "\x00" + subEntryName + "\x00" + resourcePackageName
}

func advisorBlockOwnerResourcePackageDomain(poolName, resourcePackageName string) (string, bool) {
	if commonstate.GetPoolType(poolName) == commonstate.PoolNameReclaim && resourcePackageName == "" {
		return "", false
	}
	return resourcePackageName, true
}

func advisorBlockNUMACPUSet(allCPUs machine.CPUSet, cpuDetails machine.CPUDetails, numaID int) (machine.CPUSet, error) {
	if numaID == commonstate.FakedNUMAID {
		return allCPUs.Clone(), nil
	}
	numaCPUs := cpuDetails.CPUsInNUMANodes(numaID)
	if numaCPUs.IsEmpty() {
		return machine.NewCPUSet(), fmt.Errorf("NUMA %d has no CPUs", numaID)
	}
	return numaCPUs, nil
}

func advisorBlockOwnerEligible(
	ownerPoolName string,
	resourcePackageName string,
	numaCPUs machine.CPUSet,
	allPinnedCPUs machine.CPUSet,
	rpPinnedCPUSet map[string]machine.CPUSet,
	nonReclaimableCPUSet machine.CPUSet,
) machine.CPUSet {
	if commonstate.GetPoolType(ownerPoolName) == commonstate.PoolNameReclaim {
		return numaCPUs.Difference(nonReclaimableCPUSet)
	}
	if resourcePackageName != "" && !rpPinnedCPUSet[resourcePackageName].IsEmpty() {
		return numaCPUs.Intersection(rpPinnedCPUSet[resourcePackageName])
	}
	return numaCPUs.Difference(allPinnedCPUs)
}

func advisorBlockOwnerOldPreferred(
	podEntries state.PodEntries,
	entryName, subEntryName string,
	numaID int,
	numaCPUs machine.CPUSet,
) machine.CPUSet {
	if podEntries == nil || podEntries[entryName] == nil {
		return machine.NewCPUSet()
	}
	allocationInfo := podEntries[entryName][subEntryName]
	if allocationInfo == nil {
		return machine.NewCPUSet()
	}
	if numaID != commonstate.FakedNUMAID {
		if cpus, found := allocationInfo.TopologyAwareAssignments[numaID]; found {
			return cpus.Clone()
		}
	}
	return allocationInfo.AllocationResult.Intersection(numaCPUs)
}

func uint64ToAdvisorBlockQuantity(quantity uint64) (int, error) {
	converted := int(quantity)
	if converted < 0 || uint64(converted) != quantity {
		return 0, fmt.Errorf("quantity %d overflows int", quantity)
	}
	return converted, nil
}

func advisorBlockClassRank(class advisorBlockClass) int {
	switch class {
	case advisorBlockClassStatic:
		return 0
	case advisorBlockClassMandatoryReclaim:
		return 1
	case advisorBlockClassDedicated:
		return 2
	case advisorBlockClassShared:
		return 3
	case advisorBlockClassReclaimOverlap:
		return 4
	default:
		return 5
	}
}

func hasFakeNUMAMandatoryReclaimDescriptor(descriptors []advisorBlockDescriptor) bool {
	for _, descriptor := range descriptors {
		if descriptor.Class == advisorBlockClassMandatoryReclaim &&
			descriptor.NUMAID == commonstate.FakedNUMAID {
			return true
		}
	}
	return false
}

func uniformCandidateCoreWidth(
	topology *machine.CPUTopology,
	candidateCPUs machine.CPUSet,
) (int, error) {
	if topology == nil {
		return 0, fmt.Errorf("cannot validate physical core width with nil CPU topology")
	}
	candidates := coreAlignedCandidates(topology, candidateCPUs, machine.NewCPUSet())
	width := 0
	for _, candidate := range candidates {
		cpus := candidate.cpus
		if width == 0 {
			width = cpus.Size()
			continue
		}
		if cpus.Size() != width {
			return 0, fmt.Errorf(
				"non-uniform physical core width: NUMA %d socket %d core %d has %d CPUs, want %d",
				candidate.key.numaID, candidate.key.socketID, candidate.key.coreID, cpus.Size(), width)
		}
	}
	if width <= 0 {
		return 0, fmt.Errorf("candidate CPU set has no complete physical cores")
	}
	return width, nil
}

func completePreferredCoreCPUCount(
	topology *machine.CPUTopology,
	eligible, preferred machine.CPUSet,
) int {
	return completePreferredCoreCPUCountWithIndex(newPhysicalCoreIndex(topology), eligible, preferred)
}

func completePreferredCoreCPUCountWithIndex(
	index physicalCoreIndex,
	eligible, preferred machine.CPUSet,
) int {
	count := 0
	for _, candidate := range coreAlignedCandidatesWithIndex(index, eligible, preferred) {
		if candidate.cpus.IsSubsetOf(preferred) {
			count += candidate.cpus.Size()
		}
	}
	return count
}

// planWholeCoreCapacityQuotas is the canonical owner of whole-core quota
// apportionment across NUMA nodes. It preserves each NUMA minimum, rounds only
// residual capacity to physical-core width, and gives the next core to the
// currently smallest quota. preferredByNUMA is deliberately only a tie-breaker
// between equal quotas, so an already core-aligned historical placement cannot
// preserve cross-NUMA imbalance. enforceSaturation verifies that no legal
// one-core transfer can improve the resulting capacity-aware balance.
func planWholeCoreCapacityQuotas(
	quantity, coreWidth int,
	numaIDs []int,
	capacityByNUMA, minimumByNUMA, preferredByNUMA map[int]int,
	enforceSaturation bool,
) (map[int]int, error) {
	if coreWidth <= 0 {
		return nil, fmt.Errorf("whole-core quota planner has non-positive core width %d", coreWidth)
	}
	if quantity < 0 {
		return nil, fmt.Errorf("whole-core quota has negative quantity %d", quantity)
	}

	sortedNUMAs := append([]int(nil), numaIDs...)
	sort.Ints(sortedNUMAs)
	quotas := make(map[int]int, len(sortedNUMAs))
	normalizedCapacity := make(map[int]int, len(sortedNUMAs))
	total := 0
	for _, numaID := range sortedNUMAs {
		minimum := minimumByNUMA[numaID]
		if minimum < 0 {
			return nil, fmt.Errorf(
				"whole-core quota NUMA %d has negative minimum %d", numaID, minimum)
		}
		capacity := capacityByNUMA[numaID]
		if minimum > capacity {
			return nil, fmt.Errorf(
				"whole-core quota NUMA %d minimum %d exceeds capacity %d",
				numaID, minimum, capacity)
		}
		capacity -= (capacity - minimum) % coreWidth
		quotas[numaID] = minimum
		total += minimum
		normalizedCapacity[numaID] = capacity
	}
	if total > quantity {
		return nil, fmt.Errorf(
			"whole-core quota quantity %d is smaller than required minimum %d",
			quantity, total)
	}
	if (quantity-total)%coreWidth != 0 {
		return nil, fmt.Errorf(
			"whole-core quota residual quantity %d is not a multiple of %d",
			quantity-total, coreWidth)
	}

	for total < quantity {
		selectedNUMA := 0
		selected := false
		selectedImprovement := 0
		for _, numaID := range sortedNUMAs {
			if quotas[numaID]+coreWidth > normalizedCapacity[numaID] {
				continue
			}
			improvement := absInt(quotas[numaID]-preferredByNUMA[numaID]) -
				absInt(quotas[numaID]+coreWidth-preferredByNUMA[numaID])
			if !selected ||
				quotas[numaID] < quotas[selectedNUMA] ||
				(quotas[numaID] == quotas[selectedNUMA] &&
					(improvement > selectedImprovement ||
						(improvement == selectedImprovement && numaID < selectedNUMA))) {
				selectedNUMA = numaID
				selected = true
				selectedImprovement = improvement
			}
		}
		if !selected {
			return nil, fmt.Errorf(
				"whole-core quota has insufficient aggregate capacity for quantity %d", quantity)
		}
		quotas[selectedNUMA] += coreWidth
		total += coreWidth
	}
	if enforceSaturation {
		if err := validateWholeCoreQuotaSaturation(
			quotas, normalizedCapacity, minimumByNUMA, sortedNUMAs, coreWidth,
		); err != nil {
			return nil, err
		}
	}
	return quotas, nil
}

func validateWholeCoreQuotaSaturation(
	quotas, capacityByNUMA, minimumByNUMA map[int]int,
	numaIDs []int,
	coreWidth int,
) error {
	for _, highNUMA := range numaIDs {
		for _, lowNUMA := range numaIDs {
			if quotas[highNUMA] <= quotas[lowNUMA]+coreWidth {
				continue
			}
			donorCanYield := quotas[highNUMA]-coreWidth >= minimumByNUMA[highNUMA]
			receiverHasCapacity := quotas[lowNUMA]+coreWidth <= capacityByNUMA[lowNUMA]
			if donorCanYield && receiverHasCapacity {
				return fmt.Errorf(
					"whole-core quota is not capacity-saturated: NUMA %d quota %d can transfer one core to NUMA %d quota %d",
					highNUMA, quotas[highNUMA], lowNUMA, quotas[lowNUMA])
			}
		}
	}
	return nil
}

type fakeNUMAReclaimCapacity struct {
	rawEligible     machine.CPUSet
	effectiveByNUMA map[int]int
	numaIDs         []int
}

func hardPartitionFakeNUMAReclaimCapacity(
	fake advisorBlockDescriptor,
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	skipNUMAs sets.Int,
) fakeNUMAReclaimCapacity {
	skippedCPUs := topology.CPUDetails.CPUsInNUMANodes(skipNUMAs.List()...)
	realMandatoryNUMAs := sets.NewInt()
	dedicatedByNUMA := make(map[int]int)
	for _, descriptor := range descriptors {
		if descriptor.NUMAID == commonstate.FakedNUMAID {
			continue
		}
		switch descriptor.Class {
		case advisorBlockClassMandatoryReclaim:
			realMandatoryNUMAs.Insert(descriptor.NUMAID)
		case advisorBlockClassDedicated:
			dedicatedByNUMA[descriptor.NUMAID] += descriptor.Quantity
		}
	}
	excludedCPUs := skippedCPUs.Union(
		topology.CPUDetails.CPUsInNUMANodes(realMandatoryNUMAs.List()...))
	rawEligible := fake.Eligible.Intersection(available).Difference(excludedCPUs)

	result := fakeNUMAReclaimCapacity{
		rawEligible:     rawEligible,
		effectiveByNUMA: make(map[int]int),
	}
	for _, numaID := range topology.CPUDetails.KeepOnly(rawEligible).NUMANodes().ToSliceInt() {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		numaEligible := rawEligible.Intersection(numaCPUs)
		candidates := coreAlignedCandidates(topology, numaEligible, machine.NewCPUSet())
		if len(candidates) == 0 {
			continue
		}
		coreWidth := candidates[0].cpus.Size()
		completeCapacity := 0
		for _, candidate := range candidates {
			completeCapacity += candidate.cpus.Size()
		}
		remainingCapacity := general.Max(
			0, available.Intersection(numaCPUs).Size()-dedicatedByNUMA[numaID])
		remainingCapacity -= remainingCapacity % coreWidth
		effectiveCapacity := general.Min(completeCapacity, remainingCapacity)
		if effectiveCapacity == 0 {
			continue
		}
		result.numaIDs = append(result.numaIDs, numaID)
		result.effectiveByNUMA[numaID] = effectiveCapacity
	}
	sort.Ints(result.numaIDs)
	return result
}

func mandatoryReclaimCoreWidth(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	skipNUMAs sets.Int,
) (int, error) {
	participatingCPUs := machine.NewCPUSet()
	for _, descriptor := range descriptors {
		if descriptor.Class != advisorBlockClassMandatoryReclaim {
			continue
		}
		if descriptor.Quantity == 0 {
			continue
		}
		if descriptor.NUMAID == commonstate.FakedNUMAID {
			capacity := hardPartitionFakeNUMAReclaimCapacity(
				descriptor, descriptors, available, topology, skipNUMAs)
			for _, numaID := range capacity.numaIDs {
				if capacity.effectiveByNUMA[numaID] == 0 {
					continue
				}
				participatingCPUs = participatingCPUs.Union(
					capacity.rawEligible.Intersection(
						topology.CPUDetails.CPUsInNUMANodes(numaID)))
			}
			continue
		}
		if skipNUMAs.Has(descriptor.NUMAID) {
			continue
		}
		participatingCPUs = participatingCPUs.Union(
			descriptor.Eligible.
				Intersection(available).
				Intersection(topology.CPUDetails.CPUsInNUMANodes(descriptor.NUMAID)))
	}
	if participatingCPUs.IsEmpty() {
		return 1, nil
	}
	return uniformCandidateCoreWidth(topology, participatingCPUs)
}

func normalizeAdvisorDescriptorsForHardPartitionWholeCoreReclaim(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	skipNUMAs sets.Int,
) ([]advisorBlockDescriptor, error) {
	if topology == nil {
		return nil, fmt.Errorf("cannot normalize advisor descriptors with nil CPU topology")
	}
	participatingCPUs := machine.NewCPUSet()
	for _, descriptor := range descriptors {
		if descriptor.Class != advisorBlockClassMandatoryReclaim ||
			descriptor.NUMAID == commonstate.FakedNUMAID ||
			skipNUMAs.Has(descriptor.NUMAID) ||
			descriptor.Quantity == 0 {
			continue
		}
		participatingCPUs = participatingCPUs.Union(
			descriptor.Eligible.
				Intersection(available).
				Intersection(topology.CPUDetails.CPUsInNUMANodes(descriptor.NUMAID)))
	}
	if participatingCPUs.IsEmpty() {
		return append([]advisorBlockDescriptor(nil), descriptors...), nil
	}
	cpusPerCore, err := uniformCandidateCoreWidth(topology, participatingCPUs)
	if err != nil {
		return nil, fmt.Errorf("cannot normalize advisor descriptors: %w", err)
	}
	return normalizeAdvisorDescriptorsForWholeCoreReclaimWithWidthAndSkipNUMAs(
		descriptors, cpusPerCore, skipNUMAs)
}

func normalizeAdvisorDescriptorsForWholeCoreReclaimWithWidthAndSkipNUMAs(
	descriptors []advisorBlockDescriptor,
	cpusPerCore int,
	skipNUMAs sets.Int,
) ([]advisorBlockDescriptor, error) {
	if cpusPerCore <= 1 {
		return append([]advisorBlockDescriptor(nil), descriptors...), nil
	}

	normalized := append([]advisorBlockDescriptor(nil), descriptors...)
	mandatoryByNUMA := make(map[int][]int)
	dedicatedByNUMA := make(map[int][]int)
	mandatoryQuantityByNUMA := make(map[int]int)
	for i, descriptor := range normalized {
		if descriptor.NUMAID == commonstate.FakedNUMAID || skipNUMAs.Has(descriptor.NUMAID) {
			continue
		}
		switch descriptor.Class {
		case advisorBlockClassMandatoryReclaim:
			mandatoryByNUMA[descriptor.NUMAID] = append(mandatoryByNUMA[descriptor.NUMAID], i)
			mandatoryQuantityByNUMA[descriptor.NUMAID] += descriptor.Quantity
		case advisorBlockClassDedicated:
			dedicatedByNUMA[descriptor.NUMAID] = append(dedicatedByNUMA[descriptor.NUMAID], i)
		}
	}

	numaIDs := make([]int, 0, len(mandatoryQuantityByNUMA))
	for numaID := range mandatoryQuantityByNUMA {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)
	for _, numaID := range numaIDs {
		quantity := mandatoryQuantityByNUMA[numaID]
		remainder := quantity % cpusPerCore
		if remainder == 0 {
			continue
		}
		deficit := cpusPerCore - remainder
		mandatoryIndexes := append([]int(nil), mandatoryByNUMA[numaID]...)
		dedicatedIndexes := append([]int(nil), dedicatedByNUMA[numaID]...)
		sort.Slice(mandatoryIndexes, func(i, j int) bool {
			return advisorBlockDescriptorLess(normalized[mandatoryIndexes[i]], normalized[mandatoryIndexes[j]])
		})
		sort.Slice(dedicatedIndexes, func(i, j int) bool {
			return advisorBlockDescriptorLess(normalized[dedicatedIndexes[i]], normalized[dedicatedIndexes[j]])
		})
		if len(mandatoryIndexes) == 0 {
			return nil, fmt.Errorf("NUMA %d has mandatory reclaim quantity %d but no reclaim descriptor",
				numaID, quantity)
		}
		if len(dedicatedIndexes) == 0 {
			return nil, fmt.Errorf(
				"NUMA %d mandatory reclaim quantity %d needs %d CPUs to become a whole-core multiple of %d, but no dedicated descriptor can shrink",
				numaID, quantity, deficit, cpusPerCore)
		}
		remaining := deficit
		for _, index := range dedicatedIndexes {
			if remaining == 0 {
				break
			}
			take := general.Min(normalized[index].Quantity, remaining)
			normalized[index].Quantity -= take
			remaining -= take
		}
		if remaining > 0 {
			return nil, fmt.Errorf(
				"NUMA %d mandatory reclaim quantity %d needs %d CPUs to become a whole-core multiple of %d, but dedicated descriptors are short by %d",
				numaID, quantity, deficit, cpusPerCore, remaining)
		}
		normalized[mandatoryIndexes[0]].Quantity += deficit
		general.InfoS("normalize advisor descriptors to preserve whole-core reclaim",
			"numaID", numaID,
			"cpusPerCore", cpusPerCore,
			"mandatoryQuantityBefore", quantity,
			"deficit", deficit,
			"mandatoryBlockID", normalized[mandatoryIndexes[0]].BlockID,
			"mandatoryQuantityAfter", normalized[mandatoryIndexes[0]].Quantity)
	}
	return normalized, nil
}

// expandSteadyFakeNUMAReclaimPhase reserves one complete physical core on each
// eligible NUMA before leaving the remaining fake-NUMA quantity to the regular
// joint solver. This preserves the advisor-published total while preventing a
// stable preferred set from concentrating the whole reclaim pool on a subset of
// NUMAs. The residual remains global so narrow share and dedicated demands can
// still participate in the same feasibility solve.
func expandSteadyFakeNUMAReclaimPhase(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	skipNUMAs sets.Int,
) ([]partitionDemand, map[string]string, []partitionCoreFloorConstraint, error) {
	if topology == nil {
		return nil, nil, nil, fmt.Errorf("cannot expand steady reclaim phase with nil CPU topology")
	}
	cpusPerCore := topology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return nil, nil, nil, fmt.Errorf(
			"cannot expand steady reclaim phase with non-positive cpus per core %d", cpusPerCore)
	}
	// Build the physical-core decomposition once for the whole expansion: the
	// NUMA loop below calls coreAlignedCandidates/completePreferredCoreCPUCount
	// per domain, and rebuilding the index per call re-scans every logical CPU.
	coreIndex := newPhysicalCoreIndex(topology)

	mandatory := filterAdvisorDescriptors(descriptors, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.Class == advisorBlockClassMandatoryReclaim
	})
	sort.Slice(mandatory, func(i, j int) bool {
		return advisorBlockDescriptorLess(mandatory[i], mandatory[j])
	})
	fakeDescriptors := filterAdvisorDescriptors(mandatory, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.NUMAID == commonstate.FakedNUMAID
	})
	if len(fakeDescriptors) != 1 {
		return nil, nil, nil, fmt.Errorf(
			"steady reclaim protocol error: expected exactly one fake-NUMA mandatory reclaim block, got %d",
			len(fakeDescriptors))
	}

	demands := make([]partitionDemand, 0, len(mandatory)+topology.NumNUMANodes)
	blockIDByDemandKey := make(map[string]string, len(mandatory)+topology.NumNUMANodes)
	floors := make([]partitionCoreFloorConstraint, 0, topology.NumNUMANodes)
	for _, descriptor := range mandatory {
		if descriptor.NUMAID == commonstate.FakedNUMAID {
			continue
		}
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(descriptor.NUMAID)
		eligible := descriptor.Eligible.Intersection(available).Intersection(numaCPUs)
		if eligible.Size() < descriptor.Quantity {
			return nil, nil, nil, fmt.Errorf(
				"steady reclaim block %q NUMA %d eligible capacity %d is smaller than quantity %d",
				descriptor.BlockID, descriptor.NUMAID, eligible.Size(), descriptor.Quantity)
		}
		key := hardReclaimPhaseDemandKey(descriptor, descriptor.NUMAID)
		coreCandidates := coreAlignedCandidatesWithIndex(
			coreIndex, eligible, descriptor.OldPreferred.Intersection(eligible))
		if descriptor.Quantity >= cpusPerCore && len(coreCandidates) > 0 {
			floorEligible := machine.NewCPUSet()
			for _, candidate := range coreCandidates {
				floorEligible = floorEligible.Union(candidate.cpus)
			}
			demands = append(demands, partitionDemand{
				key:       key,
				quantity:  cpusPerCore,
				eligible:  floorEligible,
				preferred: descriptor.OldPreferred.Intersection(floorEligible),
				class:     advisorBlockClassMandatoryReclaim,
			})
			blockIDByDemandKey[key] = descriptor.BlockID
			floors = append(floors, partitionCoreFloorConstraint{
				demandKey:        key,
				committedBlockID: descriptor.BlockID,
			})

			residual := descriptor.Quantity - cpusPerCore
			if residual > 0 {
				residualKey := key + "\x00residual"
				demands = append(demands, partitionDemand{
					key:       residualKey,
					quantity:  residual,
					eligible:  eligible,
					preferred: descriptor.OldPreferred.Intersection(eligible),
					class:     advisorBlockClassMandatoryReclaim,
				})
				blockIDByDemandKey[residualKey] = descriptor.BlockID
			}
			continue
		}
		demands = append(demands, partitionDemand{
			key:       key,
			quantity:  descriptor.Quantity,
			eligible:  eligible,
			preferred: descriptor.OldPreferred.Intersection(eligible),
			class:     advisorBlockClassMandatoryReclaim,
		})
		blockIDByDemandKey[key] = descriptor.BlockID
	}

	fake := fakeDescriptors[0]
	realMandatoryNUMAs := sets.NewInt()
	realMandatoryPreferred := machine.NewCPUSet()
	for _, descriptor := range mandatory {
		if descriptor.NUMAID != commonstate.FakedNUMAID {
			realMandatoryNUMAs.Insert(descriptor.NUMAID)
			realMandatoryPreferred = realMandatoryPreferred.Union(descriptor.OldPreferred)
		}
	}
	// Real-NUMA reclaim descriptors and committed steady exclusive DNB
	// allocations already have a finalized NUMA owner. Exclude those NUMAs from
	// the fake block instead of treating their CPUs as movable balance capacity.
	// skipNUMAs is state-derived by SteadyExclusiveNUMAs; a shared-core pod's
	// numa_exclusive annotation alone must not exclude a NUMA.
	excludedNUMAs := realMandatoryNUMAs.Union(skipNUMAs)
	fakeEligible := fake.Eligible.Intersection(available).Difference(
		topology.CPUDetails.CPUsInNUMANodes(excludedNUMAs.List()...))
	fakeOldPreferred := fake.OldPreferred.Difference(realMandatoryPreferred).Intersection(fakeEligible)
	numaIDs := topology.CPUDetails.KeepOnly(fakeEligible).NUMANodes().ToSliceInt()
	sort.Ints(numaIDs)
	minimumByNUMA := make(map[int]int, len(numaIDs))
	maximumByNUMA := make(map[int]int, len(numaIDs))
	preferredByNUMA := make(map[int]int, len(numaIDs))
	fixedQuantityByNUMA := make(map[int]int, len(numaIDs))
	realMandatoryQuantityByNUMA := make(map[int]int, len(numaIDs))
	for _, descriptor := range descriptors {
		if descriptor.BlockID == fake.BlockID {
			continue
		}
		finalEligible := descriptor.Eligible.Intersection(available)
		eligibleNUMAs := topology.CPUDetails.KeepOnly(finalEligible).NUMANodes().ToSliceInt()
		if len(eligibleNUMAs) == 1 {
			fixedQuantityByNUMA[eligibleNUMAs[0]] += descriptor.Quantity
		}
		if descriptor.Class == advisorBlockClassMandatoryReclaim &&
			descriptor.NUMAID != commonstate.FakedNUMAID {
			realMandatoryQuantityByNUMA[descriptor.NUMAID] += descriptor.Quantity
		}
	}
	for _, numaID := range numaIDs {
		if skipNUMAs.Has(numaID) {
			maximumByNUMA[numaID] = general.Max(
				0,
				available.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID)).Size()-
					fixedQuantityByNUMA[numaID],
			)
		} else {
			numaEligible := fakeEligible.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
			realMandatory := realMandatoryQuantityByNUMA[numaID]
			if realMandatory == 0 {
				coreCandidates := coreAlignedCandidatesWithIndex(
					coreIndex, numaEligible, fakeOldPreferred.Intersection(numaEligible))
				if len(coreCandidates) > 0 {
					minimumByNUMA[numaID] = cpusPerCore
				}
			} else if remainder := realMandatory % cpusPerCore; remainder != 0 {
				minimumByNUMA[numaID] = cpusPerCore - remainder
			}
			maximumByNUMA[numaID] = general.Max(
				0,
				available.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID)).Size()-
					fixedQuantityByNUMA[numaID],
			)
		}
		numaEligible := fakeEligible.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		preferredByNUMA[numaID] = completePreferredCoreCPUCountWithIndex(
			coreIndex, numaEligible, fakeOldPreferred.Intersection(numaEligible))
	}
	quotas, err := planWholeCoreCapacityQuotas(
		fake.Quantity,
		cpusPerCore,
		numaIDs,
		maximumByNUMA,
		minimumByNUMA,
		preferredByNUMA,
		true,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	general.InfoS("steady reclaim fake NUMA quota planned",
		"quantity", fake.Quantity,
		"coreWidth", cpusPerCore,
		"eligibleNUMAs", numaIDs,
		"excludedRealNUMAs", realMandatoryNUMAs.List(),
		"excludedSteadyExclusiveNUMAs", skipNUMAs.List(),
		"capacityByNUMA", maximumByNUMA,
		"minimumByNUMA", minimumByNUMA,
		"oldQuotaByNUMA", preferredByNUMA,
		"targetQuotaByNUMA", quotas,
		"saturationValidated", true)

	for _, numaID := range numaIDs {
		quota := quotas[numaID]
		if quota == 0 {
			continue
		}
		eligible := fakeEligible.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		key := hardReclaimPhaseDemandKey(fake, numaID)
		demands = append(demands, partitionDemand{
			key:       key,
			quantity:  quota,
			eligible:  eligible,
			preferred: fakeOldPreferred.Intersection(eligible),
			class:     advisorBlockClassMandatoryReclaim,
		})
		blockIDByDemandKey[key] = fake.BlockID
	}
	return demands, blockIDByDemandKey, floors, nil
}

func expandHardPartitionReclaimPhase(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	topology *machine.CPUTopology,
	skipNUMAs sets.Int,
) ([]partitionDemand, map[string]string, error) {
	if topology == nil {
		return nil, nil, fmt.Errorf("cannot expand hard reclaim phase with nil CPU topology")
	}
	// Build the physical-core decomposition once for the whole expansion: the fake
	// NUMA loop below calls coreAlignedCandidates / completePreferredCoreCPUCount
	// per domain, and rebuilding the index per call re-scans every logical CPU.
	coreIndex := newPhysicalCoreIndex(topology)

	mandatory := filterAdvisorDescriptors(descriptors, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.Class == advisorBlockClassMandatoryReclaim
	})
	sort.Slice(mandatory, func(i, j int) bool {
		return advisorBlockDescriptorLess(mandatory[i], mandatory[j])
	})
	if len(mandatory) == 0 {
		return nil, map[string]string{}, nil
	}
	fakeDescriptors := filterAdvisorDescriptors(mandatory, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.NUMAID == commonstate.FakedNUMAID
	})
	if len(fakeDescriptors) > 1 {
		return nil, nil, fmt.Errorf(
			"hard reclaim protocol error: expected at most one fake-NUMA mandatory reclaim block, got %d",
			len(fakeDescriptors))
	}

	demands := make([]partitionDemand, 0, len(mandatory))
	blockIDByDemandKey := make(map[string]string, len(mandatory))
	finalByNUMA := make(map[int]int)
	fixedDedicatedByNUMA := make(map[int]int)
	capacityByNUMA := make(map[int]int)
	eligibleNUMAs := make(map[int]struct{})
	totalQuantity := 0
	var fakeEligible machine.CPUSet
	var fakeCapacity fakeNUMAReclaimCapacity
	if len(fakeDescriptors) == 1 {
		fakeCapacity = hardPartitionFakeNUMAReclaimCapacity(
			fakeDescriptors[0], descriptors, available, topology, skipNUMAs)
		fakeEligible = fakeCapacity.rawEligible
	}

	for _, descriptor := range descriptors {
		if descriptor.Class == advisorBlockClassDedicated && descriptor.NUMAID != commonstate.FakedNUMAID {
			fixedDedicatedByNUMA[descriptor.NUMAID] += descriptor.Quantity
		}
	}
	general.InfoS("hard reclaim phase input",
		"availableSize", available.Size(),
		"available", available.String(),
		"descriptorCount", len(descriptors),
		"mandatoryCount", len(mandatory),
		"fakeMandatoryCount", len(fakeDescriptors),
		"fixedDedicatedByNUMA", fixedDedicatedByNUMA)

	for _, descriptor := range mandatory {
		totalQuantity += descriptor.Quantity
		finalEligible := descriptor.Eligible.Intersection(available)
		general.InfoS("hard reclaim mandatory descriptor",
			"blockID", descriptor.BlockID,
			"numaID", descriptor.NUMAID,
			"quantity", descriptor.Quantity,
			"componentKey", descriptor.ComponentKey,
			"eligibleSize", descriptor.Eligible.Size(),
			"eligible", descriptor.Eligible.String(),
			"finalEligibleSize", finalEligible.Size(),
			"finalEligible", finalEligible.String())
		if descriptor.NUMAID == commonstate.FakedNUMAID {
			if descriptor.Quantity > 0 &&
				topology.CPUDetails.KeepOnly(fakeEligible).NUMANodes().IsEmpty() {
				return nil, nil, fmt.Errorf(
					"hard reclaim fake block %q has quantity %d but no effective NUMA capacity remains after dedicated occupancy, real mandatory NUMAs, and skip NUMAs",
					descriptor.BlockID, descriptor.Quantity)
			}
			for _, numaID := range fakeCapacity.numaIDs {
				numaEligible := fakeEligible.Intersection(
					topology.CPUDetails.CPUsInNUMANodes(numaID))
				if len(coreAlignedCandidatesWithIndex(
					coreIndex, numaEligible, descriptor.OldPreferred.Intersection(numaEligible))) == 0 {
					continue
				}
				eligibleNUMAs[numaID] = struct{}{}
				capacityByNUMA[numaID] = available.Intersection(
					topology.CPUDetails.CPUsInNUMANodes(numaID)).Size()
			}
			continue
		}

		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(descriptor.NUMAID)
		eligible := finalEligible.Intersection(numaCPUs)
		if eligible.Size() < descriptor.Quantity {
			return nil, nil, fmt.Errorf(
				"hard reclaim block %q NUMA %d eligible capacity %d is smaller than quantity %d",
				descriptor.BlockID, descriptor.NUMAID, eligible.Size(), descriptor.Quantity)
		}
		capacityByNUMA[descriptor.NUMAID] = available.Intersection(numaCPUs).Size()
		finalByNUMA[descriptor.NUMAID] += descriptor.Quantity
		key := hardReclaimPhaseDemandKey(descriptor, descriptor.NUMAID)
		demands = append(demands, partitionDemand{
			key:       key,
			quantity:  descriptor.Quantity,
			eligible:  eligible,
			preferred: descriptor.OldPreferred.Intersection(eligible),
			class:     advisorBlockClassMandatoryReclaim,
		})
		blockIDByDemandKey[key] = descriptor.BlockID
	}
	general.InfoS("hard reclaim phase seeded real mandatory",
		"totalQuantity", totalQuantity,
		"eligibleNUMAs", sortedIntKeys(eligibleNUMAs),
		"capacityByNUMA", capacityByNUMA,
		"finalByNUMA", finalByNUMA,
		"fixedDedicatedByNUMA", fixedDedicatedByNUMA)

	if len(fakeDescriptors) == 0 || fakeDescriptors[0].Quantity == 0 {
		return demands, blockIDByDemandKey, nil
	}
	fake := fakeDescriptors[0]
	if len(eligibleNUMAs) == 0 {
		return nil, nil, fmt.Errorf(
			"hard reclaim fake block %q has quantity %d but no effective NUMA capacity remains after dedicated occupancy, real mandatory NUMAs, and skip NUMAs",
			fake.BlockID, fake.Quantity)
	}
	cpusPerCore, err := mandatoryReclaimCoreWidth(
		descriptors, available, topology, skipNUMAs)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot expand hard reclaim phase: %w", err)
	}
	requiredFakeMinimum := len(fakeCapacity.numaIDs) * minimumHardReclaimCoresPerNUMA * cpusPerCore
	if fakeDescriptors[0].Quantity < requiredFakeMinimum {
		return nil, nil, fmt.Errorf(
			"hard reclaim fake quantity %d is smaller than required minimum %d",
			fakeDescriptors[0].Quantity, requiredFakeMinimum)
	}

	for numaID := range capacityByNUMA {
		quantity := finalByNUMA[numaID]
		if fixedDedicatedByNUMA[numaID]+quantity > capacityByNUMA[numaID] {
			return nil, nil, fmt.Errorf(
				"hard reclaim NUMA %d initial quantity %d with fixed dedicated load %d exceeds capacity %d",
				numaID, quantity, fixedDedicatedByNUMA[numaID], capacityByNUMA[numaID])
		}
	}

	numaIDs := make([]int, 0, len(eligibleNUMAs))
	for numaID := range eligibleNUMAs {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)
	eligibleCapacityByNUMA := make(map[int]int, len(numaIDs))
	minimumByNUMA := make(map[int]int, len(numaIDs))
	preferredByNUMA := make(map[int]int, len(numaIDs))
	for _, numaID := range numaIDs {
		numaEligible := fakeEligible.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		eligibleCapacityByNUMA[numaID] = fakeCapacity.effectiveByNUMA[numaID]
		minimumByNUMA[numaID] = minimumHardReclaimCoresPerNUMA * cpusPerCore
		preferredByNUMA[numaID] = completePreferredCoreCPUCountWithIndex(
			coreIndex, numaEligible, fake.OldPreferred.Intersection(numaEligible))
	}
	general.InfoS("hard reclaim fake mandatory water-filling input",
		"blockID", fake.BlockID,
		"quantity", fake.Quantity,
		"componentKey", fake.ComponentKey,
		"eligibleNUMAs", numaIDs,
		"eligibleCapacityByNUMA", eligibleCapacityByNUMA,
		"capacityByNUMA", capacityByNUMA,
		"fixedDedicatedByNUMA", fixedDedicatedByNUMA,
		"finalByNUMABeforeFake", finalByNUMA)
	if fake.Quantity%cpusPerCore != 0 {
		return nil, nil, fmt.Errorf(
			"hard reclaim fake block %q quantity %d is not a whole-core multiple of %d",
			fake.BlockID, fake.Quantity, cpusPerCore)
	}
	quotas, err := planWholeCoreCapacityQuotas(
		fake.Quantity,
		cpusPerCore,
		numaIDs,
		eligibleCapacityByNUMA,
		minimumByNUMA,
		preferredByNUMA,
		true,
	)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"hard reclaim fake block %q: %w", fake.BlockID, err)
	}
	excludedRealNUMAs := sets.NewInt()
	for _, descriptor := range mandatory {
		if descriptor.NUMAID != commonstate.FakedNUMAID {
			excludedRealNUMAs.Insert(descriptor.NUMAID)
		}
	}
	general.InfoS("hard reclaim fake NUMA quota planned",
		"quantity", fake.Quantity,
		"coreWidth", cpusPerCore,
		"eligibleNUMAs", numaIDs,
		"excludedRealNUMAs", excludedRealNUMAs.List(),
		"excludedSteadyExclusiveNUMAs", skipNUMAs.List(),
		"capacityByNUMA", eligibleCapacityByNUMA,
		"minimumByNUMA", minimumByNUMA,
		"oldQuotaByNUMA", preferredByNUMA,
		"targetQuotaByNUMA", quotas,
		"saturationValidated", true)
	for numaID, quota := range quotas {
		finalByNUMA[numaID] += quota
	}
	general.InfoS("hard reclaim fake mandatory water-filling result",
		"blockID", fake.BlockID,
		"quotas", quotas,
		"finalByNUMAAfterFake", finalByNUMA)
	for _, numaID := range numaIDs {
		if quotas[numaID] == 0 {
			continue
		}
		eligible := fakeEligible.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		key := hardReclaimPhaseDemandKey(fake, numaID)
		demands = append(demands, partitionDemand{
			key:       key,
			quantity:  quotas[numaID],
			eligible:  eligible,
			preferred: fake.OldPreferred.Intersection(eligible),
			class:     advisorBlockClassMandatoryReclaim,
		})
		blockIDByDemandKey[key] = fake.BlockID
	}
	return demands, blockIDByDemandKey, nil
}

func sortedIntKeys(keys map[int]struct{}) []int {
	result := make([]int, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Ints(result)
	return result
}

func hardReclaimPhaseDemandKey(descriptor advisorBlockDescriptor, numaID int) string {
	return fmt.Sprintf("%s\x00block\x00%s\x00numa\x00%d",
		descriptor.ComponentKey, descriptor.BlockID, numaID)
}
