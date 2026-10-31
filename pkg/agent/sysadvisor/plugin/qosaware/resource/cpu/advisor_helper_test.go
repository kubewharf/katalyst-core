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
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCPUResourceAdvisorUpdateNUMAsAvailableUsesRealNUMAIDs(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	for cpuID, cpuInfo := range topology.CPUDetails {
		if cpuInfo.NUMANodeID == 0 {
			cpuInfo.NUMANodeID = 2
		} else {
			cpuInfo.NUMANodeID = 7
		}
		topology.CPUDetails[cpuID] = cpuInfo
	}
	topology.NUMAToCPUs = machine.NUMANodeInfo{
		2: topology.CPUDetails.CPUsInNUMANodes(2),
		7: topology.CPUDetails.CPUsInNUMANodes(7),
	}

	metaCache := metacache.NewDummyMetaCacheImp()
	require.NoError(t, metaCache.SetPoolInfo(commonstate.PoolNameReserve, &types.PoolInfo{
		PoolName: commonstate.PoolNameReserve,
		TopologyAwareAssignments: types.TopologyAwareAssignment{
			2: machine.NewCPUSet(0),
			7: machine.NewCPUSet(2, 6),
		},
	}))
	cra := &cpuResourceAdvisor{
		conf:      conf,
		metaCache: metaCache,
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: topology,
				},
			},
		},
	}

	require.NoError(t, cra.updateNumasAvailableResource(
		conf.GetDynamicConfiguration(), false, nil, sets.NewInt()))
	require.Equal(t, map[int]int{2: 3, 7: 2}, cra.numaAvailable)
}

func Test_cpuResourceAdvisor_updateReservedForReclaim(t *testing.T) {
	t.Parallel()
	type fields struct {
		numaNum                                  int
		socketNum                                int
		numCPUs                                  int
		minReclaimedResourceForAllocate          v1.ResourceList
		numaMinReclaimedResourceRatioForAllocate v1.ResourceList
	}
	tests := []struct {
		name                   string
		fields                 fields
		wantReservedForReclaim map[int]int
	}{
		{
			name: "reserved for reclaim",
			fields: fields{
				numaNum:   2,
				socketNum: 1,
				numCPUs:   16,
				minReclaimedResourceForAllocate: v1.ResourceList{
					v1.ResourceCPU: resource.MustParse("4"),
				},
			},
			wantReservedForReclaim: map[int]int{
				0: 2,
				1: 2,
			},
		},
		{
			name: "reserved all for reclaim",
			fields: fields{
				numaNum:   2,
				socketNum: 1,
				numCPUs:   16,
				minReclaimedResourceForAllocate: v1.ResourceList{
					v1.ResourceCPU: resource.MustParse("16"),
				},
			},
			wantReservedForReclaim: map[int]int{
				0: 8,
				1: 8,
			},
		},
		{
			name: "reserved for reclaim less than numa num",
			fields: fields{
				numaNum:   8,
				socketNum: 2,
				numCPUs:   16,
				minReclaimedResourceForAllocate: v1.ResourceList{
					v1.ResourceCPU: resource.MustParse("4"),
				},
			},
			// cpusPerCore==2: the per-NUMA reserve is rounded up to a complete
			// physical core, so the historical 1-CPU-per-NUMA split becomes one
			// whole core (2 CPUs) per NUMA under the core-aligned invariant.
			wantReservedForReclaim: map[int]int{
				0: 2,
				1: 2,
				2: 2,
				3: 2,
				4: 2,
				5: 2,
				6: 2,
				7: 2,
			},
		},
		{
			name: "reserved with numa size ratio",
			fields: fields{
				numaNum:   2,
				socketNum: 1,
				numCPUs:   64,
				minReclaimedResourceForAllocate: v1.ResourceList{
					v1.ResourceCPU: resource.MustParse("4"),
				},
				numaMinReclaimedResourceRatioForAllocate: v1.ResourceList{
					v1.ResourceCPU: resource.MustParse("0.05"),
				},
			},
			wantReservedForReclaim: map[int]int{
				0: 2,
				1: 2,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ckDir, err := ioutil.TempDir("", "checkpoint-updateReservedForReclaim")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(ckDir) }()

			sfDir, err := ioutil.TempDir("", "statefile")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(sfDir) }()

			conf := generateTestConfiguration(t, ckDir, sfDir)
			conf.GetDynamicConfiguration().EnableStrategyGroup = true
			conf.GetDynamicConfiguration().MinReclaimedResourceForAllocate = tt.fields.minReclaimedResourceForAllocate
			conf.GetDynamicConfiguration().NumaMinReclaimedResourceRatioForAllocate = tt.fields.numaMinReclaimedResourceRatioForAllocate

			cpuTopology, err := machine.GenerateDummyCPUTopology(tt.fields.numCPUs, tt.fields.socketNum, tt.fields.numaNum)
			assert.NoError(t, err)

			metaServer := &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					KatalystMachineInfo: &machine.KatalystMachineInfo{
						CPUTopology: cpuTopology,
					},
				},
			}

			cra := &cpuResourceAdvisor{
				conf:       conf,
				metaServer: metaServer,
			}
			cra.updateReservedForReclaim(cra.conf.GetDynamicConfiguration())

			assert.Equal(t, tt.wantReservedForReclaim, cra.reservedForReclaim)
		})
	}
}

