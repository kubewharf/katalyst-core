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

package validator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type cpuAdvisorValidationFunc func(resp *advisorapi.ListAndWatchResponse) error

type CPUAdvisorValidator struct {
	state       state.ReadonlyState
	machineInfo *machine.KatalystMachineInfo
}

func NewCPUAdvisorValidator(state state.ReadonlyState, machineInfo *machine.KatalystMachineInfo) *CPUAdvisorValidator {
	return &CPUAdvisorValidator{
		state:       state,
		machineInfo: machineInfo,
	}
}

// ValidateRequest validates the GetAdvice request.
// We validate the request because we cannot infer the container metadata from sys-advisor response.
func (c *CPUAdvisorValidator) ValidateRequest(req *advisorapi.GetAdviceRequest) error {
	if req == nil {
		return fmt.Errorf("got nil req")
	}

	entries := c.state.GetPodEntries()

	// validate shared_cores with numa_binding entries
	sharedNUMABindingAllocationInfos := entries.GetFilteredPodEntries(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedNUMABinding))

	for podUID, containerEntries := range sharedNUMABindingAllocationInfos {
		for containerName, containerInfo := range containerEntries {
			if req.Entries[podUID] == nil || req.Entries[podUID].Entries[containerName] == nil {
				return fmt.Errorf("missing request entry for shared_cores with numa_binding pod: %s container: %s", podUID, containerName)
			}
			requestInfo := req.Entries[podUID].Entries[containerName]
			// This container may have been changed from shared_cores without numa_binding to shared_cores with numa_binding.
			// Verify if we have included this information in the request.
			// If we have, sys-advisor must have observed it.
			if requestInfo.Metadata.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] != consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				return fmt.Errorf(
					"shared_cores with numa_binding pod: %s container: %s has invalid owner pool name: %s in request, expected %s",
					podUID, containerName, requestInfo.AllocationInfo.OwnerPoolName, containerInfo.OwnerPoolName)
			}
		}
	}

	for podUID, containerEntries := range req.Entries {
		if containerEntries == nil {
			continue
		}
		for containerName, requestInfo := range containerEntries.Entries {
			if requestInfo.Metadata.QosLevel == consts.PodAnnotationQoSLevelSharedCores &&
				requestInfo.Metadata.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				if entries[podUID][containerName] == nil {
					return fmt.Errorf("missing state entry for shared_cores with numa_binding pod: %s container: %s", podUID, containerName)
				}
			}
		}
	}

	return nil
}

func (c *CPUAdvisorValidator) Validate(resp *advisorapi.ListAndWatchResponse) error {
	return c.validate(resp, false)
}

func (c *CPUAdvisorValidator) ValidateWithDefaultShareUpperBound(resp *advisorapi.ListAndWatchResponse) error {
	return c.validate(resp, true)
}

func (c *CPUAdvisorValidator) validate(resp *advisorapi.ListAndWatchResponse, defaultShareUpperBound bool) error {
	if resp == nil {
		return fmt.Errorf("got nil cpu advisor resp")
	}

	blockResp := resp
	var defaultShareUpperBoundErr error
	if defaultShareUpperBound {
		defaultShareUpperBoundErr = c.validateDefaultShareUpperBound(resp)
		blockResp = resp.WithoutDefaultShareEntry()
	}

	var errList []error
	for _, validator := range []cpuAdvisorValidationFunc{
		c.validateEntries,
		c.validateForbiddenPools,
	} {
		errList = append(errList, validator(resp))
	}
	errList = append(errList,
		defaultShareUpperBoundErr,
		c.validateStaticPools(resp),
		c.validateOverlapPolicy(blockResp),
		c.validateResourcePackageOwners(blockResp),
		c.validateBlocks(blockResp),
	)
	return errors.NewAggregate(errList)
}

