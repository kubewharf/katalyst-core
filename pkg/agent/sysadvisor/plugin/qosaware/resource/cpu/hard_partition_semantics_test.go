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

package cpu

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// fakeRegion is a minimal QoSRegion stand-in for scopeNUMAMapping tests.
// It implements only the surface scopeNUMAMapping reads; every other method
// is a no-op because the scope mapping never touches them.
type fakeRegion struct {
	name          string
	regionType    configapi.QoSRegionType
	numaBinding   bool
	numaExclusive bool
	bindingNumas  machine.CPUSet
}

func (f *fakeRegion) Name() string                             { return f.name }
func (f *fakeRegion) Type() configapi.QoSRegionType            { return f.regionType }
func (f *fakeRegion) OwnerPoolName() string                    { return f.name }
func (f *fakeRegion) GetResourcePackageName() string           { return "" }
func (f *fakeRegion) IsEmpty() bool                            { return false }
func (f *fakeRegion) Clear()                                   {}
func (f *fakeRegion) GetBindingNumas() machine.CPUSet          { return f.bindingNumas }
func (f *fakeRegion) GetPods() types.PodSet                    { return types.PodSet{} }
func (f *fakeRegion) GetPodsRequest() float64                  { return 0 }
func (f *fakeRegion) SetBindingNumas(machine.CPUSet)           {}
func (f *fakeRegion) SetEssentials(types.ResourceEssentials)   {}
func (f *fakeRegion) IsNumaBinding() bool                      { return f.numaBinding }
func (f *fakeRegion) IsNumaExclusive() bool                    { return f.numaExclusive }
func (f *fakeRegion) SetThrottled(bool)                        {}
func (f *fakeRegion) AddContainer(*types.ContainerInfo) error  { return nil }
func (f *fakeRegion) TryUpdateProvision()                      {}
func (f *fakeRegion) TryUpdateHeadroom()                       {}
func (f *fakeRegion) UpdateStatus()                            {}
func (f *fakeRegion) GetProvision() (types.ControlKnob, error) { return types.ControlKnob{}, nil }
func (f *fakeRegion) GetHeadroom() (float64, error)            { return 0, nil }
func (f *fakeRegion) IsThrottled() bool                        { return false }
func (f *fakeRegion) EnableReclaim() bool                      { return true }
func (f *fakeRegion) GetProvisionPolicy() (types.CPUProvisionPolicyName, types.CPUProvisionPolicyName) {
	return types.CPUProvisionPolicyNone, types.CPUProvisionPolicyNone
}

func (f *fakeRegion) GetHeadRoomPolicy() (types.CPUHeadroomPolicyName, types.CPUHeadroomPolicyName) {
	return types.CPUHeadroomPolicyNone, types.CPUHeadroomPolicyNone
}
func (f *fakeRegion) GetStatus() types.RegionStatus                 { return types.RegionStatus{} }
func (f *fakeRegion) GetControlEssentials() types.ControlEssentials { return types.ControlEssentials{} }
func (f *fakeRegion) GetMetaInfo() string                           { return f.name }

// newScopeMappingAdvisor builds a cpuResourceAdvisor with a stub metaServer that
// reports the given real NUMA set, plus a regionMap populated from the provided
// fake regions. This is exactly the surface scopeNUMAMapping reads.
func newScopeMappingAdvisor(t *testing.T, realNUMAs []int, regions map[string]region.QoSRegion, disableExclusiveOverlap bool) *cpuResourceAdvisor {
	t.Helper()

	cpuDetails := machine.CPUDetails{}
	numaToCPUs := machine.NUMANodeInfo{}
	for _, numaID := range realNUMAs {
		cpuID := numaID * 100
		cpuDetails[cpuID] = machine.CPUTopoInfo{NUMANodeID: numaID}
		numaToCPUs[numaID] = machine.NewCPUSet(cpuID)
	}
	topology := &machine.CPUTopology{
		NumCPUs:      len(realNUMAs),
		NumCores:     len(realNUMAs),
		NumNUMANodes: len(realNUMAs),
		NUMAToCPUs:   numaToCPUs,
		CPUDetails:   cpuDetails,
	}
	return &cpuResourceAdvisor{
		regionMap: regions,
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: topology,
				},
			},
		},
	}
}

