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

package monitor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroupMBStatsKeepsPhysicalCLOSKeysDistinct feeds per-CCD mon-group readings
// through the production aggregation seam (putGroupCCDMBToDomain/putCCDMBStat)
// and asserts that distinct group keys are kept separate instead of being
// collapsed onto one bucket. The earlier version built the GroupMBStats literal
// directly, so it passed regardless of how the production put path routed keys.
func TestGroupMBStatsKeepsPhysicalCLOSKeysDistinct(t *testing.T) {
	t.Parallel()

	domainGroupStats := map[int]GroupMBStats{}
	putGroupCCDMBToDomain(domainGroupStats, 0, "shared-50", 0, MBInfo{TotalMB: 100})
	putGroupCCDMBToDomain(domainGroupStats, 0, "share-50", 0, MBInfo{TotalMB: 200})

	stats := domainGroupStats[0]
	require.Len(t, stats, 2)
	require.Contains(t, stats, "shared-50")
	require.Contains(t, stats, "share-50")
	require.Equal(t, 100, stats["shared-50"].SumStat().TotalMB)
	require.Equal(t, 200, stats["share-50"].SumStat().TotalMB)
}
