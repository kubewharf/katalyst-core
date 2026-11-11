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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Benchmarks isolate hierarchy-size and depth costs with a small CPU universe.
// The separately gated correctness test covers the 1024-CPU shape.
const benchmarkTraceCPUs = 8

type traceBenchmarkCase struct {
	nodes int
	depth int
}

type traceBenchmarkFixture struct {
	driver *traceBenchmarkDriver
	round  *coordinatorRound
	base   *CompleteSnapshot
}

type traceBenchmarkDriver struct {
	nodes        map[string]EntryState
	children     map[string][]ChildRef
	roots        []RootRef
	capabilities HierarchyCapabilities
	reads        int64
	writes       int64
	totals       *traceBenchmarkDriver
}

func BenchmarkCompileFixedPointTrace(b *testing.B) {
	runTraceBenchmarks(b, func(b *testing.B, fixture *traceBenchmarkFixture) {
		var trace *CompiledPhaseTrace
		fixture.driver.resetCounts()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var err error
			trace, err = fixture.compile()
			if err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		reportTraceMetrics(b, trace, fixture.driver, b.N)
	})
}

func BenchmarkFreezePhaseTrace(b *testing.B) {
	runTraceBenchmarks(b, func(b *testing.B, fixture *traceBenchmarkFixture) {
		trace, err := fixture.compile()
		if err != nil {
			b.Fatal(err)
		}
		fixture.driver.resetCounts()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			frozen, err := FreezePhaseTrace(trace)
			if err != nil {
				b.Fatal(err)
			}
			trace = frozen
		}
		b.StopTimer()
		reportTraceMetrics(b, trace, fixture.driver, b.N)
	})
}

func BenchmarkPreflightFrozenTrace(b *testing.B) {
	runTraceBenchmarks(b, func(b *testing.B, fixture *traceBenchmarkFixture) {
		trace, err := fixture.compile()
		if err != nil {
			b.Fatal(err)
		}
		fixture.driver.resetCounts()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			writer := newSafeCPUSetWriter(
				fixture.driver,
				NewBudgetTracker(traceScaleBudget(len(trace.InitialSnapshot.Entries), fixture.base.Cost.MaxDepth)),
				&ConvergenceResult{},
			)
			b.StartTimer()
			if err := writer.preflightFrozenTrace(context.Background(), trace); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		reportTraceMetrics(b, trace, fixture.driver, b.N)
	})
}

func BenchmarkRollbackFrozenTracePrefix(b *testing.B) {
	runTraceBenchmarks(b, func(b *testing.B, fixture *traceBenchmarkFixture) {
		trace, err := fixture.compile()
		if err != nil {
			b.Fatal(err)
		}
		counts := &traceBenchmarkDriver{}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			driver, stack := benchmarkRollbackState(trace)
			driver.totals = counts
			ticket, err := NewBudgetTracker(ConvergenceBudget{}).ReservePhaseTrace(trace, 0)
			if err != nil {
				b.Fatal(err)
			}
			writer := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), &ConvergenceResult{})
			driver.resetCounts()
			b.StartTimer()
			if err := writer.rollbackTracePrefix(context.Background(), stack, ticket); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
		}
		b.StopTimer()
		reportTraceMetrics(b, trace, counts, b.N)
	})
}

func runTraceBenchmarks(
	b *testing.B,
	run func(*testing.B, *traceBenchmarkFixture),
) {
	b.Helper()
	for _, tc := range traceBenchmarkCases() {
		tc := tc
		b.Run(fmt.Sprintf("nodes=%d/depth=%d", tc.nodes, tc.depth), func(b *testing.B) {
			fixture := newTraceBenchmarkFixture(b, tc.nodes, tc.depth, benchmarkTraceCPUs)
			b.ReportAllocs()
			b.ResetTimer()
			run(b, fixture)
		})
	}
}