// TestScopeNUMAMappingGlobalFallback verifies that every NUMA not claimed by a
// dedicated binding region falls into the global (FakedNUMAID) non-exclusive
// scope, and that the global scope's member list holds the real backing NUMAs
// (not FakedNUMAID itself).
func TestScopeNUMAMappingGlobalFallback(t *testing.T) {
	t.Parallel()

	regions := map[string]region.QoSRegion{}
	cra := newScopeMappingAdvisor(t, []int{0, 1, 2, 3}, regions, false)

	mapping := cra.scopeNUMAMapping(false)
	global := provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)

	require.Contains(t, mapping, global, "global scope must always exist")
	require.ElementsMatch(t, []int{0, 1, 2, 3}, mapping[global],
		"all unclaimed NUMAs back the global faked scope")
	require.Len(t, mapping, 1, "no dedicated regions => only global scope")
}

// TestScopeNUMAMappingExclusiveDedicated verifies that a NUMA-exclusive dedicated
// region claims its binding NUMAs into an exclusive scope, and the remaining
// NUMAs fall back to the global scope.
func TestScopeNUMAMappingExclusiveDedicated(t *testing.T) {
	t.Parallel()

	regions := map[string]region.QoSRegion{
		"dedicated-excl-0": &fakeRegion{
			name:          "dedicated-excl-0",
			regionType:    configapi.QoSRegionTypeDedicated,
			numaBinding:   true,
			numaExclusive: true,
			bindingNumas:  machine.NewCPUSet(0),
		},
	}

	t.Run("with overlap disabled => exclusive scope", func(t *testing.T) {
		t.Parallel()
		cra := newScopeMappingAdvisor(t, []int{0, 1}, regions, true)
		mapping := cra.scopeNUMAMapping(true)

		excl := provisionassembler.NewExclusiveReclaimConstraintScope("dedicated-excl-0")
		global := provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)

		require.Contains(t, mapping, excl)
		require.Equal(t, []int{0}, mapping[excl], "exclusive region claims NUMA 0")
		require.Contains(t, mapping, global)
		require.Equal(t, []int{1}, mapping[global], "NUMA 1 falls back to global")
	})

	t.Run("with overlap enabled => legacy-exclusive scope", func(t *testing.T) {
		t.Parallel()
		cra := newScopeMappingAdvisor(t, []int{0, 1}, regions, false)
		mapping := cra.scopeNUMAMapping(false)

		legacy := provisionassembler.NewLegacyExclusiveReclaimConstraintScope("dedicated-excl-0")
		global := provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)

		require.Contains(t, mapping, legacy)
		require.Equal(t, []int{0}, mapping[legacy])
		require.Contains(t, mapping, global)
		require.Equal(t, []int{1}, mapping[global])
	})
}

// TestScopeNUMAMappingSNBNonExclusive verifies that a non-exclusive NUMA-binding
// dedicated region (SNB) creates one per-NUMA non-exclusive scope for each of
// its binding NUMAs, and those NUMAs are removed from the global fallback.
func TestScopeNUMAMappingSNBNonExclusive(t *testing.T) {
	t.Parallel()

	regions := map[string]region.QoSRegion{
		"dedicated-snb": &fakeRegion{
			name:          "dedicated-snb",
			regionType:    configapi.QoSRegionTypeDedicated,
			numaBinding:   true,
			numaExclusive: false,
			bindingNumas:  machine.NewCPUSet(1, 2),
		},
	}
	cra := newScopeMappingAdvisor(t, []int{0, 1, 2, 3}, regions, false)
	mapping := cra.scopeNUMAMapping(false)

	snb1 := provisionassembler.NewNonExclusiveReclaimConstraintScope(1)
	snb2 := provisionassembler.NewNonExclusiveReclaimConstraintScope(2)
	global := provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)

	require.Contains(t, mapping, snb1)
	require.Equal(t, []int{1}, mapping[snb1])
	require.Contains(t, mapping, snb2)
	require.Equal(t, []int{2}, mapping[snb2])
	require.Contains(t, mapping, global)
	require.ElementsMatch(t, []int{0, 3}, mapping[global],
		"unclaimed NUMAs 0 and 3 back the global scope")
}

