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

package canonical

import (
	"math"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const HintOptimizerNameCanonical = "canonical"

// canonicalHintOptimizer implements the HintOptimizer interface with a generic policy.
type canonicalHintOptimizer struct {
	state        state.State
	reservedCPUs machine.CPUSet

	cpuNUMAHintPreferPolicy       string
	cpuNUMAHintPreferLowThreshold float64
}

func (o *canonicalHintOptimizer) Run(<-chan struct{}) error {
	return nil
}

// NewCanonicalHintOptimizer creates a new canonicalHintOptimizer.
func NewCanonicalHintOptimizer(
	options policy.HintOptimizerFactoryOptions,
) (hintoptimizer.HintOptimizer, error) {
	return &canonicalHintOptimizer{
		state:                         options.State,
		reservedCPUs:                  options.ReservedCPUs,
		cpuNUMAHintPreferPolicy:       options.Conf.CPUNUMAHintPreferPolicy,
		cpuNUMAHintPreferLowThreshold: options.Conf.CPUNUMAHintPreferLowThreshold,
	}, nil
}

// OptimizeHints optimizes the topology hints based on the generic policy.
func (o *canonicalHintOptimizer) OptimizeHints(
	request hintoptimizer.Request,
	hints *pluginapi.ListOfTopologyHints,
) error {
	err := hintoptimizerutil.GenericOptimizeHintsCheck(request, hints)
	if err != nil {
		general.Errorf("GenericOptimizeHintsCheck failed with error: %v", err)
		return err
	}

	if qosutil.AnnotationsIndicateNUMAExclusive(request.Annotations) {
		general.Infof("skip canonicalHintOptimizer for exclusive numa pod: %s/%s, container: %s",
			request.PodNamespace, request.PodName, request.ContainerName)
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	numaNodes, err := hintoptimizerutil.GetSingleNUMATopologyHintNUMANodes(hints.Hints)
	if err != nil {
		return err
	}

	// todo: set the hints to empty now, and support post-filtering hints later
	hints.Hints = make([]*pluginapi.TopologyHint, 0, len(numaNodes))

	machineState := o.state.GetMachineState()
	switch o.cpuNUMAHintPreferPolicy {
	case cpuconsts.CPUNUMAHintPreferPolicyPacking, cpuconsts.CPUNUMAHintPreferPolicySpreading:
		general.Infof("apply %s state policy on NUMAs: %+v", o.cpuNUMAHintPreferPolicy, numaNodes)
		o.populateHintsByPreferPolicy(numaNodes, o.cpuNUMAHintPreferPolicy, hints, machineState, request.CPURequest)
	case cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking:
		filteredNUMANodes, filteredOutNUMANodes := o.filterNUMANodesByHintPreferLowThreshold(request.CPURequest, machineState, numaNodes)

		if len(filteredNUMANodes) > 0 {
			general.Infof("dynamically apply packing policy on NUMAs: %+v", filteredNUMANodes)
			o.populateHintsByPreferPolicy(filteredNUMANodes, cpuconsts.CPUNUMAHintPreferPolicyPacking, hints, machineState, request.CPURequest)
			cpuutil.PopulateHintsByAvailableNUMANodes(filteredOutNUMANodes, hints, false)
		} else {
			general.Infof("empty filteredNUMANodes, dynamically apply spreading policy on NUMAs: %+v", numaNodes)
			o.populateHintsByPreferPolicy(numaNodes, cpuconsts.CPUNUMAHintPreferPolicySpreading, hints, machineState, request.CPURequest)
		}
	default:
		general.Infof("unknown policy: %state, apply default spreading policy on NUMAs: %+v", o.cpuNUMAHintPreferPolicy, numaNodes)
		o.populateHintsByPreferPolicy(numaNodes, cpuconsts.CPUNUMAHintPreferPolicySpreading, hints, machineState, request.CPURequest)
	}

	return nil
}

// populateHintsByPreferPolicy is adapted from DynamicPolicy.populateHintsByPreferPolicy
func (o *canonicalHintOptimizer) populateHintsByPreferPolicy(numaNodes []int, preferPolicy string,
	hints *pluginapi.ListOfTopologyHints, machineState state.NUMANodeMap, request float64,
) {
	var preferIndexes []int
	maxLeft, minLeft := float64(-1), math.MaxFloat64
	for _, nodeID := range numaNodes {
		if machineState[nodeID] == nil {
			general.Warningf("NUMA node %d not found in machineState, skipping", nodeID)
			continue
		}
		availableCPUQuantity := machineState[nodeID].GetAvailableCPUQuantity(o.reservedCPUs)
		if !cpuutil.CPUIsSufficient(request, availableCPUQuantity) {
			general.Warningf("numa_binding shared_cores container skip NUMA: %d available: %.3f request: %.3f",
				nodeID, availableCPUQuantity, request)
			continue
		}

		hints.Hints = append(hints.Hints, &pluginapi.TopologyHint{
			Nodes: []uint64{uint64(nodeID)},
		})

		curLeft := availableCPUQuantity - request

		general.Infof("NUMA: %d, request %.3f, left cpu quantity: %.3f, ", nodeID, request, curLeft)

		if preferPolicy == cpuconsts.CPUNUMAHintPreferPolicyPacking {
			if curLeft < minLeft {
				minLeft = curLeft
				preferIndexes = []int{len(hints.Hints) - 1}
			} else if curLeft == minLeft {
				preferIndexes = append(preferIndexes, len(hints.Hints)-1)
			}
		} else {
			if curLeft > maxLeft {
				maxLeft = curLeft
				preferIndexes = []int{len(hints.Hints) - 1}
			} else if curLeft == maxLeft {
				preferIndexes = append(preferIndexes, len(hints.Hints)-1)
			}
		}
	}

	if len(preferIndexes) > 0 {
		for _, preferIndex := range preferIndexes {
			hints.Hints[preferIndex].Preferred = true
		}
	}
}

// filterNUMANodesByHintPreferLowThreshold is adapted from DynamicPolicy.filterNUMANodesByHintPreferLowThreshold
func (o *canonicalHintOptimizer) filterNUMANodesByHintPreferLowThreshold(request float64,
	machineState state.NUMANodeMap, numaNodes []int,
) ([]int, []int) {
	filteredNUMANodes := make([]int, 0, len(numaNodes))
	filteredOutNUMANodes := make([]int, 0, len(numaNodes))

	for _, nodeID := range numaNodes {
		if machineState[nodeID] == nil {
			general.Warningf("NUMA node %d not found in machineState, skipping", nodeID)
			continue
		}
		availableCPUQuantity := machineState[nodeID].GetAvailableCPUQuantity(o.reservedCPUs)
		allocatableCPUQuantity := machineState[nodeID].GetFilteredDefaultCPUSet(nil, nil).Difference(o.reservedCPUs).Size()

		if allocatableCPUQuantity == 0 {
			general.Warningf("numa: %d allocatable cpu quantity is zero", nodeID)
			continue
		}

		availAfterAllocated := availableCPUQuantity - request

		availableRatio := availAfterAllocated / float64(allocatableCPUQuantity)

		general.Infof("NUMA: %d, availableCPUQuantity: %.3f, availAfterAllocated: %.3f, allocatableCPUQuantity: %d, availableRatio: %.3f, cpuNUMAHintPreferLowThreshold:%.3f",
			nodeID, availableCPUQuantity, availAfterAllocated, allocatableCPUQuantity, availableRatio, o.cpuNUMAHintPreferLowThreshold)

		if availableRatio >= o.cpuNUMAHintPreferLowThreshold {
			filteredNUMANodes = append(filteredNUMANodes, nodeID)
		} else {
			filteredOutNUMANodes = append(filteredOutNUMANodes, nodeID)
		}
	}

	return filteredNUMANodes, filteredOutNUMANodes
}
