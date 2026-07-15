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

package reclaim

import (
	"reflect"
	"strings"
	"testing"
)

func TestGenericConsumer(t *testing.T) {
	t.Parallel()
	c := NewGenericConsumer("/kubepods/besteffort", 80)

	if got := c.GetCgroupPath(); got != "/kubepods/besteffort" {
		t.Fatalf("GetCgroupPath: got %q, want %q", got, "/kubepods/besteffort")
	}

	got := c.GetNumaBindingCgroupPaths([]int{0, 1})
	want := map[int]string{
		0: "/kubepods/besteffort-0",
		1: "/kubepods/besteffort-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetNumaBindingCgroupPaths: got %v, want %v", got, want)
	}

	if got := c.GetReclaimedPercentage(); got != 80 {
		t.Fatalf("GetReclaimedPercentage: got %v, want %v", got, 80.0)
	}
}

func TestRegisterConsumerAndGetReclaimedPercentage(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	c := NewGenericConsumer("/x", 50)

	if err := registerConsumer("custom", c); err != nil {
		t.Fatalf("registerConsumer(\"custom\"): unexpected error: %v", err)
	}
	pct, ok := GetReclaimedPercentage("custom")
	if !ok {
		t.Fatal("GetReclaimedPercentage(\"custom\") returned ok=false")
	}
	if pct != 100 {
		t.Fatalf("GetReclaimedPercentage: got %v, want %v", pct, 100.0)
	}
}

func TestRegisterNamedGenericConsumer(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := RegisterNamedGenericConsumer(GenericConsumerName, "/y", 75); err != nil {
		t.Fatalf("RegisterNamedGenericConsumer: unexpected error: %v", err)
	}
	pct, ok := GetReclaimedPercentage(GenericConsumerName)
	if !ok {
		t.Fatalf("GetReclaimedPercentage(%q) returned ok=false", GenericConsumerName)
	}
	if pct != 100 {
		t.Fatalf("GetReclaimedPercentage: got %v, want %v", pct, 100.0)
	}
}

func TestRegisterConsumer_Duplicate(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := registerConsumer("dup", NewGenericConsumer("", 0)); err != nil {
		t.Fatalf("first register: unexpected error: %v", err)
	}
	err := registerConsumer("dup", NewGenericConsumer("", 0))
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("second register: expected 'already registered' error, got %v", err)
	}
}

func TestRegisterConsumer_TotalExceeds100(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := registerConsumer("a", NewGenericConsumer("", 60)); err != nil {
		t.Fatalf("first register: unexpected error: %v", err)
	}
	err := registerConsumer("b", NewGenericConsumer("", 50))
	if err == nil || !strings.Contains(err.Error(), "> 100") {
		t.Fatalf("expected total>100 error, got %v", err)
	}
}

func containsAll(haystack []string, needles []string) bool {
	found := map[string]bool{}
	for _, s := range haystack {
		found[s] = true
	}
	for _, n := range needles {
		if !found[n] {
			return false
		}
	}
	return true
}

func TestAggregateCgroupPaths(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := registerConsumer("agg-1", NewGenericConsumer("/a", 0)); err != nil {
		t.Fatalf("registerConsumer(\"agg-1\"): unexpected error: %v", err)
	}
	if err := registerConsumer("agg-2", NewGenericConsumer("/b", 0)); err != nil {
		t.Fatalf("registerConsumer(\"agg-2\"): unexpected error: %v", err)
	}

	got := AggregateCgroupPaths()
	if !containsAll(got, []string{"/a", "/b"}) {
		t.Fatalf("AggregateCgroupPaths: got %v, expected /a and /b to be present", got)
	}
}

func TestAggregateNumaBindingCgroupPaths(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := registerConsumer("agg-1", NewGenericConsumer("/a", 0)); err != nil {
		t.Fatalf("registerConsumer(\"agg-1\"): unexpected error: %v", err)
	}
	if err := registerConsumer("agg-2", NewGenericConsumer("/b", 0)); err != nil {
		t.Fatalf("registerConsumer(\"agg-2\"): unexpected error: %v", err)
	}

	got := AggregateNumaBindingCgroupPaths([]int{0, 1})
	for _, k := range []int{0, 1} {
		if len(got[k]) != 2 {
			t.Fatalf("NUMA %d: got %v, want 2 entries", k, got[k])
		}
	}
	want0 := map[string]struct{}{"/a-0": {}, "/b-0": {}}
	for _, v := range got[0] {
		delete(want0, v)
	}
	if len(want0) != 0 {
		t.Fatalf("missing NUMA-0 paths: %v (got %v)", want0, got[0])
	}
}

func TestAggregateCgroupPathsWithPercentage_FiltersEmptyPath(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	// Empty-path consumer must be skipped. Only the /a consumer contributes,
	// so its percentage is forced to 100 (post-filter single-contributor override).
	if err := registerConsumer("empty", NewGenericConsumer("", 30)); err != nil {
		t.Fatalf("register empty: %v", err)
	}
	if err := registerConsumer("real", NewGenericConsumer("/a", 40)); err != nil {
		t.Fatalf("register real: %v", err)
	}

	got := AggregateCgroupPathsWithPercentage()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %v", len(got), got)
	}
	if got[0].Path != "/a" || got[0].Percentage != 100 {
		t.Fatalf("got %+v, want {Path:/a Percentage:100}", got[0])
	}

	// Adding a second real consumer disables the override; both declared
	// percentages must be preserved.
	if err := registerConsumer("real2", NewGenericConsumer("/b", 25)); err != nil {
		t.Fatalf("register real2: %v", err)
	}
	got = AggregateCgroupPathsWithPercentage()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(got), got)
	}
	byPath := map[string]float64{}
	for _, e := range got {
		byPath[e.Path] = e.Percentage
	}
	if byPath["/a"] != 40 || byPath["/b"] != 25 {
		t.Fatalf("got %v, want /a=40 /b=25", byPath)
	}
}

func TestAggregateNumaBindingCgroupPathsWithPercentage_FiltersNilMap(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	// GenericConsumer with empty cgroupPath returns nil from
	// GetNumaBindingCgroupPaths; that consumer must be skipped, and the
	// single real contributor gets its percentage forced to 100.
	if err := registerConsumer("empty", NewGenericConsumer("", 20)); err != nil {
		t.Fatalf("register empty: %v", err)
	}
	if err := registerConsumer("real", NewGenericConsumer("/a", 30)); err != nil {
		t.Fatalf("register real: %v", err)
	}

	got := AggregateNumaBindingCgroupPathsWithPercentage([]int{0, 1})
	for _, numaID := range []int{0, 1} {
		if len(got[numaID]) != 1 {
			t.Fatalf("NUMA %d: got %d entries, want 1: %v", numaID, len(got[numaID]), got[numaID])
		}
		if got[numaID][0].Percentage != 100 {
			t.Fatalf("NUMA %d: percentage %v, want 100", numaID, got[numaID][0].Percentage)
		}
	}
}
