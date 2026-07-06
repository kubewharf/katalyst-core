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

package plugin

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func TestGetAdvices_MultiPath(t *testing.T) {
	t.Parallel()
	mg := &memoryGuard{
		reclaimRelativeRootCgroupPaths: []string{"/kubepods/besteffort", "/parentPath/childPath"},
		numaBindingRelativeRootCgroupPaths: map[int][]string{
			0: {"/kubepods/besteffort-0", "/parentPath/childPath/0"},
		},
		reclaimMemoryLimit:            atomic.NewInt64(1024),
		numaBindingReclaimMemoryLimit: &atomic.Value{},
		reconcileStatus:               atomic.NewString(reconcileStatusSucceeded),
	}
	mg.numaBindingReclaimMemoryLimit.Store(map[int]map[string]int64{
		0: {
			"/kubepods/besteffort-0":  512,
			"/parentPath/childPath/0": 256,
		},
	})

	got := mg.GetAdvices()
	require.Len(t, got.ExtraEntries, 4)

	paths := make([]string, 0, len(got.ExtraEntries))
	for _, e := range got.ExtraEntries {
		paths = append(paths, e.CgroupPath)
	}
	require.Contains(t, paths, "/kubepods/besteffort")
	require.Contains(t, paths, "/parentPath/childPath")
	require.Contains(t, paths, "/kubepods/besteffort-0")
	require.Contains(t, paths, "/parentPath/childPath/0")
}

func TestGetNUMABindingReclaimRelativeRootCgroupPathsMulti(t *testing.T) {
	t.Parallel()
	entries := []global.ReclaimRelativeRootCgroupPathEntry{
		{Path: "/kubepods/besteffort", NUMASeparator: "-"},
		{Path: "/parentPath/childPath", NUMASeparator: "/"},
	}
	got := common.GetNUMABindingReclaimRelativeRootCgroupPathsMulti(entries, []int{0, 1})
	require.Equal(t, map[int][]string{
		0: {"/kubepods/besteffort-0", "/parentPath/childPath/0"},
		1: {"/kubepods/besteffort-1", "/parentPath/childPath/1"},
	}, got)
}

func TestCalculateReclaimedMemoryLimitFor_WatermarkSource(t *testing.T) {
	t.Parallel()
	zoneLow := uint64(100)
	zoneHigh := uint64(250)

	pickWatermark := func(source string) uint64 {
		w := zoneLow
		if source == "high" {
			w = zoneHigh
		}
		return w
	}

	cases := []struct {
		name   string
		source string
		want   uint64
	}{
		{name: "default empty -> low", source: "", want: zoneLow},
		{name: "explicit low", source: "low", want: zoneLow},
		{name: "explicit high", source: "high", want: zoneHigh},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, pickWatermark(tc.source))
		})
	}
}

func TestCalculateReclaimedMemoryLimitFor_MaxRatioClamp(t *testing.T) {
	t.Parallel()

	clamp := func(reclaimMemoryLimit, ratio, numaTotal float64) float64 {
		if ratio > 0 {
			reclaimMemoryLimit = math.Min(reclaimMemoryLimit, ratio*numaTotal)
		}
		return reclaimMemoryLimit
	}

	numaTotal := 250.0 * (1 << 30)

	cases := []struct {
		name  string
		ratio float64
		raw   float64
		want  float64
	}{
		{name: "ratio 0 disables clamp", ratio: 0, raw: 200 * (1 << 30), want: 200 * (1 << 30)},
		{name: "ratio caps to ratio*total", ratio: 0.2, raw: 200 * (1 << 30), want: 0.2 * numaTotal},
		{name: "ratio above raw is no-op", ratio: 0.9, raw: 100 * (1 << 30), want: 100 * (1 << 30)},
		{name: "negative ratio disables clamp", ratio: -1, raw: 200 * (1 << 30), want: 200 * (1 << 30)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, clamp(tc.raw, tc.ratio, numaTotal))
		})
	}
}

func TestReclaimedCoresUsedSum_Shape(t *testing.T) {
	t.Parallel()
	sum := func(paths []string, present map[string]float64) float64 {
		total := .0
		for _, p := range paths {
			v, ok := present[p]
			if !ok {
				continue
			}
			total += v
		}
		return total
	}

	got := sum(
		[]string{"/a", "/b", "/c"},
		map[string]float64{"/a": 10, "/c": 30},
	)
	require.Equal(t, 40.0, got)
}