func traceBenchmarkCases() []traceBenchmarkCase {
	cases := make([]traceBenchmarkCase, 0, 9)
	for _, nodes := range []int{100, 1000, 10000} {
		for _, depth := range []int{4, 8, 16} {
			cases = append(cases, traceBenchmarkCase{nodes: nodes, depth: depth})
		}
	}
	return cases
}

func TestBenchmarkMetricPerIterationUsesAllIterations(t *testing.T) {
	t.Parallel()
	if got, want := benchmarkMetricPerIteration(18, 3), float64(6); got != want {
		t.Fatalf("benchmarkMetricPerIteration(18, 3) = %v, want %v", got, want)
	}
}

func TestCompileFixedPointTraceOperationHeavyScale(t *testing.T) {
	t.Parallel()
	if os.Getenv(topologyScaleTestEnv) != "1" {
		t.Skipf("set %s=1 to run high-cost topology scale tests", topologyScaleTestEnv)
	}
	const linearScaleTolerance = 3.0
	var previous operationHeavyScaleMeasurement
	for _, nodes := range []int{100, 1000, 10000} {
		measurement := measureOperationHeavyScale(t, nodes)
		t.Logf("nodes=%d operations=%d elapsed=%s allocations=%d peak_live_bytes=%d",
			nodes, measurement.operations, measurement.elapsed,
			measurement.allocations, measurement.peakLiveBytes)
		require.GreaterOrEqual(t, measurement.operations, nodes-18)
		if previous.nodes != 0 {
			nodeRatio := float64(nodes) / float64(previous.nodes)
			require.LessOrEqual(t,
				float64(measurement.allocations)/float64(previous.allocations),
				nodeRatio*linearScaleTolerance)
			require.LessOrEqual(t,
				float64(measurement.peakLiveBytes)/float64(previous.peakLiveBytes),
				nodeRatio*linearScaleTolerance)
			require.LessOrEqual(t,
				float64(measurement.elapsed)/float64(previous.elapsed),
				nodeRatio*linearScaleTolerance)
		}
		previous = measurement
	}
}

func TestFrozenPreflightOperationHeavyExecutionOverheadRemainsLinear(t *testing.T) {
	t.Parallel()
	if os.Getenv(topologyScaleTestEnv) != "1" {
		t.Skipf("set %s=1 to run high-cost topology scale tests", topologyScaleTestEnv)
	}
	const linearScaleTolerance = 3.0
	var previous uint64
	for _, nodes := range []int{100, 1000} {
		fixture := newOperationHeavyTraceFixture(t, nodes)
		trace, err := fixture.compile()
		require.NoError(t, err)

		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		writer := newSafeCPUSetWriter(
			fixture.driver,
			NewBudgetTracker(traceScaleBudget(nodes, fixture.base.Cost.MaxDepth)),
			&ConvergenceResult{},
		)
		_, err = writer.preflightFrozenTraceOperations(context.Background(), trace)
		require.NoError(t, err)
		runtime.ReadMemStats(&after)
		allocations := after.Mallocs - before.Mallocs
		t.Logf("nodes=%d operations=%d reads=%d allocations=%d",
			nodes, trace.OperationCount(), fixture.driver.readCount(), allocations)
		require.LessOrEqual(t, fixture.driver.readCount(), int64(nodes*8),
			"operation-heavy preflight hierarchy reads must remain O(N)")
		if previous != 0 {
			require.LessOrEqual(t, float64(allocations)/float64(previous),
				10*linearScaleTolerance,
				"10x more operations must not cause super-linear full preflight allocations")
		}
		previous = allocations
	}
}

