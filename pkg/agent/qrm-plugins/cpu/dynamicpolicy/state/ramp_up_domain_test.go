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

package state

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newRampUpTestTopology builds a 3-NUMA dummy topology (NUMA 0/1/2).
func newRampUpTestTopology(t *testing.T) *machine.CPUTopology {
	t.Helper()
	topology, err := machine.GenerateDummyCPUTopology(12, 1, 3)
	require.NoError(t, err)
	return topology
}

// newRampUpAllocation builds a ramp-up AllocationInfo for the given placement.
// When numaBinding is true the numa-binding enhancement annotation is set; the
// caller additionally controls the NUMA hint annotation via numaHint.
func newRampUpAllocation(podUID string, rampUp bool, numaBinding bool, numaHint string,
	placement map[int]machine.CPUSet,
) *AllocationInfo {
	annotations := map[string]string{
		consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
	}
	if numaBinding {
		annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
	}
	if numaHint != "" {
		annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint] = numaHint
	}

	return &AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "ns",
			PodName:       podUID,
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
			Annotations:   annotations,
		},
		RampUp:                           rampUp,
		AllocationResult:                 machine.MustParse("0"),
		OriginalAllocationResult:         machine.MustParse("0"),
		TopologyAwareAssignments:         placement,
		OriginalTopologyAwareAssignments: placement,
	}
}

// newDedicatedRampUpAllocation builds a ramp-up AllocationInfo for a dedicated
// (dedicated-cores QoS) numa-binding allocation placed across the given NUMAs.
func newDedicatedRampUpAllocation(podUID string, rampUp bool,
	placement map[int]machine.CPUSet,
) *AllocationInfo {
	annotations := map[string]string{
		consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
		consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}
	allocationResult := machine.NewCPUSet()
	for _, cpus := range placement {
		allocationResult = allocationResult.Union(cpus)
	}
	return &AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "ns",
			PodName:       podUID,
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
			Annotations:   annotations,
		},
		RampUp:                           rampUp,
		AllocationResult:                 allocationResult,
		OriginalAllocationResult:         allocationResult.Clone(),
		TopologyAwareAssignments:         placement,
		OriginalTopologyAwareAssignments: placement,
	}
}

func TestAllocationInfoRampUpReclaimDomains(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	topology := newRampUpTestTopology(t)

	nonBinding := newRampUpAllocation("pod-global", false, false, "", map[int]machine.CPUSet{})
	domains, err := nonBinding.RampUpReclaimDomains(topology)
	as.NoError(err)
	as.True(domains.Equal(sets.NewInt(commonstate.FakedNUMAID)))

	numaBinding := newRampUpAllocation("pod-n1", false, true, "1", map[int]machine.CPUSet{
		1: machine.NewCPUSet(2),
	})
	domains, err = numaBinding.RampUpReclaimDomains(topology)
	as.NoError(err)
	as.True(domains.Equal(sets.NewInt(1)))

	var nilAllocation *AllocationInfo
	_, err = nilAllocation.RampUpReclaimDomains(topology)
	as.Error(err)
}

