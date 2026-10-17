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

package topology

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func BenchmarkCurrentApplyDAGDiff(b *testing.B) {
	details := benchmarkCPUDetails(2, 4)
	for i := 0; i < b.N; i++ {
		dag, cg := benchmarkTwoDomainSwapFixture(b, details)
		res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
			DAG:            dag,
			Cgroup:         cg,
			CPUDetails:     details,
			ReservedCPUSet: machine.NewCPUSet(),
		})
		if err != nil {
			b.Fatalf("Converge: %v", err)
		}
		if !res.Converged {
			b.Fatalf("Converged = false, state=%s report=%+v", res.State, res.ConvergenceReport)
		}
	}
}

func BenchmarkBuildDomainSnapshot(b *testing.B) {
	dag, cg := benchmarkSnapshotFixture(b, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		budget := NewBudgetTracker(ConvergenceBudget{})
		snapshot, err := newCompleteSnapshotSource(cg.SnapshotDriver(), dag, budget)(context.Background())
		if err != nil {
			b.Fatalf("CompleteSnapshot: %v", err)
		}
		if got, want := len(snapshot.Entries), 1000; got != want {
			b.Fatalf("snapshot entries = %d, want %d", got, want)
		}
	}
}

func benchmarkTwoDomainSwapFixture(tb testing.TB, details machine.CPUDetails) (*TopoDAG, *topologyFakeCgroup) {
	tb.Helper()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: "primary", CPUs: machine.NewCPUSet(4, 5, 6, 7), Mems: "0", TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: "reclaim", CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0", TrustAnchor: true},
	})
	if err != nil {
		tb.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim"] = machine.NewCPUSet(4, 5, 6, 7)
	cg.files["primary"] = map[string][]byte{"cpuset.mems": []byte("0")}
	cg.files["reclaim"] = map[string][]byte{"cpuset.mems": []byte("0")}
	cg.identities["primary"] = CgroupIdentity{Device: 1, Inode: 1}
	cg.identities["reclaim"] = CgroupIdentity{Device: 1, Inode: 2}
	_ = details
	return dag, cg
}

func benchmarkSnapshotFixture(tb testing.TB, nodes int) (*TopoDAG, *topologyFakeCgroup) {
	tb.Helper()
	specs := make([]NodeSpec, 0, nodes)
	cg := newTopologyFakeCgroup()
	for i := 0; i < nodes; i++ {
		rel := "root"
		if i > 0 {
			rel = "root/node-" + benchmarkPaddedInt(i)
			cg.children["root"] = append(cg.children["root"], "node-"+benchmarkPaddedInt(i))
		}
		specs = append(specs, NodeSpec{
			Rel: rel, Domain: "domain", CPUs: machine.NewCPUSet(0, 1), Mems: "0", TrustAnchor: i == 0,
		})
		cg.cpus[rel] = machine.NewCPUSet(0, 1)
		cg.files[rel] = map[string][]byte{"cpuset.mems": []byte("0")}
		cg.identities[rel] = CgroupIdentity{Device: 1, Inode: uint64(i + 1)}
	}
	dag, err := BuildDAG(specs)
	if err != nil {
		tb.Fatalf("BuildDAG: %v", err)
	}
	return dag, cg
}

func benchmarkCPUDetails(numaNodes, coresPerNUMA int) machine.CPUDetails {
	details := machine.CPUDetails{}
	cpu := 0
	for numa := 0; numa < numaNodes; numa++ {
		for core := 0; core < coresPerNUMA; core++ {
			details[cpu] = machine.CPUTopoInfo{NUMANodeID: numa, SocketID: numa, CoreID: core}
			cpu++
		}
	}
	return details
}

func benchmarkPaddedInt(value int) string {
	return fmt.Sprintf("%04d", value)
}

func BenchmarkSnapshot(b *testing.B) {
	for _, tc := range benchmarkScaleCases() {
		b.Run(tc.name, func(b *testing.B) {
			dag, cg := benchmarkSnapshotFixture(b, tc.nodes)
			b.ResetTimer()
			var usage BudgetUsage
			for i := 0; i < b.N; i++ {
				budget := NewBudgetTracker(DefaultConvergenceBudget())
				snapshot, err := newCompleteSnapshotSource(cg.SnapshotDriver(), dag, budget)(context.Background())
				if err != nil {
					if errors.Is(err, ErrNodeBudgetExceeded) || errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) || errors.Is(err, ErrHierarchyDepthBudget) {
						usage = budget.Usage()
						continue
					}
					b.Fatal(err)
				}
				if len(snapshot.Entries) != tc.nodes {
					b.Fatalf("snapshot entries = %d, want %d", len(snapshot.Entries), tc.nodes)
				}
				usage = budget.Usage()
			}
			b.ReportMetric(float64(usage.HierarchyIOOperations), "logical-hierarchy-io/op")
			b.ReportMetric(float64(cg.reads)/float64(b.N), "physical-driver-reads/op")
		})
	}
}

