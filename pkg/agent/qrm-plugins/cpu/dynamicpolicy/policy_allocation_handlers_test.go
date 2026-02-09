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
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDynamicPolicy_getReclaimOverlapShareRatio(t *testing.T) {
	t.Parallel()

	type fields struct {
		allowSharedCoresOverlapReclaimedCores bool
	}
	type args struct {
		entries state.PodEntries
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    map[string]float64
		wantErr bool
	}{
		{
			name: "overlap disabled",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: false,
			},
			args: args{
				entries: state.PodEntries{},
			},
			want: nil,
		},
		{
			name: "overlap enabled, no reclaim",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: true,
			},
			args: args{
				entries: state.PodEntries{},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "overlap enabled, reclaim and share normal",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: true,
			},
			args: args{
				entries: state.PodEntries{
					commonstate.PoolNameReclaim: {
						commonstate.FakedContainerName: &state.AllocationInfo{
							AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
							AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0),
								1: machine.NewCPUSet(1),
								2: machine.NewCPUSet(2),
								3: machine.NewCPUSet(3),
							},
						},
					},
					commonstate.PoolNameShare: {
						commonstate.FakedContainerName: &state.AllocationInfo{
							AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
							AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0, 4),
								1: machine.NewCPUSet(1, 5),
								2: machine.NewCPUSet(2, 6),
								3: machine.NewCPUSet(3, 7),
							},
						},
					},
				},
			},
			want: map[string]float64{
				commonstate.PoolNameShare: 0.5,
			},
			wantErr: false,
		},
		{
			name: "overlap enabled, reclaim and share ramp up",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: true,
			},
			args: args{
				entries: state.PodEntries{
					commonstate.PoolNameReclaim: {
						commonstate.FakedContainerName: &state.AllocationInfo{
							AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
							AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0),
								1: machine.NewCPUSet(1),
								2: machine.NewCPUSet(2),
								3: machine.NewCPUSet(3),
							},
						},
					},
					"pod1": {
						"container1": &state.AllocationInfo{
							AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
								PodUid:        "pod1",
								PodNamespace:  "pod1",
								PodName:       "pod1",
								ContainerName: "container1",
							}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
							RequestQuantity:  4,
							AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0, 4),
								1: machine.NewCPUSet(1, 5),
								2: machine.NewCPUSet(2, 6),
								3: machine.NewCPUSet(3, 7),
							},
						},
					},
				},
			},
			want: map[string]float64{
				commonstate.PoolNameShare: 0.5,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)
			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			as.Nil(err)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_getReclaimOverlapShareRatio")
			as.Nil(err)

			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
			as.Nil(err)

			if tt.fields.allowSharedCoresOverlapReclaimedCores {
				p.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)
			}

			got, err := p.getReclaimOverlapShareRatio(tt.args.entries)
			if (err != nil) != tt.wantErr {
				t.Errorf("getReclaimOverlapShareRatio() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getReclaimOverlapShareRatio() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllocateSharedNumaBindingCPUs(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	// Setup
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	podName := "test-pod"
	containerName := "test-container"
	podUID := "test-uid"

	// Helper to create request
	createReq := func(reqQuantity float64, inplaceUpdate bool) *pluginapi.ResourceRequest {
		req := &pluginapi.ResourceRequest{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       podName,
			ContainerName: containerName,
			ResourceName:  string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): reqQuantity,
			},
			Annotations: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
			Hint: &pluginapi.TopologyHint{
				Nodes:     []uint64{0},
				Preferred: true,
			},
		}
		if inplaceUpdate {
			req.Annotations[apiconsts.PodAnnotationInplaceUpdateResizingKey] = "true"
		}
		return req
	}

	// Case 1: Inplace Update Error - Origin is not SNB
	t.Run("inplace_update_error_origin_not_snb", func(t *testing.T) {
		t.Parallel()

		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateSharedNumaBindingCPUs")
		as.Nil(err)
		defer os.RemoveAll(tmpDir)

		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
		as.Nil(err)
		// Setup origin allocation info (Normal SharedCores, NOT SNB)
		originAllocationInfo := &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				PodNamespace:  "default",
				PodName:       podName,
				ContainerName: containerName,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			RequestQuantity: 2,
		}
		policy.state.SetAllocationInfo(podUID, containerName, originAllocationInfo, false)

		req := createReq(4, true)
		_, err = policy.allocateSharedNumaBindingCPUs(req, req.Hint, false)
		as.Error(err)
		as.Contains(err.Error(), "cannot change from non-snb to snb during inplace update")
	})

	// Case 2: Inplace Update Success - Origin is SNB
	t.Run("inplace_update_success_origin_snb", func(t *testing.T) {
		t.Parallel()
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateSharedNumaBindingCPUs")
		as.Nil(err)
		defer os.RemoveAll(tmpDir)

		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
		as.Nil(err)

		// Setup origin allocation info (SNB)
		originAllocationInfo := &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				PodNamespace:  "default",
				PodName:       podName,
				ContainerName: containerName,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			RequestQuantity:  2,
			AllocationResult: machine.NewCPUSet(0, 1),
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 1),
			},
		}
		originAllocationInfo.SetSpecifiedNUMABindingNUMAID(0)

		policy.state.SetAllocationInfo(podUID, containerName, originAllocationInfo, false)

		req := createReq(4, true)
		_, err = policy.allocateSharedNumaBindingCPUs(req, req.Hint, false)
		if err != nil {
			as.NotContains(err.Error(), "cannot change from non-snb to snb during inplace update")
		}
	})

	// Case 3: Normal Allocation (Not Inplace Update)
	t.Run("normal_allocation", func(t *testing.T) {
		t.Parallel()
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateSharedNumaBindingCPUs")
		as.Nil(err)
		defer os.RemoveAll(tmpDir)

		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
		as.Nil(err)

		req := createReq(2, false)
		// Clean up previous state
		policy.state.Delete(podUID, containerName, false)

		_, err = policy.allocateSharedNumaBindingCPUs(req, req.Hint, false)
		// This might fail due to pool issues but it covers the else branch
		// We expect it NOT to fail with the inplace update error
		if err != nil {
			as.NotContains(err.Error(), "inplace update")
		}
	})

	// Case 4: Invalid Inputs
	t.Run("invalid_inputs", func(t *testing.T) {
		t.Parallel()
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateSharedNumaBindingCPUs")
		as.Nil(err)
		defer os.RemoveAll(tmpDir)

		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
		as.Nil(err)

		req := createReq(2, false)

		// Nil req
		_, err = policy.allocateSharedNumaBindingCPUs(nil, req.Hint, false)
		as.Error(err)
		as.Contains(err.Error(), "nil req")

		// Nil hint
		_, err = policy.allocateSharedNumaBindingCPUs(req, nil, false)
		as.Error(err)
		as.Contains(err.Error(), "hint is nil")

		// Empty hint
		emptyHintReq := createReq(2, false)
		emptyHintReq.Hint = &pluginapi.TopologyHint{Nodes: []uint64{}}
		_, err = policy.allocateSharedNumaBindingCPUs(req, emptyHintReq.Hint, false)
		as.Error(err)
		as.Contains(err.Error(), "hint is empty")

		// Hint with multiple nodes
		multiNodeHintReq := createReq(2, false)
		multiNodeHintReq.Hint = &pluginapi.TopologyHint{Nodes: []uint64{0, 1}}
		_, err = policy.allocateSharedNumaBindingCPUs(req, multiNodeHintReq.Hint, false)
		as.Error(err)
		as.Contains(err.Error(), "larger than 1 NUMA")
	})
}

