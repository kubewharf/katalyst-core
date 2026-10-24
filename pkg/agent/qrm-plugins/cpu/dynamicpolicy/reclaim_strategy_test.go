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

package dynamicpolicy

import (
	"reflect"
	"testing"

	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

func TestRegisteredReclaimStrategiesOrder(t *testing.T) {
	t.Parallel()

	got := make([]ReclaimStrategyName, 0)
	for _, s := range RegisteredReclaimStrategies() {
		got = append(got, s.Name())
	}
	want := []ReclaimStrategyName{
		ReclaimStrategyHardPartitionRampUp,
		ReclaimStrategySteadyFakeNUMAReclaim,
		ReclaimStrategyLegacyOverlapReclaim,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registered strategy order = %v, want %v", got, want)
	}
}

func TestReclaimStrategyNamesNonEmpty(t *testing.T) {
	t.Parallel()
	for _, s := range RegisteredReclaimStrategies() {
		if s.Name() == "" || s.Description() == "" {
			t.Fatalf("strategy %q has empty name/description", s.Name())
		}
	}
}

// newReclaimTestConfig builds a Configuration with all embedded pointer sections
// populated (via NewConfiguration) so the promoted reclaim booleans are safe to
// read.
func newReclaimTestConfig(enableReclaim, hardPartition bool) *dynamicconfig.Configuration {
	c := dynamicconfig.NewConfiguration()
	c.EnableReclaim = enableReclaim
	c.EnableRampUpReclaimHardPartition = hardPartition
	return c
}

// TestHardPartitionRegistryMatchesLegacyPredicate guards the 1:1 equivalence
// between the registry lookup and the inline predicate it replaced, across nil /
// disabled / enabled configurations.
func TestHardPartitionRegistryMatchesLegacyPredicate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                 string
		dyn                  *dynamicconfig.Configuration
		wantHardPartition    bool
		wantSteadyOrLegacyOn bool
	}{
		{
			name: "nil config",
			dyn:  nil,
		},
		{
			name:                 "reclaim off",
			dyn:                  newReclaimTestConfig(false, false),
			wantSteadyOrLegacyOn: false,
		},
		{
			name:                 "reclaim on, hard partition off -> steady/legacy family on",
			dyn:                  newReclaimTestConfig(true, false),
			wantSteadyOrLegacyOn: true,
		},
		{
			name:                 "hard partition on",
			dyn:                  newReclaimTestConfig(true, true),
			wantHardPartition:    true,
			wantSteadyOrLegacyOn: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			legacy := isRampUpReclaimHardPartitionEnabledWithConfig(tc.dyn)
			viaRegistry := hardPartitionReclaimConfigGated(tc.dyn)
			if legacy != viaRegistry {
				t.Fatalf("registry lookup = %v, legacy predicate = %v", viaRegistry, legacy)
			}
			if legacy != tc.wantHardPartition {
				t.Fatalf("hard partition = %v, want %v", legacy, tc.wantHardPartition)
			}

			active := activeReclaimStrategyNames(tc.dyn)
			hasHard := false
			hasSteadyOrLegacy := false
			for _, n := range active {
				if n == ReclaimStrategyHardPartitionRampUp {
					hasHard = true
				}
				if n == ReclaimStrategySteadyFakeNUMAReclaim || n == ReclaimStrategyLegacyOverlapReclaim {
					hasSteadyOrLegacy = true
				}
			}
			if hasHard != tc.wantHardPartition {
				t.Fatalf("active hard partition = %v, active set %v", hasHard, active)
			}
			if hasSteadyOrLegacy != tc.wantSteadyOrLegacyOn {
				t.Fatalf("active steady/legacy = %v, active set %v", hasSteadyOrLegacy, active)
			}
		})
	}
}
