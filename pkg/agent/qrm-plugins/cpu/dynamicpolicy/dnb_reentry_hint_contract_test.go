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
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestExclusiveDNBHintsSeparateNewRequestFromExistingAllocation(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()

	entries := state.PodEntries{
		"dnb-a": {"main": newExclusiveDNBAllocation(topology, "dnb-a", 6, 0, 1)},
		"dnb-b": {"main": newExclusiveDNBAllocation(topology, "dnb-b", 6, 2, 3)},
	}
	machineState, err := generateMachineStateFromPodEntries(topology, entries, policy.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, policy.state.CommitAdvisorState(entries, machineState, false, false, false))

	newRequest := newExclusiveDNBHintRequest("dnb-c", 6)
	beforeRevision := policy.state.GetRevision()
	beforeEntries := policy.state.GetPodEntries()
	beforeMachineState := policy.state.GetMachineState()

	_, err = policy.dedicatedCoresWithNUMABindingHintHandler(context.Background(), newRequest)
	require.ErrorContains(t, err, cpuutil.ErrNoAvailableCPUHints.Error())
	require.Equal(t, beforeRevision, policy.state.GetRevision())
	require.True(t, reflect.DeepEqual(beforeEntries, policy.state.GetPodEntries()))
	require.True(t, reflect.DeepEqual(beforeMachineState, policy.state.GetMachineState()))

	existingRequest := newExclusiveDNBHintRequest("dnb-a", 6)
	response, err := policy.dedicatedCoresWithNUMABindingHintHandler(context.Background(), existingRequest)
	require.NoError(t, err)
	require.NotNil(t, response)
	hints := response.ResourceHints[string(v1.ResourceCPU)]
	require.NotNil(t, hints)
	require.Len(t, hints.Hints, 1)
	require.Equal(t, []uint64{0, 1}, hints.Hints[0].Nodes)
	require.True(t, hints.Hints[0].Preferred)
	require.Equal(t, beforeRevision, policy.state.GetRevision())
	require.True(t, reflect.DeepEqual(beforeEntries, policy.state.GetPodEntries()))
	require.True(t, reflect.DeepEqual(beforeMachineState, policy.state.GetMachineState()))
}

func TestExistingShortDNBAllocateReturnsWithoutReplanningAfterHardPartitionDisabled(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()
	dynamicConfig := policy.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConfig.EnableReclaim = true
	dynamicConfig.EnableRampUpReclaimHardPartition = true

	allocation := newExclusiveDNBAllocation(topology, "dnb-a", 6, 0, 1)
	shortAssignments := map[int]machine.CPUSet{
		0: firstDNBContractCPUs(allocation.TopologyAwareAssignments[0], 2),
		1: firstDNBContractCPUs(allocation.TopologyAwareAssignments[1], 2),
	}
	shortAllocation := shortAssignments[0].Union(shortAssignments[1])
	allocation.AllocationResult = shortAllocation.Clone()
	allocation.OriginalAllocationResult = shortAllocation.Clone()
	allocation.TopologyAwareAssignments = machine.DeepcopyCPUAssignment(shortAssignments)
	allocation.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(shortAssignments)
	allocation.RampUp = false

	entries := state.PodEntries{"dnb-a": {"main": allocation}}
	machineState, err := generateMachineStateFromPodEntries(topology, entries, policy.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, policy.state.CommitAdvisorState(entries, machineState, false, false, false))

	// The allocation identity survives dynamic configuration changes. Re-entry
	// must reuse the committed short result instead of trying to grow it again.
	dynamicConfig.EnableReclaim = false
	dynamicConfig.EnableRampUpReclaimHardPartition = false

	handlerCalled := false
	policy.allocation.allocationHandlers[apiconsts.PodAnnotationQoSLevelDedicatedCores] = func(
		_ context.Context,
		_ *pluginapi.ResourceRequest,
		_ bool,
	) (*pluginapi.ResourceAllocationResponse, error) {
		handlerCalled = true
		return nil, nil
	}

	beforeRevision := policy.state.GetRevision()
	beforeEntries := policy.state.GetPodEntries()
	beforeMachineState := policy.state.GetMachineState()
	response, err := policy.Allocate(context.Background(), newExclusiveDNBHintRequest("dnb-a", 6))
	require.NoError(t, err)
	require.NotNil(t, response)
	require.False(t, handlerCalled, "persisted short DNB allocation must survive a hard-partition config change")
	cpuAllocation := response.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)]
	require.NotNil(t, cpuAllocation)
	require.Equal(t, shortAllocation.String(), cpuAllocation.AllocationResult)
	require.Equal(t, beforeRevision, policy.state.GetRevision())
	require.True(t, reflect.DeepEqual(beforeEntries, policy.state.GetPodEntries()))
	require.True(t, reflect.DeepEqual(beforeMachineState, policy.state.GetMachineState()))
}

