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
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestFrozenReclaimConstraintToDisjointDescriptors(t *testing.T) {
	t.Parallel()

	frozen := machine.NewCPUSet(0, 4)
	all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	entries := state.PodEntries{
		"steady-share": {"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
			PodUid: "steady-share", ContainerName: "main", QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
		}}},
		"ramp-share": {"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
			PodUid: "ramp-share", ContainerName: "main", QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
		}, RampUp: true}},
		"ramp-isolation": {"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
			PodUid: "ramp-isolation", ContainerName: "main", QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
			OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-ramp",
		}, RampUp: true}},
	}

	tests := []struct {
		name             string
		descriptor       advisorBlockDescriptor
		allowShared      bool
		disableDedicated bool
		want             machine.CPUSet
	}{
		{
			name: "steady ordinary share is disjoint when AO is disabled",
			descriptor: advisorBlockDescriptor{
				Class: advisorBlockClassShared, Eligible: all,
				Owners: []string{canonicalAdvisorBlockOwner(commonstate.PoolNameShare, "steady-share", "main", "")},
			},
			want: all.Difference(frozen),
		},
		{
			name: "isolation is disjoint when AO is disabled",
			descriptor: advisorBlockDescriptor{
				Class: advisorBlockClassShared, Eligible: all,
				Owners: []string{canonicalAdvisorBlockOwner(commonstate.PoolNamePrefixIsolation+"-pod", "steady-share", "main", "")},
			},
			want: all.Difference(frozen),
		},
		{
			name: "dedicated overlap remains legal",
			descriptor: advisorBlockDescriptor{
				Class: advisorBlockClassDedicated, Eligible: all,
				Owners: []string{canonicalAdvisorBlockOwner(commonstate.PoolNameDedicated, "dedicated", "main", "")},
			},
			want: all,
		},
		{
			name: "dedicated is disjoint when configured",
			descriptor: advisorBlockDescriptor{
				Class: advisorBlockClassDedicated, Eligible: all,
				Owners: []string{canonicalAdvisorBlockOwner(commonstate.PoolNameDedicated, "dedicated", "main", "")},
			},
			disableDedicated: true,
			want:             all.Difference(frozen),
		},
		{
			name: "ordinary share overlap remains legal when AO is enabled",
			descriptor: advisorBlockDescriptor{
				Class: advisorBlockClassShared, Eligible: all,
				Owners: []string{canonicalAdvisorBlockOwner(commonstate.PoolNameShare, "steady-share", "main", "")},
			},
			allowShared: true,
			want:        all,
		},
		{
			name: "ramp-up shared keeps existing non-HP semantics",
			descriptor: advisorBlockDescriptor{
				Class: advisorBlockClassShared, Eligible: all,
				Owners: []string{canonicalAdvisorBlockOwner(commonstate.PoolNameShare, "ramp-share", "main", "")},
			},
			want: all,
		},
		{
			name: "pool-style ramp-up isolation keeps existing non-HP semantics",
			descriptor: advisorBlockDescriptor{
				Class: advisorBlockClassShared, Eligible: all,
				Owners: []string{canonicalAdvisorBlockOwner(
					commonstate.PoolNamePrefixIsolation+"-ramp",
					commonstate.PoolNamePrefixIsolation+"-ramp", "", "")},
			},
			want: all,
		},
		{
			name: "reclaim descriptor is fixed to frozen identity",
			descriptor: advisorBlockDescriptor{
				Class: advisorBlockClassMandatoryReclaim, Eligible: all,
				Owners: []string{canonicalAdvisorBlockOwner(commonstate.PoolNameReclaim, commonstate.PoolNameReclaim, commonstate.FakedContainerName, "")},
			},
			want: frozen,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			constraint := frozenReclaimPlacementConstraint{
				entries: entries, cpus: frozen,
				allowSharedOverlap: tc.allowShared, disableDedicatedOverlap: tc.disableDedicated,
			}
			got, err := applyFrozenReclaimConstraintToDisjointDescriptors(
				[]advisorBlockDescriptor{tc.descriptor}, constraint)
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.True(t, got[0].Eligible.Equals(tc.want), "eligible=%s want=%s", got[0].Eligible, tc.want)
		})
	}
}

