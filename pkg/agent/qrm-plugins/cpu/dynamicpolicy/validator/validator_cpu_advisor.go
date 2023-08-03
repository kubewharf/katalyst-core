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

	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type cpuAdvisorValidationFunc func(resp *advisorapi.ListAndWatchResponse) error

type CPUAdvisorValidator struct {
	state       state.State
	machineInfo *machine.KatalystMachineInfo
}

func NewCPUAdvisorValidator(state state.State, machineInfo *machine.KatalystMachineInfo) *CPUAdvisorValidator {
	return &CPUAdvisorValidator{
		state:       state,
		machineInfo: machineInfo,
	}
}

func (c *CPUAdvisorValidator) Validate(resp *advisorapi.ListAndWatchResponse) error {
	if resp == nil {
		return fmt.Errorf("got nil cpu advisor resp")
	}

	var errList []error
	for _, validator := range []cpuAdvisorValidationFunc{
		c.validateDedicatedEntries,
		c.validateStaticPools,
		c.validateBlocks,
	} {
		errList = append(errList, validator(resp))
	}
	return errors.NewAggregate(errList)
}

func (c *CPUAdvisorValidator) validateDedicatedEntries(resp *advisorapi.ListAndWatchResponse) error {
	entries := c.state.GetPodEntries()

	dedicatedAllocationInfos := entries.GetFilteredPodEntries(state.CheckDedicated)
	dedicatedCalculationInfos := resp.FilterCalculationInfo(state.PoolNameDedicated)
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

			if !state.CheckDedicatedNUMABinding(allocationInfo) {
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

				// currently, we don't support strategy to adjust cpuset of dedicated_cores containers.
				// for stability if the dedicated_cores container calculation result and allocation result, we will return error.
				if calculationQuantity != allocationInfo.AllocationResult.Size() {
					return fmt.Errorf("pod: %s container: %s calculation result: %d and allocation result: %d mismatch",
						podUID, containerName, calculationQuantity, allocationInfo.AllocationResult.Size())
				}
			}
		}
	}
	return nil
}

func (c *CPUAdvisorValidator) validateStaticPools(resp *advisorapi.ListAndWatchResponse) error {
	entries := c.state.GetPodEntries()

	for _, poolName := range state.StaticPools.List() {
		var nilStateEntry, nilRespEntry bool
		if entries[poolName] == nil || entries[poolName][advisorapi.FakedContainerName] == nil {
			nilStateEntry = true
		}
		if resp.Entries[poolName] == nil || resp.Entries[poolName].Entries[advisorapi.FakedContainerName] == nil {
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

		allocationInfo := entries[poolName][advisorapi.FakedContainerName]
		calculationInfo := resp.Entries[poolName].Entries[advisorapi.FakedContainerName]
		if calculationInfo.OwnerPoolName != poolName {
			return fmt.Errorf("pool: %s has invalid owner pool name: %s in cpu advisor resp",
				poolName, calculationInfo.OwnerPoolName)
		}

		if len(calculationInfo.CalculationResultsByNumas) != 1 ||
			calculationInfo.CalculationResultsByNumas[advisorapi.FakedNUMAID] == nil ||
			len(calculationInfo.CalculationResultsByNumas[advisorapi.FakedNUMAID].Blocks) != 1 {
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

func (c *CPUAdvisorValidator) validateBlocks(resp *advisorapi.ListAndWatchResponse) error {
	if c.machineInfo == nil || c.machineInfo.CPUTopology == nil {
		return fmt.Errorf("validateBlocksByTopology got nil topology")
	}

	numaToBlocks, err := resp.GetBlocks()
	if err != nil {
		return fmt.Errorf("GetBlocks failed with error: %v", err)
	}

	totalQuantity := 0
	numas := c.machineInfo.CPUTopology.CPUDetails.NUMANodes()
	for numaId, blocks := range numaToBlocks {
		if numaId != advisorapi.FakedNUMAID && !numas.Contains(numaId) {
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

		if numaId != advisorapi.FakedNUMAID {
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
