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
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestNewCanonicalHintOptimizer(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	conf.CPUNUMAHintPreferPolicy = cpuconsts.CPUNUMAHintPreferPolicySpreading
	conf.CPUNUMAHintPreferLowThreshold = 0.5

	cpuTopology, _ := machine.GenerateDummyCPUTopology(8, 1, 2) // 2 NUMA nodes
	tmpDir, err := os.MkdirTemp("", "checkpoint-TestNewMemoryBandwidthHintOptimizer")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	stateImpl, err := state.NewCheckpointState(tmpDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
	require.NoError(t, err)

	options := policy.HintOptimizerFactoryOptions{
		Conf:         conf,
		State:        stateImpl,
		ReservedCPUs: machine.NewCPUSet(0),
	}

	optimizer, err := NewCanonicalHintOptimizer(options)
	require.NoError(t, err)
	require.NotNil(t, optimizer)

	co, ok := optimizer.(*canonicalHintOptimizer)
	require.True(t, ok)
	assert.Equal(t, options.State, co.state)
	assert.Equal(t, options.ReservedCPUs, co.reservedCPUs)
	assert.Equal(t, conf.CPUNUMAHintPreferPolicy, co.cpuNUMAHintPreferPolicy)
	assert.Equal(t, conf.CPUNUMAHintPreferLowThreshold, co.cpuNUMAHintPreferLowThreshold)
}

func TestCanonicalHintOptimizer_OptimizeHints(t *testing.T) {
	t.Parallel()

	tmpDir, _ := ioutil.TempDir("", "test-canonical-hint-optimizer")
	defer os.RemoveAll(tmpDir)

	type fields struct {
		state                         state.State
		reservedCPUs                  machine.CPUSet
		cpuNUMAHintPreferPolicy       string
		cpuNUMAHintPreferLowThreshold float64
	}
	type args struct {
		request hintoptimizer.Request
		hints   *pluginapi.ListOfTopologyHints
	}
	tests := []struct {
		name          string
		fields        fields
		args          args
		wantErr       error
		expectedHints *pluginapi.ListOfTopologyHints
	}{
		{
			name: "NUMAExclusive pod skip",
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
						},
					},
					CPURequest: 2,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: false}}},
			},
			wantErr:       hintoptimizerutil.ErrHintOptimizerSkip,
			expectedHints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: false}}},
		},
		{
			name: "GetSingleNUMATopologyHintNUMANodes fails",
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					},
					CPURequest: 2,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0, 1}, Preferred: false}}}, // Multiple nodes in a hint
			},
			wantErr:       fmt.Errorf("hint 0 has invalid node count"),
			expectedHints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0, 1}, Preferred: false}}},
		},
		{
			name: "policy packing",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1, 2, 3, 4), PodEntries: make(state.PodEntries)},
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8), PodEntries: make(state.PodEntries)},
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "policy_packing", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:            machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyPacking,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					},
					CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true},
					{Nodes: []uint64{1}, Preferred: true}, // Both have 3 left, so both preferred
				},
			},
		},
		{
			name: "policy packing - one NUMA insufficient",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1), PodEntries: make(state.PodEntries)},          // 1 CPU
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8), PodEntries: make(state.PodEntries)}, // 4 CPUs
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "policy_packing_one_NUMA_insufficient", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:            machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyPacking,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					},
					CPURequest: 2,
				}, // Request 2 CPUs
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{1}, Preferred: true}, // NUMA 0 is skipped, NUMA 1 is preferred
				},
			},
		},
		{
			name: "policy spreading",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1, 2), PodEntries: make(state.PodEntries)},       // 2 CPUs, 1 left
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8), PodEntries: make(state.PodEntries)}, // 4 CPUs, 3 left
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "policy_spreading", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:            machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicySpreading,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					},
					CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: true}, // NUMA 1 has more left
				},
			},
		},
		{
			name: "policy dynamic packing - all above threshold",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1, 2, 3, 4), PodEntries: make(state.PodEntries)}, // 4 CPUs, available ratio 1.0
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8), PodEntries: make(state.PodEntries)}, // 4 CPUs, available ratio 1.0
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "policy_dynamic_packing_all_above_threshold", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:                  machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy:       cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
				cpuNUMAHintPreferLowThreshold: 0.5,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true},
					{Nodes: []uint64{1}, Preferred: true}, // Packing applied, both have 3 left
				},
			},
		},
		{
			name: "policy dynamic packing - one below threshold",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						// NUMA 0: 4 total, 1 used by a pod, 3 available. Available ratio = 3/4 = 0.75 (above threshold)
						0: &state.NUMANodeState{
							DefaultCPUSet: machine.NewCPUSet(1, 2, 3, 4),
							PodEntries: state.PodEntries{"pod1": {"cont1": {
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:        "pod1",
									PodNamespace:  "ns1",
									PodName:       "pod1",
									ContainerName: "cont1",
									ContainerType: pluginapi.ContainerType_MAIN.String(),
									QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
									Annotations: map[string]string{
										consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									},
								},
								AllocationResult: machine.NewCPUSet(1),
								RequestQuantity:  1,
							}}},
						},
						// NUMA 1: 4 total, 3 used by a pod, 1 available. Available ratio = 1/4 = 0.25 (below threshold)
						1: &state.NUMANodeState{
							DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8),
							PodEntries: state.PodEntries{"pod2": {"cont2": {
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:        "pod2",
									PodNamespace:  "ns2",
									PodName:       "pod2",
									ContainerName: "cont2",
									ContainerType: pluginapi.ContainerType_MAIN.String(),
									QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
									Annotations: map[string]string{
										consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									},
								},
								AllocationResult: machine.NewCPUSet(5, 6, 7),
								RequestQuantity:  3,
							}}},
						},
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "policy_dynamic_packing_one_below_threshold", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:                  machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy:       cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
				cpuNUMAHintPreferLowThreshold: 0.5,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true},  // Packing on NUMA 0 (2 left)
					{Nodes: []uint64{1}, Preferred: false}, // NUMA 1 is filtered out, not preferred
				},
			},
		},
		{
			name: "policy dynamic packing - all below threshold, fallback to spreading",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						// NUMA 0: 4 total, 2 used. Available = 2. Ratio = 2/4 = 0.5 (below threshold)
						0: &state.NUMANodeState{
							DefaultCPUSet: machine.NewCPUSet(1, 2, 3, 4),
							PodEntries: state.PodEntries{"pod1": {"cont1": {
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:        "pod1",
									PodNamespace:  "ns1",
									PodName:       "pod1",
									ContainerName: "cont1",
									ContainerType: pluginapi.ContainerType_MAIN.String(),
									QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
									Annotations: map[string]string{
										consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									},
								},
								AllocationResult: machine.NewCPUSet(1, 2),
								RequestQuantity:  2,
							}}},
						},
						// NUMA 0: 4 total, 2 used. Available = 2. Ratio = 2/4 = 0.5 (below threshold)
						1: &state.NUMANodeState{
							DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8),
							PodEntries: state.PodEntries{"pod2": {"cont2": {
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:        "pod2",
									PodNamespace:  "ns2",
									PodName:       "pod2",
									ContainerName: "cont2",
									ContainerType: pluginapi.ContainerType_MAIN.String(),
									QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
									Annotations: map[string]string{
										consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									},
								},
								AllocationResult: machine.NewCPUSet(5, 6),
								RequestQuantity:  2,
							}}},
						},
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "policy_dynamic_packing_all_below_threshold_fallback_to_spreading", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:                  machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy:       cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
				cpuNUMAHintPreferLowThreshold: 0.6, // Higher threshold to make both NUMAs fall below
			},
			args: args{
				// Request 0.5 CPU, NUMA0 has 1 left (0.5 after req), NUMA1 has 1 left (0.5 after req)
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					},
					CPURequest: 0.5,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					// Spreading applied to original numaNodes, both have 0.5 left, so both preferred
					{Nodes: []uint64{0}, Preferred: true},
					{Nodes: []uint64{1}, Preferred: true},
				},
			},
		},
		{
			name: "unknown policy, fallback to spreading",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1, 2), PodEntries: make(state.PodEntries)},       // 2 CPUs, 1 left
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8), PodEntries: make(state.PodEntries)}, // 4 CPUs, 3 left
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "unknown_policy_fallback_to_spreading", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:            machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy: "unknown_policy",
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: true}, // Spreading applied, NUMA 1 has more left
				},
			},
		},
		{
			name: "no available NUMA nodes after filtering",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1), PodEntries: make(state.PodEntries)}, // 1 CPU
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(5), PodEntries: make(state.PodEntries)}, // 1 CPU
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "no_available_NUMA_nodes_after_filtering", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:            machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyPacking,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 2,
				}, // Request 2 CPUs, both NUMAs insufficient
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{}, // No hints populated
			},
		},
		{
			name: "policy packing - multiple preferred due to same minLeft",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1, 2, 3), PodEntries: make(state.PodEntries)}, // 3 left
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(6, 7, 8), PodEntries: make(state.PodEntries)}, // 3 left
						2: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(10), PodEntries: make(state.PodEntries)},      // 1 left
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(12, 1, 3) // 3 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "policy_packing_multiple_preferred_due_to_same_minLeft", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:            machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyPacking,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}, {Nodes: []uint64{2}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false}, // NUMA 0 has 3 left
					{Nodes: []uint64{1}, Preferred: false}, // NUMA 1 has 3 left
					{Nodes: []uint64{2}, Preferred: true},  // NUMA 2 has 1 left (minLeft)
				},
			},
		},
		{
			name: "policy spreading - multiple preferred due to same maxLeft",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1, 2, 3), PodEntries: make(state.PodEntries)}, // 3 left
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(6, 7, 8), PodEntries: make(state.PodEntries)}, // 3 left
						2: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(10), PodEntries: make(state.PodEntries)},      // 1 left
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "policy_spreading_multiple_preferred_due_to_same_maxLeft", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:            machine.NewCPUSet(),
				cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicySpreading,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}, {Nodes: []uint64{2}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true},  // NUMA 0 has 3 left (maxLeft)
					{Nodes: []uint64{1}, Preferred: true},  // NUMA 1 has 3 left (maxLeft)
					{Nodes: []uint64{2}, Preferred: false}, // NUMA 2 has 1 left
				},
			},
		},
		{
			name: "dynamic packing - reserved CPUs affect availability",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						// NUMA 0: 4 cpus, 1 reserved. Allocatable = 3. Available = 3. Ratio = 1.0
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1, 2, 3, 4), PodEntries: make(state.PodEntries)},
						// NUMA 1: 4 cpus, 2 reserved. Allocatable = 2. Available = 2. Ratio = 1.0
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8), PodEntries: make(state.PodEntries)},
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "dynamic_packing_reserved_CPUs_affect_availability", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:                  machine.NewCPUSet(1, 5, 6), // reserve 1 from NUMA0, 5,6 from NUMA1
				cpuNUMAHintPreferPolicy:       cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
				cpuNUMAHintPreferLowThreshold: 0.5,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					// NUMA0: available (4-1)=3. After req: 2. Filtered in.
					// NUMA1: available (4-2)=2. After req: 1. Filtered in.
					// Packing: NUMA1 has less left (1 vs 2), so NUMA1 is preferred.
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: true},
				},
			},
		},
		{
			name: "dynamic packing - one NUMA filtered out due to threshold, reserved CPUs considered",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						// NUMA 0: 4 total, 1 reserved. Allocatable = 3. Pod uses 1. Available = 2. Ratio = 2/3 = 0.66 (above threshold)
						0: &state.NUMANodeState{
							DefaultCPUSet: machine.NewCPUSet(1, 2, 3, 4),
							PodEntries: state.PodEntries{"pod1": {"cont1": {
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:        "pod1",
									PodNamespace:  "default",
									PodName:       "pod1",
									ContainerName: "cont1",
								},
								AllocationResult: machine.NewCPUSet(2),
							}}},
						},
						// NUMA 1: 4 total, 0 reserved. Allocatable = 4. Pod uses 3. Available = 1. Ratio = 1/4 = 0.25 (below threshold)
						1: &state.NUMANodeState{
							DefaultCPUSet: machine.NewCPUSet(5, 6, 7, 8),
							PodEntries: state.PodEntries{"pod2": {"cont2": {
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:        "pod2",
									PodNamespace:  "default",
									PodName:       "pod2",
									ContainerName: "cont2",
								},
								AllocationResult: machine.NewCPUSet(5, 6, 7),
							}}},
						},
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "dynamic_packing_one_NUMA_filtered_out_due_to_threshold_reserved_CPUs_considered", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:                  machine.NewCPUSet(1), // reserve 1 from NUMA0
				cpuNUMAHintPreferPolicy:       cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
				cpuNUMAHintPreferLowThreshold: 0.5,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					// NUMA0: available (3-1)=2. After req: 1. Filtered in.
					// NUMA1: available (4-3)=1. Filtered out by threshold. Not preferred.
					{Nodes: []uint64{0}, Preferred: true},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
		},
		{
			name: "dynamic packing - allocatableCPUQuantity is zero for a NUMA",
			fields: fields{
				state: func() state.State {
					ms := state.NUMANodeMap{
						// NUMA 0: 2 total, 2 reserved. Allocatable = 0.
						0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(1, 2), PodEntries: make(state.PodEntries)},
						// NUMA 1: 2 total, 0 reserved. Allocatable = 2. Available = 2. Ratio = 1.0
						1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(3, 4), PodEntries: make(state.PodEntries)},
					}
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2) // 2 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "dynamic_packing_allocatableCPUQuantity_is_zero_for_a_NUMA", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					st.SetMachineState(ms, false)
					return st
				}(),
				reservedCPUs:                  machine.NewCPUSet(1, 2), // reserve all from NUMA0
				cpuNUMAHintPreferPolicy:       cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
				cpuNUMAHintPreferLowThreshold: 0.1,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					}, CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}}, {Nodes: []uint64{1}}}},
			},
			wantErr: nil,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					// NUMA0 is skipped by filterNUMANodesByHintPreferLowThreshold because allocatable is 0.
					// NUMA1 is the only one in filteredNUMANodes, so it's preferred by packing.
					{Nodes: []uint64{1}, Preferred: true},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			o := &canonicalHintOptimizer{
				state:                         tt.fields.state,
				reservedCPUs:                  tt.fields.reservedCPUs,
				cpuNUMAHintPreferPolicy:       tt.fields.cpuNUMAHintPreferPolicy,
				cpuNUMAHintPreferLowThreshold: tt.fields.cpuNUMAHintPreferLowThreshold,
			}

			// Create a mutable copy of hints for the test
			currentHints := &pluginapi.ListOfTopologyHints{}
			if tt.args.hints != nil && tt.args.hints.Hints != nil {
				currentHints.Hints = make([]*pluginapi.TopologyHint, len(tt.args.hints.Hints))
				for i, h := range tt.args.hints.Hints {
					// Deep copy hint
					nodesCopy := make([]uint64, len(h.Nodes))
					copy(nodesCopy, h.Nodes)
					currentHints.Hints[i] = &pluginapi.TopologyHint{
						Nodes:     nodesCopy,
						Preferred: h.Preferred,
					}
				}
			} else if tt.args.hints != nil { // hints itself is not nil, but Hints field is nil
				currentHints.Hints = []*pluginapi.TopologyHint{}
			}

			err := o.OptimizeHints(tt.args.request, currentHints)

			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedHints, currentHints)
		})
	}
}
