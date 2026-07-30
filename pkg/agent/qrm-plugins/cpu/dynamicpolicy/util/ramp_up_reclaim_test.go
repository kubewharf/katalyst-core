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

package util

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalculateRampUpReclaimTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ratio     float64
		eligible  int
		reserve   int
		cap       int
		exclusive bool
		want      int
		wantErr   string
	}{
		{
			name:     "reserve wins",
			ratio:    0.1,
			eligible: 20,
			reserve:  4,
			cap:      10,
			want:     4,
		},
		{
			name:     "ratio wins after rounding down to even",
			ratio:    0.26,
			eligible: 20,
			reserve:  1,
			cap:      10,
			want:     4,
		},
		{
			name:     "ratio point two rounds nineteen point two down to eighteen",
			ratio:    0.2,
			eligible: 96,
			reserve:  4,
			cap:      95,
			want:     18,
		},
		{
			name:     "zero ratio uses reserve",
			ratio:    0,
			eligible: 20,
			reserve:  2,
			cap:      10,
			want:     2,
		},
		{
			name:     "target above cap rejects",
			ratio:    0.8,
			eligible: 20,
			reserve:  1,
			cap:      10,
			wantErr:  "bootstrap target exceeds reclaim cap",
		},
		{
			name:      "exclusive remainder empty rejects",
			ratio:     1,
			eligible:  20,
			reserve:   1,
			cap:       20,
			exclusive: true,
			wantErr:   "exclusive ramp-up requires non-empty dedicated remainder",
		},
		{
			name:     "empty target rejects",
			ratio:    0,
			eligible: 20,
			reserve:  0,
			cap:      10,
			wantErr:  "bootstrap target must be positive",
		},
		{
			name:     "invalid ratio rejects",
			ratio:    1.1,
			eligible: 20,
			reserve:  1,
			cap:      10,
			wantErr:  "initial ramp-up reclaim ratio must be in [0,1]",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CalculateRampUpReclaimTarget(tt.eligible, tt.reserve, tt.cap, tt.ratio, tt.exclusive)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