func TestCPUResourceAdvisorUpdateReservedForReclaimIgnoresHardPartitionRatio(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		ratio             float64
		configuredReserve resource.Quantity
		wantReserved      map[int]int
	}{
		{
			name:              "half ratio keeps steady reserve",
			ratio:             0.5,
			configuredReserve: resource.MustParse("4"),
			wantReserved:      map[int]int{0: 2, 1: 2},
		},
		{
			name:              "fractional ratio keeps steady reserve",
			ratio:             0.5625,
			configuredReserve: resource.MustParse("4"),
			wantReserved:      map[int]int{0: 2, 1: 2},
		},
		{
			name:              "larger configured reserve is statically balanced",
			ratio:             0.25,
			configuredReserve: resource.MustParse("8"),
			wantReserved:      map[int]int{0: 4, 1: 4},
		},
		{
			name:              "odd configured reserve uses real-id helper distribution",
			ratio:             0,
			configuredReserve: resource.MustParse("5"),
			wantReserved:      map[int]int{0: 4, 1: 2},
		},
		{
			name:              "larger ratio does not replace static reserve",
			ratio:             0.75,
			configuredReserve: resource.MustParse("4"),
			wantReserved:      map[int]int{0: 2, 1: 2},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
			dynamicConf := conf.GetDynamicConfiguration()
			dynamicConf.EnableStrategyGroup = true
			dynamicConf.EnableReclaim = true
			dynamicConf.EnableRampUpReclaimHardPartition = true
			dynamicConf.InitialRampUpReclaimCPUSetRatio = tt.ratio
			dynamicConf.MinReclaimedResourceForAllocate = v1.ResourceList{
				v1.ResourceCPU: tt.configuredReserve,
			}
			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
			require.NoError(t, err)

			cra := &cpuResourceAdvisor{
				conf: conf,
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{
							CPUTopology: cpuTopology,
						},
					},
				},
				numaAvailable: map[int]int{0: 4, 1: 12},
			}

			err = cra.updateReservedForReclaim(cra.conf.GetDynamicConfiguration())
			require.NoError(t, err)
			assert.Equal(t, tt.wantReserved, cra.reservedForReclaim)
		})
	}
}

