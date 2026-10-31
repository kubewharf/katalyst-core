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

package validator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

func TestCPUAdvisorValidatorRejectsPolicyForbiddenAliases(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	v := NewCPUAdvisorValidator(cpustate.NewCPUPluginState(nil),
		&machine.KatalystMachineInfo{CPUTopology: topology})

	for _, tc := range []struct {
		name    string
		dd      bool
		as      bool
		owners  []string
		wantErr string
	}{
		{
			name:    "DD rejects dedicated reclaim alias",
			dd:      true,
			as:      true,
			owners:  []string{commonstate.PoolNameDedicated, commonstate.PoolNameReclaim},
			wantErr: "dedicated and reclaim share block",
		},
		{
			name:   "legacy permits dedicated reclaim alias",
			as:     true,
			owners: []string{commonstate.PoolNameDedicated, commonstate.PoolNameReclaim},
		},
		{
			name:    "AS false rejects shared reclaim alias",
			owners:  []string{commonstate.PoolNameShare, commonstate.PoolNameReclaim},
			wantErr: "shared and reclaim share block",
		},
		{
			name:   "AS true permits shared reclaim alias",
			as:     true,
			owners: []string{commonstate.PoolNameShare, commonstate.PoolNameReclaim},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := advisorAliasResponse("alias", tc.owners...)
			resp.DisableDedicatedCoresOverlapReclaimedCores = tc.dd
			resp.AllowSharedCoresOverlapReclaimedCores = tc.as
			err := v.validateOverlapPolicy(resp)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}

func TestCPUAdvisorValidatorRejectsIncompatibleResourcePackageAliases(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	v := NewCPUAdvisorValidator(cpustate.NewCPUPluginState(nil),
		&machine.KatalystMachineInfo{CPUTopology: topology})

	resp := advisorAliasResponse(
		"alias",
		resourcepackage.WrapOwnerPoolName("share-a", "rp-a"),
		resourcepackage.WrapOwnerPoolName("share-b", "rp-b"),
	)
	require.ErrorContains(t, v.validateResourcePackageOwners(resp), "incompatible resource packages")

	resp = advisorAliasResponse(
		"alias",
		resourcepackage.WrapOwnerPoolName("share-a", "rp-a"),
		resourcepackage.WrapOwnerPoolName("share-b", "rp-a"),
	)
	require.NoError(t, v.validateResourcePackageOwners(resp))
}

func TestCPUAdvisorValidatorValidatesNUMABindingDedicatedQuantityInDisjointMode(t *testing.T) {
	t.Parallel()

	// newValidator builds a fully isolated validator whose backing state owns a
	// single dedicated NUMA-binding container occupying allocated. Every subtest
	// constructs its own validator so parallel subtests never share the mutable
	// currentState; the historical shared state caused "legacy mode rejects
	// shrink" to observe a concurrent shrink written by "non-exclusive grow from
	// one core passes" and spuriously pass.
	newValidator := func(t *testing.T, allocated machine.CPUSet, exclusive bool) *CPUAdvisorValidator {
		t.Helper()
		topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
		require.NoError(t, err)
		annotations := map[string]string{
			consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		}
		if exclusive {
			annotations[consts.PodAnnotationMemoryEnhancementNumaExclusive] = consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable
		}
		st := cpustate.NewCPUPluginState(nil)
		st.SetPodEntries(cpustate.PodEntries{
			"pod": {
				"container": &cpustate.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "pod",
						ContainerName: "container",
						QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
						Annotations:   annotations,
					},
					AllocationResult:         allocated,
					TopologyAwareAssignments: map[int]machine.CPUSet{0: allocated},
				},
			},
		})
		return NewCPUAdvisorValidator(st, &machine.KatalystMachineInfo{CPUTopology: topology})
	}

	dedicatedResp := func(quantity uint64, disjoint bool) *advisorapi.ListAndWatchResponse {
		return &advisorapi.ListAndWatchResponse{
			DisableDedicatedCoresOverlapReclaimedCores: disjoint,
			Entries: map[string]*advisorapi.CalculationEntries{
				"pod": {
					Entries: map[string]*advisorapi.CalculationInfo{
						"container": {
							OwnerPoolName: commonstate.PoolNameDedicated,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated", Result: quantity}}},
							},
						},
					},
				},
			},
		}
	}

	for _, tc := range []struct {
		name      string
		quantity  uint64
		exclusive bool
		wantErr   string
	}{
		{name: "shrink passes", quantity: 1},
		{name: "zero rejects", quantity: 0, wantErr: "zero dedicated calculation result"},
		{name: "non-exclusive grow passes", quantity: 3},
		{name: "exclusive grow passes", quantity: 3, exclusive: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := newValidator(t, machine.NewCPUSet(0, 1), tc.exclusive)
			err := v.Validate(dedicatedResp(tc.quantity, true))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}

	for _, tc := range []struct {
		name     string
		quantity uint64
	}{
		{name: "legacy mode rejects shrink", quantity: 1},
		{name: "legacy mode rejects exclusive grow", quantity: 3},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := newValidator(t, machine.NewCPUSet(0, 1), false)
			require.ErrorContains(t, v.Validate(dedicatedResp(tc.quantity, false)), "calculation result")
		})
	}

	// Regression for the production grow-rejection deadlock observed on
	// node fdbd:dc06:2:b32::53: a non-exclusive DNB container shrank to a
	// single core and the advisor recomputed a larger target, but the
	// validator rejected the grow and pinned the pod at 1 core. The grow
	// target is kept within a single NUMA's capacity so the test isolates
	// the grow-rejection path (the real node simply had a larger NUMA).
	t.Run("non-exclusive grow from one core passes", func(t *testing.T) {
		t.Parallel()
		v := newValidator(t, machine.NewCPUSet(0), false)
		require.NoError(t, v.Validate(dedicatedResp(4, true)))
	})
}

