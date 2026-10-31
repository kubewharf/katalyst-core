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

package provisionassembler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestRegionInfoMergeMinReclaimedCoresCPUQuotaSentinel pins the -1 sentinel
// merge contract for minReclaimedCoresCPUQuota. -1 means "no reclaim quota
// set"; merging another region must never reset an already-accumulated real
// quota back to the sentinel, and the minimum of two real quotas must win.
func TestRegionInfoMergeMinReclaimedCoresCPUQuotaSentinel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// start is the quota on the receiver (r) before merge; -1 means sentinel.
		start float64
		// other is the quota merged in; -1 means sentinel.
		other float64
		// want is the expected quota on r after merge.
		want float64
	}{
		{
			name:  "both_sentinel_stays_sentinel",
			start: -1, other: -1, want: -1,
		},
		{
			name:  "receiver_sentinel_adopts_other_real",
			start: -1, other: 3, want: 3,
		},
		{
			// regression: a sentinel other must NOT overwrite a real quota.
			name:  "receiver_real_other_sentinel_keeps_real",
			start: 5, other: -1, want: 5,
		},
		{
			name:  "both_real_takes_min",
			start: 5, other: 3, want: 3,
		},
		{
			name:  "both_real_equal",
			start: 4, other: 4, want: 4,
		},
		{
			name:  "other_larger_real_keeps_receiver",
			start: 3, other: 7, want: 3,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := initRegionInfo()
			r.minReclaimedCoresCPUQuota = tt.start
			o := initRegionInfo()
			o.minReclaimedCoresCPUQuota = tt.other

			r.merge(o)

			require.Equal(t, tt.want, r.minReclaimedCoresCPUQuota,
				"merged minReclaimedCoresCPUQuota mismatch: start=%v other=%v", tt.start, tt.other)
		})
	}
}

func TestExtractShareRegionInfoSkipsZeroRequestWithoutPoolOwnership(t *testing.T) {
	t.Parallel()

	newShare := func(owner string, request float64, numaBinding bool) *FakeRegion {
		r := NewFakeRegion(owner, configapi.QoSRegionTypeShare, owner)
		r.podsRequest = request
		r.SetIsNumaBinding(numaBinding)
		r.SetProvision(types.ControlKnob{
			configapi.ControlKnobNonReclaimedCPURequirement: {Value: 1},
		})
		return r
	}

	tests := []struct {
		name          string
		owner         string
		request       float64
		numaBinding   bool
		poolCPUs      machine.CPUSet
		containerCPUs machine.CPUSet
		originalCPUs  machine.CPUSet
		wantActive    bool
	}{
		{name: "zero request without pool ownership", owner: commonstate.PoolNameShare, wantActive: false},
		{name: "positive request remains active", owner: commonstate.PoolNameShare, request: 1, wantActive: true},
		{name: "zero request with current pool ownership remains active", owner: commonstate.PoolNameShare, poolCPUs: machine.NewCPUSet(0, 1), wantActive: true},
		{name: "zero request with container ownership remains active", owner: commonstate.PoolNameShare, containerCPUs: machine.NewCPUSet(2, 3), wantActive: true},
		{name: "zero request with original container ownership remains active", owner: commonstate.PoolNameShare, originalCPUs: machine.NewCPUSet(4, 5), wantActive: true},
		{name: "named pool remains active", owner: "named-share", wantActive: true},
		{name: "numa binding pool remains active", owner: commonstate.PoolNameShare, numaBinding: true, wantActive: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := metacache.NewDummyMetaCacheImp()
			share := newShare(tt.owner, tt.request, tt.numaBinding)
			if !tt.containerCPUs.IsEmpty() || !tt.originalCPUs.IsEmpty() {
				share.SetPods(types.PodSet{"pod": sets.NewString("main")})
				require.NoError(t, reader.SetContainerInfo("pod", "main", &types.ContainerInfo{
					PodUID:                           "pod",
					ContainerName:                    "main",
					OwnerPoolName:                    tt.owner,
					TopologyAwareAssignments:         types.TopologyAwareAssignment{0: tt.containerCPUs},
					OriginalTopologyAwareAssignments: types.TopologyAwareAssignment{0: tt.originalCPUs},
				}))
			}
			if !tt.poolCPUs.IsEmpty() {
				require.NoError(t, reader.SetPoolInfo(tt.owner, &types.PoolInfo{
					PoolName:                 tt.owner,
					TopologyAwareAssignments: map[int]machine.CPUSet{0: tt.poolCPUs},
				}))
			}
			unpinned, pinned, err := extractShareRegionInfo(
				[]region.QoSRegion{share}, nil, sets.NewString(), reader)
			require.NoError(t, err)
			require.Empty(t, pinned)
			if tt.wantActive {
				require.Contains(t, unpinned.regionMap, tt.owner)
				require.Greater(t, unpinned.requests[tt.owner], 0)
			} else {
				require.NotContains(t, unpinned.regionMap, tt.owner)
				require.NotContains(t, unpinned.requests, tt.owner)
			}
		})
	}
}