func (c *CPUAdvisorValidator) validateDefaultShareUpperBound(resp *advisorapi.ListAndWatchResponse) error {
	calculationEntries := resp.Entries[commonstate.PoolNameShare]
	if calculationEntries == nil {
		return fmt.Errorf("default share upper bound is missing")
	}
	calculationInfo := calculationEntries.Entries[commonstate.FakedContainerName]
	if calculationInfo == nil {
		return fmt.Errorf("default share upper bound is missing")
	}
	if calculationInfo.OwnerPoolName != commonstate.PoolNameShare {
		return fmt.Errorf("default share has invalid owner pool name: %s", calculationInfo.OwnerPoolName)
	}
	if len(calculationInfo.CalculationResultsByNumas) != 1 {
		return fmt.Errorf("default share upper bound must contain only faked numa id")
	}
	result := calculationInfo.CalculationResultsByNumas[commonstate.FakedNUMAID]
	if result == nil || len(result.Blocks) != 1 || result.Blocks[0] == nil {
		return fmt.Errorf("default share upper bound must contain exactly one block")
	}
	quantity, err := general.CovertUInt64ToInt(result.Blocks[0].Result)
	if err != nil {
		return fmt.Errorf("convert default share upper bound failed: %v", err)
	}
	if c.machineInfo == nil || c.machineInfo.CPUTopology == nil {
		return fmt.Errorf("validate default share upper bound got nil topology")
	}
	if quantity > c.machineInfo.CPUTopology.NumCPUs {
		return fmt.Errorf("default share upper bound %d exceeds total capacity %d",
			quantity, c.machineInfo.CPUTopology.NumCPUs)
	}
	return nil
}

func (c *CPUAdvisorValidator) validateEntries(resp *advisorapi.ListAndWatchResponse) error {
	entries := c.state.GetPodEntries()

	// validate dedicated_cores entries
	dedicatedAllocationInfos := entries.GetFilteredPodEntries(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicated))
	dedicatedCalculationInfos := resp.FilterCalculationInfo(commonstate.PoolNameDedicated)
	if len(dedicatedAllocationInfos) != len(dedicatedCalculationInfos) {
		return fmt.Errorf("dedicatedAllocationInfos length: %d and dedicatedCalculationInfos length: %d mismatch",
			len(dedicatedAllocationInfos), len(dedicatedCalculationInfos))
	}

	for podUID, containerEntries := range dedicatedAllocationInfos {
		for containerName, allocationInfo := range containerEntries {
			calculationInfo := dedicatedCalculationInfos[podUID][containerName]
			if calculationInfo == nil {
				return fmt.Errorf("missing CalculationInfo for pod: %s container: %s", podUID, containerName)
			}

			if !allocationInfo.CheckDedicatedNUMABinding() {
				numaCalculationQuantities, err := calculationInfo.GetNUMAQuantities()
				if err != nil {
					return fmt.Errorf("GetNUMAQuantities failed with error: %v, pod: %s container: %s",
						err, podUID, containerName)
				}

				// currently, we don't support strategy to adjust cpuset of dedicated_cores containers.
				// for stability if the dedicated_cores container calculation result and allocation result, we will return error.
				for numaId, cset := range allocationInfo.TopologyAwareAssignments {
					if cset.Size() != numaCalculationQuantities[numaId] {
						return fmt.Errorf("NUMA: %d calculation quantity: %d and allocation quantity: %d mismatch, pod: %s container: %s",
							numaId, numaCalculationQuantities[numaId], cset.Size(), podUID, containerName)
					}
				}

				for numaId, calQuantity := range numaCalculationQuantities {
					if calQuantity != allocationInfo.TopologyAwareAssignments[numaId].Size() {
						return fmt.Errorf("NUMA: %d calculation quantity: %d and allocation quantity: %d mismatch, pod: %s container: %s",
							numaId, calQuantity, allocationInfo.TopologyAwareAssignments[numaId].Size(), podUID, containerName)
					}
				}
			} else {
				calculationQuantity, err := calculationInfo.GetTotalQuantity()
				if err != nil {
					return fmt.Errorf("GetTotalQuantity failed with error: %v, pod: %s container: %s",
						err, podUID, containerName)
				}

				if resp.DisableDedicatedCoresOverlapReclaimedCores && calculationQuantity == 0 {
					return fmt.Errorf("pod: %s container: %s has zero dedicated calculation result in disjoint mode",
						podUID, containerName)
				}

				// In disjoint mode the dedicated/reclaim partition is
				// re-driven by the incoming advisor response, so NUMA-binding
				// dedicated allocations may legitimately shrink or grow (e.g. a
				// non-exclusive DNB container whose target expands as the
				// reclaim pool changes). Rejecting such grows would deadlock the
				// container at its current (possibly minimal) size. Legacy /
				// overlap mode retains the exact-size contract.
				allocationQuantity := allocationInfo.AllocationResult.Size()
				if calculationQuantity != allocationQuantity &&
					(!resp.DisableDedicatedCoresOverlapReclaimedCores ||
						!allocationInfo.CheckDedicatedNUMABinding()) {
					return fmt.Errorf("pod: %s container: %s calculation result: %d and allocation result: %d mismatch",
						podUID, containerName, calculationQuantity, allocationQuantity)
				}
			}
		}
	}

	// validate shared_cores with numa_binding entries
	sharedNUMABindingAllocationInfos := entries.GetFilteredPodEntries(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedNUMABinding))

	for podUID, containerEntries := range sharedNUMABindingAllocationInfos {
		for containerName, allocationInfo := range containerEntries {
			calculationInfo, ok := resp.GetCalculationInfo(podUID, containerName)

			if !ok {
				if allocationInfo.RampUp && allocationInfo.OwnerPoolName == commonstate.EmptyOwnerPoolName {
					continue
				}
				return fmt.Errorf("missing CalculationInfo for pod: %s container: %s", podUID, containerName)
			}

			if calculationInfo.OwnerPoolName == commonstate.EmptyOwnerPoolName {
				return fmt.Errorf("shared_cores with numa_biding pod: %s container: %s has empty pool name", podUID, containerName)
			}
		}
	}
	return nil
}