func TestExistingExclusiveDNBAllocateReturnsWithoutReplanning(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()

	allocation := newExclusiveDNBAllocation(topology, "dnb-a", 6, 0, 1)
	entries := state.PodEntries{"dnb-a": {"main": allocation}}
	machineState, err := generateMachineStateFromPodEntries(topology, entries, policy.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, policy.state.CommitAdvisorState(entries, machineState, false, false, false))

	handlerCalled := false
	policy.allocation.allocationHandlers[apiconsts.PodAnnotationQoSLevelDedicatedCores] = func(
		_ context.Context,
		_ *pluginapi.ResourceRequest,
		_ bool,
	) (*pluginapi.ResourceAllocationResponse, error) {
		handlerCalled = true
		return nil, nil
	}

	beforeRevision := policy.state.GetRevision()
	beforeEntries := policy.state.GetPodEntries()
	beforeMachineState := policy.state.GetMachineState()
	response, err := policy.Allocate(context.Background(), newExclusiveDNBHintRequest("dnb-a", 6))
	require.NoError(t, err)
	require.NotNil(t, response)
	require.False(t, handlerCalled, "satisfied allocation must not enter the reallocation handler")
	cpuAllocation := response.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)]
	require.NotNil(t, cpuAllocation)
	require.Equal(t, allocation.AllocationResult.String(), cpuAllocation.AllocationResult)
	require.Equal(t, beforeRevision, policy.state.GetRevision())
	require.True(t, reflect.DeepEqual(beforeEntries, policy.state.GetPodEntries()))
	require.True(t, reflect.DeepEqual(beforeMachineState, policy.state.GetMachineState()))
}

func newExclusiveDNBAllocation(
	topology *machine.CPUTopology,
	podUID string,
	request float64,
	numaIDs ...int,
) *state.AllocationInfo {
	cpus := topology.CPUDetails.CPUsInNUMANodes(numaIDs...)
	assignments := make(map[int]machine.CPUSet, len(numaIDs))
	for _, numaID := range numaIDs {
		assignments[numaID] = topology.CPUDetails.CPUsInNUMANodes(numaID)
	}
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       podUID,
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			OwnerPoolName: commonstate.PoolNameDedicated,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Labels: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
			},
			Annotations: exclusiveAnnotations(),
		},
		AllocationResult:                 cpus.Clone(),
		OriginalAllocationResult:         cpus.Clone(),
		TopologyAwareAssignments:         machine.DeepcopyCPUAssignment(assignments),
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
		RequestQuantity:                  request,
	}
}

func newExclusiveDNBHintRequest(podUID string, request float64) *pluginapi.ResourceRequest {
	return &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   "default",
		PodName:        podUID,
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): request,
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
	}
}

func firstDNBContractCPUs(cpus machine.CPUSet, count int) machine.CPUSet {
	items := cpus.ToSliceInt()
	return machine.NewCPUSet(items[:count]...)
}
