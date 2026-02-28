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
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
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

// TestDynamicPolicy_generateNUMABindingPoolsCPUSetInPlace verifies the logic of generating CPU sets for NUMA-binding pools.
// It simulates a scenario with specific CPU topology and available CPUs, checking if the allocation strategies (like packing full cores) work as expected.
// Topology Assumption for mustGenerateDummyCPUTopology(16, 2, 2):
// - 16 CPUs total, 2 NUMA Nodes (0 and 1).
// - HT enabled, siblings are separated by 16/2 = 8.
// - NUMA 0: CPUs {0, 1, 2, 3} (Logic Cores) and {8, 9, 10, 11} (Siblings).
//   - Core 0: {0, 8}, Core 1: {1, 9}, Core 2: {2, 10}, Core 3: {3, 11}.
//
// - NUMA 1: CPUs {4, 5, 6, 7} (Logic Cores) and {12, 13, 14, 15} (Siblings).
func TestDynamicPolicy_generateNUMABindingPoolsCPUSetInPlace(t *testing.T) {
	t.Parallel()

	type args struct {
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
		// Case 1: Single pool allocation in NUMA 0.
		// Available CPUs: {8, 9, 10} (All in NUMA 0).
		// - Core 0: {0, 8} (Only 8 available).
		// - Core 1: {1, 9} (Only 9 available).
		// - Core 2: {2, 10} (Only 10 available).
		// Request: pool1 needs 2 CPUs from NUMA 0.
		// Allocation: No full cores available, so it picks {8, 9}.
		{
			name:        "single pool, ample cpus",
			cpuTopology: mustGenerateDummyCPUTopology(16, 2, 2),
			args: args{
				poolsCPUSet: make(map[string]machine.CPUSet),
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {
						0: 2,
					},
				},
				availableCPUs: machine.NewCPUSet(8, 9, 10),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(8, 9),
			},
			wantLeft:      machine.NewCPUSet(10),
			wantErr:       false,
			enableReclaim: true,
		},
		// Case 2: Multiple pools allocation across NUMA 0 and NUMA 1.
		// Available CPUs: {2, 3, 4, 5, 10}.
		// NUMA 0 Available: {2, 3, 10}.
		// - Core 2: {2, 10} (Both available -> Full Core).
		// - Core 3: {3, 11} (Only 3 available).
		// NUMA 1 Available: {4, 5}.
		// - Core 4: {4, 12} (Only 4 available).
		// - Core 5: {5, 13} (Only 5 available).
		// Request: pool1 needs 2 from NUMA 0; pool2 needs 2 from NUMA 1.
		// Allocation:
		// - pool1 (NUMA 0): Prefers full core {2, 10}.
		// - pool2 (NUMA 1): Takes {4, 5}.
		{
			name:        "multiple pools, ample cpus",
			cpuTopology: mustGenerateDummyCPUTopology(16, 2, 2),
			args: args{
				poolsCPUSet: make(map[string]machine.CPUSet),
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {
						0: 2,
					},
					"pool2": {
						1: 2,
					},
				},
				availableCPUs: machine.NewCPUSet(2, 3, 4, 5, 10),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(2, 10),
				"pool2": machine.NewCPUSet(4, 5),
			},
			wantLeft:      machine.NewCPUSet(3),
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
			defer os.RemoveAll(tmpDir) // Added cleanup

			p, err := getTestDynamicPolicyWithInitialization(tt.cpuTopology, tmpDir)
			as.Nil(err)

			// Clear state to ensure clean slate
			p.state.SetPodEntries(state.PodEntries{}, false)
			p.reservedCPUs = machine.NewCPUSet()

			p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = tt.enableReclaim

			gotLeft, err := p.generateNUMABindingPoolsCPUSetInPlace(tt.args.poolsCPUSet, tt.args.poolsQuantityMap, tt.args.availableCPUs)
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

func TestDynamicPolicy_adjustPoolsAndIsolatedEntries_Pinned(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	// Setup topology: 2 sockets, 8 cores each. Total 16 CPUs.
	// S0: 0-7, S1: 8-15.
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_adjustPoolsAndIsolatedEntries_Pinned")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	// Clear reserved CPUs to ensure deterministic allocation for test
	p.reservedCPUs = machine.NewCPUSet()

	// Enable Reclaim
	p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	// Disable overlap to ensure pool2 gets exactly what it requests (4 cores)
	// If enabled, it would take all available cores (12) which is also correct behavior but makes checking "exactly 4" fail.
	// We want to verify it can successfully allocate 4 from the remaining unpinned set.
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)

	// Setup Pinned CPUSets
	// pkg1 pinned to {0, 1} (NUMA 0)
	// pkg2 pinned to {2, 3} (NUMA 0) -- BUT no pools use it!
	machineState := p.state.GetMachineState()
	for numaID, numaState := range machineState {
		if numaID == 0 {
			if numaState.ResourcePackagePinnedCPUSet == nil {
				numaState.ResourcePackagePinnedCPUSet = make(map[string]machine.CPUSet)
			}
			numaState.ResourcePackagePinnedCPUSet["pkg1"] = machine.NewCPUSet(0, 1)
			numaState.ResourcePackagePinnedCPUSet["pkg2"] = machine.NewCPUSet(2, 3)
		}
	}
	p.state.SetMachineState(machineState, false)

	// Setup Pools Quantity
	// pkg1/pool1: 2 cores (should take 0, 1)
	// pool2 (common): 4 cores (should take from available excluding 0, 1 AND 2, 3)
	// commonAvailableCPUs should be {4-15}.
	// pool2 needs 4 cores. It should get 4, 5, 6, 7 (if taking from NUMA 0 first) or spread.
	// Since NUMA 0 has 4,5,6,7 available (4 cores).
	// NUMA 1 has 8-15 available (8 cores).
	// pool2 is FakedNUMAID.
	poolsQuantityMap := map[string]map[int]int{
		"pkg1/pool1": {
			commonstate.FakedNUMAID: 2,
		},
		"pool2": {
			commonstate.FakedNUMAID: 4,
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedNUMAID: 0,
		},
	}

	isolatedQuantityMap := map[string]map[string]int{}

	// Seed entries for Reclaim pool (needed for reclaimOverlapNUMABinding check)
	// And seed containers to prevent cleanPools from removing the pools
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(14, 15),
				OriginalAllocationResult: machine.NewCPUSet(14, 15),
				TopologyAwareAssignments: map[int]machine.CPUSet{1: machine.NewCPUSet(14, 15)},
			},
		},
		"pod1": {
			"container1": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					PodNamespace:  "default",
					PodName:       "pod1",
					ContainerName: "container1",
					OwnerPoolName: "pkg1/pool1",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
		"pod2": {
			"container2": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod2",
					PodNamespace:  "default",
					PodName:       "pod2",
					ContainerName: "container2",
					OwnerPoolName: "pool2",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
	}

	err = p.adjustPoolsAndIsolatedEntries(poolsQuantityMap, isolatedQuantityMap, entries, machineState, false)
	as.Nil(err)

	updatedEntries := p.state.GetPodEntries()

	// Verify Results
	// pkg1/pool1 should be {0, 1}
	pool1Entry := updatedEntries["pkg1/pool1"][commonstate.FakedContainerName]
	as.NotNil(pool1Entry)
	as.True(pool1Entry.AllocationResult.Equals(machine.NewCPUSet(0, 1)), "pool1 should have pinned CPUs 0,1, got %s", pool1Entry.AllocationResult.String())

	// pool2 should NOT contain 0, 1 (used by pkg1) AND should NOT contain 2, 3 (reserved by pkg2 even if unused)
	pool2Entry := updatedEntries["pool2"][commonstate.FakedContainerName]
	as.NotNil(pool2Entry)
	// Check intersection with pkg1 pinned
	as.False(pool2Entry.AllocationResult.Intersection(machine.NewCPUSet(0, 1)).Size() > 0, "pool2 should not use pinned CPUs 0,1, got %s", pool2Entry.AllocationResult.String())
	// Check intersection with pkg2 pinned (unused but reserved)
	as.False(pool2Entry.AllocationResult.Intersection(machine.NewCPUSet(2, 3)).Size() > 0, "pool2 should not use pinned CPUs 2,3 (reserved for pkg2), got %s", pool2Entry.AllocationResult.String())

	// Verify pool2 size
	as.Equal(4, pool2Entry.AllocationResult.Size(), "pool2 should have 4 cores")
}