func (c *CPUAdvisorValidator) validateStaticPools(resp *advisorapi.ListAndWatchResponse) error {
	entries := c.state.GetPodEntries()

	for _, poolName := range state.StaticPools.List() {
		var nilStateEntry, nilRespEntry bool
		if entries[poolName] == nil || entries[poolName][commonstate.FakedContainerName] == nil {
			nilStateEntry = true
		}
		if resp.Entries[poolName] == nil || resp.Entries[poolName].Entries[commonstate.FakedContainerName] == nil {
			nilRespEntry = true
		}

		if nilStateEntry != nilRespEntry {
			return fmt.Errorf("pool: %s nilStateEntry: %v and nilRespEntry: %v mismatch",
				poolName, nilStateEntry, nilRespEntry)
		}
		if nilStateEntry {
			general.Warningf("got nil state entry for static pool: %s", poolName)
			continue
		}

		allocationInfo := entries[poolName][commonstate.FakedContainerName]
		calculationInfo := resp.Entries[poolName].Entries[commonstate.FakedContainerName]
		if calculationInfo.OwnerPoolName != poolName {
			return fmt.Errorf("pool: %s has invalid owner pool name: %s in cpu advisor resp",
				poolName, calculationInfo.OwnerPoolName)
		}

		if len(calculationInfo.CalculationResultsByNumas) != 1 ||
			calculationInfo.CalculationResultsByNumas[commonstate.FakedNUMAID] == nil ||
			len(calculationInfo.CalculationResultsByNumas[commonstate.FakedNUMAID].Blocks) != 1 {
			return fmt.Errorf("static pool: %s has invalid calculationInfo", poolName)
		}

		calculationQuantity, err := calculationInfo.GetTotalQuantity()
		if err != nil {
			return fmt.Errorf("GetTotalQuantity failed with error: %v, pool: %s",
				err, poolName)
		}

		// currently, we don't support strategy to adjust cpuset of static pools.
		// for stability if the static pool calculation result and allocation result, we will return error.
		if calculationQuantity != allocationInfo.AllocationResult.Size() {
			return fmt.Errorf("static pool: %s calculation result: %d and allocation result: %d mismatch",
				poolName, calculationQuantity, allocationInfo.AllocationResult.Size())
		}
	}
	return nil
}

