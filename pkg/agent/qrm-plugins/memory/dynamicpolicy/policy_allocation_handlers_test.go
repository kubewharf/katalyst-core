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
	"io/ioutil"
	"os"
	"testing"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSharedCoresAllocationHandler(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.Nil(err)
	machineInfo := &info.MachineInfo{
		Topology: []info.Node{
			{
				Memory: 100 * 1024 * 1024 * 1024, // 100 GB
			},
		},
	}

	tests := []struct {
		name              string
		req               *pluginapi.ResourceRequest
		persistCheckpoint bool
		expectErr         bool
		checkFunc         func(*pluginapi.ResourceAllocationResponse)
	}{
		{
			name:      "nil request",
			req:       nil,
			expectErr: true,
		},
		{
			name: "shared cores with numa binding",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-1",
				PodNamespace:  "default",
				PodName:       "pod-1",
				ContainerName: "container-1",
				ContainerType: pluginapi.ContainerType_MAIN,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024 * 1024, // 1GB
				},
				Hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
			},
			persistCheckpoint: true,
			expectErr:         false,
			checkFunc: func(resp *pluginapi.ResourceAllocationResponse) {
				as.NotNil(resp)
				as.NotNil(resp.AllocationResult)
				// Verification logic for NUMA binding
			},
		},
		{
			name: "shared cores without numa binding",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-2",
				PodNamespace:  "default",
				PodName:       "pod-2",
				ContainerName: "container-1",
				ContainerType: pluginapi.ContainerType_MAIN,
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024 * 1024, // 1GB
				},
			},
			persistCheckpoint: true,
			expectErr:         false,
			checkFunc: func(resp *pluginapi.ResourceAllocationResponse) {
				as.NotNil(resp)
				as.NotNil(resp.AllocationResult)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestSharedCoresAllocationHandler")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)
			as.NotNil(policy)

			resp, err := policy.sharedCoresAllocationHandler(context.Background(), tt.req, tt.persistCheckpoint)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(resp)
				}
			}
		})
	}
}

func TestSystemCoresAllocationHandler(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.Nil(err)
	machineInfo := &info.MachineInfo{
		Topology: []info.Node{
			{
				Memory: 100 * 1024 * 1024 * 1024,
			},
		},
	}

	tests := []struct {
		name              string
		req               *pluginapi.ResourceRequest
		persistCheckpoint bool
		expectErr         bool
	}{
		{
			name:      "nil request",
			req:       nil,
			expectErr: true,
		},
		{
			name: "system cores with numa binding",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-sys-1",
				PodNamespace:  "kube-system",
				PodName:       "pod-sys-1",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSystemCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024,
				},
			},
			expectErr: false,
		},
		{
			name: "system cores without numa binding",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-sys-2",
				PodNamespace:  "kube-system",
				PodName:       "pod-sys-2",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSystemCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024,
				},
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestSystemCoresAllocationHandler")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)
			resp, err := policy.systemCoresAllocationHandler(context.Background(), tt.req, tt.persistCheckpoint)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}
		})
	}
}

func TestReclaimedCoresAllocationHandler(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.Nil(err)
	machineInfo := &info.MachineInfo{
		Topology: []info.Node{
			{
				Memory: 100 * 1024 * 1024 * 1024,
			},
		},
	}

	tests := []struct {
		name              string
		req               *pluginapi.ResourceRequest
		persistCheckpoint bool
		expectErr         bool
		errMsg            string
	}{
		{
			name:      "nil request",
			req:       nil,
			expectErr: true,
		},
		{
			name: "inplace update resizing (not supported)",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-reclaimed-1",
				PodNamespace:  "default",
				PodName:       "pod-reclaimed-1",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey:              apiconsts.PodAnnotationQoSLevelReclaimedCores,
					apiconsts.PodAnnotationInplaceUpdateResizingKey: "true",
				},
			},
			expectErr: true,
		},
		{
			name: "normal reclaimed cores allocation",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-reclaimed-2",
				PodNamespace:  "default",
				PodName:       "pod-reclaimed-2",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024,
				},
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestReclaimedCoresAllocationHandler")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)

			resp, err := policy.reclaimedCoresAllocationHandler(context.Background(), tt.req, tt.persistCheckpoint)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}
		})
	}
}