func TestFrozenPreflightSettlesEvidenceOncePerCompiledFrontier(t *testing.T) {
	t.Parallel()
	const nodes = 1000
	fixture := newOperationHeavyTraceFixture(t, nodes)
	trace, err := fixture.compile()
	require.NoError(t, err)
	require.GreaterOrEqual(t, trace.OperationCount(), nodes-18)

	projection, err := newProjectedHierarchy(trace.InitialSnapshot, trace.Capabilities)
	require.NoError(t, err)
	projection.resetEvidenceRebuildCount()

	evidence, err := projectFrozenTraceOperations(context.Background(), trace, projection)
	require.NoError(t, err)
	require.Len(t, evidence, trace.OperationCount())
	require.Equal(t, nonEmptyCompiledPhaseCount(trace), projection.evidenceRebuildCount(),
		"frozen preflight must settle evidence once per compiled frontier")
	require.Equal(t, trace.FinalSnapshot.ID, projection.snapshot.ID)
}

func nonEmptyCompiledPhaseCount(trace *CompiledPhaseTrace) int {
	count := 0
	for _, phase := range trace.Phases {
		if len(phase.Operations) > 0 {
			count++
		}
	}
	return count
}

type operationHeavyScaleMeasurement struct {
	nodes         int
	operations    int
	elapsed       time.Duration
	allocations   uint64
	peakLiveBytes uint64
}

func measureOperationHeavyScale(t testing.TB, nodes int) operationHeavyScaleMeasurement {
	t.Helper()
	fixture := newOperationHeavyTraceFixture(t, nodes)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	trace, err := fixture.compile()
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.True(t, trace.FinalEvaluation.ParentSafety.Safe)
	require.Zero(t, fixture.driver.readCount())
	require.Zero(t, fixture.driver.writeCount())
	peakLive := after.HeapAlloc
	if peakLive < before.HeapAlloc {
		peakLive = before.HeapAlloc
	}
	return operationHeavyScaleMeasurement{
		nodes:         nodes,
		operations:    trace.OperationCount(),
		elapsed:       elapsed,
		allocations:   after.Mallocs - before.Mallocs,
		peakLiveBytes: peakLive,
	}
}

func newOperationHeavyTraceFixture(tb testing.TB, nodes int) *traceBenchmarkFixture {
	tb.Helper()
	if nodes < 20 {
		tb.Fatalf("nodes=%d must be at least 20", nodes)
	}
	capabilities := cgroupV2Policy.capabilities(true)
	driver := &traceBenchmarkDriver{
		nodes:        make(map[string]EntryState, nodes),
		children:     make(map[string][]ChildRef, nodes),
		capabilities: capabilities,
	}
	specs := make([]NodeSpec, 0, nodes)
	targetByRel := make(map[string]machine.CPUSet, nodes)
	dynamicByRel := make(map[string]machine.CPUSet, nodes)
	requiredByRel := make(map[string]machine.CPUSet, nodes)
	nextInode := uint64(1)
	add := func(rel, parent string, role TopoNodeRole, domain DomainID, observed, target machine.CPUSet) {
		identity := CgroupIdentity{Device: 1, Inode: nextInode}
		nextInode++
		driver.nodes[rel] = EntryState{
			Rel: rel, Identity: identity,
			CPUs: observed.Clone(), ConfiguredCPUs: observed.Clone(),
			Mems: "0", ConfiguredMems: "0",
		}
		if parent == "" {
			driver.roots = append(driver.roots, RootRef{Rel: rel, Identity: identity})
		} else {
			driver.children[parent] = append(driver.children[parent], ChildRef{
				Name: filepath.Base(rel), Identity: identity,
			})
		}
		specs = append(specs, NodeSpec{
			Rel: rel, ParentRel: parent, Role: role, Domain: domain,
			CPUs: target.Clone(), Mems: "0", TrustAnchor: parent == "",
		})
		targetByRel[rel] = target.Clone()
	}

	primaryCPUs := machine.MustParse("0-6")
	add("primary", "", TopoNodeRolePrimary, DomainPrimary, primaryCPUs, primaryCPUs)
	add("reclaim", "", TopoNodeRoleReclaim, DomainReclaim,
		machine.NewCPUSet(0, 7), machine.NewCPUSet(7))
	for index := 2; index < nodes; index++ {
		rel := fmt.Sprintf("reclaim/cleanup-%06d", index)
		add(rel, "reclaim", TopoNodeRoleReclaim, DomainReclaim,
			machine.NewCPUSet(0), machine.NewCPUSet())
		dynamicByRel[rel] = machine.NewCPUSet()
		requiredByRel[rel] = machine.NewCPUSet()
	}
	sort.Slice(driver.children["reclaim"], func(i, j int) bool {
		return driver.children["reclaim"][i].Name < driver.children["reclaim"][j].Name
	})
	sort.Slice(driver.roots, func(i, j int) bool { return driver.roots[i].Rel < driver.roots[j].Rel })
	dag, err := BuildDAG(specs)
	require.NoError(tb, err)
	budget := NewBudgetTracker(traceScaleBudget(nodes, 2))
	base, err := newCompleteSnapshotSource(driver, dag, budget)(context.Background())
	require.NoError(tb, err)
	driver.resetCounts()
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		2: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		3: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
		4: {NUMANodeID: 0, SocketID: 0, CoreID: 4},
		5: {NUMANodeID: 0, SocketID: 0, CoreID: 5},
		6: {NUMANodeID: 0, SocketID: 0, CoreID: 6},
		7: {NUMANodeID: 0, SocketID: 0, CoreID: 7},
	}
	round := &coordinatorRound{
		objective:        ConvergenceObjectiveFull,
		dag:              dag,
		driver:           driver,
		budget:           NewBudgetTracker(traceScaleBudget(nodes, 2)),
		selection:        DefaultDrainSelectionPolicy(),
		targetByRel:      targetByRel,
		dynamicByRel:     dynamicByRel,
		requiredByRel:    requiredByRel,
		cpuDetails:       cpuDetails,
		reservedCPUs:     machine.NewCPUSet(),
		blocked:          map[DomainID]machine.CPUSet{},
		maxRounds:        64,
		allowEmptyTarget: true,
	}
	return &traceBenchmarkFixture{driver: driver, round: round, base: base}
}

