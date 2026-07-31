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

package planner

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPendingAdviceSnapshotValidate(t *testing.T) {
	t.Parallel()

	snapshot := PendingAdviceSnapshot{
		Token:                 3,
		InMemoryRevision:      8,
		NormalizedRequestHash: 99,
	}

	tests := []struct {
		name        string
		current     AdviceFreshness
		wantErrText string
	}{
		{
			name: "rejects stale token",
			current: AdviceFreshness{
				Token:                 4,
				InMemoryRevision:      8,
				NormalizedRequestHash: 99,
			},
			wantErrText: "advice freshness token mismatch",
		},
		{
			name: "rejects stale in-memory revision",
			current: AdviceFreshness{
				Token:                 3,
				InMemoryRevision:      9,
				NormalizedRequestHash: 99,
			},
			wantErrText: "advice freshness in-memory revision mismatch",
		},
		{
			name: "rejects changed normalized request hash",
			current: AdviceFreshness{
				Token:                 3,
				InMemoryRevision:      8,
				NormalizedRequestHash: 100,
			},
			wantErrText: "advice freshness normalized request hash mismatch",
		},
		{
			name: "accepts matching freshness",
			current: AdviceFreshness{
				Token:                 3,
				InMemoryRevision:      8,
				NormalizedRequestHash: 99,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := snapshot.Validate(tt.current)

			if tt.wantErrText != "" {
				require.ErrorContains(t, err, tt.wantErrText)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestPendingAdviceSnapshotValidateAcceptsZeroValues(t *testing.T) {
	t.Parallel()

	err := PendingAdviceSnapshot{}.Validate(AdviceFreshness{})

	require.NoError(t, err)
}