func TestActiveRampUpDomains(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	topology := newRampUpTestTopology(t)

	// placement helpers
	placeOn := func(numaID int, cpus ...int) map[int]machine.CPUSet {
		return map[int]machine.CPUSet{numaID: machine.NewCPUSet(cpus...)}
	}

	tests := []struct {
		name        string
		podEntries  PodEntries
		wantDomains setsIntLiteral
		wantErr     bool
	}{
		{
			name:        "empty entries yields empty domains",
			podEntries:  PodEntries{},
			wantDomains: setsIntLiteral{},
		},
		{
			name: "non-binding shared ramp-up maps to the global domain",
			podEntries: PodEntries{
				"pod-global": ContainerEntries{
					"main": newRampUpAllocation("pod-global", true, false, "", placeOn(0, 0)),
				},
			},
			wantDomains: setsIntLiteral{commonstate.FakedNUMAID: true},
		},
		{
			name: "shared-numa-binding ramp-up on NUMA 0 maps to NUMA 0",
			podEntries: PodEntries{
				"pod-n0": ContainerEntries{
					"main": newRampUpAllocation("pod-n0", true, true, "0", placeOn(0, 0)),
				},
			},
			wantDomains: setsIntLiteral{0: true},
		},
		{
			name: "shared-numa-binding ramp-up on NUMA 1 maps to NUMA 1",
			podEntries: PodEntries{
				"pod-n1": ContainerEntries{
					"main": newRampUpAllocation("pod-n1", true, true, "1", placeOn(1, 2)),
				},
			},
			wantDomains: setsIntLiteral{1: true},
		},
		{
			name: "global and NUMA domains coexist",
			podEntries: PodEntries{
				"pod-global": ContainerEntries{
					"main": newRampUpAllocation("pod-global", true, false, "", placeOn(0, 0)),
				},
				"pod-n1": ContainerEntries{
					"main": newRampUpAllocation("pod-n1", true, true, "1", placeOn(1, 2)),
				},
			},
			wantDomains: setsIntLiteral{commonstate.FakedNUMAID: true, 1: true},
		},
		{
			name: "numa-binding ramp-up without hint resolves domain from placement",
			podEntries: PodEntries{
				"pod-n2": ContainerEntries{
					"main": newRampUpAllocation("pod-n2", true, true, "", placeOn(2, 4)),
				},
			},
			wantDomains: setsIntLiteral{2: true},
		},
		{
			name: "steady (non-ramp-up) allocation does not activate a domain",
			podEntries: PodEntries{
				"pod-steady": ContainerEntries{
					"main": newRampUpAllocation("pod-steady", false, true, "0", placeOn(0, 0)),
				},
			},
			wantDomains: setsIntLiteral{},
		},
		{
			name: "pool entries are ignored",
			podEntries: PodEntries{
				"pool-entry": ContainerEntries{
					commonstate.FakedContainerName: newRampUpAllocation("pool", true, false, "", placeOn(0, 0)),
				},
				"pod-global": ContainerEntries{
					"main": newRampUpAllocation("pod-global", true, false, "", placeOn(0, 0)),
				},
			},
			wantDomains: setsIntLiteral{commonstate.FakedNUMAID: true},
		},
		// --- fail-closed matrix ---
		{
			name: "missing domain: numa-binding ramp-up with no placement and no hint",
			podEntries: PodEntries{
				"pod-missing": ContainerEntries{
					"main": newRampUpAllocation("pod-missing", true, true, "", map[int]machine.CPUSet{}),
				},
			},
			wantErr: true,
		},
		{
			name: "in-flight numa-binding ramp-up resolves domain from its hint",
			podEntries: PodEntries{
				"pod-inflight": ContainerEntries{
					"main": newRampUpAllocation("pod-inflight", true, true, "1", map[int]machine.CPUSet{}),
				},
			},
			wantDomains: setsIntLiteral{1: true},
		},
		{
			name: "in-flight numa-binding ramp-up with out-of-range hint fails closed",
			podEntries: PodEntries{
				"pod-inflight-oob": ContainerEntries{
					"main": newRampUpAllocation("pod-inflight-oob", true, true, "5", map[int]machine.CPUSet{}),
				},
			},
			wantErr: true,
		},
		{
			name: "ambiguous domain: shared numa-binding ramp-up spans multiple NUMAs",
			podEntries: PodEntries{
				"pod-spans": ContainerEntries{
					"main": newRampUpAllocation("pod-spans", true, true, "", map[int]machine.CPUSet{
						0: machine.NewCPUSet(0),
						1: machine.NewCPUSet(2),
					}),
				},
			},
			wantErr: true,
		},
		{
			name: "dedicated numa-binding ramp-up spanning multiple NUMAs maps to every placement NUMA",
			podEntries: PodEntries{
				"pod-dnb-spans": ContainerEntries{
					"main": newDedicatedRampUpAllocation("pod-dnb-spans", true, map[int]machine.CPUSet{
						0: machine.NewCPUSet(0),
						1: machine.NewCPUSet(2),
					}),
				},
			},
			wantDomains: setsIntLiteral{0: true, 1: true},
		},
		{
			name: "dedicated numa-binding ramp-up on a single NUMA maps to that NUMA",
			podEntries: PodEntries{
				"pod-dnb": ContainerEntries{
					"main": newDedicatedRampUpAllocation("pod-dnb", true, map[int]machine.CPUSet{
						2: machine.NewCPUSet(4),
					}),
				},
			},
			wantDomains: setsIntLiteral{2: true},
		},
		{
			name: "ambiguous domain: NUMA hint disagrees with placement",
			podEntries: PodEntries{
				"pod-mismatch": ContainerEntries{
					"main": newRampUpAllocation("pod-mismatch", true, true, "1", placeOn(0, 0)),
				},
			},
			wantErr: true,
		},
		{
			name: "ambiguous domain: NUMA hint parses to multiple NUMAs",
			podEntries: PodEntries{
				"pod-hintmulti": ContainerEntries{
					"main": newRampUpAllocation("pod-hintmulti", true, true, "0-1", placeOn(0, 0)),
				},
			},
			wantErr: true,
		},
		{
			name: "out-of-range domain: placement on a NUMA absent from topology",
			podEntries: PodEntries{
				"pod-oob": ContainerEntries{
					"main": newRampUpAllocation("pod-oob", true, true, "5", placeOn(5, 40)),
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			domains, err := tc.podEntries.ActiveRampUpDomains(topology)
			if tc.wantErr {
				as.Error(err)
				return
			}
			as.NoError(err)
			as.Equal(len(tc.wantDomains), domains.Len())
			for d := range tc.wantDomains {
				as.True(domains.Has(d), "expected domain %d to be present", d)
			}
		})
	}
}

// setsIntLiteral is a tiny helper so table entries can express expected domains
// as a set literal without importing sets at call sites.
type setsIntLiteral map[int]bool

func TestActiveRampUpDomainsNilTopology(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	pe := PodEntries{
		"pod": ContainerEntries{
			"main": newRampUpAllocation("pod", true, false, "", map[int]machine.CPUSet{0: machine.NewCPUSet(0)}),
		},
	}
	_, err := pe.ActiveRampUpDomains(nil)
	as.Error(err)
}

func TestActiveRampUpDomainsConvergesWhenRampUpEnds(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	topology := newRampUpTestTopology(t)

	entry := ContainerEntries{
		"main": newRampUpAllocation("pod-n1", true, true, "1", map[int]machine.CPUSet{1: machine.NewCPUSet(2)}),
	}
	pe := PodEntries{"pod-n1": entry}

	domains, err := pe.ActiveRampUpDomains(topology)
	as.NoError(err)
	as.True(domains.Has(1))

	// ramp-up ends: the domain must disappear immediately, with no exit lag.
	entry["main"].RampUp = false
	domains, err = pe.ActiveRampUpDomains(topology)
	as.NoError(err)
	as.Equal(0, domains.Len())
}

func TestActiveRampUpDomainsDeterministic(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	topology := newRampUpTestTopology(t)

	pe := PodEntries{
		"pod-global": ContainerEntries{
			"main": newRampUpAllocation("pod-global", true, false, "", map[int]machine.CPUSet{0: machine.NewCPUSet(0)}),
		},
		"pod-n0": ContainerEntries{
			"main": newRampUpAllocation("pod-n0", true, true, "0", map[int]machine.CPUSet{0: machine.NewCPUSet(0)}),
		},
		"pod-n2": ContainerEntries{
			"main": newRampUpAllocation("pod-n2", true, true, "2", map[int]machine.CPUSet{2: machine.NewCPUSet(4)}),
		},
	}

	first, err := pe.ActiveRampUpDomains(topology)
	as.NoError(err)
	for i := 0; i < 50; i++ {
		got, err := pe.ActiveRampUpDomains(topology)
		as.NoError(err)
		as.True(got.Equal(first), "iteration %d diverged", i)
	}
	as.True(first.Has(commonstate.FakedNUMAID))
	as.True(first.Has(0))
	as.True(first.Has(2))
}

func TestHasActiveRampUpIsNodeGlobalObservation(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	topology := newRampUpTestTopology(t)

	pe := PodEntries{
		"pod-n1": ContainerEntries{
			"main": newRampUpAllocation("pod-n1", true, true, "1", map[int]machine.CPUSet{1: machine.NewCPUSet(2)}),
		},
	}

	// HasActiveRampUp stays a node-global boolean: it is true whenever any
	// domain has ramp-up, regardless of which NUMA it lives on.
	as.True(pe.HasActiveRampUp())

	domains, err := pe.ActiveRampUpDomains(topology)
	as.NoError(err)
	as.True(domains.Has(1))
	as.False(domains.Has(0))
	as.False(domains.Has(2))
}