func benchmarkMetricPerIteration(total int64, iterations int) float64 {
	return float64(total) / float64(iterations)
}

func reportTraceMetrics(
	b *testing.B,
	trace *CompiledPhaseTrace,
	driver *traceBenchmarkDriver,
	iterations int,
) {
	b.Helper()
	if iterations <= 0 {
		return
	}
	operations := 0
	rounds := 0
	for _, phase := range trace.Phases {
		operations += len(phase.Operations)
		if phase.Kind == PhaseExpand {
			rounds++
		}
	}
	if rounds == 0 && len(trace.Phases) > 0 {
		rounds = 1
	}
	edges := 0
	for _, children := range trace.InitialSnapshot.Children {
		edges += len(children)
	}
	b.ReportMetric(float64(rounds), "rounds/op")
	b.ReportMetric(float64(operations), "compiled-ops/op")
	b.ReportMetric(float64(len(trace.InitialSnapshot.Entries)), "nodes/op")
	b.ReportMetric(float64(edges), "edges/op")
	b.ReportMetric(benchmarkMetricPerIteration(driver.readCount(), iterations), "driver-reads/op")
	b.ReportMetric(benchmarkMetricPerIteration(driver.writeCount(), iterations), "driver-writes/op")
}

func newTraceBenchmarkFixture(
	tb testing.TB,
	nodes, depth, cpuCount int,
) *traceBenchmarkFixture {
	tb.Helper()
	if nodes < depth+2 {
		tb.Fatalf("nodes=%d must accommodate depth=%d plus reclaim nodes", nodes, depth)
	}
	if depth < 1 {
		tb.Fatalf("depth=%d must be positive", depth)
	}
	if cpuCount < 4 {
		tb.Fatalf("cpuCount=%d must be at least 4", cpuCount)
	}

	capabilities := cgroupV2Policy.capabilities(true)
	driver := &traceBenchmarkDriver{
		nodes:        make(map[string]EntryState, nodes),
		children:     make(map[string][]ChildRef, nodes),
		capabilities: capabilities,
	}
	specs := make([]NodeSpec, 0, nodes)
	targetByRel := make(map[string]machine.CPUSet, nodes)
	dynamicByRel := make(map[string]machine.CPUSet, nodes)
	requiredByRel := make(map[string]machine.CPUSet, depth)
	primaryObserved := machine.NewCPUSet()
	for cpu := 1; cpu < cpuCount-1; cpu++ {
		primaryObserved = primaryObserved.Union(machine.NewCPUSet(cpu))
	}
	primaryTarget := primaryObserved.Union(machine.NewCPUSet(0))
	reclaimAnchor := machine.NewCPUSet(cpuCount - 1)
	nextInode := uint64(1)

	add := func(rel, parent string, role TopoNodeRole, domain DomainID, observed, target machine.CPUSet, trustAnchor bool) {
		identity := CgroupIdentity{Device: 1, Inode: nextInode}
		nextInode++
		entry := EntryState{
			Rel: rel, Identity: identity,
			CPUs: observed.Clone(), ConfiguredCPUs: observed.Clone(),
			Mems: "0", ConfiguredMems: "0",
		}
		driver.nodes[rel] = entry
		if parent == "" {
			driver.roots = append(driver.roots, RootRef{Rel: rel, Identity: identity})
		} else {
			driver.children[parent] = append(driver.children[parent], ChildRef{
				Name: filepath.Base(rel), Identity: identity,
			})
		}
		specs = append(specs, NodeSpec{
			Rel: rel, ParentRel: parent, Role: role, Domain: domain,
			CPUs: target.Clone(), Mems: "0", TrustAnchor: trustAnchor,
		})
		targetByRel[rel] = target.Clone()
	}

	add("primary", "", TopoNodeRolePrimary, DomainPrimary, primaryObserved, primaryTarget, true)
	parent := "primary"
	for level := 1; level < depth; level++ {
		rel := fmt.Sprintf("%s/level-%02d", parent, level)
		add(rel, parent, TopoNodeRolePrimary, DomainPrimary, primaryObserved, primaryTarget, false)
		dynamicByRel[rel] = primaryTarget.Clone()
		requiredByRel[rel] = primaryTarget.Clone()
		parent = rel
	}
	add("reclaim", "", TopoNodeRoleReclaim, DomainReclaim, reclaimAnchor, reclaimAnchor, true)
	add("reclaim/release", "reclaim", TopoNodeRoleReclaim, DomainReclaim,
		machine.NewCPUSet(0), machine.NewCPUSet(), false)

	for index := len(specs); index < nodes; index++ {
		rel := fmt.Sprintf("%s/steady-%06d", parent, index)
		add(rel, parent, TopoNodeRolePrimary, DomainPrimary, primaryObserved, primaryObserved, false)
		dynamicByRel[rel] = primaryObserved.Clone()
	}
	for rel := range driver.children {
		sort.Slice(driver.children[rel], func(i, j int) bool {
			return driver.children[rel][i].Name < driver.children[rel][j].Name
		})
	}
	sort.Slice(driver.roots, func(i, j int) bool { return driver.roots[i].Rel < driver.roots[j].Rel })

	dag, err := BuildDAG(specs)
	if err != nil {
		tb.Fatalf("BuildDAG: %v", err)
	}
	budget := NewBudgetTracker(traceScaleBudget(nodes, depth))
	base, err := newCompleteSnapshotSource(driver, dag, budget)(context.Background())
	if err != nil {
		tb.Fatalf("build scale snapshot: %v", err)
	}
	driver.resetCounts()

	cpuDetails := make(machine.CPUDetails, cpuCount)
	for cpu := 0; cpu < cpuCount; cpu++ {
		cpuDetails[cpu] = machine.CPUTopoInfo{
			NUMANodeID: cpu / (cpuCount / 2),
			SocketID:   cpu / (cpuCount / 2),
			CoreID:     cpu / 2,
		}
	}
	round := &coordinatorRound{
		objective:        ConvergenceObjectiveParentSafe,
		dag:              dag,
		driver:           driver,
		budget:           NewBudgetTracker(traceScaleBudget(nodes, depth)),
		selection:        DefaultDrainSelectionPolicy(),
		targetByRel:      targetByRel,
		requiredByRel:    requiredByRel,
		dynamicByRel:     dynamicByRel,
		cpuDetails:       cpuDetails,
		reservedCPUs:     machine.NewCPUSet(),
		blocked:          map[DomainID]machine.CPUSet{},
		maxRounds:        64,
		allowEmptyTarget: true,
	}
	return &traceBenchmarkFixture{driver: driver, round: round, base: base}
}

