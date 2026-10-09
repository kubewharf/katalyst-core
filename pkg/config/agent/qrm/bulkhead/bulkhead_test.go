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

package bulkhead

import (
	"testing"
	"time"
)

func TestBulkheadReclaimPerNUMAUsesTrimmedPrefix(t *testing.T) {
	t.Parallel()

	cfg := BulkheadConfiguration{BulkheadReclaimNumaPrefixes: []string{"/reclaimed/reclaimed-"}}

	if got := cfg.ReclaimPerNUMA(0, 2); got != "reclaimed/reclaimed-2" {
		t.Fatalf("unexpected rel: %q", got)
	}
	if got := cfg.ReclaimPerNUMA(1, 2); got != "" {
		t.Fatalf("out-of-range prefix should return empty rel, got %q", got)
	}
}

func TestNewBulkheadConfigurationDefaultsConvergenceBudgetAndDrainSelection(t *testing.T) {
	t.Parallel()

	cfg := NewBulkheadConfiguration()
	if DefaultTopologyConvergenceDeadline != 10*time.Second {
		t.Fatalf("default topology convergence deadline = %s, want 10s validated by high-churn E2E",
			DefaultTopologyConvergenceDeadline)
	}
	budget := cfg.TopologyConvergenceBudget
	if budget.MaxRounds != 0 {
		t.Fatalf("round budget default = %d, want 0/auto", budget.MaxRounds)
	}
	if budget.MaxHierarchyIOOperations != 0 {
		t.Fatalf("hierarchy I/O budget default = %d, want 0/auto", budget.MaxHierarchyIOOperations)
	}
	if budget.MaxPlanOperations != 0 {
		t.Fatalf("plan operation budget default = %d, want 0/auto", budget.MaxPlanOperations)
	}
	if budget.MaxSnapshotNodes <= 0 ||
		budget.MaxSnapshotDepth <= 0 ||
		budget.MaxDomains <= 0 ||
		budget.MaxTransferEdges <= 0 {
		t.Fatalf("budget defaults must be non-zero defensive limits: %+v", budget)
	}
	if budget.MaxDeadlockProbeOperations != 0 {
		t.Fatalf("deadlock probe budget default = %d, want 0/auto", budget.MaxDeadlockProbeOperations)
	}
	if budget.DeadlineDuration != DefaultTopologyConvergenceDeadline {
		t.Fatalf("deadline default = %s, want %s", budget.DeadlineDuration, DefaultTopologyConvergenceDeadline)
	}
	if got := TopologyHandlerTimeout(cfg); got != 15*time.Second {
		t.Fatalf("outer handler timeout = %s, want 15s to preserve post-topology handler budget", got)
	}
	selection := cfg.TopologyDrainSelection
	if selection.MaxCPUsDrainRatio != 0 {
		t.Fatalf("default drain ratio = %v, want 0/full-drain", selection.MaxCPUsDrainRatio)
	}
	if selection.GroupByNUMA || !selection.RequirePairedSwapProgress {
		t.Fatalf("selection defaults must use full-drain with paired progress and no small-step topology grouping: %+v", selection)
	}
	if !cfg.EnableAdmissionLeafDefer {
		t.Fatal("admission leaf defer must be enabled by default")
	}
	if cfg.AdmissionMaxRequiredWrites != 0 {
		t.Fatalf("admission required write budget = %d, want 0/auto", cfg.AdmissionMaxRequiredWrites)
	}
	if cfg.AdmissionSafeDuration != 5*time.Second {
		t.Fatalf("admission safe duration = %s, want 5s", cfg.AdmissionSafeDuration)
	}
	if cfg.PreserveReclaimCPUSetWhenTopologyDisabled {
		t.Fatal("preserve reclaim cpuset while topology is disabled must default to false")
	}
}

func TestTopologyHandlerTimeoutSaturatesNearMaxDuration(t *testing.T) {
	t.Parallel()

	const maxDuration = time.Duration(1<<63 - 1)
	cfg := NewBulkheadConfiguration()
	cfg.TopologyConvergenceBudget.DeadlineDuration = maxDuration

	if got := TopologyHandlerTimeout(cfg); got != maxDuration {
		t.Fatalf("outer handler timeout = %s, want saturation at %s", got, maxDuration)
	}
}
