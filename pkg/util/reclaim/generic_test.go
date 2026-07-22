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

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func newTestConf(cgroupPath string) *config.Configuration {
	c := config.NewConfiguration()
	c.BaseConfiguration.ReclaimRelativeRootCgroupPath = cgroupPath
	return c
}

func newTestMachineInfo(numaIDs ...int) *machine.KatalystMachineInfo {
	details := machine.CPUDetails{}
	for i, numaID := range numaIDs {
		details[i] = machine.CPUTopoInfo{NUMANodeID: numaID}
	}
	return &machine.KatalystMachineInfo{
		CPUTopology: &machine.CPUTopology{CPUDetails: details},
	}
}

func newDynamicWithPercentages(m map[string]int) *dynamic.Configuration {
	dc := dynamic.NewDynamicAgentConfiguration()
	d := dc.GetDynamicConfiguration()
	d.ReclaimedPercentageByConsumer = m
	return d
}

func TestGenericConsumer(t *testing.T) {
	t.Parallel()
	c := NewGenericConsumer(newTestConf("/kubepods/besteffort"), newTestMachineInfo(0, 1))

	if got := c.GetCgroupPath(); got != "/kubepods/besteffort" {
		t.Fatalf("GetCgroupPath: got %q, want %q", got, "/kubepods/besteffort")
	}

	got := c.GetNumaBindingCgroupPaths()
	want := map[int]string{
		0: "/kubepods/besteffort-0",
		1: "/kubepods/besteffort-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetNumaBindingCgroupPaths: got %v, want %v", got, want)
	}
}

func TestRegisterConsumerAndGetReclaimedPercentage(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	c := NewGenericConsumer(newTestConf("/x"), nil)

	if err := registerConsumer("custom", c); err != nil {
		t.Fatalf("registerConsumer(\"custom\"): unexpected error: %v", err)
	}
	pct := GetReclaimedPercentage(newDynamicWithPercentages(map[string]int{"custom": 50}), "custom")
	if pct != 50 {
		t.Fatalf("GetReclaimedPercentage: got %v, want %v", pct, 50.0)
	}
}

func TestRegisterNamedGenericConsumer(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := RegisterNamedGenericConsumer(GenericConsumerName, newTestConf("/y"), nil); err != nil {
		t.Fatalf("RegisterNamedGenericConsumer: unexpected error: %v", err)
	}
	pct := GetReclaimedPercentage(newDynamicWithPercentages(map[string]int{GenericConsumerName: 75}), GenericConsumerName)
	if pct != 75 {
		t.Fatalf("GetReclaimedPercentage: got %v, want %v", pct, 75.0)
	}
}

func TestRegisterConsumer_Duplicate(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := registerConsumer("dup", NewGenericConsumer(newTestConf(""), nil)); err != nil {
		t.Fatalf("first register: unexpected error: %v", err)
	}
	err := registerConsumer("dup", NewGenericConsumer(newTestConf(""), nil))
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("second register: expected 'already registered' error, got %v", err)
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

	if err := registerConsumer("agg-1", NewGenericConsumer(newTestConf("/a"), nil)); err != nil {
		t.Fatalf("registerConsumer(\"agg-1\"): unexpected error: %v", err)
	}
	if err := registerConsumer("agg-2", NewGenericConsumer(newTestConf("/b"), nil)); err != nil {
		t.Fatalf("registerConsumer(\"agg-2\"): unexpected error: %v", err)
	}

	got := AggregateCgroupPaths()
	if !containsAll(got, []string{"/a", "/b"}) {
		t.Fatalf("AggregateCgroupPaths: got %v, expected /a and /b to be present", got)
	}
}

func TestAggregateAllCgroupPaths(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := registerConsumer("agg-1", NewGenericConsumer(newTestConf("/a"), newTestMachineInfo(0, 1))); err != nil {
		t.Fatalf("registerConsumer(\"agg-1\"): unexpected error: %v", err)
	}
	if err := registerConsumer("agg-2", NewGenericConsumer(newTestConf("/b"), newTestMachineInfo(0, 1))); err != nil {
		t.Fatalf("registerConsumer(\"agg-2\"): unexpected error: %v", err)
	}

	got := AggregateAllCgroupPaths()
	want := []string{"/a", "/a-0", "/a-1", "/b", "/b-0", "/b-1"}
	if len(got) != len(want) || !containsAll(got, want) {
		t.Fatalf("AggregateAllCgroupPaths: got %v, want %v", got, want)
	}
}