func traceScaleBudget(nodes, depth int) ConvergenceBudget {
	return ConvergenceBudget{
		MaxRounds:                   64,
		MaxHierarchyIOOperations:    nodes*64 + 1024,
		MaxSnapshotNodes:            nodes + 16,
		MaxSnapshotDepth:            depth + 2,
		MaxDomains:                  8,
		MaxTransferEdges:            nodes*4 + 16,
		MaxPlanOperations:           nodes*16 + 64,
		MaxDeadlockProbeOperations:  nodes*16 + 64,
		AutoDeadlockProbeOperations: false,
	}
}

func (f *traceBenchmarkFixture) compile() (*CompiledPhaseTrace, error) {
	f.round.budget = NewBudgetTracker(traceScaleBudget(len(f.base.Entries), f.base.Cost.MaxDepth))
	return f.round.compileFixedPointTrace(context.Background(), f.base)
}

func (d *traceBenchmarkDriver) Close() error { return nil }

func (d *traceBenchmarkDriver) Roots(context.Context) ([]RootRef, error) {
	d.addRead()
	return append([]RootRef(nil), d.roots...), nil
}

func (d *traceBenchmarkDriver) StatIdentity(_ context.Context, rel string) (CgroupIdentity, error) {
	d.addRead()
	entry, ok := d.nodes[rel]
	if !ok {
		return CgroupIdentity{}, fmt.Errorf("missing benchmark rel %q", rel)
	}
	return entry.Identity, nil
}

