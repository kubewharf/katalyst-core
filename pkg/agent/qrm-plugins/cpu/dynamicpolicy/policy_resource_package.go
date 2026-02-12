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

package dynamicpolicy

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func (p *DynamicPolicy) syncResourcePackagePinnedCPUSet() {
	startTime := time.Now()
	p.Lock()
	defer func() {
		p.Unlock()
		general.InfoS("finished",
			"duration", time.Since(startTime).String(),
		)
	}()

	resourcePackages, err := p.resourcePackageManager.NodeResourcePackages(context.Background())
	if err != nil {
		general.Errorf("failed to get node resource packages: %v", err)
		_ = p.emitter.StoreInt64(util.MetricNameSyncResourcePackagePinnedCPUSetFailed, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		return
	}

	pinnedCPUSetSize, err := resourcePackages.ListAllPinnedCPUSetSize()
	if err != nil {
		general.Errorf("failed to get all pinned cpuset size: %v", err)
		_ = p.emitter.StoreInt64(util.MetricNameSyncResourcePackagePinnedCPUSetFailed, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		return
	}

	interruptAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameInterrupt, commonstate.FakedContainerName)

	machineState := p.state.GetMachineState()
	podEntries := p.state.GetPodEntries()

	newResourcePackagePinnedCPUSetMap := make(map[int]map[string]machine.CPUSet)
	stateChanged := false

	for _, numaID := range p.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
		numaState := machineState[numaID]
		if numaState == nil {
			continue
		}

		newPinnedMap, changed, err := p.syncNumaResourcePackage(numaID, numaState, pinnedCPUSetSize, interruptAllocationInfo, podEntries)
		if err != nil {
			general.Errorf("failed to sync resource package for numa %d: %v", numaID, err)
			_ = p.emitter.StoreInt64(util.MetricNameSyncResourcePackagePinnedCPUSetFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
				metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)})
			return
		}

		if newPinnedMap != nil {
			newResourcePackagePinnedCPUSetMap[numaID] = newPinnedMap
		}

		if changed {
			stateChanged = true
		}
	}

	if stateChanged {
		for numaID, pkgs := range newResourcePackagePinnedCPUSetMap {
			if machineState[numaID] != nil {
				machineState[numaID].ResourcePackagePinnedCPUSet = pkgs
			}
		}

		err = p.adjustAllocationEntries(podEntries, machineState, true)
		if err != nil {
			general.Errorf("adjustAllocationEntries failed: %v", err)
			return
		}
	}

	for numaID, pkgs := range newResourcePackagePinnedCPUSetMap {
		for pkgName, cset := range pkgs {
			_ = p.emitter.StoreInt64(util.MetricNameResourcePackagePinnedCPUSetSize, int64(cset.Size()), metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
				metrics.MetricTag{Key: "package_name", Val: pkgName})
		}
	}
}

