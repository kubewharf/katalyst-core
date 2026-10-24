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

package regulator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCPURegulatorRoundAlignsToWholeCore pins the whole-core alignment
// invariant of round(). when HT alignment is on, the rounded requirement must
// be a whole-core multiple of CPUsPerCore() so a latency-critical share pool
// never owns a partial physical core (which would force it to share a core
// with best-effort/reclaim pods).
//
// The legacy round() hard-codes "+1 when odd", i.e. it assumes CPUsPerCore()==2.
// On smt4 hardware (CPUsPerCore()==4) a requirement of 5 rounds to 6, which is
// still not a 4-multiple: cores are sliced in half. This case is RED against
// the %2 implementation and turns GREEN once round() aligns up to CPUsPerCore().
func TestCPURegulatorRoundAlignsToWholeCore(t *testing.T) {
	t.Parallel()

	newRegulator := func(cpusPerCore int) *CPURegulator {
		return &CPURegulator{
			RegulatorOptions: RegulatorOptions{
				NeedHTAligned: func() bool { return true },
				CPUsPerCore:   func() int { return cpusPerCore },
			},
		}
	}

	testCases := []struct {
		name        string
		cpusPerCore int
		requirement float64
		expected    int
	}{
		{name: "smt2 even stays", cpusPerCore: 2, requirement: 4, expected: 4},
		{name: "smt2 odd rounds up", cpusPerCore: 2, requirement: 5, expected: 6},
		{name: "smt2 fractional rounds up to even", cpusPerCore: 2, requirement: 3.2, expected: 4},
		{name: "smt4 aligns up to core", cpusPerCore: 4, requirement: 5, expected: 8},
		{name: "smt4 exact core stays", cpusPerCore: 4, requirement: 8, expected: 8},
		{name: "smt4 just over core", cpusPerCore: 4, requirement: 4.1, expected: 8},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newRegulator(tc.cpusPerCore)
			got := c.round(tc.requirement)
			require.Equalf(t, tc.expected, got,
				"round(%v) with CPUsPerCore=%d must align to a whole core", tc.requirement, tc.cpusPerCore)
		})
	}
}

// TestCPURegulatorRoundWithoutHTAlignment verifies the non-HT-aligned path is
// untouched: round() just ceils the requirement, no core alignment applied.
func TestCPURegulatorRoundWithoutHTAlignment(t *testing.T) {
	t.Parallel()

	c := &CPURegulator{
		RegulatorOptions: RegulatorOptions{
			NeedHTAligned: func() bool { return false },
			CPUsPerCore:   func() int { return 4 },
		},
	}

	require.Equal(t, 5, c.round(4.1))
	require.Equal(t, 5, c.round(5))
}