func TestDedicatedCoresAllocationHandler(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.Nil(err)
	machineInfo := &info.MachineInfo{
		Topology: []info.Node{
			{
				Memory: 100 * 1024 * 1024 * 1024,
			},
		},
	}

	tests := []struct {
		name              string
		req               *pluginapi.ResourceRequest
		persistCheckpoint bool
		expectErr         bool
	}{
		{
			name:      "nil request",
			req:       nil,
			expectErr: true,
		},
		{
			name: "inplace update resizing (not supported)",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-dedicated-1",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey:              apiconsts.PodAnnotationQoSLevelDedicatedCores,
					apiconsts.PodAnnotationInplaceUpdateResizingKey: "true",
				},
			},
			expectErr: true,
		},
		{
			name: "dedicated cores with numa binding",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-dedicated-2",
				PodNamespace:  "default",
				PodName:       "pod-dedicated-2",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024 * 1024,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
			},
			expectErr: false,
		},
		{
			name: "dedicated cores without numa binding (not supported yet)",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-dedicated-3",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectErr: true, // currently returns error "not support dedicated_cores without NUMA binding"
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDedicatedCoresAllocationHandler")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)
			resp, err := policy.dedicatedCoresAllocationHandler(context.Background(), tt.req, tt.persistCheckpoint)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}
		})
	}
}

func TestNumaBindingAllocationHandler(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNumaBindingAllocationHandler")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.Nil(err)
	machineInfo := &info.MachineInfo{
		Topology: []info.Node{
			{
				Memory: 100 * 1024 * 1024 * 1024,
			},
		},
	}

	policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	// Pre-populate state for some tests
	// We need to inject state manually or via allocations
	// For "already allocated" case:
	reqAlreadyAllocated := &pluginapi.ResourceRequest{
		PodUid:        "pod-existing",
		PodNamespace:  "default",
		PodName:       "pod-existing",
		ContainerName: "container-1",
		ContainerType: pluginapi.ContainerType_MAIN,
		Annotations: map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 1024 * 1024,
		},
		Hint: &pluginapi.TopologyHint{
			Nodes: []uint64{0},
		},
	}
	// Initial allocation
	_, err = policy.numaBindingAllocationHandler(context.Background(), reqAlreadyAllocated, apiconsts.PodAnnotationQoSLevelSharedCores, true)
	as.NoError(err)

	tests := []struct {
		name      string
		req       *pluginapi.ResourceRequest
		qosLevel  string
		expectErr bool
		checkFunc func(*pluginapi.ResourceAllocationResponse)
	}{
		{
			name: "sidecar container",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-1",
				ContainerName: "sidecar-1",
				ContainerType: pluginapi.ContainerType_SIDECAR,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			qosLevel:  apiconsts.PodAnnotationQoSLevelSharedCores,
			expectErr: false, // returns empty response if main container not found
		},
		{
			name: "new main container allocation",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-new",
				PodNamespace:  "default",
				PodName:       "pod-new",
				ContainerName: "container-1",
				ContainerType: pluginapi.ContainerType_MAIN,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
			},
			qosLevel:  apiconsts.PodAnnotationQoSLevelSharedCores,
			expectErr: false,
		},
		{
			name:      "already allocated and meet requirement",
			req:       reqAlreadyAllocated,
			qosLevel:  apiconsts.PodAnnotationQoSLevelSharedCores,
			expectErr: false,
		},
		{
			name: "inplace update resize: non-binding to binding (error)",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-resize-error",
				PodNamespace:  "default",
				PodName:       "pod-resize-error",
				ContainerName: "container-1",
				ContainerType: pluginapi.ContainerType_MAIN,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationInplaceUpdateResizingKey:     "true",
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 2048 * 1024,
				},
			},
			qosLevel:  apiconsts.PodAnnotationQoSLevelSharedCores,
			expectErr: true, // Should fail because no origin allocation info (simulated by not allocating first)
		},
	}

	// For the "inplace update resize: non-binding to binding" test, we need to first allocate it WITHOUT binding, then try to update WITH binding.
	// But `policy_allocation_handlers.go` L154 checks `!allocationInfo.CheckNUMABinding()`.
	// So we need:
	// 1. Allocate a pod WITHOUT binding.
	// 2. Try to allocate SAME pod WITH binding AND inplace update flag.

	// Setup for "non-binding to binding" failure case
	reqNonBinding := &pluginapi.ResourceRequest{
		PodUid:        "pod-non-binding",
		PodNamespace:  "default",
		PodName:       "pod-non-binding",
		ContainerName: "container-1",
		ContainerType: pluginapi.ContainerType_MAIN,
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 1024 * 1024,
		},
	}
	// Allocate without binding
	_, err = policy.allocateNUMAsWithoutNUMABindingPods(context.Background(), reqNonBinding, apiconsts.PodAnnotationQoSLevelSharedCores, true)
	as.NoError(err)

	// Now try to update it with binding
	reqUpdateToBinding := &pluginapi.ResourceRequest{
		PodUid:        "pod-non-binding",
		PodNamespace:  "default",
		PodName:       "pod-non-binding",
		ContainerName: "container-1",
		ContainerType: pluginapi.ContainerType_MAIN,
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationInplaceUpdateResizingKey:     "true",
		},
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2048 * 1024,
		},
		Hint: &pluginapi.TopologyHint{
			Nodes: []uint64{0},
		},
	}

	tests = append(tests, struct {
		name      string
		req       *pluginapi.ResourceRequest
		qosLevel  string
		expectErr bool
		checkFunc func(*pluginapi.ResourceAllocationResponse)
	}{
		name:      "cannot change from non-numa_binding to numa_binding during inplace update",
		req:       reqUpdateToBinding,
		qosLevel:  apiconsts.PodAnnotationQoSLevelSharedCores,
		expectErr: true,
	})

	for _, tt := range tests {
		resp, err := policy.numaBindingAllocationHandler(context.Background(), tt.req, tt.qosLevel, true)
		if tt.expectErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.NotNil(t, resp)
			if tt.checkFunc != nil {
				tt.checkFunc(resp)
			}
		}
	}
}