func TestCPUAdvisorValidatorMissingSharedNUMABindingCalculationInfo(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)

	for _, tc := range []struct {
		name          string
		rampUp        bool
		ownerPoolName string
		wantErr       bool
	}{
		{
			name:    "ramp-up with empty owner is allowed",
			rampUp:  true,
			wantErr: false,
		},
		{
			name:          "ramp-up with assigned owner fails closed",
			rampUp:        true,
			ownerPoolName: commonstate.PoolNameShare,
			wantErr:       true,
		},
		{
			name:    "steady with empty owner fails closed",
			rampUp:  false,
			wantErr: true,
		},
		{
			name:          "steady with assigned owner fails closed",
			rampUp:        false,
			ownerPoolName: commonstate.PoolNameShare,
			wantErr:       true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			currentState := cpustate.NewCPUPluginState(nil)
			currentState.SetPodEntries(cpustate.PodEntries{
				"pod": {
					"container": &cpustate.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "pod",
							ContainerName: "container",
							QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
							OwnerPoolName: tc.ownerPoolName,
							Annotations: map[string]string{
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
						},
						RampUp: tc.rampUp,
					},
				},
			})
			v := NewCPUAdvisorValidator(currentState,
				&machine.KatalystMachineInfo{CPUTopology: topology})

			err := v.validateEntries(&advisorapi.ListAndWatchResponse{})
			if tc.wantErr {
				require.ErrorContains(t, err, "missing CalculationInfo")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCPUAdvisorValidatorRejectsMissingDedicatedRampUpCalculationInfo(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	currentState := cpustate.NewCPUPluginState(nil)
	currentState.SetPodEntries(cpustate.PodEntries{
		"pod": {
			"container": &cpustate.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod",
					ContainerName: "container",
					QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
				},
				RampUp: true,
			},
		},
	})
	v := NewCPUAdvisorValidator(currentState,
		&machine.KatalystMachineInfo{CPUTopology: topology})

	require.Error(t, v.validateEntries(&advisorapi.ListAndWatchResponse{}))
}

