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

package resctrl

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

func TestSharedSubgroupClosID(t *testing.T) {
	t.Parallel()

	require.Equal(t, "share", SharedSubgroupClosID(-1))
	require.Equal(t, "share-00", SharedSubgroupClosID(0))
	require.Equal(t, "share-01", SharedSubgroupClosID(1))
	require.Equal(t, "share-12", SharedSubgroupClosID(12))
	require.Equal(t, "share-50", SharedSubgroupClosID(50))
	require.Equal(t, "share-01", NormalizeClosID("shared-01"))
	require.Equal(t, "share-01", NormalizeClosID("share-01"))
	require.Equal(t, "shared-50", NormalizeClosID("shared-50"))
	require.Equal(t, "share-50", NormalizeClosID("share-50"))
}

func TestResolvePoolClosID(t *testing.T) {
	t.Parallel()

	conf := &qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"share": 30, "batch": 3},
		DefaultClosIDs:             []string{"legacy"},
	}

	tests := []struct {
		name string
		meta ClosAssignmentMeta
		want string
	}{
		{
			name: "mapped shared pool",
			meta: ClosAssignmentMeta{QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores, OwnerPool: "batch"},
			want: "share-03",
		},
		{
			name: "default shared pool",
			meta: ClosAssignmentMeta{QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores, OwnerPool: "share"},
			want: "shared-50",
		},
		{
			name: "unmapped shared pool",
			meta: ClosAssignmentMeta{QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores, OwnerPool: "unmapped"},
			want: "shared-50",
		},
		{
			name: "dedicated qos",
			meta: ClosAssignmentMeta{QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores, OwnerPool: "dedicated"},
			want: consts.ResctrlGroupDedicated,
		},
		{
			name: "reclaimed qos",
			meta: ClosAssignmentMeta{QoSLevel: apiconsts.PodAnnotationQoSLevelReclaimedCores, OwnerPool: "reclaim"},
			want: consts.ResctrlGroupReclaim,
		},
		{
			name: "system qos",
			meta: ClosAssignmentMeta{QoSLevel: apiconsts.PodAnnotationQoSLevelSystemCores, OwnerPool: "system-a"},
			want: consts.ResctrlGroupSystem,
		},
		{
			name: "unknown qos falls back to owner pool",
			meta: ClosAssignmentMeta{QoSLevel: "custom", OwnerPool: "custom-clos"},
			want: "custom-clos",
		},
		{
			name: "obsolete shared clos is normalized",
			meta: ClosAssignmentMeta{QoSLevel: "custom", OwnerPool: "shared-07"},
			want: "share-07",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolvePoolClosID(tc.meta, conf)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestResolveSharedPoolClosIDIgnoresInvalidExplicitMappings(t *testing.T) {
	t.Parallel()

	conf := &qrmresctrl.ResctrlConfig{CPUSetPoolToSharedSubgroup: map[string]int{
		"dedicated":  1,
		"shared-50":  2,
		"share-50":   3,
		"shared-30":  4,
		"negative":   -1,
		"valid-pool": 5,
	}}

	for _, poolName := range []string{"dedicated", "shared-50", "share-50", "shared-30", "negative"} {
		require.Equal(t, "shared-50", ResolveSharedPoolClosID(poolName, conf), poolName)
	}
	require.Equal(t, "share-05", ResolveSharedPoolClosID("valid-pool", conf))
}

func TestResolvePoolClosIDRejectsEmptyOwnerPoolForUnknownQoS(t *testing.T) {
	t.Parallel()

	_, err := ResolvePoolClosID(ClosAssignmentMeta{QoSLevel: "custom"}, nil)
	require.ErrorContains(t, err, "empty owner pool")
}

func TestBuildExpectedClosPools(t *testing.T) {
	t.Parallel()

	got, err := BuildExpectedClosPools([]ClosAssignmentMeta{
		{QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores, OwnerPool: "share"},
		{QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores, OwnerPool: "batch-a"},
		{QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores, OwnerPool: "batch-b"},
		{QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores, OwnerPool: "unmapped"},
		{QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores, OwnerPool: "dedicated"},
		{QoSLevel: apiconsts.PodAnnotationQoSLevelReclaimedCores, OwnerPool: "reclaim"},
	}, &qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"batch-a": 3, "batch-b": 3},
	})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{
		"shared-50": {"share", "unmapped"},
		"share-03":  {"batch-a", "batch-b"},
		"dedicated": {"dedicated"},
		"reclaim":   {"reclaim"},
	}, got)
}

func TestResolveCATWayKey(t *testing.T) {
	t.Parallel()

	conf := &qrmresctrl.ResctrlConfig{CPUSetPoolToSharedSubgroup: map[string]int{
		"share":       30,
		"batch":       3,
		"shared-50":   31,
		"share-50":    32,
		"share-NUMA0": 4,
		"negative":    -1,
	}}
	require.Equal(t, "shared-50", ResolveCATWayKey("share", conf))
	require.Equal(t, "shared-50", ResolveCATWayKey("shared-50", conf))
	require.Equal(t, "share-50", ResolveCATWayKey("share-50", conf))
	require.Equal(t, "share-03", ResolveCATWayKey("batch", conf))
	require.Equal(t, "share-03", ResolveCATWayKey("shared-03", conf))
	require.Equal(t, "share-04", ResolveCATWayKey("share-NUMA0", conf))
	require.Equal(t, "negative", ResolveCATWayKey("negative", conf))
	require.Equal(t, "dedicated", ResolveCATWayKey("dedicated", conf))
}

func TestIsManagedClosID(t *testing.T) {
	t.Parallel()

	conf := &qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"share": 30, "batch": 3, "negative": -1},
		DefaultClosIDs:             []string{"custom-default"},
	}

	for _, closID := range []string{"dedicated", "reclaim", "system", "shared-50", "share-03", "custom-default"} {
		require.True(t, IsManagedClosID(closID, conf), closID)
	}
	require.False(t, IsManagedClosID("share", conf))
	require.False(t, IsManagedClosID("share-30", conf))
	require.False(t, IsCPUListManagedClosID("share", conf))
	require.False(t, IsCPUListManagedClosID("share-30", conf))
	require.True(t, IsCPUListManagedClosID("shared-50", conf))
	require.True(t, IsCPUListManagedClosID("share-03", conf))
	require.False(t, IsManagedClosID("external", conf))
	require.False(t, IsManagedClosID("foreign", conf))
}
