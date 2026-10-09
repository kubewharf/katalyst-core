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

package provisionassembler

import (
	"testing"

	"github.com/stretchr/testify/require"
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
