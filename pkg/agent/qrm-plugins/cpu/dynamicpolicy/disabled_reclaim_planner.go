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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

// frozenReclaimPlacementConstraint carries the disabled-reclaim identity and
// owner semantics captured at the start of one advisor transaction. An empty
// frozen set is the zero-value/no-op constraint.
type frozenReclaimPlacementConstraint struct {
	entries                 state.PodEntries
	cpus                    machine.CPUSet
	allowSharedOverlap      bool
	disableDedicatedOverlap bool
}

func newFrozenReclaimPlacementConstraint(
	entries state.PodEntries,
	dynamicConf *dynamicconfig.Configuration,
	allowSharedOverlap bool,
	disableDedicatedOverlap bool,
) frozenReclaimPlacementConstraint {
	constraint := frozenReclaimPlacementConstraint{
		entries:                 entries,
		cpus:                    machine.NewCPUSet(),
		allowSharedOverlap:      allowSharedOverlap,
		disableDedicatedOverlap: disableDedicatedOverlap,
	}
	if dynamicConf != nil && !dynamicConf.EnableReclaim {
		constraint.cpus = reclaimPoolCPUSet(entries)
	}
	return constraint
}

func reclaimOverlapDisallowed(
	poolName string,
	allocationInfo *state.AllocationInfo,
	allowSharedOverlap bool,
	disableDedicatedOverlap bool,
) bool {
	poolName, _ = resourcepackage.UnwrapOwnerPoolName(poolName)
	if poolName == commonstate.PoolNameReclaim || commonstate.IsSystemPool(poolName) {
		return false
	}
	if commonstate.GetPoolType(poolName) == commonstate.PoolNameDedicated {
		return disableDedicatedOverlap
	}
	if allocationInfo != nil {
		if allocationInfo.CheckReclaimed() || allocationInfo.CheckSystem() {
			return false
		}
		if allocationInfo.CheckDedicated() {
			return disableDedicatedOverlap
		}
		// Ramp-up shared overlap is governed exclusively by the hard-partition
		// validator. Outside that gate it remains legal regardless of AO.
		if allocationInfo.RampUp && allocationInfo.CheckShared() {
			return false
		}
	}
	return !allowSharedOverlap
}

func (c frozenReclaimPlacementConstraint) disallows(
	poolName, entryName, subEntryName string,
) bool {
	if c.cpus.IsEmpty() {
		return false
	}
	originalPoolName := poolName
	unwrappedPoolName, resourcePackageName := resourcepackage.UnwrapOwnerPoolName(poolName)
	allocationInfo := c.entries[entryName][subEntryName]
	if allocationInfo == nil && commonstate.IsIsolationPool(unwrappedPoolName) {
		allocationInfo = resolveIsolationOwnerAllocation(
			c.entries, entryName, subEntryName, unwrappedPoolName, resourcePackageName)
	}
	return reclaimOverlapDisallowed(
		originalPoolName, allocationInfo,
		c.allowSharedOverlap, c.disableDedicatedOverlap)
}

func (c frozenReclaimPlacementConstraint) blockDisallowed(
	blockOwnerEntries map[string]advisorapi.BlockEntry,
) bool {
	for poolName, entry := range blockOwnerEntries {
		if c.disallows(poolName, entry.EntryName, entry.SubEntryName) {
			return true
		}
	}
	return false
}

func applyFrozenReclaimConstraintToDisjointDescriptors(
	descriptors []advisorBlockDescriptor,
	constraint frozenReclaimPlacementConstraint,
) ([]advisorBlockDescriptor, error) {
	if constraint.cpus.IsEmpty() {
		return descriptors, nil
	}

	result := append([]advisorBlockDescriptor(nil), descriptors...)
	for i := range result {
		if result[i].Class == advisorBlockClassMandatoryReclaim ||
			result[i].Class == advisorBlockClassReclaimOverlap {
			result[i].Eligible = result[i].Eligible.Intersection(constraint.cpus)
			result[i].Committed = result[i].Committed.Intersection(result[i].Eligible)
			result[i].OldPreferred = result[i].OldPreferred.Intersection(result[i].Eligible)
			if result[i].Eligible.Size() < result[i].Quantity {
				return nil, fmt.Errorf(
					"reclaim block %q frozen identity capacity %d is smaller than quantity %d: %s",
					result[i].BlockID, result[i].Eligible.Size(), result[i].Quantity, constraint.cpus.String())
			}
			continue
		}

		disallowed := false
		for _, owner := range result[i].Owners {
			poolName, entryName, subEntryName, _, ok := advisorDescriptorOwner(owner)
			if !ok {
				return nil, fmt.Errorf("block %q has malformed owner %q", result[i].BlockID, owner)
			}
			if constraint.disallows(poolName, entryName, subEntryName) {
				disallowed = true
				break
			}
		}
		if !disallowed {
			continue
		}

		result[i].Eligible = result[i].Eligible.Difference(constraint.cpus)
		result[i].Committed = result[i].Committed.Intersection(result[i].Eligible)
		result[i].OldPreferred = result[i].OldPreferred.Intersection(result[i].Eligible)
		if result[i].Eligible.Size() < result[i].Quantity {
			return nil, fmt.Errorf(
				"block %q eligible capacity %d is smaller than quantity %d after excluding frozen reclaim %s",
				result[i].BlockID, result[i].Eligible.Size(), result[i].Quantity, constraint.cpus.String())
		}
	}
	return result, nil
}