func (c *CPUAdvisorValidator) validateForbiddenPools(resp *advisorapi.ListAndWatchResponse) error {
	entries := c.state.GetPodEntries()

	for _, poolName := range state.ForbiddenPools.List() {
		var nilStateEntry, nilRespEntry bool
		if entries[poolName] == nil || entries[poolName][commonstate.FakedContainerName] == nil {
			nilStateEntry = true
		}
		if resp.Entries[poolName] == nil || resp.Entries[poolName].Entries[commonstate.FakedContainerName] == nil {
			nilRespEntry = true
		}

		if nilStateEntry != nilRespEntry {
			return fmt.Errorf("pool: %s nilStateEntry: %v and nilRespEntry: %v mismatch",
				poolName, nilStateEntry, nilRespEntry)
		}
	}
	return nil
}

type blockOwnerPolicy struct {
	hasDedicated bool
	hasShared    bool
	hasReclaim   bool
}

func (c *CPUAdvisorValidator) validateOverlapPolicy(resp *advisorapi.ListAndWatchResponse) error {
	blockPolicies := make(map[string]*blockOwnerPolicy)
	err := visitAdvisorBlocks(resp, func(ownerPoolName string, block *advisorapi.Block) error {
		ownerPoolName, _ = resourcepackage.UnwrapOwnerPoolName(ownerPoolName)
		policy := blockPolicies[block.BlockId]
		if policy == nil {
			policy = &blockOwnerPolicy{}
			blockPolicies[block.BlockId] = policy
		}
		switch commonstate.GetPoolType(ownerPoolName) {
		case commonstate.PoolNameDedicated:
			policy.hasDedicated = true
		case commonstate.PoolNameReclaim:
			policy.hasReclaim = true
		default:
			policy.hasShared = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	for blockID, policy := range blockPolicies {
		if resp.DisableDedicatedCoresOverlapReclaimedCores && policy.hasDedicated && policy.hasReclaim {
			return fmt.Errorf("dedicated and reclaim share block %s while overlap is disabled", blockID)
		}
		if !resp.AllowSharedCoresOverlapReclaimedCores && policy.hasShared && policy.hasReclaim {
			return fmt.Errorf("shared and reclaim share block %s while overlap is disabled", blockID)
		}
	}
	return nil
}

func (c *CPUAdvisorValidator) validateResourcePackageOwners(resp *advisorapi.ListAndWatchResponse) error {
	blockPackages := make(map[string]string)
	blockPackageSet := make(map[string]bool)
	return visitAdvisorBlocks(resp, func(ownerPoolName string, block *advisorapi.Block) error {
		poolName, packageName := resourcepackage.UnwrapOwnerPoolName(ownerPoolName)
		if commonstate.GetPoolType(poolName) == commonstate.PoolNameReclaim && packageName == "" {
			return nil
		}
		if blockPackageSet[block.BlockId] && blockPackages[block.BlockId] != packageName {
			return fmt.Errorf("block %s aliases have incompatible resource packages", block.BlockId)
		}
		blockPackages[block.BlockId] = packageName
		blockPackageSet[block.BlockId] = true
		return nil
	})
}

func visitAdvisorBlocks(
	resp *advisorapi.ListAndWatchResponse,
	visit func(ownerPoolName string, block *advisorapi.Block) error,
) error {
	if resp == nil {
		return fmt.Errorf("got nil cpu advisor resp")
	}
	for entryName, entries := range resp.Entries {
		if entries == nil {
			continue
		}
		for subEntryName, info := range entries.Entries {
			if info == nil {
				continue
			}
			for numaID, result := range info.CalculationResultsByNumas {
				if result == nil {
					continue
				}
				for _, block := range result.Blocks {
					if block == nil {
						continue
					}
					if err := visit(info.OwnerPoolName, block); err != nil {
						return fmt.Errorf("entry %s sub-entry %s NUMA %d: %w",
							entryName, subEntryName, numaID, err)
					}
				}
			}
		}
	}
	return nil
}

func (c *CPUAdvisorValidator) validateBlocks(resp *advisorapi.ListAndWatchResponse) error {
	if c.machineInfo == nil || c.machineInfo.CPUTopology == nil {
		return fmt.Errorf("validateBlocksByTopology got nil topology")
	}

	if err := c.validateReclaimNUMAResults(resp); err != nil {
		return err
	}

	numaToBlocks, err := resp.GetBlocks()
	if err != nil {
		return fmt.Errorf("GetBlocks failed with error: %v", err)
	}

	totalQuantity := 0
	numas := c.machineInfo.CPUTopology.CPUDetails.NUMANodes()
	for numaId, blocks := range numaToBlocks {
		if numaId != commonstate.FakedNUMAID && !numas.Contains(numaId) {
			return fmt.Errorf("NUMA: %d referred by blocks isn't in topology", numaId)
		}

		numaQuantity := 0
		for _, block := range blocks {
			if block == nil {
				general.Warningf("got nil block")
				continue
			}

			quantityInt, err := general.CovertUInt64ToInt(block.Result)
			if err != nil {
				return fmt.Errorf("CovertUInt64ToInt failed with error: %v, blockId: %s, numaId: %d", err, block.BlockId, numaId)
			}
			numaQuantity += quantityInt
		}

		if numaId != commonstate.FakedNUMAID {
			numaCapacity := c.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(numaId).Size()
			if numaQuantity > numaCapacity {
				return fmt.Errorf("numaQuantity: %d exceeds NUMA capacity: %d in NUMA: %d", numaQuantity, numaCapacity, numaId)
			}
		}
		totalQuantity += numaQuantity
	}

	if totalQuantity > c.machineInfo.CPUTopology.NumCPUs {
		return fmt.Errorf("numaQuantity: %d exceeds total capacity: %d", totalQuantity, c.machineInfo.CPUTopology.NumCPUs)
	}
	return nil
}

func (c *CPUAdvisorValidator) validateReclaimNUMAResults(resp *advisorapi.ListAndWatchResponse) error {
	if resp == nil || resp.AllowSharedCoresOverlapReclaimedCores {
		return nil
	}
	reclaimInfo := getReclaimCalculationInfo(resp)
	if reclaimInfo == nil {
		return nil
	}

	for numaID, result := range reclaimInfo.CalculationResultsByNumas {
		if numaID == commonstate.FakedNUMAID {
			continue
		}
		if result == nil {
			return fmt.Errorf("empty reclaim blocks for NUMA: %d", numaID)
		}
		if len(result.Blocks) == 0 {
			return fmt.Errorf("empty reclaim blocks for NUMA: %d", numaID)
		}
		hasValidBlock := false
		for _, block := range result.Blocks {
			if block != nil {
				hasValidBlock = true
				break
			}
		}
		if !hasValidBlock {
			return fmt.Errorf("empty reclaim blocks for NUMA: %d", numaID)
		}
	}
	return nil
}

func getReclaimCalculationInfo(resp *advisorapi.ListAndWatchResponse) *advisorapi.CalculationInfo {
	if resp == nil || resp.Entries == nil {
		return nil
	}
	reclaimEntries := resp.Entries[commonstate.PoolNameReclaim]
	if reclaimEntries == nil {
		return nil
	}
	if info := reclaimEntries.Entries[commonstate.FakedContainerName]; info != nil {
		return info
	}
	for _, info := range reclaimEntries.Entries {
		if info != nil && info.OwnerPoolName == commonstate.PoolNameReclaim {
			return info
		}
	}
	for _, info := range reclaimEntries.Entries {
		if info != nil {
			return info
		}
	}
	return nil
}