func TestCPUResourceAdvisorUpdateRampUpReclaimRejectsConfiguredFloorAboveCapacity(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
	dynamicConf := conf.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = true
	dynamicConf.InitialRampUpReclaimCPUSetRatio = 0.2
	dynamicConf.MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("17"),
	}
	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	advisor := &cpuResourceAdvisor{
		conf:      conf,
		metaCache: metacache.NewDummyMetaCacheImp(),
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: topology,
				},
			},
		},
	}
	require.NoError(t, advisor.metaCache.AddContainer("pod", "main", &types.ContainerInfo{RampUp: true}))

	require.NoError(t, advisor.updateReservedForReclaim(advisor.conf.GetDynamicConfiguration()))
	err = advisor.updateRampUpReclaimCPUSetCap(advisor.conf.GetDynamicConfiguration(), true, nil, sets.NewInt(0, 1))

	require.ErrorContains(t, err, "configured hard reclaim floor 17 exceeds total core-aligned NUMA capacity 16")
}

func TestCPUResourceAdvisorUpdateRampUpReclaimUsesImmutableNUMACapacity(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
	dynamicConf := conf.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = true
	dynamicConf.InitialRampUpReclaimCPUSetRatio = 0.2
	dynamicConf.MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("4"),
	}

	numaToCPUs := machine.NUMANodeInfo{
		0: machine.NewCPUSet(),
		1: machine.NewCPUSet(),
	}
	cpuDetails := machine.CPUDetails{}
	for cpuID := 0; cpuID < 24; cpuID++ {
		numaToCPUs[0].Add(cpuID)
		cpuDetails[cpuID] = machine.CPUTopoInfo{NUMANodeID: 0}
	}
	for cpuID := 24; cpuID < 56; cpuID++ {
		numaToCPUs[1].Add(cpuID)
		cpuDetails[cpuID] = machine.CPUTopoInfo{NUMANodeID: 1}
	}
	cra := &cpuResourceAdvisor{
		conf:      conf,
		metaCache: metacache.NewDummyMetaCacheImp(),
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: &machine.CPUTopology{
						NumCPUs:      56,
						NumCores:     28,
						NumNUMANodes: 2,
						NUMAToCPUs:   numaToCPUs,
						CPUDetails:   cpuDetails,
					},
				},
			},
		},
		numaAvailable: map[int]int{0: 2, 1: 30},
	}
	require.NoError(t, cra.metaCache.AddContainer("pod", "main", &types.ContainerInfo{RampUp: true}))

	require.NoError(t, cra.updateReservedForReclaim(cra.conf.GetDynamicConfiguration()))
	assert.Equal(t, map[int]int{0: 2, 1: 2}, cra.reservedForReclaim)
	require.NoError(t, cra.updateRampUpReclaimCPUSetCap(cra.conf.GetDynamicConfiguration(), true, nil, sets.NewInt(0, 1)))
	// capacities: NUMA0 24 CPUs (12 cores), NUMA1 32 CPUs (16 cores),
	// cpusPerCore==2, ratio 0.2. donated cores = floor(cores*0.2) complete
	// cores: NUMA0 floor(2.4)=2 cores=4 CPUs, NUMA1 floor(3.2)=3 cores=6
	// CPUs. the configured floor of 4 is already met by the 10-CPU
	// baseline sum, so nothing is lifted. targets follow the immutable topology
	// capacity, not the smaller live numaAvailable, and stay whole-core.
	assert.Equal(t, map[int]int{0: 4, 1: 6}, cra.rampUpReclaimCPUSetCap)
}