// TestActiveReclaimScopesGlobalOnlyByFakedDomain verifies the hard-partition
// boundary: the global (FakedNUMAID) scope is activated ONLY by the
// FakedNUMAID ramp-up domain. A real-NUMA ramp-up must never activate the
// global scope, and vice versa.
func TestActiveReclaimScopesGlobalOnlyByFakedDomain(t *testing.T) {
	t.Parallel()

	scopeNumas := map[provisionassembler.ReclaimConstraintScope][]int{
		provisionassembler.NewNonExclusiveReclaimConstraintScope(0):                       {0},
		provisionassembler.NewNonExclusiveReclaimConstraintScope(1):                       {1},
		provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID): {2, 3},
	}

	t.Run("faked domain activates only global scope", func(t *testing.T) {
		t.Parallel()
		cra := &cpuResourceAdvisor{}
		active := cra.activeReclaimScopes(scopeNumas, sets.NewInt(commonstate.FakedNUMAID))
		require.Len(t, active, 1)
		require.True(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)])
		require.False(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(0)])
		require.False(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(1)])
	})

	t.Run("real NUMA 0 domain activates only NUMA 0 scope, never global", func(t *testing.T) {
		t.Parallel()
		cra := &cpuResourceAdvisor{}
		active := cra.activeReclaimScopes(scopeNumas, sets.NewInt(0))
		require.Len(t, active, 1)
		require.True(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(0)])
		require.False(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(1)])
		require.False(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)],
			"real-NUMA ramp-up must not activate the global faked scope")
	})

	t.Run("both real NUMAs activate their scopes independently", func(t *testing.T) {
		t.Parallel()
		cra := &cpuResourceAdvisor{}
		active := cra.activeReclaimScopes(scopeNumas, sets.NewInt(0, 1))
		require.Len(t, active, 2)
		require.True(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(0)])
		require.True(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(1)])
		require.False(t, active[provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)])
	})

	t.Run("empty ramp-up set activates nothing", func(t *testing.T) {
		t.Parallel()
		cra := &cpuResourceAdvisor{}
		active := cra.activeReclaimScopes(scopeNumas, sets.NewInt())
		require.Empty(t, active)
	})
}