func (d *traceBenchmarkDriver) ReadEntry(_ context.Context, rel string) (EntryState, error) {
	d.addRead()
	entry, ok := d.nodes[rel]
	if !ok {
		return EntryState{}, fmt.Errorf("missing benchmark rel %q", rel)
	}
	entry.CPUs = entry.CPUs.Clone()
	entry.ConfiguredCPUs = entry.ConfiguredCPUs.Clone()
	return entry, nil
}

func (d *traceBenchmarkDriver) ListChildren(_ context.Context, rel string) ([]ChildRef, error) {
	d.addRead()
	return append([]ChildRef(nil), d.children[rel]...), nil
}

func (d *traceBenchmarkDriver) WriteCPUs(
	_ context.Context,
	rel string,
	expected CgroupIdentity,
	cpus machine.CPUSet,
) error {
	d.addWrite()
	entry, ok := d.nodes[rel]
	if !ok || entry.Identity != expected {
		return ErrCgroupIdentityChanged
	}
	entry.CPUs = cpus.Clone()
	entry.ConfiguredCPUs = cpus.Clone()
	d.nodes[rel] = entry
	return nil
}

func (d *traceBenchmarkDriver) WriteMems(
	_ context.Context,
	rel string,
	expected CgroupIdentity,
	mems string,
) error {
	d.addWrite()
	entry, ok := d.nodes[rel]
	if !ok || entry.Identity != expected {
		return ErrCgroupIdentityChanged
	}
	entry.Mems = mems
	entry.ConfiguredMems = mems
	d.nodes[rel] = entry
	return nil
}