func TestCPUResourceAdvisorUpdateReservedForReclaimFallbacks(t *testing.T) {
	t.Parallel()

	t.Run("hard partition single NUMA missing CPU key uses static minimum", func(t *testing.T) {
		t.Parallel()

		conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
		dynamicConf := conf.GetDynamicConfiguration()
		dynamicConf.EnableReclaim = true
		dynamicConf.EnableRampUpReclaimHardPartition = true
		dynamicConf.MinReclaimedResourceForAllocate = v1.ResourceList{}

		cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
		require.NoError(t, err)
		cra := &cpuResourceAdvisor{
			conf: conf,
			metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					KatalystMachineInfo: &machine.KatalystMachineInfo{
						CPUTopology: cpuTopology,
					},
				},
			},
			numaAvailable: map[int]int{0: 8},
		}

		require.NoError(t, cra.updateReservedForReclaim(cra.conf.GetDynamicConfiguration()))
		assert.Equal(t, map[int]int{0: 2}, cra.reservedForReclaim)
	})

	t.Run("hard partition two NUMAs missing CPU key uses static minimum", func(t *testing.T) {
		t.Parallel()

		conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
		dynamicConf := conf.GetDynamicConfiguration()
		dynamicConf.EnableReclaim = true
		dynamicConf.EnableRampUpReclaimHardPartition = true
		dynamicConf.MinReclaimedResourceForAllocate = v1.ResourceList{}

		cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
		require.NoError(t, err)
		cra := &cpuResourceAdvisor{
			conf: conf,
			metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					KatalystMachineInfo: &machine.KatalystMachineInfo{
						CPUTopology: cpuTopology,
					},
				},
			},
			numaAvailable: map[int]int{0: 4, 1: 12},
		}

		require.NoError(t, cra.updateReservedForReclaim(cra.conf.GetDynamicConfiguration()))
		assert.Equal(t, map[int]int{0: 2, 1: 2}, cra.reservedForReclaim)
	})

	t.Run("hard partition is bypassed when reclaim is disabled", func(t *testing.T) {
		t.Parallel()

		conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
		dynamicConf := conf.GetDynamicConfiguration()
		dynamicConf.EnableReclaim = false
		dynamicConf.EnableRampUpReclaimHardPartition = true
		dynamicConf.InitialRampUpReclaimCPUSetRatio = 0.5
		dynamicConf.MinReclaimedResourceForAllocate = v1.ResourceList{
			v1.ResourceCPU: resource.MustParse("6"),
		}
		cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
		require.NoError(t, err)
		cra := &cpuResourceAdvisor{
			conf: conf,
			metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: cpuTopology},
				},
			},
			numaAvailable: map[int]int{0: 4, 1: 12},
		}

		require.NoError(t, cra.updateReservedForReclaim(cra.conf.GetDynamicConfiguration()))
		// The real-ID helper preserves the six-CPU global floor in complete
		// cores, assigning the remainder deterministically to NUMA 0.
		assert.Equal(t, map[int]int{0: 4, 1: 2}, cra.reservedForReclaim)
	})

	t.Run("nil dynamic configuration returns error and clears reservation", func(t *testing.T) {
		t.Parallel()

		conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
		conf.SetDynamicConfiguration(nil)
		cra := &cpuResourceAdvisor{
			conf:               conf,
			reservedForReclaim: map[int]int{0: 4},
		}

		require.EqualError(t, cra.updateReservedForReclaim(nil), "dynamic configuration is nil")
		assert.Nil(t, cra.reservedForReclaim)
	})

	t.Run("non-hard partition keeps configured reservation", func(t *testing.T) {
		t.Parallel()

		conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
		dynamicConf := conf.GetDynamicConfiguration()
		dynamicConf.EnableRampUpReclaimHardPartition = false
		dynamicConf.MinReclaimedResourceForAllocate = v1.ResourceList{
			v1.ResourceCPU: resource.MustParse("6"),
		}
		cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
		require.NoError(t, err)
		cra := &cpuResourceAdvisor{
			conf: conf,
			metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: cpuTopology},
				},
			},
		}

		require.NoError(t, cra.updateReservedForReclaim(cra.conf.GetDynamicConfiguration()))
		// The real-ID helper preserves the six-CPU global floor in complete
		// cores, assigning the remainder deterministically to NUMA 0.
		assert.Equal(t, map[int]int{0: 4, 1: 2}, cra.reservedForReclaim)
	})
}