func TestFrozenReclaimAdvisorAttemptAvoidsDisallowedOwners(t *testing.T) {
	t.Parallel()

	for _, disjoint := range []bool{false, true} {
		disjoint := disjoint
		t.Run(map[bool]string{false: "legacy", true: "disjoint"}[disjoint], func(t *testing.T) {
			t.Parallel()
			topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
			require.NoError(t, err)
			dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
			dynamicConf.EnableReclaim = false

			frozen := machine.NewCPUSet(0, 4)
			entries := state.PodEntries{
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult: frozen,
					},
				},
				"pod": {"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
					PodUid: "pod", ContainerName: "main", QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod",
				}}},
			}
			p.state.SetPodEntries(entries, false)

			resp := &advisorapi.ListAndWatchResponse{
				DisableDedicatedCoresOverlapReclaimedCores: disjoint,
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameShare: {Entries: map[string]*advisorapi.CalculationInfo{
						commonstate.FakedContainerName: {
							OwnerPoolName: commonstate.PoolNameShare,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "share", Result: 3}}},
							},
						},
					}},
					"pod": {Entries: map[string]*advisorapi.CalculationInfo{
						"main": {
							OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod",
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "isolation", Result: 1}}},
							},
						},
					}},
					commonstate.PoolNameReclaim: {Entries: map[string]*advisorapi.CalculationInfo{
						commonstate.FakedContainerName: {
							OwnerPoolName: commonstate.PoolNameReclaim,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 2}}},
							},
						},
					}},
				},
			}
			featureGates := map[string]*advisorsvc.FeatureGate{}
			if disjoint {
				featureGates[feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition] = &advisorsvc.FeatureGate{
					Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
				}
			}
			constraint := newFrozenReclaimPlacementConstraint(
				entries, dynamicConf, false, disjoint)

			planned, _, err := p.generateBlockCPUSetWithCheckpointTransitionForAttempt(
				resp, featureGates, sets.NewInt(), dynamicConf, constraint)
			require.NoError(t, err)
			require.True(t, planned["share"].Intersection(frozen).IsEmpty(), "share=%s", planned["share"])
			require.True(t, planned["isolation"].Intersection(frozen).IsEmpty(), "isolation=%s", planned["isolation"])
		})
	}
}

func TestFrozenReclaimConstraintFailsAtPlannerCapacity(t *testing.T) {
	t.Parallel()

	all := machine.NewCPUSet(0, 1, 2, 3)
	descriptor := advisorBlockDescriptor{
		BlockID: "isolation", Class: advisorBlockClassShared, Quantity: 3,
		Eligible: all, Owners: []string{
			canonicalAdvisorBlockOwner(commonstate.PoolNamePrefixIsolation+"-pod", "pod", "main", ""),
		},
	}
	_, err := applyFrozenReclaimConstraintToDisjointDescriptors(
		[]advisorBlockDescriptor{descriptor}, frozenReclaimPlacementConstraint{
			entries: state.PodEntries{"pod": {"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
				PodUid: "pod", ContainerName: "main", QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
			}}}},
			cpus: machine.NewCPUSet(0, 1),
		})
	require.ErrorContains(t, err, "eligible capacity 2 is smaller than quantity 3")
	require.ErrorContains(t, err, "after excluding frozen reclaim 0-1")
}

func TestAllocateAdvisorSourceBlocksForCarveExcludesFrozenReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.state.SetPodEntries(state.PodEntries{
		"pod": {"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
			PodUid: "pod", ContainerName: "main", QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
			OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod",
		}}},
	}, false)

	blockCPUSet := advisorapi.NewBlockCPUSet()
	available := topology.CPUDetails.CPUs()
	remaining := available.Clone()
	frozen := machine.NewCPUSet(0, 4)

	err = p.allocateAdvisorSourceBlocksForCarveWithConstraint(
		[]*advisorapi.BlockInfo{{
			Block: advisorapi.Block{BlockId: "source", Result: 2},
			OwnerPoolEntryMap: map[string]advisorapi.BlockEntry{
				commonstate.PoolNameShare: {EntryName: commonstate.PoolNameShare, SubEntryName: commonstate.FakedContainerName},
			},
		}},
		[]*advisorapi.BlockInfo{{
			Block: advisorapi.Block{BlockId: "isolation", Result: 1},
			OwnerPoolEntryMap: map[string]advisorapi.BlockEntry{
				commonstate.PoolNamePrefixIsolation + "-pod": {EntryName: "pod", SubEntryName: "main"},
			},
		}},
		blockCPUSet, &available, &remaining, machine.NewCPUSet(),
		map[string]string{commonstate.PoolNameShare: "source"}, frozenReclaimPlacementConstraint{
			entries: p.state.GetPodEntries(), cpus: frozen,
		})
	require.NoError(t, err)
	require.True(t, blockCPUSet["source"].Intersection(frozen).IsEmpty(), "source=%s", blockCPUSet["source"])
}