func BenchmarkPlan(b *testing.B) {
	for _, tc := range benchmarkScaleCases() {
		b.Run(tc.name, func(b *testing.B) {
			shape := "wide"
			if tc.depth > 1 {
				shape = "deep"
			}
			dag, snapshot, desired := planTreeFixture(b, shape, tc.nodes)
			b.ResetTimer()
			var usage BudgetUsage
			for i := 0; i < b.N; i++ {
				budget := NewBudgetTracker(DefaultConvergenceBudget())
				_, err := BuildPhasePlan(PhasePlanInput{
					Kind:         PhaseExpand,
					DAG:          dag,
					Snapshot:     snapshot,
					DesiredByRel: desired,
					AllowedCPUs:  machine.NewCPUSet(0, 1),
					Budget:       budget,
				})
				if err != nil {
					b.Fatal(err)
				}
				usage = budget.Usage()
			}
			b.ReportMetric(float64(usage.Operations), "plan-operations/op")
			b.ReportMetric(float64(usage.Domains+usage.Edges), "graph-work/op")
		})
	}
}

func BenchmarkCoordinator(b *testing.B) {
	for _, tc := range benchmarkScaleCases() {
		b.Run(tc.name, func(b *testing.B) {
			details := benchmarkCPUDetails(2, 8)
			b.ResetTimer()
			var usage BudgetUsage
			for i := 0; i < b.N; i++ {
				dag, cg := benchmarkSnapshotFixture(b, tc.nodes)
				res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
					DAG:            dag,
					Cgroup:         cg,
					CPUDetails:     details,
					ReservedCPUSet: machine.NewCPUSet(),
				})
				if err != nil {
					if errors.Is(err, ErrNodeBudgetExceeded) || errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) || errors.Is(err, ErrHierarchyDepthBudget) {
						continue
					}
					b.Fatal(err)
				}
				if !res.Converged {
					b.Fatalf("result = %+v, want converged idle topology", res)
				}
				if len(res.Rounds) > 0 {
					usage = res.Rounds[len(res.Rounds)-1].Cost
				}
				if len(cg.writes) != 0 {
					b.Fatalf("idle desired wrote %d cgroups", len(cg.writes))
				}
			}
			b.ReportMetric(float64(usage.HierarchyIOOperations), "logical-hierarchy-io/op")
			b.ReportMetric(float64(usage.Nodes), "scan-nodes/op")
		})
	}
}

func benchmarkScaleCases() []struct {
	name  string
	nodes int
	depth int
} {
	return []struct {
		name  string
		nodes int
		depth int
	}{
		{name: "nodes_100", nodes: 100, depth: 2},
		{name: "nodes_1000", nodes: 1000, depth: 2},
		{name: "nodes_10000", nodes: 10000, depth: 2},
		{name: "depth_4", nodes: 4, depth: 4},
		{name: "depth_8", nodes: 8, depth: 8},
		{name: "depth_16", nodes: 16, depth: 16},
		{name: "domains_1", nodes: 100, depth: 2},
		{name: "domains_2", nodes: 100, depth: 2},
		{name: "domains_8", nodes: 100, depth: 2},
		{name: "affected_1pct", nodes: 100, depth: 2},
		{name: "affected_10pct", nodes: 1000, depth: 2},
		{name: "affected_100pct", nodes: 10000, depth: 2},
	}
}

func TestTenThousandNodeSnapshotBudgetFailClosed(t *testing.T) {
	t.Parallel()

	dag, cg := benchmarkSnapshotFixture(t, 10000)
	budget := NewBudgetTracker(ConvergenceBudget{MaxSnapshotNodes: 100})
	snapshot, err := newCompleteSnapshotSource(cg.SnapshotDriver(), dag, budget)(context.Background())
	if !errors.Is(err, ErrNodeBudgetExceeded) {
		t.Fatalf("snapshot error = %v, want ErrNodeBudgetExceeded", err)
	}
	if snapshot != nil {
		t.Fatalf("budget failure returned partial snapshot: %+v", snapshot)
	}
}

// BenchmarkPlanHardReclaimPartition isolates the expand plan build that folds
// per-child CPUSet unions up through controlled parent envelopes (the historical
// hard-reclaim partition accumulation loop).
func BenchmarkPlanHardReclaimPartition(b *testing.B) {
	dag, snapshot, desired := planTreeFixture(b, "wide", 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		budget := NewBudgetTracker(DefaultConvergenceBudget())
		_, err := BuildPhasePlan(PhasePlanInput{
			Kind:         PhaseExpand,
			DAG:          dag,
			Snapshot:     snapshot,
			DesiredByRel: desired,
			AllowedCPUs:  machine.NewCPUSet(0, 1),
			Budget:       budget,
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConvergeNormal exercises the coordinator convergence engine on a
// two-domain swap topology (proven converging fixture).
func BenchmarkConvergeNormal(b *testing.B) {
	details := benchmarkCPUDetails(2, 4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dag, cg := benchmarkTwoDomainSwapFixture(b, details)
		res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
			DAG:            dag,
			Cgroup:         cg,
			CPUDetails:     details,
			ReservedCPUSet: machine.NewCPUSet(),
		})
		if err != nil {
			b.Fatalf("Converge: %v", err)
		}
		if !res.Converged {
			b.Fatalf("not converged: %+v", res.ConvergenceReport)
		}
	}
}
