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
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalculateExclusiveDisjointTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		in            exclusivePartitionInput
		wantDedicated int
		wantReclaim   int
		wantErr       bool
	}{
		{
			name: "reclaim enabled uses non reclaimed requirement",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 16,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      10,
				EnableReclaim:     true,
			},
			wantDedicated: 10,
			wantReclaim:   6,
		},
		{
			name: "reclaim disabled keeps reserve only",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 16,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      10,
			},
			wantDedicated: 12,
			wantReclaim:   4,
		},
		{
			name: "non reclaimed below zero is clamped",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 16,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      -1,
				EnableReclaim:     true,
			},
			wantErr: true,
		},
		{
			name: "non reclaimed above dedicated eligibility is clamped",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 12,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      20,
				EnableReclaim:     true,
			},
			wantDedicated: 12,
			wantReclaim:   4,
		},
		{
			name: "dedicated eligibility raises reclaim lower bound",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 8,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      8,
				EnableReclaim:     true,
			},
			wantDedicated: 8,
			wantReclaim:   8,
		},
		{
			name: "ratio physical cap limits reclaim target",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 16,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      10,
				EnableReclaim:     true,
				RatioPhysicalCap:  4,
			},
			wantDedicated: 12,
			wantReclaim:   4,
		},
		{
			name: "zero ratio physical cap is unlimited",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 16,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      3,
				EnableReclaim:     true,
			},
			wantDedicated: 3,
			wantReclaim:   13,
		},
		{
			name: "negative partition capacity is rejected",
			in: exclusivePartitionInput{
				PartitionCapacity: -1,
				DedicatedCapacity: 16,
				ReclaimCapacity:   16,
				NonReclaimed:      16,
				EnableReclaim:     true,
			},
			wantErr: true,
		},
		{
			name: "negative dedicated capacity is rejected",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: -1,
				ReclaimCapacity:   16,
				NonReclaimed:      16,
				EnableReclaim:     true,
			},
			wantErr: true,
		},
		{
			name: "negative reclaim capacity is rejected",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 16,
				ReclaimCapacity:   -1,
				NonReclaimed:      16,
				EnableReclaim:     true,
			},
			wantErr: true,
		},
		{
			name: "reserve is bounded by reclaim eligibility",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 16,
				ReclaimCapacity:   2,
				Reserved:          4,
			},
			wantDedicated: 14,
			wantReclaim:   2,
		},
		{
			name: "eligibility lower bound exceeds ratio cap",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 8,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      10,
				EnableReclaim:     true,
				RatioPhysicalCap:  4,
			},
			wantErr: true,
		},
		{
			name: "reclaim target cannot consume whole partition",
			in: exclusivePartitionInput{
				PartitionCapacity: 16,
				DedicatedCapacity: 16,
				ReclaimCapacity:   16,
				Reserved:          4,
				NonReclaimed:      0,
				EnableReclaim:     true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dedicated, reclaim, err := calculateExclusiveDisjointTargets(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, strings.ToLower(err.Error()), err.Error())
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantDedicated, dedicated)
			require.Equal(t, tt.wantReclaim, reclaim)
		})
	}
}

func TestCalculateReclaimQuotaLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		physicalTarget int
		quotaKnob      float64
		ratioCap       int
		want           float64
	}{
		{name: "missing quota is unlimited", physicalTarget: 6, quotaKnob: -1, want: -1},
		{name: "zero quota does not shrink physical target", physicalTarget: 6, quotaKnob: 0, want: 0},
		{name: "quota below reserve is independent", physicalTarget: 6, quotaKnob: 2, want: 2},
		{name: "fractional quota is retained", physicalTarget: 6, quotaKnob: 2.5, want: 2.5},
		{name: "quota above target is capped", physicalTarget: 6, quotaKnob: 8, want: 6},
		{name: "ratio cap also caps quota", physicalTarget: 8, quotaKnob: 7, ratioCap: 4, want: 4},
		{name: "negative physical target caps quota at zero", physicalTarget: -1, quotaKnob: 2, want: 0},
		{name: "NaN quota is unlimited", physicalTarget: 6, quotaKnob: math.NaN(), want: -1},
		{name: "negative infinity quota is unlimited", physicalTarget: 6, quotaKnob: math.Inf(-1), want: -1},
		{name: "positive infinity quota is capped by physical target", physicalTarget: 6, quotaKnob: math.Inf(1), want: 6},
		{name: "positive infinity quota is also capped by ratio", physicalTarget: 8, quotaKnob: math.Inf(1), ratioCap: 4, want: 4},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := calculateReclaimQuotaLimit(tt.physicalTarget, tt.quotaKnob, tt.ratioCap)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDesiredDedicatedPhysical(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		rawRequest     int
		rawRequirement int
		enableReclaim  bool
		disableOverlap bool
		want           int
	}{
		{
			name:           "disjoint reclaim enabled uses requirement",
			rawRequest:     16,
			rawRequirement: 10,
			enableReclaim:  true,
			disableOverlap: true,
			want:           10,
		},
		{
			name:           "disjoint reclaim disabled keeps request",
			rawRequest:     16,
			rawRequirement: 10,
			disableOverlap: true,
			want:           16,
		},
		{
			name:           "legacy overlap keeps request",
			rawRequest:     16,
			rawRequirement: 10,
			enableReclaim:  true,
			want:           16,
		},
		{
			name:           "requirement above request is capped",
			rawRequest:     8,
			rawRequirement: 10,
			enableReclaim:  true,
			disableOverlap: true,
			want:           8,
		},
		{
			name:           "negative requirement is clamped",
			rawRequest:     8,
			rawRequirement: -1,
			enableReclaim:  true,
			disableOverlap: true,
			want:           0,
		},
		{
			name:           "negative request is clamped",
			rawRequest:     -1,
			rawRequirement: 8,
			enableReclaim:  true,
			disableOverlap: true,
			want:           0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := desiredDedicatedPhysical(
				tt.rawRequest,
				tt.rawRequirement,
				tt.enableReclaim,
				tt.disableOverlap,
			)
			require.Equal(t, tt.want, got)
		})
	}
}