func TestAggregateNumaBindingCgroupPaths(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	if err := registerConsumer("agg-1", NewGenericConsumer(newTestConf("/a"), newTestMachineInfo(0, 1))); err != nil {
		t.Fatalf("registerConsumer(\"agg-1\"): unexpected error: %v", err)
	}
	if err := registerConsumer("agg-2", NewGenericConsumer(newTestConf("/b"), newTestMachineInfo(0, 1))); err != nil {
		t.Fatalf("registerConsumer(\"agg-2\"): unexpected error: %v", err)
	}

	got := AggregateNumaBindingCgroupPaths()
	for _, k := range []int{0, 1} {
		if len(got[k]) != 2 {
			t.Fatalf("NUMA %d: got %v, want 2 entries", k, got[k])
		}
	}
	if _, ok := got[2]; ok {
		t.Fatalf("NUMA 2: got %v, want no entry", got[2])
	}
	want0 := map[string]struct{}{"/a-0": {}, "/b-0": {}}
	for _, v := range got[0] {
		delete(want0, v)
	}
	if len(want0) != 0 {
		t.Fatalf("missing NUMA-0 paths: %v (got %v)", want0, got[0])
	}
}

func TestGetReclaimedPercentageByPath_FiltersEmptyPath(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()
	dc := newDynamicWithPercentages(map[string]int{"empty": 30, "real": 40, "real2": 25})

	// Empty-path consumer contributes no reverse-index entry.
	if err := registerConsumer("empty", NewGenericConsumer(newTestConf(""), nil)); err != nil {
		t.Fatalf("register empty: %v", err)
	}
	if err := registerConsumer("real", NewGenericConsumer(newTestConf("/a"), nil)); err != nil {
		t.Fatalf("register real: %v", err)
	}
	if err := registerConsumer("real2", NewGenericConsumer(newTestConf("/b"), nil)); err != nil {
		t.Fatalf("register real2: %v", err)
	}

	if pct := GetReclaimedPercentageByPath(dc, "/a"); pct != 40 {
		t.Fatalf("GetReclaimedPercentageByPath(/a): got %v, want 40", pct)
	}
	if pct := GetReclaimedPercentageByPath(dc, "/b"); pct != 25 {
		t.Fatalf("GetReclaimedPercentageByPath(/b): got %v, want 25", pct)
	}
	if pct := GetReclaimedPercentageByPath(dc, ""); pct != 0 {
		t.Fatalf("GetReclaimedPercentageByPath(\"\"): got %v, want 0", pct)
	}
}

func TestGetReclaimedPercentageByPath_NUMA(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()
	dc := newDynamicWithPercentages(map[string]int{"empty": 20, "real": 30})

	// GenericConsumer with empty cgroupPath contributes no NUMA path; the
	// real consumer's configured percentage is returned as-is.
	if err := registerConsumer("empty", NewGenericConsumer(newTestConf(""), newTestMachineInfo(0, 1))); err != nil {
		t.Fatalf("register empty: %v", err)
	}
	if err := registerConsumer("real", NewGenericConsumer(newTestConf("/a"), newTestMachineInfo(0, 1))); err != nil {
		t.Fatalf("register real: %v", err)
	}

	numaPaths := common.GetNUMABindingReclaimRelativeRootCgroupPaths("/a", []int{0, 1})
	for _, numaID := range []int{0, 1} {
		pct := GetReclaimedPercentageByPath(dc, numaPaths[numaID])
		if pct != 30 {
			t.Fatalf("NUMA %d: got %v, want 30", numaID, pct)
		}
	}
}

func TestGetReclaimedPercentageByPath_UsesConfigured(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()
	dc := newDynamicWithPercentages(map[string]int{"a": 40, "b": 60})
	if err := registerConsumer("a", NewGenericConsumer(newTestConf("/a"), nil)); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := registerConsumer("b", NewGenericConsumer(newTestConf("/b"), nil)); err != nil {
		t.Fatalf("register b: %v", err)
	}

	if pct := GetReclaimedPercentageByPath(dc, "/a"); pct != 40 {
		t.Fatalf("GetReclaimedPercentageByPath(/a): got %v, want 40", pct)
	}
	if pct := GetReclaimedPercentageByPath(dc, "/b"); pct != 60 {
		t.Fatalf("GetReclaimedPercentageByPath(/b): got %v, want 60", pct)
	}
}

func TestGetReclaimedPercentageByPath_Unknown(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()
	dc := newDynamicWithPercentages(map[string]int{"a": 40})
	if err := registerConsumer("a", NewGenericConsumer(newTestConf("/a"), newTestMachineInfo(0))); err != nil {
		t.Fatalf("register a: %v", err)
	}
	for _, path := range []string{"", "/nope", "/a-999"} {
		if pct := GetReclaimedPercentageByPath(dc, path); pct != 0 {
			t.Fatalf("GetReclaimedPercentageByPath(%q): got %v, want 0", path, pct)
		}
	}
}