func (p *DynamicPolicy) syncNumaResourcePackage(
	numaID int,
	numaState *state.NUMANodeState,
	pinnedCPUSetSize map[int]map[string]int,
	interruptAllocationInfo *state.AllocationInfo,
	podEntries state.PodEntries,
) (map[string]machine.CPUSet, bool, error) {
	mandatoryCPUsMap := make(map[string]machine.CPUSet)
	sharedRequestsMap := make(map[string]float64)
	activePackages := sets.NewString()
	sharedPodsMap := make(map[string][]*state.AllocationInfo)
	stateChanged := false
	newResourcePackagePinnedCPUSet := make(map[string]machine.CPUSet)

	for _, containerEntries := range numaState.PodEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			pkgName := allocationInfo.GetResourcePackageName()
			if pkgName == "" {
				continue
			}

			activePackages.Insert(pkgName)

			if allocationInfo.CheckDedicated() {
				if _, ok := mandatoryCPUsMap[pkgName]; !ok {
					mandatoryCPUsMap[pkgName] = machine.NewCPUSet()
				}

				// Ensure we only count CPUs on this NUMA node, handling cross-NUMA cases
				dedicatedCPUs := allocationInfo.AllocationResult.Intersection(p.machineInfo.CPUDetails.CPUsInNUMANodes(numaID))
				mandatoryCPUsMap[pkgName] = mandatoryCPUsMap[pkgName].Union(dedicatedCPUs)
			} else if allocationInfo.CheckSharedNUMABinding() {
				sharedRequestsMap[pkgName] += allocationInfo.RequestQuantity
				if sharedPodsMap[pkgName] == nil {
					sharedPodsMap[pkgName] = make([]*state.AllocationInfo, 0)
				}
				sharedPodsMap[pkgName] = append(sharedPodsMap[pkgName], allocationInfo)
			}
		}
	}

	availableCPUs := p.machineInfo.CPUDetails.CPUsInNUMANodes(numaID).Difference(p.reservedCPUs)
	// exclude interrupt cpuset from available cpuset
	if interruptAllocationInfo != nil {
		availableCPUs = availableCPUs.Difference(interruptAllocationInfo.AllocationResult)
	}

	allMandatoryPinned := machine.NewCPUSet()
	allSharedPinnedRequest := float64(0)
	if pkgs, ok := pinnedCPUSetSize[numaID]; ok {
		for pkg := range pkgs {
			if cset, exists := mandatoryCPUsMap[pkg]; exists {
				allMandatoryPinned = allMandatoryPinned.Union(cset)
			}

			if sharedReq, exists := sharedRequestsMap[pkg]; exists {
				allSharedPinnedRequest += sharedReq
			}
		}
	}
	allocatedDedicatedNonPinned := numaState.AllocatedCPUSet.Difference(allMandatoryPinned)

	if pkgs, ok := pinnedCPUSetSize[numaID]; ok {
		for pkgName, neededSize := range pkgs {
			mandatoryCPUs := mandatoryCPUsMap[pkgName]
			sharedReq := sharedRequestsMap[pkgName]

			minSize := mandatoryCPUs.Size() + int(math.Ceil(sharedReq))
			targetSize := neededSize
			if minSize > targetSize {
				targetSize = minSize
			}

			currentPinned := numaState.ResourcePackagePinnedCPUSet[pkgName]
			otherPinned := machine.NewCPUSet()
			for otherPkg, cset := range numaState.ResourcePackagePinnedCPUSet {
				if otherPkg != pkgName {
					otherPinned = otherPinned.Union(cset)
				}
			}

			availableForPkg := availableCPUs.Difference(allocatedDedicatedNonPinned).Difference(otherPinned)

			var newPinned machine.CPUSet

			if currentPinned.Size() < targetSize {
				delta := targetSize - currentPinned.Size()
				candidates := availableForPkg.Difference(currentPinned)
				newCPUs, err := calculator.TakeByTopology(p.machineInfo, candidates, delta, true)
				if err != nil {
					general.Errorf("failed to expand pinned cpuset for pkg %s: %v", pkgName, err)
					_ = p.emitter.StoreInt64(util.MetricNameSyncNumaResourcePackageFailed, 1, metrics.MetricTypeNameRaw,
						metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
						metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
						metrics.MetricTag{Key: "package_name", Val: pkgName},
						metrics.MetricTag{Key: "reason", Val: "expand_failed"})
					newPinned = currentPinned
				} else {
					newPinned = currentPinned.Union(newCPUs)
				}
			} else if currentPinned.Size() > targetSize {
				candidates := currentPinned.Difference(mandatoryCPUs)
				keepSize := targetSize - mandatoryCPUs.Size()

				if keepSize > 0 {
					kept, err := calculator.TakeByTopology(p.machineInfo, candidates, keepSize, true)
					if err != nil {
						general.Errorf("failed to shrink (select kept) for pkg %s: %v", pkgName, err)
						_ = p.emitter.StoreInt64(util.MetricNameSyncNumaResourcePackageFailed, 1, metrics.MetricTypeNameRaw,
							metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
							metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
							metrics.MetricTag{Key: "package_name", Val: pkgName},
							metrics.MetricTag{Key: "reason", Val: "shrink_failed"})
						newPinned = currentPinned
					} else {
						newPinned = mandatoryCPUs.Union(kept)
					}
				} else {
					newPinned = mandatoryCPUs
				}
			} else {
				newPinned = currentPinned
			}

			newResourcePackagePinnedCPUSet[pkgName] = newPinned

			if !newPinned.Equals(currentPinned) {
				stateChanged = true
			}
		}
	}

	for pkgName, currentPinned := range numaState.ResourcePackagePinnedCPUSet {
		// Check if pinnedCPUSetSize[numaID] exists before accessing inner map
		if pkgs, ok := pinnedCPUSetSize[numaID]; !ok || pkgs == nil {
			// If NUMA ID is not in config, check if package is active
			if activePackages.Has(pkgName) {
				newResourcePackagePinnedCPUSet[pkgName] = currentPinned
				general.Errorf("resource package %s removed from config (NUMA %d missing) but still has pods", pkgName, numaID)
			} else {
				stateChanged = true
				general.Infof("resource package %s removed from numa %d (config missing)", pkgName, numaID)
			}
		} else {
			// If NUMA ID exists, check if package is in config
			if _, ok := pkgs[pkgName]; !ok {
				if activePackages.Has(pkgName) {
					newResourcePackagePinnedCPUSet[pkgName] = currentPinned
					general.Errorf("resource package %s removed from config but still has pods on numa %d", pkgName, numaID)
				} else {
					stateChanged = true
					general.Infof("resource package %s removed from numa %d", pkgName, numaID)
				}
			}
		}
	}

	if stateChanged {
		// Validate resource availability for non-pinned shared cores
		totalPinnedCPUSet := machine.NewCPUSet()
		for _, cset := range newResourcePackagePinnedCPUSet {
			totalPinnedCPUSet = totalPinnedCPUSet.Union(cset)
		}

		availableForNonPinned := availableCPUs.Difference(totalPinnedCPUSet).Difference(numaState.AllocatedCPUSet)
		totalNonPinnedRequest := 0.0
		hasNoPackagePod := false

		for _, containerEntries := range numaState.PodEntries {
			if containerEntries.IsPoolEntry() {
				continue
			}
			for _, allocationInfo := range containerEntries {
				if allocationInfo == nil {
					continue
				}

				// Only check shared cores pods
				if !allocationInfo.CheckSharedNUMABinding() {
					continue
				}

				pkgName := allocationInfo.GetResourcePackageName()
				// Check if pod is non-pinned:
				// 1. No package name
				// 2. Package name exists but not in newResourcePackagePinnedCPUSet
				isPinned := false
				if pkgName != "" {
					if _, ok := newResourcePackagePinnedCPUSet[pkgName]; ok {
						isPinned = true
					}
				} else {
					hasNoPackagePod = true
				}

				if !isPinned {
					totalNonPinnedRequest += allocationInfo.RequestQuantity
				}
			}
		}

		if totalNonPinnedRequest > float64(availableForNonPinned.Size()) {
			general.Errorf("resource validation failed for numa %d: non-pinned request %.2f exceeds available capacity %d. "+
				"Pinned packages occupy %d CPUs. Has pods without package: %v. "+
				"Details: availableCPUs=%s, totalPinned=%s, availableForNonPinned=%s",
				numaID, totalNonPinnedRequest, availableForNonPinned.Size(), totalPinnedCPUSet.Size(), hasNoPackagePod,
				availableCPUs.String(), totalPinnedCPUSet.String(), availableForNonPinned.String())
			return nil, false, fmt.Errorf("insufficient resources for non-pinned shared pods on numa %d: request %.2f > available %d",
				numaID, totalNonPinnedRequest, availableForNonPinned.Size())
		}
	}

	return newResourcePackagePinnedCPUSet, stateChanged, nil
}
