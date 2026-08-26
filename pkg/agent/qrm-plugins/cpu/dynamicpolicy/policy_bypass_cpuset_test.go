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
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
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
		name     string
		enabled  bool
		qosLevel string
		want     bool
	}{
		{"switch off + shared", false, consts.PodAnnotationQoSLevelSharedCores, false},
		{"switch off + reclaimed", false, consts.PodAnnotationQoSLevelReclaimedCores, false},
		{"switch off + system", false, consts.PodAnnotationQoSLevelSystemCores, false},
		{"switch off + dedicated", false, consts.PodAnnotationQoSLevelDedicatedCores, false},
		{"switch on + shared", true, consts.PodAnnotationQoSLevelSharedCores, true},
		{"switch on + reclaimed", true, consts.PodAnnotationQoSLevelReclaimedCores, true},
		{"switch on + system", true, consts.PodAnnotationQoSLevelSystemCores, true},
		{"switch on + dedicated", true, consts.PodAnnotationQoSLevelDedicatedCores, false},
		{"switch on + unknown QoS", true, "unknown", false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPolicyForBypassTest(tc.enabled)
			assert.Equal(t, tc.want, p.shouldBypassCPUSetAdjustment(tc.qosLevel))
		})
	}

	t.Run("nil dynamicConfig", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{dynamicConfig: nil}
		assert.False(t, p.shouldBypassCPUSetAdjustment(consts.PodAnnotationQoSLevelSharedCores))
	})
}

func TestApplyCPUSetBypass(t *testing.T) {
	t.Parallel()

	// buildResp constructs a canonical response with cpuset string and
	// TopologyAssignments filled, so we can verify per-field preservation.
	buildResp := func() *pluginapi.ResourceAllocationResponse {
		return &pluginapi.ResourceAllocationResponse{
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					"cpu": {
						IsScalarResource:  true,
						AllocatedQuantity: 4,
						AllocationResult:  "0-3",
						ResourceHints: &pluginapi.ListOfTopologyHints{
							Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}},
						},
						Annotations: map[string]string{"foo": "bar"},
					},
				},
			},
		}
	}

	t.Run("nil response", func(t *testing.T) {
		t.Parallel()
		p := newPolicyForBypassTest(true)
		got := p.applyCPUSetBypass(nil, consts.PodAnnotationQoSLevelSharedCores)
		assert.Nil(t, got)
	})

	t.Run("nil AllocationResult", func(t *testing.T) {
		t.Parallel()
		p := newPolicyForBypassTest(true)
		resp := &pluginapi.ResourceAllocationResponse{AllocationResult: nil}
		got := p.applyCPUSetBypass(resp, consts.PodAnnotationQoSLevelSharedCores)
		assert.Same(t, resp, got)
		assert.Nil(t, got.AllocationResult)
	})

	t.Run("bypass on + shared clears cpuset only", func(t *testing.T) {
		t.Parallel()
		p := newPolicyForBypassTest(true)
		resp := buildResp()
		got := p.applyCPUSetBypass(resp, consts.PodAnnotationQoSLevelSharedCores)

		info := got.AllocationResult.ResourceAllocation["cpu"]
		assert.Equal(t, "", info.AllocationResult, "cpuset string should be cleared")
		assert.EqualValues(t, 4, info.AllocatedQuantity, "AllocatedQuantity preserved")
		assert.NotNil(t, info.ResourceHints, "ResourceHints preserved")
		assert.Equal(t, map[string]string{"foo": "bar"}, info.Annotations, "Annotations preserved")
	})

	t.Run("bypass on + shared preserves non-CPU allocation results", func(t *testing.T) {
		t.Parallel()
		p := newPolicyForBypassTest(true)
		resp := buildResp()
		resp.AllocationResult.ResourceAllocation["example.com/device"] = &pluginapi.ResourceAllocationInfo{
			AllocationResult: "device-0",
		}
		got := p.applyCPUSetBypass(resp, consts.PodAnnotationQoSLevelSharedCores)

		assert.Equal(t, "", got.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)].AllocationResult)
		assert.Equal(t, "device-0", got.AllocationResult.ResourceAllocation["example.com/device"].AllocationResult)
	})

	t.Run("bypass on + dedicated keeps cpuset", func(t *testing.T) {
		t.Parallel()
		p := newPolicyForBypassTest(true)
		resp := buildResp()
		got := p.applyCPUSetBypass(resp, consts.PodAnnotationQoSLevelDedicatedCores)
		assert.Equal(t, "0-3", got.AllocationResult.ResourceAllocation["cpu"].AllocationResult)
	})

	t.Run("bypass off keeps cpuset", func(t *testing.T) {
		t.Parallel()
		p := newPolicyForBypassTest(false)
		resp := buildResp()
		got := p.applyCPUSetBypass(resp, consts.PodAnnotationQoSLevelSharedCores)
		assert.Equal(t, "0-3", got.AllocationResult.ResourceAllocation["cpu"].AllocationResult)
	})

	t.Run("nil entry inside ResourceAllocation is skipped", func(t *testing.T) {
		t.Parallel()
		p := newPolicyForBypassTest(true)
		resp := &pluginapi.ResourceAllocationResponse{
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					"cpu":  nil,
					"cpu2": {AllocationResult: "10-11"},
				},
			},
		}
		assert.NotPanics(t, func() {
			p.applyCPUSetBypass(resp, consts.PodAnnotationQoSLevelSharedCores)
		})
		assert.Equal(t, "10-11", resp.AllocationResult.ResourceAllocation["cpu2"].AllocationResult)
	})
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
