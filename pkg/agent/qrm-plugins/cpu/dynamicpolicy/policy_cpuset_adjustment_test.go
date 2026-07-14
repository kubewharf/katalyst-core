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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newPolicyForBypassTest builds a minimal DynamicPolicy that only carries the
// dynamic-configuration field required by the bypass helpers under test.
func newPolicyForBypassTest(enabled bool) *DynamicPolicy {
	dyn := dynamicconfig.NewDynamicAgentConfiguration()
	dyn.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = enabled
	return &DynamicPolicy{dynamicConfig: dyn}
}

func TestShouldBypassCPUSetAdjustment(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		enabled bool
		want    bool
	}{
		{"switch off", false, false},
		{"switch on", true, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPolicyForBypassTest(tc.enabled)
			assert.Equal(t, tc.want, p.shouldBypassCPUSetAdjustment())
		})
	}

	t.Run("nil dynamicConfig", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{dynamicConfig: nil}
		assert.False(t, p.shouldBypassCPUSetAdjustment())
	})
}

func TestShouldBypassCPUSetAdjustmentForAllocation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		qos  string
		want bool
	}{
		{"dedicated keeps cpuset", consts.PodAnnotationQoSLevelDedicatedCores, false},
		{"shared bypasses cpuset", consts.PodAnnotationQoSLevelSharedCores, true},
		{"reclaimed bypasses cpuset", consts.PodAnnotationQoSLevelReclaimedCores, true},
		{"system bypasses cpuset", consts.PodAnnotationQoSLevelSystemCores, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPolicyForBypassTest(true)
			req := &pluginapi.ResourceRequest{
				PodUid:        tc.name,
				PodNamespace:  "default",
				PodName:       tc.name,
				ContainerName: "main",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: tc.qos,
				},
			}
			allocationInfo := &state.AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req, commonstate.EmptyOwnerPoolName, tc.qos),
			}
			assert.Equal(t, tc.want, p.shouldBypassCPUSetAdjustmentForAllocation(allocationInfo))
		})
	}
}

func TestGetResourcesAllocationBypassClearsNonDedicatedQoS(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true

	testCases := []struct {
		podUID string
		qos    string
		cpus   machine.CPUSet
	}{
		{"pod-dedicated", consts.PodAnnotationQoSLevelDedicatedCores, machine.NewCPUSet(0, 1)},
		{"pod-shared", consts.PodAnnotationQoSLevelSharedCores, machine.NewCPUSet(2, 3)},
		{"pod-reclaimed", consts.PodAnnotationQoSLevelReclaimedCores, machine.NewCPUSet(4, 5)},
		{"pod-system", consts.PodAnnotationQoSLevelSystemCores, machine.NewCPUSet(6, 7)},
	}

	for _, tc := range testCases {
		req := &pluginapi.ResourceRequest{
			PodUid:        tc.podUID,
			PodNamespace:  "default",
			PodName:       tc.podUID,
			ContainerName: "main",
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: tc.qos,
				"test-key":                      tc.qos,
			},
		}
		allocationInfo := &state.AllocationInfo{
			AllocationMeta:           commonstate.GenerateGenericContainerAllocationMeta(req, commonstate.EmptyOwnerPoolName, tc.qos),
			AllocationResult:         tc.cpus,
			OriginalAllocationResult: tc.cpus.Clone(),
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: tc.cpus,
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: tc.cpus.Clone(),
			},
			RequestQuantity: float64(tc.cpus.Size()),
		}
		p.state.SetAllocationInfo(tc.podUID, "main", allocationInfo, false)
	}

	resp, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)

	for _, tc := range testCases {
		cpuInfo := resp.PodResources[tc.podUID].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
		if tc.qos == consts.PodAnnotationQoSLevelDedicatedCores {
			assert.Equal(t, tc.cpus.String(), cpuInfo.AllocationResult)
		} else {
			assert.Equal(t, "", cpuInfo.AllocationResult)
		}
		assert.Greater(t, cpuInfo.AllocatedQuantity, float64(0))
		assert.NotEmpty(t, cpuInfo.TopologyAssignments)
		assert.Equal(t, tc.qos, cpuInfo.Annotations["test-key"])
	}
}

func TestAllocateResponseClearsSharedCPUSetWhenBypassEnabled(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true

	req := &pluginapi.ResourceRequest{
		PodUid:         "shared-pod",
		PodNamespace:   "default",
		PodName:        "shared-pod",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	resp, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.AllocationResult)

	cpuInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)]
	require.NotNil(t, cpuInfo)
	assert.Empty(t, cpuInfo.AllocationResult)
}

func TestClearCPUSetInAllocation(t *testing.T) {
	t.Parallel()

	t.Run("nil allocation", func(t *testing.T) {
		t.Parallel()
		assert.NotPanics(t, func() { clearCPUSetInAllocation(nil) })
	})

	t.Run("clears cpuset in place, preserves other fields", func(t *testing.T) {
		t.Parallel()
		alloc := &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				"cpu": {
					AllocatedQuantity:   4,
					AllocationResult:    "0-3",
					TopologyAssignments: map[uint64]uint64{0: 4},
					Annotations:         map[string]string{"k": "v"},
				},
			},
		}
		clearCPUSetInAllocation(alloc)
		info := alloc.ResourceAllocation["cpu"]
		assert.Equal(t, "", info.AllocationResult)
		assert.EqualValues(t, 4, info.AllocatedQuantity)
		assert.Equal(t, uint64(4), info.TopologyAssignments[0])
		assert.Equal(t, "v", info.Annotations["k"])
	})
}

func TestDynamicPolicyInitializesBulkheadManager(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	require.NotNil(t, p.bulkheadManager)
}
