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

package resourcepackage

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/resourcepackage"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const HintOptimizerNameResourcePackage = "resource_package"

// resourcePackageHintOptimizer implements the HintOptimizer interface based on resource package information.
type resourcePackageHintOptimizer struct {
	conf         *config.Configuration
	rpm          resourcepackage.ResourcePackageManager
	emitter      metrics.MetricEmitter
	state        state.State
	reservedCPUs machine.CPUSet
}

// NewResourcePackageHintOptimizer creates a new resourcePackageHintOptimizer.
func NewResourcePackageHintOptimizer(
	options policy.HintOptimizerFactoryOptions,
) (hintoptimizer.HintOptimizer, error) {
	o := &resourcePackageHintOptimizer{
		conf:         options.Conf,
		rpm:          options.ResourcePackageManager,
		emitter:      options.Emitter,
		state:        options.State,
		reservedCPUs: options.ReservedCPUs,
	}
	return o, nil
}

// OptimizeHints optimizes the topology hints based on resource package information.
func (o *resourcePackageHintOptimizer) OptimizeHints(
	request hintoptimizer.Request,
	hints *pluginapi.ListOfTopologyHints,
) error {
	err := hintoptimizerutil.GenericOptimizeHintsCheck(request, hints)
	if err != nil {
		general.Errorf("GenericOptimizeHintsCheck failed with error: %v", err)
		return err
	}

	if qosutil.AnnotationsIndicateNUMAExclusive(request.Annotations) {
		general.Infof("skip resourcePackageHintOptimizer for exclusive numa pod: %s/%s, container: %s",
			request.PodNamespace, request.PodName, request.ContainerName)
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	resourcePackage := rputil.GetResourcePackageName(request.ResourceRequest.Annotations)

	machineState := o.state.GetMachineState()
	numaRPPinnedCPUSet := machineState.GetNUMAResourcePackagePinnedCPUSet()
	hasPinned := false
	for _, rpPinned := range numaRPPinnedCPUSet {
		if len(rpPinned) > 0 {
			hasPinned = true
			break
		}
	}

	if resourcePackage == "" && !hasPinned {
		general.Infof("skip resourcePackageHintOptimizer for pod resource package not found in annotation and no pinned rp on node")
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	var resourcePackageAllocatableMap map[int]float64
	var resourcePackageAllocatedMap map[int]float64
	var unpinnedAvailableMap map[int]float64

	resourcePackageAllocatableMap, resourcePackageAllocatedMap, unpinnedAvailableMap, err = o.calculateCPUMetrics(resourcePackage, hasPinned, machineState, numaRPPinnedCPUSet)
	if err != nil {
		general.Errorf("calculateCPUMetrics failed with error: %v", err)
		return err
	}

	general.Infof("optimize hints for pod %s/%s, container %s, resource package %s, cpu request %.3f, resourcePackageAllocatableMap %+v, resourcePackageAllocatedMap %+v, unpinnedAvailableMap %+v, hints %+v",
		request.PodNamespace, request.PodName, request.ContainerName, resourcePackage, request.CPURequest, resourcePackageAllocatableMap, resourcePackageAllocatedMap, unpinnedAvailableMap, hints)

	// Optimize hints based on resource package information
	err = o.populateHintsByResourcePackage(hints, request.CPURequest, resourcePackage, resourcePackageAllocatableMap, resourcePackageAllocatedMap, unpinnedAvailableMap, numaRPPinnedCPUSet)
	if err != nil {
		general.Errorf("populateHintsByResourcePackage failed with error: %v", err)
		return err
	}

	if len(hints.Hints) == 0 {
		return fmt.Errorf("no hints found for resource package %s (or unpinned available not enough)", resourcePackage)
	}

	general.Infof("optimized hints for pod %s/%s, container %s, resource package %s, cpu request %.3f, resourcePackageAllocatableMap %+v, resourcePackageAllocatedMap %+v, unpinnedAvailableMap %+v, hints %+v",
		request.PodNamespace, request.PodName, request.ContainerName, resourcePackage, request.CPURequest, resourcePackageAllocatableMap, resourcePackageAllocatedMap, unpinnedAvailableMap, hints)

	return nil
}

// Run starts the resource package hint optimizer.
func (o *resourcePackageHintOptimizer) Run(_ <-chan struct{}) error {
	return nil
}

// populateHintsByResourcePackage optimizes hints based on resource package information.
func (o *resourcePackageHintOptimizer) populateHintsByResourcePackage(
	hints *pluginapi.ListOfTopologyHints,
	cpuRequest float64,
	resourcePackage string,
	resourcePackageAllocatableMap map[int]float64,
	resourcePackageAllocatedMap map[int]float64,
	unpinnedAvailableMap map[int]float64,
	numaRPPinnedCPUSet map[int]map[string]machine.CPUSet,
) error {
	canAllocateNodes := sets.NewInt()

	// Gather all node IDs from hints
	for _, hint := range hints.Hints {
		if len(hint.Nodes) != 1 {
			continue
		}
		nodeID := int(hint.Nodes[0])

		// 1. Check Resource Package logical quota
		if resourcePackage != "" {
			allocatable, ok := resourcePackageAllocatableMap[nodeID]
			if !ok || allocatable-resourcePackageAllocatedMap[nodeID] < cpuRequest {
				continue
			}
		}

		// 2. Check Unpinned physical capacity
		if unpinnedAvailableMap != nil {
			isPinned := resourcePackage != "" && numaRPPinnedCPUSet[nodeID] != nil && !numaRPPinnedCPUSet[nodeID][resourcePackage].IsEmpty()
			if !isPinned {
				if unpinnedAvailable, ok := unpinnedAvailableMap[nodeID]; !ok || unpinnedAvailable < cpuRequest {
					continue
				}
			}
		}

		canAllocateNodes.Insert(nodeID)
	}

	optimizedHints := make([]*pluginapi.TopologyHint, 0, len(hints.Hints))
	for _, hint := range hints.Hints {
		if len(hint.Nodes) != 1 {
			continue
		}

		if canAllocateNodes.Has(int(hint.Nodes[0])) {
			optimizedHints = append(optimizedHints, hint)
		}
	}

	hints.Hints = optimizedHints
	return nil
}

// calculateCPUMetrics iterates over the machineState to compute logical quota allocation
// for the given resourcePackage and physical unpinned CPU availability for nodes with pinned CPUs.
// By consolidating the loops, it avoids redundant traversal of container entries across multiple nodes.
func (o *resourcePackageHintOptimizer) calculateCPUMetrics(
	resourcePackage string,
	hasPinned bool,
	machineState state.NUMANodeMap,
	numaRPPinnedCPUSet map[int]map[string]machine.CPUSet,
) (
	resourcePackageAllocatableMap map[int]float64,
	resourcePackageAllocatedMap map[int]float64,
	unpinnedAvailableMap map[int]float64,
	err error,
) {
	if resourcePackage != "" {
		resourcePackageAllocatableMap, err = o.fetchResourcePackageAllocatable(resourcePackage)
		if err != nil {
			return
		}
		resourcePackageAllocatedMap = make(map[int]float64)
	}

	if hasPinned {
		unpinnedAvailableMap = make(map[int]float64)
	}

	for nodeID, ns := range machineState {
		allocatedForRP, unpinnedAvailable := o.calculateNodeCPUMetrics(ns, nodeID, resourcePackage, hasPinned, numaRPPinnedCPUSet)

		if resourcePackage != "" {
			resourcePackageAllocatedMap[nodeID] = allocatedForRP
		}
		if hasPinned {
			unpinnedAvailableMap[nodeID] = unpinnedAvailable
		}
	}

	return
}

// fetchResourcePackageAllocatable fetches the physical CPU allocatable limits
// (logical quota) for a specific resourcePackage across all NUMA nodes from the ResourcePackageManager.
func (o *resourcePackageHintOptimizer) fetchResourcePackageAllocatable(resourcePackage string) (map[int]float64, error) {
	resourcePackageMap, err := o.rpm.NodeResourcePackages(context.Background())
	if err != nil {
		return nil, fmt.Errorf("NodeResourcePackages failed with error: %v", err)
	}

	if resourcePackageMap == nil {
		return nil, fmt.Errorf("resourcePackageMap is nil")
	}

	allocatableMap := make(map[int]float64)
	for nodeID, packages := range resourcePackageMap {
		if pkg, ok := packages[resourcePackage]; ok && pkg.Allocatable != nil {
			cpuQuantity := native.CPUQuantityGetter()(*pkg.Allocatable)
			if !cpuQuantity.IsZero() {
				allocatableMap[nodeID] = float64(cpuQuantity.MilliValue()) / 1000
			}
		}
	}
	return allocatableMap, nil
}

// calculateNodeCPUMetrics calculates both logical allocation and unpinned physical allocation for a specific NUMA node.
// Returns:
// 1. allocatedForRP: the CPU request sum of all pods currently scheduled in the given resourcePackage.
// 2. unpinnedAvailable: the remaining unpinned CPU cores that non-pinned pods can use.
func (o *resourcePackageHintOptimizer) calculateNodeCPUMetrics(
	ns *state.NUMANodeState,
	nodeID int,
	resourcePackage string,
	hasPinned bool,
	numaRPPinnedCPUSet map[int]map[string]machine.CPUSet,
) (float64, float64) {
	if ns == nil {
		return 0, 0
	}

	var allocatedForRP float64 = 0
	var unpinnedPreciseAllocated float64 = 0
	var pinnedCPUSet machine.CPUSet

	for _, containerEntries := range ns.PodEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		// 1. Calculate logically allocated CPU requests for the specific resource package
		if resourcePackage != "" {
			for _, entry := range containerEntries {
				if entry != nil && rputil.GetResourcePackageName(entry.Annotations) == resourcePackage {
					allocatedForRP += entry.RequestQuantity
				}
			}
		}

		// 2. Calculate the precise CPU requests from pods that consume "Unpinned" CPU capacity
		if hasPinned {
			mainContainerEntry := containerEntries.GetMainContainerEntry()
			if mainContainerEntry == nil {
				continue
			}

			rpName := rputil.GetResourcePackageName(mainContainerEntry.Annotations)
			if rpName != "" && numaRPPinnedCPUSet[nodeID] != nil && !numaRPPinnedCPUSet[nodeID][rpName].IsEmpty() {
				// This pod belongs to a Resource Package that has explicitly Pinned CPU sets on this NUMA node.
				// Thus, its CPU overhead is encapsulated inside the PinnedCPUSet and does NOT consume Unpinned CPU capacity.
				continue
			}

			// For any pod that is not pinned on this node, if it's binding to NUMA, we accumulate its CPU request.
			if mainContainerEntry.CheckSharedNUMABinding() {
				if aggregatedPodResource, ok := mainContainerEntry.GetPodAggregatedRequest(); ok {
					unpinnedPreciseAllocated += aggregatedPodResource
				} else {
					for _, allocationInfo := range containerEntries {
						if allocationInfo != nil && allocationInfo.CheckSharedNUMABinding() {
							unpinnedPreciseAllocated += allocationInfo.RequestQuantity
						}
					}
				}
			} else if mainContainerEntry.CheckDedicated() {
				for _, allocationInfo := range containerEntries {
					if allocationInfo != nil && allocationInfo.CheckDedicated() {
						if cpuset, ok := allocationInfo.OriginalTopologyAwareAssignments[nodeID]; ok {
							unpinnedPreciseAllocated += float64(cpuset.Size())
						}
					}
				}
			}
		}
	}

	var unpinnedAvailable float64 = 0
	if hasPinned {
		pinnedCPUSet = machine.NewCPUSet()
		if rpPinned, ok := numaRPPinnedCPUSet[nodeID]; ok {
			for _, cpuset := range rpPinned {
				pinnedCPUSet = pinnedCPUSet.Union(cpuset)
			}
		}

		// Calculate the actual available physical CPU quantity for "Unpinned" pods.
		// unpinnedAllocatable is calculated by subtracting reservedCPUs and PinnedCPUs from DefaultCPUSet size.
		unpinnedAllocatable := float64(ns.GetFilteredDefaultCPUSet(nil, nil).Difference(o.reservedCPUs).Difference(pinnedCPUSet).Size())

		// To accurately account for non-NUMA-binding shared_cores or isolated_cores that might consume CPUs
		// on this NUMA node, we also verify the actual size of the allocated CPUSet excluding the pinned ones.
		unpinnedAllocatedCPUSet := ns.AllocatedCPUSet.Difference(pinnedCPUSet)
		unpinnedAllocatedQuantity := float64(unpinnedAllocatedCPUSet.Size())

		if unpinnedPreciseAllocated > unpinnedAllocatedQuantity {
			unpinnedAllocatedQuantity = unpinnedPreciseAllocated
		}

		unpinnedAvailable = general.MaxFloat64(unpinnedAllocatable-unpinnedAllocatedQuantity, 0)
	}

	return allocatedForRP, unpinnedAvailable
}