func (d *traceBenchmarkDriver) Classify(err error, _ HierarchyOperation) HierarchyErrorClass {
	if err == nil {
		return HierarchyErrorNone
	}
	return HierarchyErrorInvalid
}

func (d *traceBenchmarkDriver) Capabilities() HierarchyCapabilities {
	return d.capabilities
}

func (d *traceBenchmarkDriver) resetCounts() {
	atomic.StoreInt64(&d.reads, 0)
	atomic.StoreInt64(&d.writes, 0)
}

func (d *traceBenchmarkDriver) addRead() {
	atomic.AddInt64(&d.reads, 1)
	if d.totals != nil {
		atomic.AddInt64(&d.totals.reads, 1)
	}
}

func (d *traceBenchmarkDriver) addWrite() {
	atomic.AddInt64(&d.writes, 1)
	if d.totals != nil {
		atomic.AddInt64(&d.totals.writes, 1)
	}
}

func (d *traceBenchmarkDriver) readCount() int64 {
	return atomic.LoadInt64(&d.reads)
}

func (d *traceBenchmarkDriver) writeCount() int64 {
	return atomic.LoadInt64(&d.writes)
}

func benchmarkRollbackState(
	trace *CompiledPhaseTrace,
) (*traceBenchmarkDriver, *traceMutationStack) {
	driver := traceBenchmarkDriverFromSnapshot(trace.FinalSnapshot)
	stack := &traceMutationStack{}
	for _, phase := range trace.Phases {
		for operationIndex, operation := range phase.Operations {
			finalEntry := trace.FinalSnapshot.Entries[operation.Rel]
			if operation.WriteMems && operation.ExpectedCurrent.Mems != operation.Target.Mems {
				stack.writes = append(stack.writes, physicalWriteBeforeFromEntry(
					EntryState{
						Rel: operation.Rel, Identity: operation.ExpectedIdentity,
						Mems:           operation.ExpectedCurrent.Mems,
						ConfiguredMems: operation.ExpectedCurrent.Mems,
					},
					operation, HierarchyOperationWriteMems, operation.Target.Mems,
					operationIndex, phase.Kind, finalEntry.Mems,
				))
			}
			if !operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs) {
				stack.writes = append(stack.writes, physicalWriteBeforeFromEntry(
					EntryState{
						Rel: operation.Rel, Identity: operation.ExpectedIdentity,
						CPUs:           operation.ExpectedCurrent.CPUs.Clone(),
						ConfiguredCPUs: operation.ExpectedCurrent.CPUs.Clone(),
					},
					operation, HierarchyOperationWriteCPUs, operation.Target.CPUs.String(),
					operationIndex, phase.Kind, finalEntry.CPUs.String(),
				))
			}
		}
	}
	return driver, stack
}

func traceBenchmarkDriverFromSnapshot(snapshot *CompleteSnapshot) *traceBenchmarkDriver {
	driver := &traceBenchmarkDriver{
		nodes:        make(map[string]EntryState, len(snapshot.Entries)),
		children:     make(map[string][]ChildRef, len(snapshot.Children)),
		capabilities: snapshot.Capabilities,
	}
	rootSet := make(map[string]struct{}, len(snapshot.ScanBoundary.Roots))
	for _, rel := range snapshot.ScanBoundary.Roots {
		rootSet[rel] = struct{}{}
	}
	for rel, entry := range snapshot.Entries {
		entry.CPUs = entry.CPUs.Clone()
		entry.ConfiguredCPUs = entry.ConfiguredCPUs.Clone()
		driver.nodes[rel] = entry
		if _, ok := rootSet[rel]; ok {
			driver.roots = append(driver.roots, RootRef{Rel: rel, Identity: entry.Identity})
		}
	}
	for rel, children := range snapshot.Children {
		driver.children[rel] = append([]ChildRef(nil), children...)
	}
	return driver
}