// TestRampUpReclaimDomainsForContainer verifies the per-container domain
// derivation, which is the input to the hard-partition boundary. Non-binding
// ramp-up maps to FakedNUMAID; NUMA-binding ramp-up maps to its placement NUMAs;
// ambiguous or out-of-range inputs fail closed.
func TestRampUpReclaimDomainsForContainer(t *testing.T) {
	t.Parallel()

	// Build a topology with real NUMAs 0 and 1.
	cpuDetails := machine.CPUDetails{}
	numaToCPUs := machine.NUMANodeInfo{}
	for _, numaID := range []int{0, 1} {
		cpuDetails[numaID*100] = machine.CPUTopoInfo{NUMANodeID: numaID}
		numaToCPUs[numaID] = machine.NewCPUSet(numaID * 100)
	}
	topology := &machine.CPUTopology{
		NumCPUs:      2,
		NumCores:     2,
		NumNUMANodes: 2,
		NUMAToCPUs:   numaToCPUs,
		CPUDetails:   cpuDetails,
	}
	cra := &cpuResourceAdvisor{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: topology},
			},
		},
	}

	bindingEnable := map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}

	t.Run("nil container errors", func(t *testing.T) {
		t.Parallel()
		_, err := cra.rampUpReclaimDomainsForContainer(nil)
		require.Error(t, err)
	})

	t.Run("non-binding ramp-up maps to global FakedNUMAID", func(t *testing.T) {
		t.Parallel()
		ci := &types.ContainerInfo{
			PodUID:        "p1",
			ContainerName: "c1",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations:   map[string]string{},
		}
		domains, err := cra.rampUpReclaimDomainsForContainer(ci)
		require.NoError(t, err)
		require.Equal(t, sets.NewInt(commonstate.FakedNUMAID), domains)
	})

	t.Run("numa-binding ramp-up with placement maps to placement NUMA", func(t *testing.T) {
		t.Parallel()
		ci := &types.ContainerInfo{
			PodUID:        "p2",
			ContainerName: "c2",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations:   bindingEnable,
			TopologyAwareAssignments: types.TopologyAwareAssignment{
				0: machine.NewCPUSet(0),
			},
		}
		domains, err := cra.rampUpReclaimDomainsForContainer(ci)
		require.NoError(t, err)
		require.Equal(t, sets.NewInt(0), domains)
	})

	t.Run("dedicated numa-binding spanning NUMAs maps to every placement NUMA", func(t *testing.T) {
		t.Parallel()
		ci := &types.ContainerInfo{
			PodUID:        "p3",
			ContainerName: "c3",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations:   bindingEnable,
			TopologyAwareAssignments: types.TopologyAwareAssignment{
				0: machine.NewCPUSet(0),
				1: machine.NewCPUSet(100),
			},
		}
		domains, err := cra.rampUpReclaimDomainsForContainer(ci)
		require.NoError(t, err)
		require.Equal(t, sets.NewInt(0, 1), domains)
	})

	t.Run("shared numa-binding spanning NUMAs fails closed", func(t *testing.T) {
		t.Parallel()
		ci := &types.ContainerInfo{
			PodUID:        "p4",
			ContainerName: "c4",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations:   bindingEnable,
			TopologyAwareAssignments: types.TopologyAwareAssignment{
				0: machine.NewCPUSet(0),
				1: machine.NewCPUSet(100),
			},
		}
		_, err := cra.rampUpReclaimDomainsForContainer(ci)
		require.Error(t, err)
		require.Contains(t, err.Error(), "ambiguous")
	})

	t.Run("numa-binding without placement falls back to hint, rejects unparseable hint", func(t *testing.T) {
		t.Parallel()
		annotations := map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			cpuconsts.CPUStateAnnotationKeyNUMAHint:             "-1",
		}
		ci := &types.ContainerInfo{
			PodUID:        "p5",
			ContainerName: "c5",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations:   annotations,
		}
		_, err := cra.rampUpReclaimDomainsForContainer(ci)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing ramp-up domain")
	})

	t.Run("numa-binding without placement resolves real hint", func(t *testing.T) {
		t.Parallel()
		annotations := map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			cpuconsts.CPUStateAnnotationKeyNUMAHint:             "1",
		}
		ci := &types.ContainerInfo{
			PodUID:        "p6",
			ContainerName: "c6",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations:   annotations,
		}
		domains, err := cra.rampUpReclaimDomainsForContainer(ci)
		require.NoError(t, err)
		require.Equal(t, sets.NewInt(1), domains)
	})

	t.Run("out-of-topology placement fails closed", func(t *testing.T) {
		t.Parallel()
		ci := &types.ContainerInfo{
			PodUID:        "p7",
			ContainerName: "c7",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations:   bindingEnable,
			TopologyAwareAssignments: types.TopologyAwareAssignment{
				99: machine.NewCPUSet(9900),
			},
		}
		_, err := cra.rampUpReclaimDomainsForContainer(ci)
		require.Error(t, err)
		require.Contains(t, err.Error(), "out-of-range")
	})
}