func TestUpdateRampUpReclaimCPUSetCap(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(96, 1, 2)
	require.NoError(t, err)
	cpusPerCore := topology.CPUsPerCore()
	expectedTarget, err := machine.CalculatePerNUMAHardReclaimTarget(48, 0.25, 0, 0, cpusPerCore)
	require.NoError(t, err)
	require.Zero(t, expectedTarget%cpusPerCore)

	tests := []struct {
		name        string
		enable      bool
		rampDomains sets.Int
		skipNUMAs   sets.Int
		wantCap     map[int]int
	}{
		{
			name:        "disabled feature produces no cap even with active real-NUMA domain",
			enable:      false,
			rampDomains: sets.NewInt(0),
			wantCap:     map[int]int{},
		},
		{
			name:        "enabled without any ramp-up domain produces no cap",
			enable:      true,
			rampDomains: sets.NewInt(),
			wantCap:     map[int]int{},
		},
		{
			name:        "global domain (-1) activates no real NUMA: cross-domain protection",
			enable:      true,
			rampDomains: sets.NewInt(commonstate.FakedNUMAID),
			wantCap:     map[int]int{},
		},
		{
			name:        "ramp-up on NUMA 0 activates only NUMA 0, not NUMA 1",
			enable:      true,
			rampDomains: sets.NewInt(0),
			wantCap:     map[int]int{0: expectedTarget},
		},
		{
			name:        "ramp-up on NUMA 1 activates only NUMA 1, not NUMA 0",
			enable:      true,
			rampDomains: sets.NewInt(1),
			wantCap:     map[int]int{1: expectedTarget},
		},
		{
			name:        "ramp-up on both NUMAs activates both independently",
			enable:      true,
			rampDomains: sets.NewInt(0, 1),
			wantCap:     map[int]int{0: expectedTarget, 1: expectedTarget},
		},
		{
			name:        "steady exclusive NUMA keeps steady reserve while another NUMA ramps up",
			enable:      true,
			rampDomains: sets.NewInt(0, 1),
			skipNUMAs:   sets.NewInt(0),
			wantCap:     map[int]int{1: expectedTarget},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
			dynamicConf := conf.GetDynamicConfiguration()
			dynamicConf.EnableReclaim = true
			dynamicConf.EnableRampUpReclaimHardPartition = tt.enable
			dynamicConf.InitialRampUpReclaimCPUSetRatio = 0.25

			cra := &cpuResourceAdvisor{
				conf:      conf,
				metaCache: metacache.NewDummyMetaCacheImp(),
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{
							CPUTopology: topology,
						},
					},
				},
			}

			require.NoError(t, cra.updateReservedForReclaim(cra.conf.GetDynamicConfiguration()))
			require.NoError(t, cra.updateRampUpReclaimCPUSetCap(
				cra.conf.GetDynamicConfiguration(), tt.enable, tt.skipNUMAs, tt.rampDomains))

			assert.Equal(t, tt.wantCap, cra.rampUpReclaimCPUSetCap)
		})
	}
}

// TestClampDescriptorDesired pins the invariant documented on
// clampDescriptorDesired: the ramp-up desired always lands inside
// [floor, steadyCap]. A zero steadyCap means "no steady upper bound known yet",
// so it must NOT cap (otherwise a not-yet-derived steady cap would silently
// shrink a legitimate target), while the pure floor always applies.
func TestClampDescriptorDesired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		desired   int
		steadyCap int
		floor     int
		want      int
	}{
		{
			name:      "desired above steady cap is clamped down to steady cap",
			desired:   80,
			steadyCap: 50,
			floor:     5,
			want:      50,
		},
		{
			name:      "desired below floor is raised up to floor",
			desired:   1,
			steadyCap: 50,
			floor:     5,
			want:      5,
		},
		{
			name:      "desired inside bounds passes through",
			desired:   20,
			steadyCap: 50,
			floor:     5,
			want:      20,
		},
		{
			name:      "zero steady cap does not cap (unknown upper bound)",
			desired:   80,
			steadyCap: 0,
			floor:     5,
			want:      80,
		},
		{
			name:      "zero steady cap still floors the value",
			desired:   0,
			steadyCap: 0,
			floor:     5,
			want:      5,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := clampDescriptorDesired(tt.desired, tt.steadyCap, tt.floor)
			assert.Equal(t, tt.want, got)
		})
	}
}