func TestCPUAdvisorValidatorValidatesDefaultShareUpperBound(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	currentState := cpustate.NewCPUPluginState(nil)
	currentState.SetPodEntries(cpustate.PodEntries{
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &cpustate.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &cpustate.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(4, 5, 6, 7),
			},
		},
	})
	v := NewCPUAdvisorValidator(currentState, &machine.KatalystMachineInfo{CPUTopology: topology})

	response := func(defaultShareQuantity uint64) *advisorapi.ListAndWatchResponse {
		return &advisorapi.ListAndWatchResponse{
			Entries: map[string]*advisorapi.CalculationEntries{
				commonstate.PoolNameShare: {
					Entries: map[string]*advisorapi.CalculationInfo{
						commonstate.FakedContainerName: {
							OwnerPoolName: commonstate.PoolNameShare,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								commonstate.FakedNUMAID: {
									Blocks: []*advisorapi.Block{{BlockId: "share-upper-bound", Result: defaultShareQuantity}},
								},
							},
						},
					},
				},
				commonstate.PoolNameReclaim: {
					Entries: map[string]*advisorapi.CalculationInfo{
						commonstate.FakedContainerName: {
							OwnerPoolName: commonstate.PoolNameReclaim,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								commonstate.FakedNUMAID: {
									Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 4}},
								},
							},
						},
					},
				},
			},
		}
	}

	require.Error(t, v.Validate(response(6)), "legacy validation must retain exact-block capacity semantics")
	require.NoError(t, v.ValidateWithDefaultShareUpperBound(response(6)))
	require.NoError(t, v.ValidateWithDefaultShareUpperBound(response(3)),
		"a stale current share must not override the post-plan residual fail-closed check")
	require.ErrorContains(t, v.ValidateWithDefaultShareUpperBound(response(9)),
		"default share upper bound 9 exceeds total capacity 8")
}

func advisorAliasResponse(blockID string, owners ...string) *advisorapi.ListAndWatchResponse {
	resp := &advisorapi.ListAndWatchResponse{Entries: make(map[string]*advisorapi.CalculationEntries)}
	for i, owner := range owners {
		entryName := owner
		if poolName, _ := resourcepackage.UnwrapOwnerPoolName(owner); poolName == commonstate.PoolNameDedicated {
			entryName = "pod-dedicated"
		}
		resp.Entries[entryName] = &advisorapi.CalculationEntries{
			Entries: map[string]*advisorapi.CalculationInfo{
				"container": {
					OwnerPoolName: owner,
					CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
						0: {Blocks: []*advisorapi.Block{{BlockId: blockID, Result: uint64(i + 1)}}},
					},
				},
			},
		}
	}
	return resp
}