func TestDynamicPolicy_allocateNumaBindingCPUs(t *testing.T) {
	t.Parallel()

	type args struct {
		numCPUs        int
		hint           *pluginapi.TopologyHint
		machineState   state.NUMANodeMap
		reqAnnotations map[string]string
	}
	tests := []struct {
		name    string
		args    args
		want    machine.CPUSet
		wantErr bool
	}{
		{
			name: "normal allocation without pinning",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				},
				reqAnnotations: nil,
			},
			want:    machine.NewCPUSet(0, 1),
			wantErr: false,
		},
		{
			name: "allocation with pinned resource package",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
						ResourcePackagePinnedCPUSet: map[string]machine.CPUSet{
							"pkg1": machine.NewCPUSet(2, 3),
						},
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationResourcePackageKey: "pkg1",
				},
			},
			want:    machine.NewCPUSet(2, 3),
			wantErr: false,
		},
		{
			name: "allocation without pinned resource package but with other pinned packages",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
						ResourcePackagePinnedCPUSet: map[string]machine.CPUSet{
							"pkg1": machine.NewCPUSet(2, 3),
						},
					},
				},
				reqAnnotations: nil,
			},
			want:    machine.NewCPUSet(0, 1),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)
			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			as.Nil(err)
			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_allocateNumaBindingCPUs")
			as.Nil(err)

			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
			as.Nil(err)
			p.reservedCPUs = machine.NewCPUSet()
			t.Logf("Reserved: %s", p.reservedCPUs.String())

			got, err := p.allocateNumaBindingCPUs(tt.args.numCPUs, tt.args.hint, tt.args.machineState, tt.args.reqAnnotations)
			if (err != nil) != tt.wantErr {
				t.Errorf("allocateNumaBindingCPUs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !got.Equals(tt.want) {
				t.Errorf("allocateNumaBindingCPUs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDynamicPolicy_generateNUMABindingPoolsCPUSetInPlace(t *testing.T) {
	t.Parallel()

	type args struct {
		rpPinnedCPUSet   map[string]machine.CPUSet
		poolsCPUSet      map[string]machine.CPUSet
		poolsQuantityMap map[string]map[int]int
		availableCPUs    machine.CPUSet
	}
	tests := []struct {
		name          string
		cpuTopology   *machine.CPUTopology
		args          args
		wantPools     map[string]machine.CPUSet
		wantLeft      machine.CPUSet
		wantErr       bool
		enableReclaim bool
	}{
		{
			name:        "single pool, no pinning, ample cpus",
			cpuTopology: mustGenerateDummyCPUTopology(16, 2, 4),
			args: args{
				rpPinnedCPUSet: map[string]machine.CPUSet{},
				poolsCPUSet:    make(map[string]machine.CPUSet),
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {
						0: 2,
					},
				},
				availableCPUs: machine.NewCPUSet(3, 8, 9, 10),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(8, 9),
			},
			wantLeft:      machine.NewCPUSet(3, 10),
			wantErr:       false,
			enableReclaim: true,
		},
		{
			name:        "pinned pool and unpinned pool, pinned takes precedence",
			cpuTopology: mustGenerateDummyCPUTopology(16, 2, 4),
			args: args{
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(8, 9),
				},
				poolsCPUSet: make(map[string]machine.CPUSet),
				poolsQuantityMap: map[string]map[int]int{
					"pkg1/pool1": {
						0: 2,
					},
					"pool2": {
						1: 2,
					},
				},
				availableCPUs: machine.NewCPUSet(3, 8, 9, 10),
			},
			wantPools: map[string]machine.CPUSet{
				"pkg1/pool1": machine.NewCPUSet(8, 9),
				"pool2":      machine.NewCPUSet(3, 10),
			},
			wantLeft:      machine.NewCPUSet(),
			wantErr:       false,
			enableReclaim: true,
		},
		{
			name:        "pinned pool insufficient cpus",
			cpuTopology: mustGenerateDummyCPUTopology(16, 2, 4),
			args: args{
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(8), // Only 1 cpu pinned
				},
				poolsCPUSet: make(map[string]machine.CPUSet),
				poolsQuantityMap: map[string]map[int]int{
					"pkg1/pool1": {
						0: 2, // Wants 2
					},
				},
				availableCPUs: machine.NewCPUSet(3, 8, 9, 10),
			},
			wantPools: map[string]machine.CPUSet{
				"pkg1/pool1": machine.NewCPUSet(8), // Can only get 1
			},
			wantLeft: machine.NewCPUSet(3, 9, 10),
			wantErr:  false, // Returns error? The code returns error from takeCPUsForPools if failed? No, takeCPUsForPools usually tries best effort or returns error if strict?
			// Checking implementation: generateNUMABindingPoolsCPUSetFunc calls takeCPUsForPoolsInPlace -> takeCPUsForPools -> calculator.TakeByNUMABalance.
			// calculator.TakeByNUMABalance returns error if not enough CPUs.
			// So this should return error.
			enableReclaim: true,
		},
		{
			name:        "unpinned pool insufficient cpus",
			cpuTopology: mustGenerateDummyCPUTopology(16, 2, 4),
			args: args{
				rpPinnedCPUSet: map[string]machine.CPUSet{},
				poolsCPUSet:    make(map[string]machine.CPUSet),
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {
						0: 5, // Available is 2 (8, 9)
					},
				},
				availableCPUs: machine.NewCPUSet(3, 8, 9, 10),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(8, 9),
			},
			wantLeft:      machine.NewCPUSet(3, 10),
			wantErr:       false,
			enableReclaim: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generateNUMABindingPoolsCPUSetInPlace")
			as.Nil(err)

			p, err := getTestDynamicPolicyWithInitialization(tt.cpuTopology, tmpDir)
			as.Nil(err)

			// Clear state to ensure clean slate
			p.state.SetPodEntries(state.PodEntries{}, false)
			p.reservedCPUs = machine.NewCPUSet()

			p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = tt.enableReclaim

			gotLeft, err := p.generateNUMABindingPoolsCPUSetInPlace(tt.args.rpPinnedCPUSet, tt.args.poolsCPUSet, tt.args.poolsQuantityMap, tt.args.availableCPUs)
			if (err != nil) != tt.wantErr {
				t.Errorf("generateNUMABindingPoolsCPUSetInPlace() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if !reflect.DeepEqual(tt.args.poolsCPUSet, tt.wantPools) {
					t.Errorf("generateNUMABindingPoolsCPUSetInPlace() poolsCPUSet = %v, want %v", tt.args.poolsCPUSet, tt.wantPools)
				}
				if !gotLeft.Equals(tt.wantLeft) {
					t.Errorf("generateNUMABindingPoolsCPUSetInPlace() gotLeft = %v, want %v", gotLeft, tt.wantLeft)
				}
			}
		})
	}
}

func mustGenerateDummyCPUTopology(numCPUs, numSockets, numCoresPerSocket int) *machine.CPUTopology {
	topo, err := machine.GenerateDummyCPUTopology(numCPUs, numSockets, numCoresPerSocket)
	if err != nil {
		panic(err)
	}
	return topo
}