func TestAllocateNUMAsWithoutNUMABindingPods(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.Nil(err)
	machineInfo := &info.MachineInfo{
		Topology: []info.Node{
			{
				Memory: 100 * 1024 * 1024 * 1024,
			},
		},
	}

	tests := []struct {
		name      string
		req       *pluginapi.ResourceRequest
		qosLevel  string
		expectErr bool
	}{
		{
			name:      "invalid qos level",
			req:       &pluginapi.ResourceRequest{},
			qosLevel:  "invalid",
			expectErr: true,
		},
		{
			name: "valid allocation",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-valid",
				PodNamespace:  "default",
				PodName:       "pod-valid",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024,
				},
			},
			qosLevel:  apiconsts.PodAnnotationQoSLevelSharedCores,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateNUMAsWithoutNUMABindingPods")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)
			resp, err := policy.allocateNUMAsWithoutNUMABindingPods(context.Background(), tt.req, tt.qosLevel, true)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}
		})
	}
}

func TestAllocateTargetNUMAs(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.Nil(err)
	machineInfo := &info.MachineInfo{
		Topology: []info.Node{
			{
				Memory: 100 * 1024 * 1024 * 1024,
			},
		},
	}

	tests := []struct {
		name        string
		req         *pluginapi.ResourceRequest
		qosLevel    string
		targetNUMAs machine.CPUSet
		expectErr   bool
	}{
		{
			name:      "invalid qos level",
			req:       &pluginapi.ResourceRequest{},
			qosLevel:  "invalid",
			expectErr: true,
		},
		{
			name: "valid target numa allocation",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-target",
				PodNamespace:  "default",
				PodName:       "pod-target",
				ContainerName: "container-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSystemCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1024 * 1024,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
			},
			qosLevel:    apiconsts.PodAnnotationQoSLevelSystemCores,
			targetNUMAs: machine.NewCPUSet(0),
			expectErr:   false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateTargetNUMAs")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)
			resp, err := policy.allocateTargetNUMAs(tt.req, tt.qosLevel, tt.targetNUMAs, true)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}
		})
	}
}

func TestCalculateMemoryAllocation(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.Nil(err)
	machineInfo := &info.MachineInfo{
		Topology: []info.Node{
			{
				Memory: 100 * 1024 * 1024 * 1024,
			},
		},
	}

	tests := []struct {
		name             string
		req              *pluginapi.ResourceRequest
		machineStateFunc func(state.NUMANodeMap)
		qosLevel         string
		aggregatedReq    int
		expectErr        bool
	}{
		{
			name:      "nil hint",
			req:       &pluginapi.ResourceRequest{Hint: nil},
			expectErr: true,
		},
		{
			name:      "empty hint nodes",
			req:       &pluginapi.ResourceRequest{Hint: &pluginapi.TopologyHint{Nodes: []uint64{}}},
			expectErr: true,
		},
		{
			name: "hint larger than 1 for non-exclusive binding",
			req: &pluginapi.ResourceRequest{
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				Hint: &pluginapi.TopologyHint{Nodes: []uint64{0, 1}},
			},
			expectErr: true,
		},
		{
			name: "valid calculation",
			req: &pluginapi.ResourceRequest{
				PodUid:        "pod-calc",
				ContainerName: "container-1",
				Hint:          &pluginapi.TopologyHint{Nodes: []uint64{0}},
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			qosLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			aggregatedReq: 1024 * 1024,
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestCalculateMemoryAllocation")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)

			// Prepare machine state
			machineState := policy.state.GetMachineState()[v1.ResourceMemory]
			if tt.machineStateFunc != nil {
				tt.machineStateFunc(machineState)
			}

			err = policy.calculateMemoryAllocation(tt.req, machineState, tt.qosLevel, tt.aggregatedReq)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