func TestCPUAdvisorValidatorRejectsIncompleteReclaimNUMAResults(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	state := cpustate.NewCPUPluginState(nil)
	validator := NewCPUAdvisorValidator(state, &machine.KatalystMachineInfo{CPUTopology: topology})

	testCases := []struct {
		name    string
		results map[int64]*advisorapi.NumaCalculationResult
		wantErr string
	}{
		{
			name:    "empty NUMA view is compatible",
			results: map[int64]*advisorapi.NumaCalculationResult{},
		},
		{
			name: "fake NUMA only is compatible",
			results: map[int64]*advisorapi.NumaCalculationResult{
				commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 2}}},
			},
		},
		{
			name: "partial physical NUMA result is compatible",
			results: map[int64]*advisorapi.NumaCalculationResult{
				1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 2}}},
			},
		},
		{
			name: "hybrid physical and fake NUMA result is compatible",
			results: map[int64]*advisorapi.NumaCalculationResult{
				0:                       {Blocks: []*advisorapi.Block{{BlockId: "reclaim-0", Result: 2}}},
				commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-fake", Result: 2}}},
			},
		},
		{
			name: "empty physical NUMA blocks",
			results: map[int64]*advisorapi.NumaCalculationResult{
				0: {Blocks: []*advisorapi.Block{}},
				1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 2}}},
			},
			wantErr: "empty reclaim blocks for NUMA: 0",
		},
		{
			name: "nil-only physical NUMA blocks",
			results: map[int64]*advisorapi.NumaCalculationResult{
				0: {Blocks: []*advisorapi.Block{nil}},
				1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 2}}},
			},
			wantErr: "empty reclaim blocks for NUMA: 0",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*advisorapi.CalculationInfo{
							commonstate.FakedContainerName: {
								OwnerPoolName:             commonstate.PoolNameReclaim,
								CalculationResultsByNumas: tc.results,
							},
						},
					},
				},
			}

			err := validator.Validate(resp)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestCPUAdvisorValidatorOverlapModeStillValidatesPhysicalNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	validator := NewCPUAdvisorValidator(cpustate.NewCPUPluginState(nil),
		&machine.KatalystMachineInfo{CPUTopology: topology})

	for _, tc := range []struct {
		name    string
		results map[int64]*advisorapi.NumaCalculationResult
		wantErr string
	}{
		{
			name: "unknown physical NUMA",
			results: map[int64]*advisorapi.NumaCalculationResult{
				2: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-2", Result: 1}}},
			},
			wantErr: "NUMA: 2 referred by blocks isn't in topology",
		},
		{
			name: "physical NUMA capacity exceeded",
			results: map[int64]*advisorapi.NumaCalculationResult{
				0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-0", Result: 5}}},
			},
			wantErr: "exceeds NUMA capacity",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &advisorapi.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*advisorapi.CalculationInfo{
							commonstate.FakedContainerName: {
								OwnerPoolName:             commonstate.PoolNameReclaim,
								CalculationResultsByNumas: tc.results,
							},
						},
					},
				},
			}
			require.ErrorContains(t, validator.Validate(resp), tc.wantErr)
		})
	}
}

func TestCPUAdvisorValidatorUsesIncomingOverlapMode(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)

	for _, tc := range []struct {
		name            string
		stateOverlap    bool
		incomingOverlap bool
		wantErr         bool
	}{
		{
			name:            "incoming disjoint rejects empty physical NUMA blocks despite stale overlap state",
			stateOverlap:    true,
			incomingOverlap: false,
			wantErr:         true,
		},
		{
			name:            "incoming overlap accepts empty physical NUMA blocks despite stale disjoint state",
			stateOverlap:    false,
			incomingOverlap: true,
			wantErr:         false,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			currentState := cpustate.NewCPUPluginState(nil)
			currentState.SetAllowSharedCoresOverlapReclaimedCores(tc.stateOverlap)
			validator := NewCPUAdvisorValidator(currentState, &machine.KatalystMachineInfo{CPUTopology: topology})
			resp := &advisorapi.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: tc.incomingOverlap,
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*advisorapi.CalculationInfo{
							commonstate.FakedContainerName: {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: nil},
								},
							},
						},
					},
				},
			}

			err := validator.Validate(resp)
			if tc.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "empty reclaim blocks for NUMA: 0")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCPUAdvisorValidatorDoesNotApplyHardFakeCapacityToSteady(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	validator := NewCPUAdvisorValidator(cpustate.NewCPUPluginState(nil),
		&machine.KatalystMachineInfo{CPUTopology: topology})

	resp := &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {
								Blocks: []*advisorapi.Block{{BlockId: "real-0", Result: 2}},
							},
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "fake", Result: 6}},
							},
						},
					},
				},
			},
		},
	}

	require.NoError(t, validator.validateBlocks(resp),
		"the generic validator must not apply hard-only fake capacity before hardActive is known")
}
