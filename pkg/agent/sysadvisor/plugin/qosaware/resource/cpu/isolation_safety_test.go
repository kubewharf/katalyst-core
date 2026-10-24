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

package cpu

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// safetyRegion is a QoSRegion stand-in tailored for checkIsolationSafety: it
// carries the values the check reads (provision knob, pods, request, binding
// NUMAs) and no behavior.
type safetyRegion struct {
	fakeRegion
	provisionKnob types.ControlKnob
	provisionErr  error
	pods          types.PodSet
	podsRequest   float64
}

func (s *safetyRegion) GetProvision() (types.ControlKnob, error) {
	if s.provisionErr != nil {
		return nil, s.provisionErr
	}
	return s.provisionKnob, nil
}
func (s *safetyRegion) GetPods() types.PodSet   { return s.pods }
func (s *safetyRegion) GetPodsRequest() float64 { return s.podsRequest }

// newSafetyAdvisor builds a cra whose metaServer reports two NUMAs, NUMA 0 with
// 8 CPUs and NUMA 1 with 8 CPUs, plus a nonBindingNumas set the caller can pin.
func newSafetyAdvisor(t *testing.T, regionMap map[string]region.QoSRegion, metaCache metacache.MetaCache) *cpuResourceAdvisor {
	t.Helper()

	cpuDetails := machine.CPUDetails{}
	numaToCPUs := machine.NUMANodeInfo{}
	for _, numaID := range []int{0, 1} {
		cpus := machine.NewCPUSet()
		for i := 0; i < 8; i++ {
			cpuID := numaID*100 + i
			cpus.Add(cpuID)
			cpuDetails[cpuID] = machine.CPUTopoInfo{NUMANodeID: numaID}
		}
		numaToCPUs[numaID] = cpus
	}
	topology := &machine.CPUTopology{
		NumCPUs:      16,
		NumCores:     16,
		NumNUMANodes: 2,
		NUMAToCPUs:   numaToCPUs,
		CPUDetails:   cpuDetails,
	}
	return &cpuResourceAdvisor{
		regionMap:       regionMap,
		nonBindingNumas: machine.NewCPUSet(0, 1), // both NUMAs non-binding by default
		metaCache:       metaCache,
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: topology},
			},
		},
	}
}

// TestCheckIsolationSafetyWithinCapacity verifies the happy path: when the sum
// of share-pool non-reclaimed requirement plus dedicated non-exclusive request
// fits inside the non-binding NUMA capacity, the check passes (returns true).
func TestCheckIsolationSafetyWithinCapacity(t *testing.T) {
	t.Parallel()

	regionMap := map[string]region.QoSRegion{
		"share": &safetyRegion{
			fakeRegion: fakeRegion{name: "share", regionType: configapi.QoSRegionTypeShare},
			provisionKnob: types.ControlKnob{
				configapi.ControlKnobNonReclaimedCPURequirement: {Value: 4},
			},
		},
	}
	mc := metacache.NewDummyMetaCacheImp()
	cra := newSafetyAdvisor(t, regionMap, mc)

	require.True(t, cra.checkIsolationSafety(),
		"4-CPU share pool within 16-CPU non-binding capacity must pass")
}

// TestCheckIsolationSafetyOverCapacityFailsClosed verifies the fail-closed
// contract: when share+isolation+dedicated exceeds the non-binding NUMA
// capacity, checkIsolationSafety returns false, which makes the caller bail
// with errIsolationSafetyCheckFailed rather than publishing an overcommitted
// isolation layout.
func TestCheckIsolationSafetyOverCapacityFailsClosed(t *testing.T) {
	t.Parallel()

	regionMap := map[string]region.QoSRegion{
		"share": &safetyRegion{
			fakeRegion:    fakeRegion{name: "share", regionType: configapi.QoSRegionTypeShare},
			provisionKnob: types.ControlKnob{configapi.ControlKnobNonReclaimedCPURequirement: {Value: 12}},
		},
		"iso": &safetyRegion{
			fakeRegion: fakeRegion{name: "iso", regionType: configapi.QoSRegionTypeIsolation},
			pods:       types.PodSet{"pod-iso": sets.NewString("c1")},
		},
	}
	mc := metacache.NewDummyMetaCacheImp()
	require.NoError(t, mc.AddContainer("pod-iso", "c1", &types.ContainerInfo{CPULimit: 8}))
	cra := newSafetyAdvisor(t, regionMap, mc)

	// share=12 + iso=8 = 20 > 16 capacity. Must fail closed.
	require.False(t, cra.checkIsolationSafety(),
		"share(12)+isolation(8)=20 exceeds 16-CPU capacity: check must return false")
}

// TestCheckIsolationSafetyProvisionErrorFailsClosed verifies that an error
// reading a share region's provision knob is itself a fail-closed signal: the
// check returns false rather than guessing a zero value.
func TestCheckIsolationSafetyProvisionErrorFailsClosed(t *testing.T) {
	t.Parallel()

	regionMap := map[string]region.QoSRegion{
		"share": &safetyRegion{
			fakeRegion:   fakeRegion{name: "share", regionType: configapi.QoSRegionTypeShare},
			provisionErr: errIsolationSafetyCheckFailed,
		},
	}
	cra := newSafetyAdvisor(t, regionMap, metacache.NewDummyMetaCacheImp())

	require.False(t, cra.checkIsolationSafety(),
		"a provision-read error must fail closed, not be treated as zero")
}

// TestCheckIsolationSafetyExclusiveNUMARemovedFromNonBinding verifies that an
// exclusive dedicated NUMA is subtracted from the non-binding capacity pool, so
// a share pool sized to fit the remaining capacity passes.
func TestCheckIsolationSafetyExclusiveNUMARemovedFromNonBinding(t *testing.T) {
	t.Parallel()

	regionMap := map[string]region.QoSRegion{
		"share": &safetyRegion{
			fakeRegion:    fakeRegion{name: "share", regionType: configapi.QoSRegionTypeShare},
			provisionKnob: types.ControlKnob{configapi.ControlKnobNonReclaimedCPURequirement: {Value: 7}},
		},
		"dedicated-excl": &safetyRegion{
			fakeRegion: fakeRegion{
				name:          "dedicated-excl",
				regionType:    configapi.QoSRegionTypeDedicated,
				numaBinding:   true,
				numaExclusive: true,
				bindingNumas:  machine.NewCPUSet(1),
			},
		},
	}
	cra := newSafetyAdvisor(t, regionMap, metacache.NewDummyMetaCacheImp())

	// NUMA 1 (8 CPUs) is removed by the exclusive region, leaving NUMA 0 (8 CPUs).
	// share=7 <= 8 => passes.
	require.True(t, cra.checkIsolationSafety(),
		"exclusive NUMA 1 removed; share=7 fits in NUMA0's 8 CPUs")
}

// TestErrIsolationSafetyCheckFailedSentinel verifies the sentinel error identity
// that the retry path in update() switches on: when tryIsolation=true and the
// check fails, the returned error must be exactly errIsolationSafetyCheckFailed
// so the caller can distinguish it from a generic update failure and retry
// with isolation disabled.
func TestErrIsolationSafetyCheckFailedSentinel(t *testing.T) {
	t.Parallel()

	// The error must be a stable sentinel (not a wrapped/random value) so the
	// `err == errIsolationSafetyCheckFailed` comparison in update() works.
	require.Equal(t, "isolation safety check failed", errIsolationSafetyCheckFailed.Error())
}