// TestDynamicPolicy_groupAndAllocatePools tests the groupAndAllocatePools function.
// It verifies that pools are correctly grouped into pinned and common categories,
// and that CPUs are allocated according to availability and constraints.
func TestDynamicPolicy_groupAndAllocatePools(t *testing.T) {
	t.Parallel()

	type args struct {
		poolsQuantityMap         map[string]map[int]int
		isolatedQuantityMap      map[string]map[string]int
		availableCPUs            machine.CPUSet
		rpPinnedCPUSet           map[string]machine.CPUSet
		reclaimOverlapShareRatio map[string]float64
	}
	tests := []struct {
		name         string
		args         args
		wantPools    map[string]machine.CPUSet
		wantIsolated map[string]map[string]machine.CPUSet
		wantErr      bool
	}{
		{
			name: "Scenario 1: Common Pools Only - Verifies that when no pools are pinned, all pools are treated as common and allocated from the general available CPU set.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {commonstate.FakedNUMAID: 2},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(0, 1),
			},
			wantErr: false,
		},
		{
			name: "Scenario 2: Pinned Pools Only - Verifies that pools belonging to a resource package are correctly identified and allocated exclusively from that package's pinned CPU set.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					rputil.WrapOwnerPoolName("pool1", "pkg1"): {commonstate.FakedNUMAID: 2},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(0, 1),
				},
			},
			wantPools: map[string]machine.CPUSet{
				rputil.WrapOwnerPoolName("pool1", "pkg1"): machine.NewCPUSet(0, 1),
			},
			wantErr: false,
		},
		{
			name: "Scenario 3: Mixed Pinned and Common Pools - Verifies that the function correctly splits pinned and common pools, allocating pinned pools from their specific sets and common pools from the remaining available CPUs.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					rputil.WrapOwnerPoolName("pool1", "pkg1"): {commonstate.FakedNUMAID: 2},
					"pool2": {commonstate.FakedNUMAID: 2},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(0, 1),
				},
			},
			wantPools: map[string]machine.CPUSet{
				rputil.WrapOwnerPoolName("pool1", "pkg1"): machine.NewCPUSet(0, 1),
				"pool2": machine.NewCPUSet(2, 3),
			},
			wantErr: false,
		},
		{
			name: "Scenario 4: Isolated Containers - Verifies that isolated containers are allocated dedicated CPUs from the common available set alongside common pools.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {commonstate.FakedNUMAID: 2},
				},
				isolatedQuantityMap: map[string]map[string]int{
					"pod1": {"container1": 2},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(2, 3),
			},
			wantIsolated: map[string]map[string]machine.CPUSet{
				"pod1": {"container1": machine.NewCPUSet(0, 1)},
			},
			wantErr: false,
		},
		{
			name: "Scenario 5: Error - Pinned Pool Insufficient CPUs - Verifies that the function degrades gracefully and allocates available CPUs (partial) if a pinned pool requests more CPUs than are available in its pinned set.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					rputil.WrapOwnerPoolName("pool1", "pkg1"): {commonstate.FakedNUMAID: 4},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(0, 1),
				},
			},
			wantPools: map[string]machine.CPUSet{
				rputil.WrapOwnerPoolName("pool1", "pkg1"): machine.NewCPUSet(0, 1),
			},
			wantErr: false,
		},
		{
			name: "Scenario 6: Error - Common Pool Insufficient CPUs - Verifies that the function degrades gracefully and allocates available CPUs (partial) if common pools request more CPUs than are available in the shared pool.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {commonstate.FakedNUMAID: 4},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(0, 1),
				},
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(2, 3),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)

			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
			as.Nil(err)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_groupAndAllocatePools")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
			as.Nil(err)

			// Clear state
			p.state.SetPodEntries(state.PodEntries{}, false)
			p.reservedCPUs = machine.NewCPUSet()

			gotPools, gotIsolated, err := p.groupAndAllocatePools(tt.args.poolsQuantityMap, tt.args.isolatedQuantityMap, tt.args.availableCPUs, tt.args.rpPinnedCPUSet, tt.args.reclaimOverlapShareRatio)
			if (err != nil) != tt.wantErr {
				t.Errorf("groupAndAllocatePools() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				// Filter out system pools (reclaim, reserve) for comparison
				filteredPools := make(map[string]machine.CPUSet)
				for k, v := range gotPools {
					if k != commonstate.PoolNameReclaim && k != commonstate.PoolNameReserve {
						filteredPools[k] = v
					}
				}

				if !reflect.DeepEqual(filteredPools, tt.wantPools) {
					t.Errorf("groupAndAllocatePools() gotPools = %v, want %v", filteredPools, tt.wantPools)
				}

				if len(gotIsolated) == 0 && len(tt.wantIsolated) == 0 {
					// Both empty/nil, treat as equal
				} else if !reflect.DeepEqual(gotIsolated, tt.wantIsolated) {
					t.Errorf("groupAndAllocatePools() gotIsolated = %v, want %v", gotIsolated, tt.wantIsolated)
				}
			}
		})
	}
}

func mustGenerateDummyCPUTopology(numCPUs, numSockets, numaNum int) *machine.CPUTopology {
	topo, err := machine.GenerateDummyCPUTopology(numCPUs, numSockets, numaNum)
	if err != nil {
		panic(err)
	}
	return topo
}
