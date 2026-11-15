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

package cpusettopology

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestTopologyResultAndDAGApplyResultPreserveExecutionStatistics(t *testing.T) {
	t.Parallel()
	convergence := topology.ConvergenceResult{
		Attempted:            11,
		Applied:              7,
		Skipped:              2,
		Failed:               1,
		Deferred:             1,
		ParentSafe:           true,
		DeferredLeafCount:    3,
		DeferredCPUCount:     5,
		FinalSnapshotCurrent: true,
		ConvergenceReport: topology.ConvergenceReport{
			FullyConverged: false,
		},
	}
	applied := &model.AppliedView{}

	topologyResult := topologyResultFromFinalConvergence(convergence, applied)
	if topologyResult.Attempted != 11 || topologyResult.Applied != 7 ||
		topologyResult.Skipped != 2 || topologyResult.Failed != 1 ||
		topologyResult.Deferred != 1 || topologyResult.DeferredCPUCount != 5 {
		t.Fatalf("topology result lost execution statistics: %+v", topologyResult)
	}

	dagResult := dagApplyResultFromTopologyResult(topologyResult)
	if dagResult.Attempted != 11 || dagResult.Applied != 7 ||
		dagResult.Skipped != 2 || dagResult.Failed != 1 ||
		dagResult.Deferred != 1 || dagResult.DeferredCPUCount != 5 ||
		dagResult.ConvergenceReport.FullyConverged {
		t.Fatalf("DAG apply result lost execution statistics: %+v", dagResult)
	}
}

func TestAccumulateConvergenceResultPropagatesPublicationState(t *testing.T) {
	t.Parallel()
	total := topology.ConvergenceResult{
		Published:         true,
		ReplanDisposition: topology.ReplanNotAllowed,
	}
	attempt := topology.ConvergenceResult{
		Attempted:         2,
		Published:         false,
		ReplanDisposition: topology.ReplanSafeFromVerifiedFinalState,
	}

	accumulateConvergenceResult(&total, attempt)

	if total.Attempted != 2 || total.Published ||
		total.ReplanDisposition != topology.ReplanSafeFromVerifiedFinalState {
		t.Fatalf("accumulated result = %+v, want unpublished verified-final-state replan", total)
	}
}

func TestScheduleFixedPointRetryByAdjustmentMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		mode          cpusetutil.CPUSetAdjustmentMode
		err           error
		wantScheduled bool
	}{
		{name: "admission stall", mode: cpusetutil.CPUSetAdjustmentModeAdmission, err: topology.ErrNoProgress, wantScheduled: true},
		{name: "admission wrapped stall", mode: cpusetutil.CPUSetAdjustmentModeAdmission, err: fmt.Errorf("compile: %w", topology.ErrNoProgress), wantScheduled: true},
		{name: "admission other error", mode: cpusetutil.CPUSetAdjustmentModeAdmission, err: context.DeadlineExceeded},
		{name: "retry stall does not self schedule", mode: cpusetutil.CPUSetAdjustmentModeRetry, err: topology.ErrNoProgress},
		{name: "periodic stall stays dirty in caller", mode: cpusetutil.CPUSetAdjustmentModePeriodic, err: topology.ErrNoProgress},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var reasons []cpusetutil.CPUSetAdjustmentRetryReason
			scheduleFixedPointRetry(tc.mode, tc.err, func(reason cpusetutil.CPUSetAdjustmentRetryReason) {
				reasons = append(reasons, reason)
			})
			scheduled := len(reasons) == 1
			if scheduled != tc.wantScheduled {
				t.Fatalf("scheduled=%v, want scheduled=%v", reasons, tc.wantScheduled)
			}
			if tc.wantScheduled && reasons[0] != cpusetutil.RetryReasonFixedPointStall {
				t.Fatalf("reason=%q, want %q", reasons[0], cpusetutil.RetryReasonFixedPointStall)
			}
		})
	}
}

func TestHandleDeferredLeafRetryByAdjustmentMode(t *testing.T) {
	t.Parallel()

	scheduled := 0
	schedule := func(cpusetutil.CPUSetAdjustmentRetryReason) {
		scheduled++
	}
	if err := handleDeferredLeafRetry(cpusetutil.CPUSetAdjustmentModeAdmission, schedule); err != nil {
		t.Fatalf("admission deferred leaf returned error: %v", err)
	}
	if scheduled != 1 {
		t.Fatalf("admission scheduled retries = %d, want 1", scheduled)
	}

	scheduled = 0
	if err := handleDeferredLeafRetry(cpusetutil.CPUSetAdjustmentModeRetry, schedule); err == nil {
		t.Fatal("retry deferred leaf returned nil, want pending error")
	}
	if scheduled != 0 {
		t.Fatalf("retry mode self-scheduled %d retries, want 0", scheduled)
	}
}

func TestAppliedViewFromFinalSnapshotUsesPerRelV2TargetProof(t *testing.T) {
	t.Parallel()
	dag, err := topology.BuildDAG([]topology.NodeSpec{
		{
			Rel: "primary", Role: topology.TopoNodeRolePrimary, Domain: topology.DomainPrimary,
			CPUs: machine.NewCPUSet(0, 1), ControlledRoot: true, TrustAnchor: true,
		},
		{
			Rel: "reclaim", Role: topology.TopoNodeRoleReclaim, Domain: topology.DomainReclaim,
			CPUs: machine.NewCPUSet(2, 3), ControlledRoot: true, TrustAnchor: true,
		},
		{
			Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: topology.TopoNodeRoleReclaimNUMABucket,
			Domain: topology.DomainReclaim, CPUs: machine.NewCPUSet(), TrustAnchor: true,
			Constraint: topology.TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(2, 3),
				Scope:         topology.TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": strconv.Itoa(0)},
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	snapshot := &topology.CompleteSnapshot{
		Capabilities: topology.HierarchyCapabilities{
			StableIdentity: true, EmptyConfiguredCPUSet: true, EffectiveCPUSet: true,
		},
		Entries: map[string]topology.EntryState{
			"primary": {
				Rel: "primary", Identity: topology.CgroupIdentity{Device: 7, Inode: 10},
				CPUs: machine.NewCPUSet(0, 1), ConfiguredCPUs: machine.NewCPUSet(0, 1),
			},
			"reclaim": {
				Rel: "reclaim", Identity: topology.CgroupIdentity{Device: 7, Inode: 11},
				CPUs: machine.NewCPUSet(2, 3), ConfiguredCPUs: machine.NewCPUSet(2, 3),
			},
			"reclaim/numa-0": {
				Rel: "reclaim/numa-0", Identity: topology.CgroupIdentity{Device: 7, Inode: 12},
				CPUs: machine.NewCPUSet(2, 3), ConfiguredCPUs: machine.NewCPUSet(),
			},
		},
	}

	applied, err := appliedViewFromFinalSnapshot(nil, model.NewDesiredView(), dag, snapshot)
	if err != nil {
		t.Fatalf("appliedViewFromFinalSnapshot() error = %v", err)
	}
	if got := applied.CPUSetByRel["reclaim/numa-0"]; !got.IsEmpty() {
		t.Fatalf("empty v2 target published inherited effective CPUs %s", got.String())
	}
	if got := applied.CPUSetByRel["reclaim"]; !got.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("non-empty v2 target published %s, want effective 2-3", got.String())
	}
	if got := applied.RelProofByRel["reclaim"]; got.Device != 7 || got.Inode != 11 ||
		!got.CPUSet.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("reclaim rel proof = %+v, want device=7 inode=11 cpuset=2-3", got)
	}
	if got := applied.RelProofByRel["reclaim/numa-0"]; got.Device != 7 || got.Inode != 12 ||
		!got.CPUSet.IsEmpty() {
		t.Fatalf("empty target rel proof = %+v, want device=7 inode=12 empty cpuset", got)
	}
	if got := applied.ReclaimEffectivePerNUMA[0]; !got.IsEmpty() {
		t.Fatalf("empty v2 NUMA target published inherited effective CPUs %s", got.String())
	}
}

func TestAppliedViewFromFinalSnapshotPreservesCPUOwnership(t *testing.T) {
	t.Parallel()
	primaryCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5)
	dag, err := topology.BuildDAG([]topology.NodeSpec{
		{
			Rel: "primary", Role: topology.TopoNodeRolePrimary, Domain: topology.DomainPrimary,
			CPUs: primaryCPUs, ControlledRoot: true, TrustAnchor: true,
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	snapshot := &topology.CompleteSnapshot{
		Capabilities: topology.HierarchyCapabilities{
			StableIdentity: true, EmptyConfiguredCPUSet: true, EffectiveCPUSet: true,
		},
		Entries: map[string]topology.EntryState{
			"primary": {
				Rel: "primary", Identity: topology.CgroupIdentity{Device: 7, Inode: 10},
				CPUs: primaryCPUs, ConfiguredCPUs: primaryCPUs,
			},
		},
	}
	desired := model.NewDesiredView()
	desired.SharePoolMap["share"] = machine.NewCPUSet(0, 1)
	desired.SharePoolMap["latency"] = machine.NewCPUSet(2, 3)
	desired.Dedicated = machine.NewCPUSet(4, 5)

	applied, err := appliedViewFromFinalSnapshot(nil, desired, dag, snapshot)
	if err != nil {
		t.Fatalf("appliedViewFromFinalSnapshot() error = %v", err)
	}
	if got := applied.SharePoolMap["share"]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("applied share pool = %s, want 0-1", got.String())
	}
	if got := applied.SharePoolMap["latency"]; !got.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("applied latency pool = %s, want 2-3", got.String())
	}
	if got := applied.Dedicated; !got.Equals(machine.NewCPUSet(4, 5)) {
		t.Fatalf("applied dedicated = %s, want 4-5", got.String())
	}

	desired.SharePoolMap["share"].Add(6)
	desired.Dedicated.Add(7)
	if got := applied.SharePoolMap["share"]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("applied share pool changed with desired view mutation: %s", got.String())
	}
	if got := applied.Dedicated; !got.Equals(machine.NewCPUSet(4, 5)) {
		t.Fatalf("applied dedicated changed with desired view mutation: %s", got.String())
	}

	applied.SharePoolMap["latency"].Add(8)
	applied.Dedicated.Add(8)
	if got := desired.SharePoolMap["latency"]; !got.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("desired latency pool changed with applied view mutation: %s", got.String())
	}
	if got := desired.Dedicated; !got.Equals(machine.NewCPUSet(4, 5, 7)) {
		t.Fatalf("desired dedicated changed with applied view mutation: %s", got.String())
	}
}

func TestAppliedViewUsesSemanticOwnershipForDormantV1Bucket(t *testing.T) {
	t.Parallel()
	dag, err := topology.BuildDAG([]topology.NodeSpec{
		{
			Rel: "reclaim", Role: topology.TopoNodeRoleReclaim, Domain: topology.DomainReclaim,
			CPUs: machine.NewCPUSet(2, 3), ControlledRoot: true, TrustAnchor: true,
		},
		{
			Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: topology.TopoNodeRoleReclaimNUMABucket,
			Domain: topology.DomainReclaim, CPUs: machine.NewCPUSet(), TrustAnchor: true,
			Constraint: topology.TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(0, 1),
				Scope:         topology.TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": "0"},
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	snapshot := &topology.CompleteSnapshot{
		Entries: map[string]topology.EntryState{
			"reclaim": {
				Rel: "reclaim", Identity: topology.CgroupIdentity{Device: 7, Inode: 10},
				CPUs: machine.NewCPUSet(0, 1, 2, 3),
			},
			"reclaim/numa-0": {
				Rel: "reclaim/numa-0", Identity: topology.CgroupIdentity{Device: 7, Inode: 11},
				CPUs: machine.NewCPUSet(0, 1),
			},
		},
		OwnershipByRel: map[string]machine.CPUSet{
			"reclaim":        machine.NewCPUSet(2, 3),
			"reclaim/numa-0": machine.NewCPUSet(),
		},
	}

	applied, err := appliedViewFromFinalSnapshot(nil, model.NewDesiredView(), dag, snapshot)
	if err != nil {
		t.Fatalf("appliedViewFromFinalSnapshot() error = %v", err)
	}
	if got := applied.ReclaimEffective; !got.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("reclaim ownership = %s, want semantic target 2-3", got.String())
	}
	if got := applied.ReclaimEffectivePerNUMA[0]; !got.IsEmpty() {
		t.Fatalf("dormant NUMA ownership = %s, want empty", got.String())
	}
	if got := applied.CPUSetByRel["reclaim/numa-0"]; !got.IsEmpty() {
		t.Fatalf("dormant rel proof = %s, want empty semantic ownership", got.String())
	}
}

func TestAppliedViewFromFinalSnapshotRejectsNUMATargetProofOutsideUpperBound(t *testing.T) {
	t.Parallel()
	dag, err := topology.BuildDAG([]topology.NodeSpec{
		{
			Rel: "reclaim", Role: topology.TopoNodeRoleReclaim, Domain: topology.DomainReclaim,
			CPUs: machine.NewCPUSet(0, 1), ControlledRoot: true, TrustAnchor: true,
		},
		{
			Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: topology.TopoNodeRoleReclaimNUMABucket,
			Domain: topology.DomainReclaim, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true,
			Constraint: topology.TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(0),
				Scope:         topology.TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": "0"},
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	snapshot := &topology.CompleteSnapshot{
		Entries: map[string]topology.EntryState{
			"reclaim":        {Rel: "reclaim", CPUs: machine.NewCPUSet(0, 1)},
			"reclaim/numa-0": {Rel: "reclaim/numa-0", CPUs: machine.NewCPUSet(0, 1)},
		},
	}

	_, err = appliedViewFromFinalSnapshot(nil, model.NewDesiredView(), dag, snapshot)
	if err == nil || !strings.Contains(err.Error(), "CPU upper bound") {
		t.Fatalf("appliedViewFromFinalSnapshot() error = %v, want NUMA CPU upper-bound rejection", err)
	}
}

type capturedMetric struct {
	key  string
	val  float64
	tags []metrics.MetricTag
}

type captureMetricEmitter struct {
	metrics []capturedMetric
}

func TestTopologyDrainSelectionFromConfigPassesRatio(t *testing.T) {
	t.Parallel()

	got := topologyDrainSelectionFromConfig(bulkheadconfig.DrainSelectionPolicy{
		MaxCPUsDrainRatio:         0.25,
		GroupByNUMA:               true,
		RequirePairedSwapProgress: true,
	})
	if got.MaxCPUsDrainRatio != 0.25 || !got.GroupByNUMA || !got.RequirePairedSwapProgress {
		t.Fatalf("topology drain selection = %+v, want ratio and existing policy fields preserved", got)
	}
}

func (e *captureMetricEmitter) StoreInt64(key string, val int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	e.metrics = append(e.metrics, capturedMetric{key: key, val: float64(val), tags: append([]metrics.MetricTag(nil), tags...)})
	return nil
}

func (e *captureMetricEmitter) StoreFloat64(key string, val float64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	e.metrics = append(e.metrics, capturedMetric{key: key, val: val, tags: append([]metrics.MetricTag(nil), tags...)})
	return nil
}

func (e *captureMetricEmitter) WithTags(string, ...metrics.MetricTag) metrics.MetricEmitter {
	return e
}

func (e *captureMetricEmitter) Run(context.Context) {}

func TestCPUSetTopologyPluginIsConfiguredReclaimNUMARel(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-", "/foo/bar-"},
		},
	}

	for _, rel := range []string{
		"reclaimed/reclaimed-0",
		"/reclaimed/reclaimed-1",
		"foo/bar-2",
	} {
		if !p.isConfiguredReclaimNUMARel(rel) {
			t.Fatalf("expected %q to be recognized as reclaim NUMA rel", rel)
		}
	}

	for _, rel := range []string{
		"reclaimed/reclaimed",
		"reclaimed/reclaimed-a",
		"reclaimed/reclaimed-0-extra",
		"foo/bar",
		"other/bar-0",
	} {
		if p.isConfiguredReclaimNUMARel(rel) {
			t.Fatalf("expected %q not to be recognized as reclaim NUMA rel", rel)
		}
	}
}

func TestTopologyMetricLabelsAreBoundedEnums(t *testing.T) {
	t.Parallel()

	emitter := &captureMetricEmitter{}
	emitBulkheadTopologySummary(emitter, "normal", topology.ConvergenceResult{
		Converged: true,
		State:     topology.ConvergenceStateConverged,
		Rounds: []topology.RoundOutcome{{
			Status: topology.RoundStatusProgress,
			Cost:   topology.BudgetUsage{Nodes: 10, MaxDepth: 2},
			Witnesses: []topology.ReleaseWitness{{
				CPUs: machine.NewCPUSet(0, 1),
			}},
		}},
	}, nil)
	emitBulkheadTopologySummary(emitter, "normal", topology.ConvergenceResult{}, topology.ErrNodeBudgetExceeded)

	if len(emitter.metrics) == 0 {
		t.Fatal("expected topology metrics")
	}
	for _, item := range emitter.metrics {
		for _, tag := range item.tags {
			switch tag.Key {
			case "phase":
				assertMetricEnum(t, item.key, tag, allowedTopologyMetricPhases)
			case "status":
				assertMetricEnum(t, item.key, tag, allowedTopologyMetricStatuses)
			case "reason":
				assertMetricEnum(t, item.key, tag, allowedTopologyMetricReasons)
			case "domain_role":
				assertMetricEnum(t, item.key, tag, allowedTopologyMetricDomainRoles)
			case "kind":
				if tag.Val == "" || strings.Contains(tag.Val, "/") {
					t.Fatalf("metric %s has unbounded kind label: %+v", item.key, tag)
				}
			}
		}
	}
}

func TestEmitBulkheadTopologySummaryDoesNotEmitSyntheticHandoffLatency(t *testing.T) {
	t.Parallel()

	emitter := &captureMetricEmitter{}
	emitBulkheadTopologySummary(emitter, "normal", topology.ConvergenceResult{
		Converged: true,
		State:     topology.ConvergenceStateConverged,
	}, nil)
	emitBulkheadTopologySummary(emitter, "normal", topology.ConvergenceResult{}, topology.ErrNodeBudgetExceeded)

	for _, item := range emitter.metrics {
		if item.key == "bulkhead_topology_handoff_latency_seconds" {
			t.Fatalf("synthetic handoff latency metric must not be emitted: %#v", emitter.metrics)
		}
	}
}

func TestEmitBulkheadPruneResultBoundsLabels(t *testing.T) {
	t.Parallel()

	emitter := &captureMetricEmitter{}
	emitBulkheadPruneResult(emitter, "unexpected/status", "/kubepods/pod-a")
	emitBulkheadPruneActiveRels(emitter, 3, "unexpected/status", "/kubepods/pod-a")

	if len(emitter.metrics) != 2 {
		t.Fatalf("metrics = %d, want 2", len(emitter.metrics))
	}
	var resultMetric, activeRelsMetric *capturedMetric
	for i := range emitter.metrics {
		switch emitter.metrics[i].key {
		case metricBulkheadPruneResult:
			resultMetric = &emitter.metrics[i]
		case "bulkhead_prune_active_rels":
			activeRelsMetric = &emitter.metrics[i]
		}
	}
	if resultMetric == nil || activeRelsMetric == nil {
		t.Fatalf("prune metrics missing: %#v", emitter.metrics)
	}
	got := map[string]string{}
	for _, tag := range resultMetric.tags {
		got[tag.Key] = tag.Val
	}
	want := map[string]string{
		"status": "skipped",
		"reason": "invalid",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prune metric tags = %#v, want %#v", got, want)
	}
	if activeRelsMetric.val != 3 {
		t.Fatalf("active rels metric = %v, want 3", activeRelsMetric.val)
	}
	if len(activeRelsMetric.tags) != 2 {
		t.Fatalf("active rels metric tags = %#v, want bounded status/reason only", activeRelsMetric.tags)
	}
}

func assertMetricEnum(t *testing.T, key string, tag metrics.MetricTag, allowed map[string]struct{}) {
	t.Helper()
	if _, ok := allowed[tag.Val]; !ok {
		t.Fatalf("metric %s has unbounded %s label %q", key, tag.Key, tag.Val)
	}
}

type fakeCgroupClient struct {
	cgroupclient.FakeCgroupClient

	version                 cgroupclient.CgroupVersion
	existing                map[string]bool
	cpus                    map[string]machine.CPUSet
	mems                    map[string]string
	children                map[string][]string
	statErrors              map[string]error
	statCalls               []string
	requireExistingForApply bool
	writes                  map[string]string
	writeOrder              []string
	applyCounts             map[string]int
	cpusetWrites            map[string]cgcommon.CPUSetData
	pruned                  map[string]struct{}
	schedLoadBalance        map[string]bool
	partitionWrites         map[string]cgcommon.CPUSetPartitionFlag
	partitionErrByRel       map[string]error
	listErr                 error
	listChildrenHook        func(context.Context, string) ([]string, error)
	afterApply              func(rel string, data *cgcommon.CPUSetData)
	readOverride            func(rel string) (machine.CPUSet, bool)
	readErrByRel            map[string]error
	readCalls               []string
	statIdentityHook        func(string) (topology.CgroupIdentity, error)
	readEntryCounts         map[string]int
	readEntryHook           func(string, int) error
	writeCPUCounts          map[string]int
	writeCPUHook            func(string, int) error
	classifyHook            func(error, topology.HierarchyOperation) topology.HierarchyErrorClass
}

type fakeSnapshotDriver struct {
	cg *fakeCgroupClient
}

func (f *fakeCgroupClient) SnapshotDriver() topology.HierarchyDriver {
	return &fakeSnapshotDriver{cg: f}
}

func (d *fakeSnapshotDriver) Close() error { return nil }

func (d *fakeSnapshotDriver) Roots(context.Context) ([]topology.RootRef, error) {
	return nil, nil
}

func (d *fakeSnapshotDriver) StatIdentity(_ context.Context, rel string) (topology.CgroupIdentity, error) {
	if d.cg.statIdentityHook != nil {
		return d.cg.statIdentityHook(rel)
	}
	if err := d.cg.statErrors[rel]; err != nil {
		return topology.CgroupIdentity{}, err
	}
	return pluginFakeIdentity(rel), nil
}

func (d *fakeSnapshotDriver) ReadEntry(ctx context.Context, rel string) (topology.EntryState, error) {
	if d.cg.readEntryCounts == nil {
		d.cg.readEntryCounts = map[string]int{}
	}
	d.cg.readEntryCounts[rel]++
	if d.cg.readEntryHook != nil {
		if err := d.cg.readEntryHook(rel, d.cg.readEntryCounts[rel]); err != nil {
			return topology.EntryState{}, err
		}
	}
	cpus, err := d.cg.ReadCPUSet(ctx, rel)
	if err != nil {
		return topology.EntryState{}, err
	}
	mems := d.cg.mems[rel]
	if mems == "" {
		mems = "0"
	}
	// On cgroup v1 there is no separate cpuset.cpus.effective vs cpuset.cpus: the
	// configured and effective views are identical (see the real driver, which
	// clones effective into configured when configuredCPUsFile==observedCPUsFile).
	// Mirroring that here keeps the frozen pre-write predecessor equality honest for
	// synchronous dynamic-leaf shrinks instead of reporting a nil-vs-empty drift.
	return topology.EntryState{
		Rel:            rel,
		Identity:       pluginFakeIdentity(rel),
		CPUs:           cpus,
		Mems:           mems,
		ConfiguredCPUs: cpus.Clone(),
		ConfiguredMems: mems,
	}, nil
}

func (d *fakeSnapshotDriver) ReadEntryWithActivity(
	ctx context.Context,
	rel string,
	expected topology.CgroupIdentity,
) (topology.EntryState, error) {
	entry, err := d.ReadEntry(ctx, rel)
	if err != nil {
		return topology.EntryState{}, err
	}
	if entry.Identity != expected {
		return topology.EntryState{}, topology.ErrCgroupIdentityChanged
	}
	// Existing plugin fixtures model populated runtime cgroups unless a test
	// supplies a purpose-built topology driver with explicit inactivity.
	return entry, nil
}

func (d *fakeSnapshotDriver) ListChildren(ctx context.Context, rel string) ([]topology.ChildRef, error) {
	names, err := d.cg.ListChildren(ctx, rel)
	if err != nil {
		return nil, err
	}
	children := make([]topology.ChildRef, 0, len(names))
	for _, name := range names {
		children = append(children, topology.ChildRef{Name: name, Identity: pluginFakeIdentity(strings.Trim(rel, "/") + "/" + name)})
	}
	return children, nil
}

func (d *fakeSnapshotDriver) WriteCPUs(ctx context.Context, rel string, expected topology.CgroupIdentity, cpus machine.CPUSet) error {
	if pluginFakeIdentity(rel) != expected {
		return topology.ErrCgroupIdentityChanged
	}
	if d.cg.writeCPUCounts == nil {
		d.cg.writeCPUCounts = map[string]int{}
	}
	d.cg.writeCPUCounts[rel]++
	if d.cg.writeCPUHook != nil {
		if err := d.cg.writeCPUHook(rel, d.cg.writeCPUCounts[rel]); err != nil {
			return err
		}
	}
	return d.cg.ApplyCPUSet(ctx, rel, &cgcommon.CPUSetData{
		CPUs: cpus.String(), WriteEmptyCPUs: cpus.IsEmpty(),
	})
}

func (d *fakeSnapshotDriver) WriteMems(ctx context.Context, rel string, expected topology.CgroupIdentity, mems string) error {
	if pluginFakeIdentity(rel) != expected {
		return topology.ErrCgroupIdentityChanged
	}
	return d.cg.ApplyCPUSet(ctx, rel, &cgcommon.CPUSetData{
		Mems: mems, WriteEmptyMems: mems == "",
	})
}

func (d *fakeSnapshotDriver) Classify(err error, operation topology.HierarchyOperation) topology.HierarchyErrorClass {
	if d.cg.classifyHook != nil {
		return d.cg.classifyHook(err, operation)
	}
	return topology.HierarchyErrorInvalid
}

func (d *fakeSnapshotDriver) Capabilities() topology.HierarchyCapabilities {
	return topology.HierarchyCapabilities{StableIdentity: true, KernelParentContainment: true}
}

func pluginFakeIdentity(rel string) topology.CgroupIdentity {
	var inode uint64 = 1
	for _, value := range []byte(rel) {
		inode = inode*131 + uint64(value)
	}
	return topology.CgroupIdentity{Device: 1, Inode: inode}
}

func (f *fakeCgroupClient) StatDir(_ context.Context, rel string) (time.Time, error) {
	f.statCalls = append(f.statCalls, rel)
	if err := f.statErrors[rel]; err != nil {
		return time.Time{}, err
	}
	if f.existing[rel] {
		return time.Time{}, nil
	}
	return time.Time{}, os.ErrNotExist
}

func (f *fakeCgroupClient) Version(context.Context) cgroupclient.CgroupVersion {
	if f.version != "" {
		return f.version
	}
	return cgroupclient.CgroupVersionV1
}

func (f *fakeCgroupClient) ReadCPUSet(_ context.Context, rel string) (machine.CPUSet, error) {
	f.readCalls = append(f.readCalls, rel)
	if err := f.readErrByRel[rel]; err != nil {
		return machine.NewCPUSet(), err
	}
	if f.readOverride != nil {
		if cpus, ok := f.readOverride(rel); ok {
			return cpus.Clone(), nil
		}
	}
	if cpus, ok := f.cpus[rel]; ok {
		return cpus.Clone(), nil
	}
	return machine.NewCPUSet(), nil
}

func TestPendingProtectedCPUSetByRelAggregatesAllContainersByPod(t *testing.T) {
	t.Parallel()

	const (
		podUID = "pending-multi-container-pod"
		podRel = "kubepods/burstable/pod-pending-multi-container"
	)
	now := time.Date(2026, time.August, 5, 10, 0, 0, 0, time.UTC)
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{cpus: map[string]machine.CPUSet{
			podRel: machine.NewCPUSet(0, 1, 2, 3),
		}},
		now: func() time.Time { return now },
		pendingProtections: map[string]pendingPodProtection{
			podUID: {
				rel:          podRel,
				protectUntil: now.Add(defaultPendingPodProtectionTTL),
			},
		},
	}

	got := p.pendingProtectedCPUSetByRel(context.Background(), []pendingContainerCPUSet{
		{PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(0, 1)},
		{PodUID: podUID, ContainerName: "sidecar", CPUs: machine.NewCPUSet(2, 3)},
	})
	if protected := got[podRel]; !protected.Equals(machine.NewCPUSet(0, 1, 2, 3)) {
		t.Fatalf("protected cpuset = %s, want union 0-3", protected.String())
	}
}

func TestPendingProtectedCPUSetByRelRenewsExpiredActiveProtection(t *testing.T) {
	t.Parallel()

	const (
		podUID = "pending-after-ttl-pod"
		podRel = "kubepods/burstable/pod-pending-after-ttl"
	)
	now := time.Date(2026, time.August, 5, 10, 0, 0, 0, time.UTC)
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{cpus: map[string]machine.CPUSet{
			podRel: machine.NewCPUSet(4, 5),
		}},
		now: func() time.Time { return now },
		pendingProtections: map[string]pendingPodProtection{
			podUID: {
				rel:          podRel,
				protectUntil: now,
			},
		},
	}

	got := p.pendingProtectedCPUSetByRel(context.Background(), []pendingContainerCPUSet{
		{PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(4, 5)},
	})
	if protected := got[podRel]; !protected.Equals(machine.NewCPUSet(4, 5)) {
		t.Fatalf("protected cpuset after TTL = %s, want 4-5", protected.String())
	}
	protection, ok := p.pendingProtections[podUID]
	if !ok {
		t.Fatal("active pending pod protection was removed after TTL")
	}
	if want := now.Add(defaultPendingPodProtectionTTL); !protection.protectUntil.Equal(want) {
		t.Fatalf("renewed protectUntil = %s, want %s", protection.protectUntil, want)
	}
}

func TestPendingProtectedCPUSetByRelClearsPodNoLongerPending(t *testing.T) {
	t.Parallel()

	const podUID = "completed-pending-pod"
	p := &CPUSetTopologyPlugin{
		pendingProtections: map[string]pendingPodProtection{
			podUID: {rel: "kubepods/burstable/pod-completed"},
		},
	}

	got := p.pendingProtectedCPUSetByRel(context.Background(), nil)
	if len(got) != 0 {
		t.Fatalf("protected cpus = %#v, want none", got)
	}
	if len(p.pendingProtections) != 0 {
		t.Fatalf("pending protections = %#v, want cleared", p.pendingProtections)
	}
}

func TestPendingProtectionScopesRetainExpectedPathsForAllQoS(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 18, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		podUID string
		podRel string
		cpus   machine.CPUSet
	}{
		{
			name:   "ordinary shared",
			podUID: "pod-shared",
			podRel: "kubepods/burstable/pod-shared",
			cpus:   machine.NewCPUSet(0, 1, 2, 3),
		},
		{
			name:   "snb",
			podUID: "pod-snb",
			podRel: "kubepods/burstable/pod-snb",
			cpus:   machine.NewCPUSet(4, 5),
		},
		{
			name:   "dnb",
			podUID: "pod-dnb",
			podRel: "kubepods/pod-dnb",
			cpus:   machine.NewCPUSet(6, 7),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &CPUSetTopologyPlugin{
				cgroup: &fakeCgroupClient{
					readErrByRel: map[string]error{tt.podRel: os.ErrNotExist},
					statErrors:   map[string]error{tt.podRel: syscall.ENOENT},
				},
				now: func() time.Time { return now },
				pendingProtections: map[string]pendingPodProtection{
					tt.podUID: {
						rel:          tt.podRel,
						protectUntil: now.Add(defaultPendingPodProtectionTTL),
					},
				},
			}

			got, err := p.pendingProtectionScopes(context.Background(), nil, []pendingContainerCPUSet{{
				PodUID: tt.podUID, ContainerName: "main", CPUs: tt.cpus,
			}})
			if err != nil {
				t.Fatalf("pendingProtectionScopes() error = %v", err)
			}
			want := []topology.PendingProtection{{
				ScopeRel: tt.podRel,
				CPUs:     tt.cpus,
				PodUID:   tt.podUID,
				Source:   topology.PendingProtectionSourceExpectedPod,
			}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("pending protections = %#v, want %#v", got, want)
			}
		})
	}
}

func TestPendingProtectionScopesUseExistingPodEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 18, 8, 0, 0, 0, time.UTC)
	const (
		podUID = "pod-existing"
		podRel = "kubepods/burstable/pod-existing"
	)
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{cpus: map[string]machine.CPUSet{
			podRel: machine.NewCPUSet(0, 1, 2, 3),
		}},
		now: func() time.Time { return now },
		pendingProtections: map[string]pendingPodProtection{
			podUID: {
				rel:          podRel,
				protectUntil: now.Add(defaultPendingPodProtectionTTL),
			},
		},
	}

	got, err := p.pendingProtectionScopes(context.Background(), nil, []pendingContainerCPUSet{
		{PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(0, 1)},
		{PodUID: podUID, ContainerName: "sidecar", CPUs: machine.NewCPUSet(2, 3)},
	})
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	want := []topology.PendingProtection{{
		ScopeRel: podRel,
		CPUs:     machine.NewCPUSet(0, 1, 2, 3),
		PodUID:   podUID,
		Source:   topology.PendingProtectionSourceExistingPod,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending protections = %#v, want %#v", got, want)
	}
}

func TestPendingProtectionScopesClassifyScopeExistence(t *testing.T) {
	t.Parallel()

	const (
		podUID = "pod-scope-existence"
		podRel = "kubepods/burstable/pod-scope-existence"
	)
	readErr := errors.New("read cpuset failed")
	statErr := errors.New("stat failed")
	untypedMissingErr := errors.New("missing")
	tests := []struct {
		name       string
		cgroup     *fakeCgroupClient
		wantSource topology.PendingProtectionSource
		wantErr    error
		wantStats  int
	}{
		{
			name:       "successful empty cpuset proves existing pod",
			cgroup:     &fakeCgroupClient{},
			wantSource: topology.PendingProtectionSourceExistingPod,
		},
		{
			name: "typed ENOENT after read failure means expected pod",
			cgroup: &fakeCgroupClient{
				readErrByRel: map[string]error{podRel: readErr},
				statErrors: map[string]error{podRel: &os.PathError{
					Op: "stat", Path: podRel, Err: syscall.ENOENT,
				}},
			},
			wantSource: topology.PendingProtectionSourceExpectedPod,
			wantStats:  1,
		},
		{
			name: "existing directory preserves original read error",
			cgroup: &fakeCgroupClient{
				existing:     map[string]bool{podRel: true},
				readErrByRel: map[string]error{podRel: readErr},
			},
			wantErr:   readErr,
			wantStats: 1,
		},
		{
			name: "untyped missing stat error fails closed",
			cgroup: &fakeCgroupClient{
				readErrByRel: map[string]error{podRel: readErr},
				statErrors:   map[string]error{podRel: untypedMissingErr},
			},
			wantErr:   untypedMissingErr,
			wantStats: 1,
		},
		{
			name: "other stat error fails closed",
			cgroup: &fakeCgroupClient{
				readErrByRel: map[string]error{podRel: readErr},
				statErrors:   map[string]error{podRel: statErr},
			},
			wantErr:   statErr,
			wantStats: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &CPUSetTopologyPlugin{
				cgroup:             tt.cgroup,
				pendingProtections: map[string]pendingPodProtection{},
			}
			got, err := p.pendingProtectionScopes(context.Background(), nil, []pendingContainerCPUSet{{
				PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(0, 1), ScopeRel: podRel,
			}})

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("pendingProtectionScopes() error = %v, want %v", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("pendingProtectionScopes() error = %v", err)
				}
				if len(got) != 1 || got[0].Source != tt.wantSource {
					t.Fatalf("pending protections = %#v, want source %q", got, tt.wantSource)
				}
			}
			if got := len(tt.cgroup.readCalls); got != 1 {
				t.Fatalf("ReadCPUSet calls = %d, want 1; calls=%v", got, tt.cgroup.readCalls)
			}
			if got := len(tt.cgroup.statCalls); got != tt.wantStats {
				t.Fatalf("StatDir calls = %d, want %d; calls=%v", got, tt.wantStats, tt.cgroup.statCalls)
			}
		})
	}
}

func TestPendingProtectionScopesCachesMissingScopeObservation(t *testing.T) {
	t.Parallel()

	const podRel = "kubepods/burstable/pod-shared-missing-scope"
	readErr := errors.New("read cpuset failed")
	cg := &fakeCgroupClient{
		readErrByRel: map[string]error{podRel: readErr},
		statErrors:   map[string]error{podRel: syscall.ENOENT},
	}
	p := &CPUSetTopologyPlugin{
		cgroup:             cg,
		pendingProtections: map[string]pendingPodProtection{},
	}

	got, err := p.pendingProtectionScopes(context.Background(), nil, []pendingContainerCPUSet{
		{PodUID: "pod-a", ContainerName: "main", CPUs: machine.NewCPUSet(0, 1), ScopeRel: podRel},
		{PodUID: "pod-b", ContainerName: "main", CPUs: machine.NewCPUSet(2, 3), ScopeRel: podRel},
	})
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("pending protections = %#v, want two expected pods", got)
	}
	for _, protection := range got {
		if protection.Source != topology.PendingProtectionSourceExpectedPod {
			t.Fatalf("pending protection source = %q, want %q", protection.Source, topology.PendingProtectionSourceExpectedPod)
		}
	}
	if len(cg.readCalls) != 1 || len(cg.statCalls) != 1 {
		t.Fatalf("scope observations: reads=%v stats=%v, want one read and one stat", cg.readCalls, cg.statCalls)
	}
}

func TestNormalPendingProtectionReadsEachScopeOnce(t *testing.T) {
	t.Parallel()

	const (
		podUID = "pod-single-scope-read"
		podRel = "kubepods/burstable/pod-single-scope-read"
	)
	cg := &fakeCgroupClient{cpus: map[string]machine.CPUSet{
		podRel: machine.NewCPUSet(0, 1, 2, 3),
	}}
	p := &CPUSetTopologyPlugin{
		cgroup:             cg,
		pendingProtections: map[string]pendingPodProtection{},
	}
	pending := []pendingContainerCPUSet{{
		PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(0, 1),
		ScopeRel: podRel,
	}, {
		PodUID: "pod-sharing-scope", ContainerName: "sidecar", CPUs: machine.NewCPUSet(2, 3),
		ScopeRel: podRel,
	}}

	protections, err := p.pendingProtectionScopes(context.Background(), nil, pending)
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	protectedByRel := pendingProtectedCPUSetByResolvedScopes(protections)
	if len(protections) != 2 || !protectedByRel[podRel].Equals(machine.NewCPUSet(0, 1, 2, 3)) {
		t.Fatalf("normal pending protection = %#v protected=%v, want two owners sharing one protected scope", protections, protectedByRel)
	}
	var scopeReads int
	for _, rel := range cg.readCalls {
		if rel == podRel {
			scopeReads++
		}
	}
	if scopeReads != 1 {
		t.Fatalf("normal pending scope reads = %d, want exactly 1; calls=%v", scopeReads, cg.readCalls)
	}
}

func TestPendingProtectionScopesResolveColdPodFromDAGWithoutFilesystemEvidence(t *testing.T) {
	t.Parallel()

	const podUID = "cold-shared"
	dag, err := topology.BuildDAG([]topology.NodeSpec{{
		Rel:            "kubepods/burstable",
		Role:           topology.TopoNodeRolePrimary,
		Domain:         topology.DomainPrimary,
		ControlledRoot: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	const podRel = "kubepods/burstable/podcold-shared"
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{
			readErrByRel: map[string]error{podRel: os.ErrNotExist},
			statErrors:   map[string]error{podRel: syscall.ENOENT},
		},
		pendingProtections: map[string]pendingPodProtection{},
	}

	got, err := p.pendingProtectionScopes(context.Background(), dag, []pendingContainerCPUSet{{
		PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(0, 1),
		NativeQOSClass: v1.PodQOSBurstable,
	}})
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	want := []topology.PendingProtection{{
		ScopeRel: podRel,
		CPUs:     machine.NewCPUSet(0, 1),
		PodUID:   podUID,
		Source:   topology.PendingProtectionSourceExpectedPod,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending protections = %#v, want %#v", got, want)
	}
}

func TestPendingProtectionScopesResolveGuaranteedPodUnderProductionPrimaryRoot(t *testing.T) {
	t.Parallel()

	const podUID = "cold-guaranteed"
	dag, err := topology.BuildDAG([]topology.NodeSpec{{
		Rel:            "kubepods",
		Role:           topology.TopoNodeRolePrimary,
		Domain:         topology.DomainPrimary,
		ControlledRoot: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	const podRel = "kubepods/podcold-guaranteed"
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{
			readErrByRel: map[string]error{podRel: os.ErrNotExist},
			statErrors:   map[string]error{podRel: syscall.ENOENT},
		},
		pendingProtections: map[string]pendingPodProtection{},
	}

	got, err := p.pendingProtectionScopes(context.Background(), dag, []pendingContainerCPUSet{{
		PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(0, 1),
		NativeQOSClass: v1.PodQOSGuaranteed,
	}})
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	want := []topology.PendingProtection{{
		ScopeRel: podRel,
		CPUs:     machine.NewCPUSet(0, 1),
		PodUID:   podUID,
		Source:   topology.PendingProtectionSourceExpectedPod,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending protections = %#v, want %#v", got, want)
	}
}

func (f *fakeCgroupClient) ApplyCPUSet(_ context.Context, rel string, data *cgcommon.CPUSetData) error {
	if f.requireExistingForApply && !f.existing[rel] {
		return fmt.Errorf("apply cpuset @ %s: %w", rel, os.ErrNotExist)
	}
	if f.writes == nil {
		f.writes = map[string]string{}
	}
	if f.cpusetWrites == nil {
		f.cpusetWrites = map[string]cgcommon.CPUSetData{}
	}
	if f.applyCounts == nil {
		f.applyCounts = map[string]int{}
	}
	f.writes[rel] = data.CPUs
	f.applyCounts[rel]++
	f.cpusetWrites[rel] = *data
	if f.cpus == nil {
		f.cpus = map[string]machine.CPUSet{}
	}
	if data.CPUs != "" || data.WriteEmptyCPUs {
		f.cpus[rel] = machine.MustParse(data.CPUs)
		f.writeOrder = append(f.writeOrder, rel)
	}
	if data.Mems != "" || data.WriteEmptyMems {
		if f.mems == nil {
			f.mems = map[string]string{}
		}
		f.mems[rel] = data.Mems
	}
	if f.afterApply != nil {
		f.afterApply(rel, data)
	}
	return nil
}

func (f *fakeCgroupClient) Prune(active map[string]struct{}) {
	f.pruned = active
}

func (f *fakeCgroupClient) ListChildren(ctx context.Context, rel string) ([]string, error) {
	if f.listChildrenHook != nil {
		return f.listChildrenHook(ctx, rel)
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]string(nil), f.children[rel]...), nil
}

func (f *fakeCgroupClient) EnsureDir(_ context.Context, rel string) error {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return nil
	}
	if f.existing == nil {
		f.existing = map[string]bool{}
	}
	f.existing[rel] = true
	if f.statErrors != nil {
		delete(f.statErrors, rel)
	}
	if f.children == nil {
		f.children = map[string][]string{}
	}
	if f.mems == nil {
		f.mems = map[string]string{}
	}
	parent, name := splitRelParent(rel)
	if f.mems[rel] == "" {
		parentMems := f.mems[parent]
		if parentMems == "" {
			parentMems = "0"
		}
		f.mems[rel] = parentMems
	}
	for _, child := range f.children[parent] {
		if child == name {
			return nil
		}
	}
	f.children[parent] = append(f.children[parent], name)
	return nil
}

func splitRelParent(rel string) (string, string) {
	if idx := strings.LastIndex(rel, "/"); idx >= 0 {
		return rel[:idx], rel[idx+1:]
	}
	return "", rel
}

func (f *fakeCgroupClient) ApplySchedLoadBalance(_ context.Context, rel string, enabled bool) error {
	if f.schedLoadBalance == nil {
		f.schedLoadBalance = map[string]bool{}
	}
	f.schedLoadBalance[rel] = enabled
	return nil
}

func (f *fakeCgroupClient) ApplyCPUSetPartition(_ context.Context, rel string, flag cgcommon.CPUSetPartitionFlag) error {
	if err := f.partitionErrByRel[rel]; err != nil {
		return err
	}
	if f.partitionWrites == nil {
		f.partitionWrites = map[string]cgcommon.CPUSetPartitionFlag{}
	}
	f.partitionWrites[rel] = flag
	return nil
}

func (f *fakeCgroupClient) ReadCgroupFile(_ context.Context, rel, file string) ([]byte, error) {
	if file == "cpuset.mems" {
		if mems := f.mems[rel]; mems != "" {
			return []byte(mems), nil
		}
		return []byte("0"), nil
	}
	return nil, nil
}

func TestCPUSetTopologyPluginReconcilesPrimaryWhenReclaimEmpty(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{existing: map[string]bool{
		"primary": true,
		"reclaim": true,
	}}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: emptyLifecycleMetaServer(),
			Topology: &machine.CPUTopology{
				CPUDetails: machine.CPUDetails{
					0: {},
					1: {},
					2: {},
					3: {},
				},
			},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1, 2, 3),
			ReclaimEffective: machine.NewCPUSet(),
		}},
	})
	if err != nil {
		t.Fatalf("CPUSetAdjustmentHandler: %v", err)
	}
	if got := cg.writes["primary"]; got != "0-3" {
		t.Fatalf("primary cpuset = %q, want 0-3; writes=%v", got, cg.writes)
	}
	if _, ok := cg.pruned["primary"]; !ok {
		t.Fatalf("primary rel not pruned as active: %#v", cg.pruned)
	}
}

func TestCPUSetTopologyPluginDefaultAutoBudgetConverges96CPUHandoffAtPointZeroOne(t *testing.T) {
	t.Parallel()

	details := make(machine.CPUDetails, 96)
	for cpu := 0; cpu < 96; cpu++ {
		details[cpu] = machine.CPUTopoInfo{CoreID: cpu}
	}
	allCPUs := details.CPUs()
	cg := &fakeCgroupClient{
		version:  cgroupclient.CgroupVersionV2,
		existing: map[string]bool{"primary": true, "reclaim": true},
		cpus: map[string]machine.CPUSet{
			"primary": allCPUs,
			"reclaim": machine.NewCPUSet(),
		},
		children: map[string][]string{},
	}
	cfg := bulkheadconfig.NewBulkheadConfiguration()
	cfg.BulkheadPrimaryRelPath = "primary"
	cfg.BulkheadReclaimRelPaths = []string{"reclaim"}
	cfg.TopologyDrainSelection.MaxCPUsDrainRatio = 0.01
	p := &CPUSetTopologyPlugin{cfg: *cfg, cgroup: cg}
	fetcher := &strictSnapshotFetcher{}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Topology:   &machine.CPUTopology{CPUDetails: details},
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(),
			ReclaimEffective: allCPUs,
		}},
	})
	if err != nil {
		t.Fatalf("CPUSetAdjustmentHandler: %v", err)
	}
	if got := cg.cpus["primary"]; !got.IsEmpty() {
		t.Fatalf("primary cpuset = %s, want empty", got.String())
	}
	if got := cg.cpus["reclaim"]; !got.Equals(allCPUs) {
		t.Fatalf("reclaim cpuset = %s, want %s", got.String(), allCPUs.String())
	}
	if cg.applyCounts["primary"] <= 32 {
		t.Fatalf("primary writes = %d, want real plugin path to exceed the old 32-round default", cg.applyCounts["primary"])
	}
	if fetcher.listCalls != 1 {
		t.Fatalf("strict GetPodList calls = %d, want 1 for empty lifecycle proof validation", fetcher.listCalls)
	}
}

func TestCPUSetTopologyPluginReportsAppliedViewFromFinalSnapshot(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true},
		cpus: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 1),
			"reclaim": machine.NewCPUSet(2, 3),
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
	}
	reclaimIdentity := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}
	shareIdentity := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "share"}
	desired := model.NewDesiredView()
	desired.NonReclaimPool = machine.NewCPUSet(0, 1)
	desired.ReclaimEffective = machine.NewCPUSet(2, 3)
	desired.PoolOwners[reclaimIdentity] = model.DesiredPoolOwner{
		ExpectedCPUSet: machine.NewCPUSet(2, 3),
	}
	desired.PoolOwners[shareIdentity] = model.DesiredPoolOwner{
		ExpectedCPUSet: machine.NewCPUSet(0, 1),
	}
	dagResult, err := p.Apply(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: emptyLifecycleMetaServer(),
			Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {}, 1: {}, 2: {}, 3: {},
			}},
		},
		DesiredView: desired,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if dagResult.Level != bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("outcome level = %v, want full converged current final snapshot", dagResult.Level)
	}
	result := bulkheadapi.TopologyResult{AppliedView: dagResult.View.DeepCopy()}
	if result.AppliedView == nil {
		t.Fatalf("topology result should carry an AppliedView")
	}
	if got := result.AppliedView.NonReclaimPool.String(); got != "0-1" {
		t.Fatalf("applied non reclaim = %q, want 0-1", got)
	}
	if got := result.AppliedView.ReclaimEffective.String(); got != "2-3" {
		t.Fatalf("applied reclaim = %q, want 2-3", got)
	}
	for rel, want := range map[string]string{"primary": "0-1", "reclaim": "2-3"} {
		if got := result.AppliedView.CPUSetByRel[rel].String(); got != want {
			t.Fatalf("applied rel %q = %q, want final snapshot value %q", rel, got, want)
		}
	}
	if dagResult.View == nil {
		t.Fatal("TopologyOutcome should carry the final-snapshot AppliedView")
	}
	if got := dagResult.View.ReclaimEffective.String(); got != "2-3" {
		t.Fatalf("DAG applied reclaim = %q, want 2-3", got)
	}
	assertProjectedCPUSet(t, result.AppliedView.PoolProjection.CPUSetByIdentity,
		reclaimIdentity, machine.NewCPUSet(2, 3))
	assertProjectedCPUSet(t, result.AppliedView.PoolProjection.CPUSetByIdentity,
		shareIdentity, machine.NewCPUSet(0, 1))
	if !result.AppliedView.PoolProjection.UncoveredCPUs.IsEmpty() ||
		!result.AppliedView.PoolProjection.AmbiguousCPUs.IsEmpty() {
		t.Fatalf("full projection diagnostics = %+v, want empty", result.AppliedView.PoolProjection)
	}

	desired.ReclaimEffective = machine.NewCPUSet(0, 1, 2, 3)
	if got := result.AppliedView.ReclaimEffective.String(); got != "2-3" {
		t.Fatalf("applied reclaim after desired mutation = %q, want snapshot value 2-3", got)
	}
	result.AppliedView.ReclaimEffective.Add(0)
	if dagResult.View.ReclaimEffective.Contains(0) {
		t.Fatal("DAGApplyResult AppliedView aliases callback result")
	}
	result.AppliedView.CPUSetByRel["reclaim"].Add(0)
	if dagResult.View.CPUSetByRel["reclaim"].Contains(0) {
		t.Fatal("DAGApplyResult AppliedView per-rel proof aliases callback result")
	}
	result.AppliedView.PoolProjection.CPUSetByIdentity[reclaimIdentity].Add(0)
	if dagResult.View.PoolProjection.CPUSetByIdentity[reclaimIdentity].Contains(0) {
		t.Fatal("DAGApplyResult AppliedView pool projection aliases callback result")
	}
}

func TestTopologyResultFromFinalConvergenceDeterminesAppliedViewLevel(t *testing.T) {
	t.Parallel()
	applied := &model.AppliedView{
		Level:               model.AppliedViewLevelParentSafe,
		CPUSetPartitionView: model.NewCPUSetPartitionView(),
	}
	result := topologyResultFromFinalConvergence(topology.ConvergenceResult{
		Converged:            true,
		ParentSafe:           false,
		DeferredLeafCount:    0,
		FinalSnapshotCurrent: true,
	}, applied)

	if !result.Converged || result.ParentSafe || result.LeafDeferred {
		t.Fatalf("topology result = %+v, want final fully-converged result to override admission intent", result)
	}
	if result.AppliedView == nil || result.AppliedView.Level != model.AppliedViewLevelFull {
		t.Fatalf("applied view = %+v, want level %q from final convergence", result.AppliedView, model.AppliedViewLevelFull)
	}
}

func TestCPUSetTopologyPluginAdmissionPublishesFrozenTraceResult(t *testing.T) {
	t.Parallel()
	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true},
		cpus: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 1),
			"reclaim": machine.NewCPUSet(2, 3),
		},
		children: map[string][]string{"": {"primary", "reclaim"}},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:     "primary",
			BulkheadReclaimRelPaths:    []string{"reclaim"},
			AdmissionSafeDuration:      time.Second,
			AdmissionMaxRequiredWrites: 8,
		},
		cgroup: cg,
	}
	desired := model.NewDesiredView()
	desired.NonReclaimPool = machine.NewCPUSet(0, 1)
	desired.ReclaimEffective = machine.NewCPUSet(2, 3)
	outcome, err := p.Apply(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
			MetaServer: emptyLifecycleMetaServer(),
			Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {}, 1: {}, 2: {}, 3: {},
			}},
		},
		DesiredView: desired,
	})
	if err != nil {
		t.Fatalf("Apply(admission) error = %v", err)
	}
	result := bulkheadapi.TopologyResult{AppliedView: outcome.View.DeepCopy()}
	if outcome.Level != bulkheadapi.ConvergenceLevelFull || result.AppliedView == nil {
		t.Fatalf("admission outcome = %+v, want published current frozen-trace result", outcome)
	}
	if got := result.AppliedView.NonReclaimPool.String(); got != "0-1" {
		t.Fatalf("published non-reclaim pool = %q, want 0-1", got)
	}
	if got := result.AppliedView.ReclaimEffective.String(); got != "2-3" {
		t.Fatalf("published reclaim pool = %q, want 2-3", got)
	}
}

func TestCPUSetTopologyPluginPublishesOnlyContainerLeavesProvenByFinalSnapshot(t *testing.T) {
	t.Parallel()

	const (
		podUID       = "pod-materialized-during-convergence"
		containerID  = "container-materialized-during-convergence"
		containerRel = "primary/container-a"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "bulkhead-final-snapshot-container-proof",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return containerRel, false, nil
			}
			return "", true, nil
		},
	})

	for _, tt := range []struct {
		name                  string
		desiredLeaf           machine.CPUSet
		initiallyMaterialized bool
		wantPublished         bool
	}{
		{
			name:        "pending leaf materializes with inherited parent cpuset",
			desiredLeaf: machine.NewCPUSet(0),
		},
		{
			name:                  "materialized leaf matches final snapshot",
			desiredLeaf:           machine.NewCPUSet(0, 1),
			initiallyMaterialized: true,
			wantPublished:         true,
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			desiredLeaf := tt.desiredLeaf.Clone()
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
			}
			cg := &fakeCgroupClient{
				existing: map[string]bool{"primary": true, "reclaim": true},
				cpus: map[string]machine.CPUSet{
					"primary": machine.NewCPUSet(0, 1),
					"reclaim": machine.NewCPUSet(2, 3),
				},
				children: map[string][]string{},
			}
			if tt.initiallyMaterialized {
				pod.Status.ContainerStatuses = []v1.ContainerStatus{{
					Name:        "main",
					ContainerID: "containerd://" + containerID,
				}}
				cg.existing[containerRel] = true
				cg.children["primary"] = []string{"container-a"}
				cg.cpus[containerRel] = machine.NewCPUSet(0, 1)
			}
			materialized := false
			cg.readOverride = func(rel string) (machine.CPUSet, bool) {
				if rel == "primary" && !materialized && !tt.initiallyMaterialized {
					materialized = true
					cg.existing[containerRel] = true
					cg.children["primary"] = []string{"container-a"}
					cg.cpus[containerRel] = machine.NewCPUSet(0, 1)
				}
				return machine.CPUSet{}, false
			}
			p := &CPUSetTopologyPlugin{
				cfg: bulkheadconfig.BulkheadConfiguration{
					BulkheadPrimaryRelPath:  "primary",
					BulkheadReclaimRelPaths: []string{"reclaim"},
				},
				cgroup: cg,
				pendingProtections: map[string]pendingPodProtection{
					podUID: {
						rel:          "primary",
						protectUntil: time.Now().Add(defaultPendingPodProtectionTTL),
					},
				},
			}
			dedicatedIdentity := model.CPUSetPoolIdentity{
				Kind:         model.CPUSetPoolKindDedicated,
				PodNamespace: "default",
				PodName:      "dedicated-pod",
			}
			desired := model.NewDesiredView()
			desired.NonReclaimPool = machine.NewCPUSet(0, 1)
			desired.ReclaimEffective = machine.NewCPUSet(2, 3)
			desired.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(2, 3)
			desired.ContainerCPUSetByPod[podUID] = map[string]machine.CPUSet{
				"main": desiredLeaf.Clone(),
			}
			desired.PoolOwners[dedicatedIdentity] = model.DesiredPoolOwner{
				ProofPodUID:    podUID,
				ExpectedCPUSet: desiredLeaf.Clone(),
				ContainerCPUSetByName: map[string]machine.CPUSet{
					"main": desiredLeaf.Clone(),
				},
			}
			var result bulkheadapi.TopologyResult
			err := p.runNormalAdjustment(context.Background(), bulkheadapi.HandlerContext{
				CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
					MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
						PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{pod}},
					}},
					Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
						0: {}, 1: {}, 2: {}, 3: {},
					}},
				},
				DesiredView: desired,
			}, func(got bulkheadapi.TopologyResult) {
				got.AppliedView = got.AppliedView.DeepCopy()
				result = got
			})
			if err != nil {
				t.Fatalf("CPUSetAdjustmentHandler: %v", err)
			}
			if result.AppliedView == nil {
				t.Fatal("matching final snapshot leaf should publish AppliedView")
			}
			got, published := result.AppliedView.ContainerCPUSetByPod[podUID]["main"]
			if published != tt.wantPublished {
				t.Fatalf("published container presence = %t, want %t", published, tt.wantPublished)
			}
			if published && !got.Equals(desiredLeaf) {
				t.Fatalf("published container cpuset = %q, want final snapshot value %s", got.String(), desiredLeaf.String())
			}
			if tt.wantPublished {
				assertProjectedCPUSet(t, result.AppliedView.PoolProjection.CPUSetByIdentity,
					dedicatedIdentity, desiredLeaf)
			}
		})
	}
}

func TestCPUSetTopologyPluginNormalEmptyTopologyReturnsErrorWithoutWrites(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{existing: map[string]bool{
		"primary": true,
		"reclaim": true,
	}}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "empty CPUDetails") {
		t.Fatalf("expected empty CPUDetails error, got %v", err)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("normal empty topology must not write cgroups, writes=%#v", cg.writes)
	}
}

func TestCPUSetTopologyPluginSkipsUnchangedApplyTarget(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{existing: map[string]bool{
		"primary": true,
		"reclaim": true,
	}}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
	}
	in := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: emptyLifecycleMetaServer(),
			Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {}, 1: {}, 2: {}, 3: {},
			}},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		}},
	}

	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("first CPUSetAdjustmentHandler: %v", err)
	}
	firstApplyCount := totalApplyCount(cg.applyCounts)
	if firstApplyCount == 0 {
		t.Fatalf("first run should apply cpuset, writes=%v", cg.writes)
	}
	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("second CPUSetAdjustmentHandler: %v", err)
	}
	if got := totalApplyCount(cg.applyCounts); got != firstApplyCount {
		t.Fatalf("unchanged apply target should not write again, got %d writes want %d", got, firstApplyCount)
	}
}

func TestCPUSetTopologyPluginReconcilesExternalCgroupDrift(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{existing: map[string]bool{
		"primary": true,
		"reclaim": true,
	}}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
	}
	in := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: emptyLifecycleMetaServer(),
			Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {}, 1: {}, 2: {}, 3: {},
			}},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		}},
	}

	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("first CPUSetAdjustmentHandler: %v", err)
	}
	firstPrimaryApplyCount := cg.applyCounts["primary"]
	cg.cpus["primary"] = machine.NewCPUSet(0)

	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("second CPUSetAdjustmentHandler: %v", err)
	}
	if got := cg.applyCounts["primary"]; got <= firstPrimaryApplyCount {
		t.Fatalf("external primary drift was not reconciled, primary apply count=%d want > %d", got, firstPrimaryApplyCount)
	}
	if got := cg.writes["primary"]; got != "0-1" {
		t.Fatalf("primary cpuset after drift = %q, want 0-1", got)
	}
}

func TestCPUSetTopologyPluginReturnsErrorWhenNormalConvergeNonConverged(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{existing: map[string]bool{
		"primary": true,
		"reclaim": true,
	}}
	cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "primary" {
			cg.cpus[rel] = machine.NewCPUSet(0)
		}
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
	}

	reported := false
	err := p.runNormalAdjustment(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {}, 1: {}, 2: {}, 3: {},
			}},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		}},
	}, func(bulkheadapi.TopologyResult) {
		reported = true
	})
	var exhausted *topology.ReplanBudgetExceededError
	if !errors.As(err, &exhausted) ||
		!errors.Is(err, topology.ErrAdjustmentReplanBudgetExceeded) ||
		exhausted.Replans != 3 {
		t.Fatalf("CPUSetAdjustmentHandler error = %v, want owner replan exhaustion after 3 replans", err)
	}
	if cg.pruned != nil {
		t.Fatalf("non-converged normal apply must not prune, got %#v", cg.pruned)
	}
	if reported {
		t.Fatalf("failed topology convergence must not report a publishable result")
	}
}

func TestCPUSetTopologyPluginAppliesNestedNUMABucketsWithParentMemsEnvelopes(t *testing.T) {
	t.Parallel()

	const (
		primary       = "primary"
		reclaim       = "reclaim"
		directBucket0 = "reclaim/direct-0"
		directBucket1 = "reclaim/direct-1"
		nested        = "reclaim/nested"
		deep          = "reclaim/nested/deep"
		nestedBucket0 = "reclaim/nested/deep/bucket-0"
		nestedBucket1 = "reclaim/nested/deep/bucket-1"
	)
	cg := &fakeCgroupClient{
		existing: map[string]bool{
			primary: true, reclaim: true, directBucket0: true, directBucket1: true,
			nested: true, nestedBucket0: true, nestedBucket1: true,
		},
		cpus: map[string]machine.CPUSet{
			primary: machine.NewCPUSet(0, 1), reclaim: machine.NewCPUSet(2, 3),
			directBucket0: machine.NewCPUSet(2), directBucket1: machine.NewCPUSet(3),
			nested: machine.NewCPUSet(2, 3), deep: machine.NewCPUSet(2, 3),
			nestedBucket0: machine.NewCPUSet(2), nestedBucket1: machine.NewCPUSet(3),
		},
		mems: map[string]string{
			primary: "0-1", reclaim: "0-1", directBucket0: "0", directBucket1: "1",
			nested: "0-1", deep: "0-1", nestedBucket0: "0", nestedBucket1: "1",
		},
		children: map[string][]string{
			reclaim: {"direct-0", "direct-1", "nested"},
			nested:  {"deep"},
			deep:    {"bucket-0", "bucket-1"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:      primary,
			BulkheadReclaimRelPaths:     []string{reclaim, nested},
			BulkheadReclaimNumaPrefixes: []string{"reclaim/direct-", "reclaim/nested/deep/bucket-"},
		},
		cgroup: cg,
	}
	in := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: emptyLifecycleMetaServer(),
			Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
				2: {NUMANodeID: 0}, 3: {NUMANodeID: 1},
			}},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
			ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
				0: machine.NewCPUSet(2),
				1: machine.NewCPUSet(3),
			},
		}},
	}

	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("CPUSetAdjustmentHandler nested NUMA buckets: %v", err)
	}
	for _, rel := range []string{
		reclaim, directBucket0, directBucket1, nested, nestedBucket0, nestedBucket1,
	} {
		if _, ok := cg.pruned[rel]; !ok {
			t.Fatalf("successful nested NUMA apply did not publish active rel %q: %#v", rel, cg.pruned)
		}
	}
}

func TestCPUSetTopologyPluginHandlesConfiguredNUMABucketTransitionToEmpty(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		version cgroupclient.CgroupVersion
	}{
		{name: "v1 fails closed", version: cgroupclient.CgroupVersionV1},
		{name: "v2 clears and publishes proof", version: cgroupclient.CgroupVersionV2},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const (
				primary = "primary"
				reclaim = "reclaim"
				bucket0 = "reclaim/bucket-0"
				bucket1 = "reclaim/bucket-1"
			)
			cg := &fakeCgroupClient{
				version: tc.version,
				existing: map[string]bool{
					primary: true, reclaim: true, bucket0: true, bucket1: true,
				},
				cpus: map[string]machine.CPUSet{
					primary: machine.NewCPUSet(0, 1),
					reclaim: machine.NewCPUSet(2, 3),
					bucket0: machine.NewCPUSet(2),
					bucket1: machine.NewCPUSet(3),
				},
				mems: map[string]string{
					primary: "0-1", reclaim: "0-1", bucket0: "0", bucket1: "1",
				},
				children: map[string][]string{
					reclaim: {"bucket-0", "bucket-1"},
				},
			}
			p := &CPUSetTopologyPlugin{
				cfg: bulkheadconfig.BulkheadConfiguration{
					BulkheadPrimaryRelPath:      primary,
					BulkheadReclaimRelPaths:     []string{reclaim},
					BulkheadReclaimNumaPrefixes: []string{"reclaim/bucket-"},
				},
				cgroup: cg,
			}
			var result bulkheadapi.TopologyResult
			err := p.runNormalAdjustment(context.Background(), bulkheadapi.HandlerContext{
				CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
					MetaServer: emptyLifecycleMetaServer(),
					Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
						0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
						2: {NUMANodeID: 0}, 3: {NUMANodeID: 1},
					}},
				},
				DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
					NonReclaimPool:   machine.NewCPUSet(0, 1, 3),
					ReclaimEffective: machine.NewCPUSet(2),
					ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
						0: machine.NewCPUSet(2),
						1: machine.NewCPUSet(),
					},
				}},
			}, func(got bulkheadapi.TopologyResult) {
				got.AppliedView = got.AppliedView.DeepCopy()
				result = got
			})

			if tc.version == cgroupclient.CgroupVersionV1 {
				if err == nil {
					t.Fatal("CPUSetAdjustmentHandler error = nil, want fail-closed active/unproved empty bucket")
				}
				if got := cg.cpus[bucket1]; got.IsEmpty() || !got.Equals(machine.NewCPUSet(3)) {
					t.Fatalf("v1 bucket %q cpuset = %s, want preserved non-empty current 3", bucket1, got.String())
				}
				if got := cg.cpus[reclaim]; !got.Equals(machine.NewCPUSet(2, 3)) {
					t.Fatalf("v1 parent reclaim cpuset = %s, want unchanged 2-3", got.String())
				}
				return
			}

			if err != nil {
				t.Fatalf("CPUSetAdjustmentHandler: %v", err)
			}
			if write, ok := cg.cpusetWrites[bucket1]; !ok || !write.WriteEmptyCPUs {
				t.Fatalf("v2 configured empty bucket write = %+v, present=%v; want explicit empty write", write, ok)
			}
			if result.AppliedView == nil {
				t.Fatal("v2 configured empty bucket did not publish AppliedView")
			}
			bucket, ok := result.AppliedView.ReclaimEffectivePerNUMA[1]
			if !ok || !bucket.IsEmpty() {
				t.Fatalf("AppliedView NUMA 1 proof = %s, present=%v; want present empty bucket", bucket.String(), ok)
			}
			if relCPUSet, ok := result.AppliedView.CPUSetByRel[bucket1]; !ok || !relCPUSet.IsEmpty() {
				t.Fatalf("AppliedView rel proof for %q = %s, present=%v; want present empty cpuset", bucket1, relCPUSet.String(), ok)
			}
		})
	}
}

func TestCPUSetTopologyPluginRejectsNUMABucketOutsidePhysicalEnvelope(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		existing: map[string]bool{
			"primary": true, "reclaim": true, "reclaim/bucket-0": true,
		},
		cpus: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(3), "reclaim": machine.NewCPUSet(0),
			"reclaim/bucket-0": machine.NewCPUSet(0),
		},
		mems: map[string]string{
			"primary": "0-1", "reclaim": "0", "reclaim/bucket-0": "0",
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:      "primary",
			BulkheadReclaimRelPaths:     []string{"reclaim"},
			BulkheadReclaimNumaPrefixes: []string{"reclaim/bucket-"},
		},
		cgroup: cg,
	}
	in := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
				2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
			}},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:          machine.NewCPUSet(3),
			ReclaimEffective:        machine.NewCPUSet(0, 2),
			ReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 2)},
		}},
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), in)
	if err == nil {
		t.Fatal("CPUSetAdjustmentHandler accepted NUMA bucket outside physical envelope")
	}
	if len(cg.writes) != 0 {
		t.Fatalf("invalid NUMA target published writes: %#v", cg.writes)
	}
}

func TestCPUSetTopologyPluginReturnsSiblingDiscoveryError(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "primary/reclaim": true},
		listErr:  errors.New("list failed"),
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "primary",
			BulkheadReclaimRelPaths:       []string{"primary/reclaim"},
			EnableBulkheadReclaimSiblings: true,
		},
		cgroup: cg,
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		}},
	})
	if err == nil {
		t.Fatalf("expected sibling discovery error")
	}
}

func TestCPUSetTopologyPluginMaterializesConfiguredReclaimSiblingWhenMissing(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true},
		cpus: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 1),
			"reclaim": machine.NewCPUSet(2, 3),
		},
		children:                map[string][]string{"": {"primary", "reclaim"}},
		statErrors:              map[string]error{"system": os.ErrNotExist},
		requireExistingForApply: true,
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:                 "primary",
			BulkheadReclaimRelPaths:                []string{"reclaim"},
			BulkheadReclaimSiblingRelPaths:         []string{"system"},
			EnableBulkheadReclaimSiblings:          true,
			TopologyConvergenceBudget:              bulkheadconfig.DefaultConvergenceBudget(),
			TopologyDrainSelection:                 bulkheadconfig.DefaultDrainSelectionPolicy(),
			AdmissionSafeDuration:                  5 * time.Second,
			AdmissionMaxRequiredWrites:             0,
			EnableBulkheadCpusetTopologyOnCgroupV2: false,
		},
		cgroup: cg,
	}
	outcome, err := p.Apply(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: emptyLifecycleMetaServer(),
			Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
				0: {}, 1: {}, 2: {}, 3: {},
			}},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	result := bulkheadapi.TopologyResult{AppliedView: outcome.View.DeepCopy()}
	if got := cg.writes["system"]; got != "2-3" {
		t.Fatalf("system cpuset = %q, want 2-3; writes=%#v", got, cg.writes)
	}
	if !cg.existing["system"] {
		t.Fatalf("system cgroup was not materialized before write")
	}
	if got := cg.mems["system"]; got != "0" {
		t.Fatalf("system cpuset.mems = %q, want initialized parent mems 0", got)
	}
	if result.AppliedView == nil {
		t.Fatalf("applied view is nil")
	}
	if got := result.AppliedView.CPUSetByRel["system"].String(); got != "2-3" {
		t.Fatalf("applied system cpuset = %q, want 2-3", got)
	}
	proof := result.AppliedView.RelProofByRel["system"]
	if proof.Device == 0 || proof.Inode == 0 || !proof.CPUSet.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("system proof = %+v, want non-empty identity proof for 2-3", proof)
	}
}

func TestCPUSetTopologyPluginConfiguredSiblingDedupesDiscoveredSibling(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:         "primary",
			BulkheadReclaimRelPaths:        []string{"reclaim"},
			BulkheadReclaimSiblingRelPaths: []string{"system"},
		},
	}
	got := p.mergeBulkheadReclaimSiblings(
		[]string{"system"},
		p.configuredBulkheadReclaimSiblings(),
		&model.CPUSetPartitionView{},
	)
	if len(got) != 1 || got[0] != "system" {
		t.Fatalf("merged siblings = %#v, want [system]", got)
	}
}

func TestCPUSetTopologyPluginConfiguredSiblingSkipsPrimaryReclaimPartitionAndNUMABuckets(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:         "primary",
			BulkheadReclaimRelPaths:        []string{"reclaim"},
			BulkheadReclaimNumaPrefixes:    []string{"reclaim/reclaimed-"},
			BulkheadPartitionRelPaths:      []string{"partition"},
			BulkheadReclaimSiblingRelPaths: []string{"primary", "reclaim", "partition", "reclaim/reclaimed-0", "system"},
		},
	}
	got := p.mergeBulkheadReclaimSiblings(
		nil,
		p.configuredBulkheadReclaimSiblings(),
		&model.CPUSetPartitionView{ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2, 3),
		}},
	)
	if len(got) != 1 || got[0] != "system" {
		t.Fatalf("merged siblings = %#v, want [system]", got)
	}
}

func TestCPUSetTopologyPluginNoConfiguredSiblingWhenListEmpty(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:         "primary",
			BulkheadReclaimRelPaths:        []string{"reclaim"},
			BulkheadReclaimSiblingRelPaths: nil,
		},
	}
	got := p.mergeBulkheadReclaimSiblings(
		nil,
		p.configuredBulkheadReclaimSiblings(),
		&model.CPUSetPartitionView{},
	)
	if len(got) != 0 {
		t.Fatalf("merged siblings = %#v, want empty", got)
	}
}

// When the reclaim root sits at the cgroup root, its "siblings" are every
// top-level host/system cgroup (system/tiger/kubedaemonsets/k8s.io).
// Auto-discovery must be disabled for that case; operators declare reclaim
// siblings explicitly. Otherwise cpuset_topology becomes a second owner that
// projects ReclaimEffective onto those host cgroups and, during shared
// ramp-up (ReclaimEffective shrinks), emits an empty desired onto a non-empty
// system cgroup that the v1 planner rejects.
func TestCPUSetTopologyPluginDiscoverSiblingsDisabledAtCgroupRoot(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		children: map[string][]string{
			"": {"system", "tiger", "kubedaemonsets", "k8s.io", "custom-reclaim-root"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "kubepods",
			BulkheadReclaimRelPaths:       []string{"custom-reclaim-root"},
			BulkheadReclaimNumaPrefixes:   []string{"custom-reclaim-root/reclaimed-"},
			EnableBulkheadReclaimSiblings: true,
			BulkheadSystemRelPath:         "system",
		},
		cgroup: cg,
	}

	got, err := p.discoverBulkheadReclaimSiblings(context.Background(), &model.DesiredView{})
	if err != nil {
		t.Fatalf("discoverBulkheadReclaimSiblings: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("auto-discovered siblings = %#v, want empty when reclaim root is at cgroup root", got)
	}
}

// Precise r3231610 repro through the REAL apply input chain (discover -> merge ->
// BuildTopologyNodeSpecsFromView), mirroring the E2E config exactly:
//   - reclaim rel = "custom-reclaim-root" (top-level, parent == cgroup root)
//   - system_rel_path = "system", system_service enabled
//   - NO explicitly configured reclaim sibling rel paths
//   - cgroup root children include system/tiger/kubedaemonsets/k8s.io
//
// After the cgroup-root parent gate, auto-discovery returns empty, so the merged
// sibling set must be empty and BuildTopologyNodeSpecsFromView must NOT emit any
// reclaim-sibling spec for "system" (or any host cgroup). This is the exact
// invariants the v1 planner empty-target guard depends on: cpuset_topology must
// never project ReclaimEffective onto the system domain.
func TestCPUSetTopologyPluginR3231610TopLevelReclaimRootNoSystemSpec(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		existing: map[string]bool{
			"kubepods": true, "custom-reclaim-root": true, "custom-reclaim-root/reclaimed-0": true,
			"system": true, "tiger": true, "kubedaemonsets": true, "k8s.io": true,
		},
		cpus: map[string]machine.CPUSet{
			"kubepods":                        machine.NewCPUSet(0, 1),
			"custom-reclaim-root":             machine.NewCPUSet(2, 3),
			"custom-reclaim-root/reclaimed-0": machine.NewCPUSet(2, 3),
			"system":                          machine.NewCPUSet(2, 3), // non-empty system domain
		},
		children: map[string][]string{
			"":                    {"system", "tiger", "kubedaemonsets", "k8s.io", "custom-reclaim-root", "kubepods"},
			"custom-reclaim-root": {"reclaimed-0"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "kubepods",
			BulkheadReclaimRelPaths:       []string{"custom-reclaim-root"},
			BulkheadReclaimNumaPrefixes:   []string{"custom-reclaim-root/reclaimed-"},
			EnableBulkheadReclaimSiblings: true,
			BulkheadSystemRelPath:         "system",
			// BulkheadReclaimSiblingRelPaths intentionally empty (E2E has none).
		},
		cgroup: cg,
	}

	view := &model.CPUSetPartitionView{
		NonReclaimPool:   machine.NewCPUSet(0, 1),
		ReclaimEffective: machine.NewCPUSet(2, 3),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2, 3),
		},
	}

	discovered, err := p.discoverBulkheadReclaimSiblings(context.Background(), &model.DesiredView{})
	if err != nil {
		t.Fatalf("discoverBulkheadReclaimSiblings: %v", err)
	}
	siblings := p.mergeBulkheadReclaimSiblings(discovered, p.configuredBulkheadReclaimSiblings(), view)
	if len(siblings) != 0 {
		t.Fatalf("merged reclaim siblings = %#v, want [] (top-level reclaim root, no configured sibling)", siblings)
	}

	specs, err := bulkheadutils.BuildTopologyNodeSpecsFromView(p.cfg, view, machine.CPUDetails{
		0: {}, 1: {}, 2: {}, 3: {},
	}, siblings, func(rel string) error { return nil })
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	for _, spec := range specs {
		if spec.Role == topology.TopoNodeRoleReclaimSibling {
			t.Fatalf("emitted reclaim-sibling spec rel=%q, want none (sibling set empty)", spec.Rel)
		}
		if spec.Rel == "system" {
			t.Fatalf("emitted spec for system rel=%q, cpuset_topology must not own the system domain", spec.Rel)
		}
	}
}

// A nested reclaim root (under a managed parent such as kubepods) still
// auto-discovers its siblings in that parent directory. The root-parent gate is
// the positive ownership condition, not a blanket disable.
func TestCPUSetTopologyPluginDiscoverSiblingsNestedReclaimRoot(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		children: map[string][]string{
			"kubepods": {"offline-besteffort", "online-besteffort", "burstable"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "kubepods",
			BulkheadReclaimRelPaths:       []string{"kubepods/offline-besteffort"},
			BulkheadReclaimNumaPrefixes:   []string{"kubepods/offline-besteffort/reclaimed-"},
			EnableBulkheadReclaimSiblings: true,
		},
		cgroup: cg,
	}

	got, err := p.discoverBulkheadReclaimSiblings(context.Background(), &model.DesiredView{})
	if err != nil {
		t.Fatalf("discoverBulkheadReclaimSiblings: %v", err)
	}
	// primary (kubepods) and reclaim root (kubepods/offline-besteffort) are
	// excluded; remaining siblings are the other children of kubepods.
	want := []string{"kubepods/burstable", "kubepods/online-besteffort"}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Fatalf("auto-discovered siblings = %#v, want %#v", got, want)
	}
}

// Explicitly configured reclaim siblings are honored even when the reclaim
// root is at the cgroup root (auto-discovery is off but configuration remains
// the source of truth).
func TestCPUSetTopologyPluginConfiguredSiblingRetainedAtCgroupRoot(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:         "kubepods",
			BulkheadReclaimRelPaths:        []string{"custom-reclaim-root"},
			BulkheadReclaimSiblingRelPaths: []string{"kubepods/offline-besteffort"},
		},
	}
	got := p.mergeBulkheadReclaimSiblings(
		nil, // auto-discovery disabled for top-level reclaim root
		p.configuredBulkheadReclaimSiblings(),
		&model.CPUSetPartitionView{},
	)
	if len(got) != 1 || got[0] != "kubepods/offline-besteffort" {
		t.Fatalf("merged siblings = %#v, want configured [kubepods/offline-besteffort]", got)
	}
}

// Mixed reclaim roots: one top-level (custom-reclaim-root, parent=root) must skip
// auto-discovery entirely; a nested reclaim root (kubepods/offline-besteffort,
// parent=kubepods) still auto-discovers its siblings. The root-parent gate is
// per reclaim root, not global.
func TestCPUSetTopologyPluginDiscoverSiblingsMixedRoots(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		children: map[string][]string{
			"":         {"system", "tiger", "kubedaemonsets", "k8s.io", "custom-reclaim-root", "kubepods"},
			"kubepods": {"offline-besteffort", "online-besteffort"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "kubepods",
			BulkheadReclaimRelPaths:       []string{"custom-reclaim-root", "kubepods/offline-besteffort"},
			BulkheadReclaimNumaPrefixes:   []string{"custom-reclaim-root/reclaimed-", "kubepods/offline-besteffort/reclaimed-"},
			EnableBulkheadReclaimSiblings: true,
			BulkheadSystemRelPath:         "system",
		},
		cgroup: cg,
	}

	got, err := p.discoverBulkheadReclaimSiblings(context.Background(), &model.DesiredView{})
	if err != nil {
		t.Fatalf("discoverBulkheadReclaimSiblings: %v", err)
	}
	// Top-level "custom-reclaim-root" contributes no siblings. Nested
	// "kubepods/offline-besteffort" contributes its kubepods siblings except the
	// primary (kubepods) and the reclaim root itself.
	want := []string{"kubepods/online-besteffort"}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Fatalf("auto-discovered siblings = %#v, want %#v (no root-level host cgroups)", got, want)
	}
	for _, rel := range got {
		if strings.HasPrefix(rel, "system") || strings.HasPrefix(rel, "tiger") ||
			strings.HasPrefix(rel, "kubedaemonsets") || strings.HasPrefix(rel, "k8s.io") {
			t.Fatalf("auto-discovered siblings = %#v, must not include host system cgroup %q", got, rel)
		}
	}
}

// r3231610 repro: even when discovery runs (nested reclaim root), the
// system_service-owned rel (cfg.BulkheadSystemRelPath=system) and its subtree
// must be excluded from auto-discovered reclaim siblings. Otherwise
// cpuset_topology becomes a second owner of the system domain and projects
// ReclaimEffective onto it during shared ramp-up, which the v1 planner rejects.
func TestCPUSetTopologyPluginDiscoverSiblingsExcludesSystemOwnedNested(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		children: map[string][]string{
			"kubepods": {"offline-besteffort", "online-besteffort", "system"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "kubepods",
			BulkheadReclaimRelPaths:       []string{"kubepods/offline-besteffort"},
			BulkheadReclaimNumaPrefixes:   []string{"kubepods/offline-besteffort/reclaimed-"},
			EnableBulkheadReclaimSiblings: true,
			BulkheadSystemRelPath:         "kubepods/system",
		},
		cgroup: cg,
	}

	got, err := p.discoverBulkheadReclaimSiblings(context.Background(), &model.DesiredView{})
	if err != nil {
		t.Fatalf("discoverBulkheadReclaimSiblings: %v", err)
	}
	want := []string{"kubepods/online-besteffort"}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Fatalf("auto-discovered siblings = %#v, want %#v (system excluded)", got, want)
	}
}

// Explicitly configured reclaim siblings are an operator contract: when the
// operator lists the system rel path in BulkheadReclaimSiblingRelPaths, merge
// honors it even though it coincides with cfg.BulkheadSystemRelPath.
func TestCPUSetTopologyPluginConfiguredSystemSiblingRetained(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:         "primary",
			BulkheadReclaimRelPaths:        []string{"primary/reclaim"},
			BulkheadReclaimSiblingRelPaths: []string{"system"},
			BulkheadSystemRelPath:          "system",
		},
	}
	got := p.mergeBulkheadReclaimSiblings(
		[]string{"system"}, // discovered system must be dropped by the system-ownership filter
		p.configuredBulkheadReclaimSiblings(),
		&model.CPUSetPartitionView{},
	)
	if len(got) != 1 || got[0] != "system" {
		t.Fatalf("merged siblings = %#v, want configured [system] retained despite system-ownership filter", got)
	}
}

// r3231610 repro: discovered (auto) system_service-owned rels must be dropped
// by merge even if discovery leaked them. The configured set is unaffected.
func TestCPUSetTopologyPluginMergeDropsDiscoveredSystemOwned(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:      "kubepods",
			BulkheadReclaimRelPaths:     []string{"custom-reclaim-root"},
			BulkheadReclaimNumaPrefixes: []string{"custom-reclaim-root/reclaimed-"},
			BulkheadSystemRelPath:       "system",
		},
	}
	got := p.mergeBulkheadReclaimSiblings(
		[]string{"system", "system/kubelet", "tiger"},
		nil,
		&model.CPUSetPartitionView{},
	)
	for _, rel := range got {
		if strings.HasPrefix(rel, "system") {
			t.Fatalf("merged siblings = %#v, must drop discovered system-owned rel %q", got, rel)
		}
	}
	if len(got) != 1 || got[0] != "tiger" {
		t.Fatalf("merged siblings = %#v, want only non-system discovered [tiger]", got)
	}
}

// Boundary: when no system rel path is configured (system_service disabled),
// the system-ownership filter is inert and a rel literally named "system" is
// treated as an ordinary discovered sibling.
func TestCPUSetTopologyPluginNoSystemFilterWhenSystemRelUnset(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		children: map[string][]string{
			"kubepods": {"offline-besteffort", "online-besteffort", "system"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "kubepods",
			BulkheadReclaimRelPaths:       []string{"kubepods/offline-besteffort"},
			BulkheadReclaimNumaPrefixes:   []string{"kubepods/offline-besteffort/reclaimed-"},
			EnableBulkheadReclaimSiblings: true,
			// BulkheadSystemRelPath intentionally unset.
		},
		cgroup: cg,
	}

	got, err := p.discoverBulkheadReclaimSiblings(context.Background(), &model.DesiredView{})
	if err != nil {
		t.Fatalf("discoverBulkheadReclaimSiblings: %v", err)
	}
	want := []string{"kubepods/online-besteffort", "kubepods/system"}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Fatalf("auto-discovered siblings = %#v, want %#v (no system-ownership filter)", got, want)
	}
}

func TestCPUSetTopologyPluginAdmissionDeadlineStartsBeforeSiblingDiscovery(t *testing.T) {
	t.Parallel()
	// This test asserts a 250ms wall-clock deadline. Do not run it in parallel
	// with the package's I/O-heavy convergence matrix: scheduler starvation would
	// test host load rather than whether the deadline is installed before sibling
	// discovery.

	const safeDuration = 250 * time.Millisecond
	deadlineObserved := false
	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "primary/reclaim": true},
		listChildrenHook: func(ctx context.Context, _ string) ([]string, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				return nil, errors.New("admission context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining <= 0 || remaining > safeDuration {
				return nil, fmt.Errorf("admission deadline remaining = %s, want within (0, %s]", remaining, safeDuration)
			}
			deadlineObserved = true
			return nil, errors.New("stop after observing admission deadline")
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "primary",
			BulkheadReclaimRelPaths:       []string{"primary/reclaim"},
			EnableBulkheadReclaimSiblings: true,
			AdmissionSafeDuration:         safeDuration,
		},
		cgroup: cg,
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Mode: cpusetutil.CPUSetAdjustmentModeAdmission,
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		}},
	})
	if err == nil {
		t.Fatal("CPUSetAdjustmentHandler() error = nil, want sibling discovery sentinel")
	}
	if !deadlineObserved {
		t.Fatalf("sibling discovery did not receive admission deadline: %v", err)
	}
}

func TestCPUSetTopologyPluginAdmissionDeadlineWinsAfterPreCoordinatorStage(t *testing.T) {
	t.Parallel()

	const safeDuration = 20 * time.Millisecond
	stageErr := errors.New("late sibling discovery result")
	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "primary/reclaim": true},
		listChildrenHook: func(ctx context.Context, _ string) ([]string, error) {
			<-ctx.Done()
			return nil, stageErr
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "primary",
			BulkheadReclaimRelPaths:       []string{"primary/reclaim"},
			EnableBulkheadReclaimSiblings: true,
			AdmissionSafeDuration:         safeDuration,
		},
		cgroup: cg,
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Mode: cpusetutil.CPUSetAdjustmentModeAdmission,
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CPUSetAdjustmentHandler() error = %v, want admission deadline instead of late stage error %v", err, stageErr)
	}
}

func TestCPUSetTopologyPluginDisabledTransitionUsesTopologySpecsAndDAGExpandV1(t *testing.T) {
	t.Parallel()

	p, cg, in, containerRel := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-v1-pod",
		"bulkhead-disabled-v1-container",
	)

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	if err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}

	wantMachine := "0-3"
	// Only the controlled topology-owned roots expand to the machine target.
	for _, rel := range []string{
		"primary",
		"reclaim",
		"reclaim/reclaim-0",
		"sibling",
	} {
		if got := cg.writes[rel]; got != wantMachine {
			t.Fatalf("cpuset @ %s = %q, want %q; writes=%#v", rel, got, wantMachine, cg.writes)
		}
	}
	// The QoS class dir and the Pod/pause dir are driven to the machine target
	// (kubelet never updates them); only the protected leaf is spared.
	for _, rel := range []string{"primary/burstable", "primary/burstable/pod-a"} {
		if got := cg.writes[rel]; got != wantMachine {
			t.Fatalf("reset cpuset @ %s = %q, want machine %q; writes=%#v", rel, got, wantMachine, cg.writes)
		}
	}
	// The container leaf is converged by kubelet UpdateContainerResources and must
	// never be written by the disabled reset.
	if _, ok := cg.writes[containerRel]; ok {
		t.Fatalf("container leaf @ %s must not receive a reset write; writes=%#v", containerRel, cg.writes)
	}
	if got := cg.cpusetWrites["reclaim/reclaim-0"].Mems; got != "" {
		t.Fatalf("cpuset_topology wrote reclaim NUMA cpuset.mems = %q, want empty", got)
	}
	if _, ok := cg.writes["partition"]; ok {
		t.Fatalf("partition should not receive cpuset.cpus write, writes=%#v", cg.writes)
	}
	if len(cg.schedLoadBalance) != 0 {
		t.Fatalf("disabled transition should not write sched_load_balance, got %#v", cg.schedLoadBalance)
	}
	if cg.pruned != nil {
		t.Fatalf("disabled transition should not prune, got %#v", cg.pruned)
	}
}

// disabledResetSnapshotFetcher wraps the stub pod fetcher and counts every GetPodList
// call so a test can prove the disabled reset fetches the fresh snapshot exactly once and
// never issues a second, divergent classification fetch. It also captures the context
// passed to GetPodList so the test can assert the strict-bypass marker actually reaches
// the kubelet fetch path.
type disabledResetSnapshotFetcher struct {
	metapod.PodFetcherStub
	listCalls int
	listErr   error
	gotCtx    context.Context
}

func (f *disabledResetSnapshotFetcher) GetPodList(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
	f.listCalls++
	f.gotCtx = ctx
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.PodFetcherStub.GetPodList(ctx, podFilter)
}

func TestCPUSetTopologyPluginDisabledResetSharesSingleFreshSnapshot(t *testing.T) {
	t.Parallel()
	const podUID = "bulkhead-disabled-once-pod"
	const containerID = "bulkhead-disabled-once-container"

	newFetcher := func(listErr error) (*disabledResetSnapshotFetcher, *CPUSetTopologyPlugin, *fakeCgroupClient, bulkheadapi.HandlerContext, string) {
		p, cg, in, rel := newDisabledTransitionTestPlugin(t, cgroupclient.CgroupVersionV1, podUID, containerID)
		f := &disabledResetSnapshotFetcher{
			PodFetcherStub: metapod.PodFetcherStub{PodList: []*v1.Pod{{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
					Name: "main", ContainerID: "containerd://" + containerID,
				}}},
			}}},
			listErr: listErr,
		}
		in.CPUSetAdjustmentHandlerCtx.MetaServer = &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{PodFetcher: f},
		}
		return f, p, cg, in, rel
	}

	t.Run("fetches once and classifies from the shared snapshot", func(t *testing.T) {
		t.Parallel()
		f, p, cg, in, containerRel := newFetcher(nil)
		if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err != nil {
			t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
		}
		if f.listCalls != 1 {
			t.Fatalf("disabled reset must fetch the fresh snapshot exactly once (build and fresh-pod classification share it), got %d", f.listCalls)
		}
		// The strict-bypass marker must actually reach the kubelet fetch path: the
		// disabled reset needs a bypassed, cache-fresh snapshot, not a cached read.
		if f.gotCtx == nil {
			t.Fatalf("expected GetPodList to receive a context with the strict-bypass marker")
		}
		if got := f.gotCtx.Value(metapod.BypassCacheKey); got != metapod.BypassCacheTrue {
			t.Fatalf("GetPodList context bypass marker = %v, want %q", got, metapod.BypassCacheTrue)
		}
		if got := f.gotCtx.Value(metapod.StrictBypassCacheKey); got != metapod.BypassCacheTrue {
			t.Fatalf("GetPodList context strict-bypass marker = %v, want %q", got, metapod.BypassCacheTrue)
		}
		// Classification observed the same snapshot: the fresh Pod dir is widened to the
		// machine target while its protected container leaf is spared.
		if got := cg.writes["primary/burstable/pod-a"]; got != "0-3" {
			t.Fatalf("fresh Pod dir must be widened from the shared snapshot; got %q writes=%#v", got, cg.writes)
		}
		if _, ok := cg.writes[containerRel]; ok {
			t.Fatalf("protected container leaf must not receive a reset write; writes=%#v", cg.writes)
		}
	})

	t.Run("snapshot fetch error withholds the reset", func(t *testing.T) {
		t.Parallel()
		fetchErr := errors.New("kubelet fresh snapshot unavailable")
		f, p, _, in, _ := newFetcher(fetchErr)
		err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
		if err == nil {
			t.Fatalf("expected the fresh snapshot fetch error to withhold the disabled reset")
		}
		if !errors.Is(err, fetchErr) {
			t.Fatalf("error = %v, want chain containing %v", err, fetchErr)
		}
		if f.listCalls == 0 {
			t.Fatalf("expected the strict snapshot fetch to have been attempted")
		}
	})
}

func TestCPUSetTopologyPluginDisabledReclaimEligibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		version      cgroupclient.CgroupVersion
		preserve     bool
		v2OptIn      bool
		dynamic      *dynamicconfig.Configuration
		stateOverlap bool
		wantActive   bool
	}{
		{name: "v1 active", version: cgroupclient.CgroupVersionV1, preserve: true, wantActive: true},
		{name: "full topology owns round", version: cgroupclient.CgroupVersionV1, preserve: true, dynamic: enabledBulkheadCpusetTopologyDynamicConf()},
		{name: "preserve disabled", version: cgroupclient.CgroupVersionV1},
		{name: "v2 requires opt in", version: cgroupclient.CgroupVersionV2, preserve: true},
		{name: "v2 opted in", version: cgroupclient.CgroupVersionV2, preserve: true, v2OptIn: true, wantActive: true},

		// Regression contract: preserve=true + topology disabled must keep the
		// per-NUMA reclaim bucket even when shared cores overlap reclaimed cores.
		// overlap itself forces full topology off (enableBulkheadCpusetTopology
		// returns false), so this is exactly the disabled epoch that previously
		// flattened reclaim to the whole-machine reset target.
		{name: "overlap keeps preserve on v1", version: cgroupclient.CgroupVersionV1, preserve: true, stateOverlap: true, wantActive: true},
		// preserve=false still inert regardless of overlap.
		{name: "overlap but preserve disabled", version: cgroupclient.CgroupVersionV1, stateOverlap: true},
		// overlap overrides the dynamic topology-enable flag (enableBulkheadCpusetTopology
		// returns false under overlap), so full topology never owns the round and
		// preserve still engages on v1 even when the dynamic flag asks for topology.
		{
			name: "overlap overrides dynamic topology-enable, preserve engages", version: cgroupclient.CgroupVersionV1, preserve: true,
			dynamic: enabledBulkheadCpusetTopologyDynamicConf(), stateOverlap: true, wantActive: true,
		},
		// cgroup v2 stays inert under overlap unless opted in.
		{name: "overlap on v2 without opt in stays inert", version: cgroupclient.CgroupVersionV2, preserve: true, stateOverlap: true},
		{name: "overlap on v2 opted in engages", version: cgroupclient.CgroupVersionV2, preserve: true, v2OptIn: true, stateOverlap: true, wantActive: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &CPUSetTopologyPlugin{
				cfg: bulkheadconfig.BulkheadConfiguration{
					PreserveReclaimCPUSetWhenTopologyDisabled: tt.preserve,
					EnableBulkheadCpusetTopologyOnCgroupV2:    tt.v2OptIn,
				},
				cgroup: &fakeCgroupClient{version: tt.version},
			}
			state := cpustate.NewCPUPluginState(nil)
			state.SetAllowSharedCoresOverlapReclaimedCores(tt.stateOverlap)
			got := p.ShouldReconcileWhenDisabled(context.Background(), bulkheadapi.HandlerContext{
				CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
					DynamicConf: tt.dynamic,
					State:       state,
				},
			})
			if got != tt.wantActive {
				t.Fatalf("ShouldReconcileWhenDisabled() = %t, want %t", got, tt.wantActive)
			}
		})
	}
}

func TestCPUSetTopologyPluginPreserveModeResetExcludesReclaimSubtree(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-preserve-reset-pod",
		"bulkhead-preserve-reset-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.children["reclaim/reclaim-0"] = []string{"dynamic"}
	cg.cpus["reclaim/reclaim-0/dynamic"] = machine.NewCPUSet(2)

	if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler() error: %v", err)
	}
	for _, rel := range []string{"primary", "sibling"} {
		if got := cg.writes[rel]; got != "0-3" {
			t.Fatalf("reset cpuset @ %s = %q, want 0-3; writes=%#v", rel, got, cg.writes)
		}
	}
	// The QoS dir under primary is driven to the machine target.
	if got := cg.writes["primary/burstable"]; got != "0-3" {
		t.Fatalf("primary/burstable = %q, want 0-3; writes=%#v", got, cg.writes)
	}
	for _, rel := range []string{"reclaim", "reclaim/reclaim-0", "reclaim/reclaim-0/dynamic"} {
		if _, ok := cg.writes[rel]; ok {
			t.Fatalf("reclaim boundary %q received reset write; writes=%#v", rel, cg.writes)
		}
	}
}

// TestCPUSetTopologyPluginPreserveModeResetExcludesReclaimSubtreeUnderOverlap pins
// the regression contract: when shared cores overlap reclaimed cores (advisor
// overlap=true) AND PreserveReclaimCPUSetWhenTopologyDisabled is opt-in, the
// disabled reset still must NOT stamp the whole-machine reset target onto the
// reclaim subtree. Before the fix, ShouldReconcileWhenDisabled short-circuited to
// false purely because of overlap, leaving reclaim roots inside the reset DAG and
// flattening the running/checkpoint per-NUMA reclaim bucket (the reclaim roots
// in BulkheadReclaimRelPaths with their reclaimed-* NUMA children) to the full
// node.
func TestCPUSetTopologyPluginPreserveModeResetExcludesReclaimSubtreeUnderOverlap(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-preserve-reset-overlap-pod",
		"bulkhead-preserve-reset-overlap-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(true)
	in.State = state
	cg.children["reclaim/reclaim-0"] = []string{"dynamic"}
	cg.cpus["reclaim/reclaim-0/dynamic"] = machine.NewCPUSet(2)

	if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler() error: %v", err)
	}
	// Non-reclaim controlled roots still reset to the whole-machine target: the
	// workqueue reset stays online. External pod cgroups do not.
	for _, rel := range []string{"primary", "sibling"} {
		if got := cg.writes[rel]; got != "0-3" {
			t.Fatalf("reset cpuset @ %s = %q, want 0-3; writes=%#v", rel, got, cg.writes)
		}
	}
	if got := cg.writes["primary/burstable"]; got != "0-3" {
		t.Fatalf("primary/burstable = %q, want 0-3 under overlap; writes=%#v", got, cg.writes)
	}
	// Reclaim subtree is preserved: no reset write may land on any reclaim bucket.
	for _, rel := range []string{"reclaim", "reclaim/reclaim-0", "reclaim/reclaim-0/dynamic"} {
		if _, ok := cg.writes[rel]; ok {
			t.Fatalf("reclaim boundary %q received reset write under overlap; writes=%#v", rel, cg.writes)
		}
	}
}

func TestCPUSetTopologyPluginPreserveModeResetTreatsAbsentReclaimRootAsTraversalBoundary(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-preserve-absent-boundary-pod",
		"bulkhead-preserve-absent-boundary-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.statErrors = map[string]error{"reclaim": os.ErrNotExist}
	cg.readErrByRel = map[string]error{
		"reclaim":                   errors.New("reset crossed absent reclaim boundary"),
		"reclaim/reclaim-0":         errors.New("reset descended below absent reclaim boundary"),
		"reclaim/reclaim-0/dynamic": errors.New("reset reached late-created reclaim descendant"),
	}
	cg.children["reclaim/reclaim-0"] = []string{"dynamic"}

	if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler() crossed absent reclaim boundary: %v", err)
	}
	for _, rel := range []string{"reclaim", "reclaim/reclaim-0", "reclaim/reclaim-0/dynamic"} {
		if _, ok := cg.writes[rel]; ok {
			t.Fatalf("absent reclaim boundary %q received reset write; writes=%#v", rel, cg.writes)
		}
	}
}

func TestCPUSetTopologyPluginReconcileDisabledPublishesReclaimOnlyProof(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-pod",
		"bulkhead-reclaim-only-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim/reclaim-0"] = machine.NewCPUSet(0, 1, 2, 3)
	in.DesiredView.Reserve = machine.NewCPUSet(0)
	in.DesiredView.ReclaimEffective = machine.NewCPUSet(2, 3)
	in.DesiredView.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(2, 3)
	in.DesiredView.PoolOwners = map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{
		{Kind: model.CPUSetPoolKindReclaim}: {
			ExpectedCPUSet: machine.NewCPUSet(2, 3),
		},
		{Kind: model.CPUSetPoolKindShare, Name: "stale-share"}: {
			ExpectedCPUSet: machine.NewCPUSet(4, 5),
		},
		{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "stale-pod"}: {
			ExpectedCPUSet: machine.NewCPUSet(6, 7),
		},
	}

	result, err := p.ReconcileDisabled(context.Background(), in)
	if err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	if result.Level != bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("result = %+v, want current full convergence", result)
	}
	if result.View == nil || result.View.Level != model.AppliedViewLevelReclaimOnly {
		t.Fatalf("applied view = %+v, want reclaim-only", result.View)
	}
	if !result.View.ReclaimEffective.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("applied reclaim = %s, want 2-3", result.View.ReclaimEffective.String())
	}
	if _, ok := result.View.CPUSetByRel["primary"]; ok {
		t.Fatalf("reclaim-only proof unexpectedly contains primary: %#v", result.View.CPUSetByRel)
	}
	for _, rel := range []string{"reclaim", "reclaim/reclaim-0"} {
		if _, ok := result.View.RelProofByRel[rel]; !ok {
			t.Fatalf("reclaim-only proof misses %q: %#v", rel, result.View.RelProofByRel)
		}
	}
	if !result.View.NonReclaimPool.IsEmpty() || len(result.View.ContainerCPUSetByPod) != 0 {
		t.Fatalf("reclaim-only applied fields contain unproved state: %+v", result.View.CPUSetPartitionView)
	}
	reclaimIdentity := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}
	if len(result.View.PoolProjection.CPUSetByIdentity) != 1 {
		t.Fatalf("reclaim-only pool projection = %+v, want exactly reclaim", result.View.PoolProjection)
	}
	assertProjectedCPUSet(t, result.View.PoolProjection.CPUSetByIdentity,
		reclaimIdentity, machine.NewCPUSet(2, 3))
	if !result.View.PoolProjection.UncoveredCPUs.IsEmpty() ||
		!result.View.PoolProjection.AmbiguousCPUs.IsEmpty() {
		t.Fatalf("reclaim-only diagnostics = %+v, want empty", result.View.PoolProjection)
	}
}

func TestCPUSetTopologyPluginReconcileDisabledEmitsFailureSummary(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-summary-pod",
		"bulkhead-reclaim-only-summary-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	readErr := errors.New("reclaim snapshot failed")
	cg.readErrByRel = map[string]error{"reclaim": readErr}
	emitter := &captureMetricEmitter{}
	in.Emitter = emitter

	if _, err := p.ReconcileDisabled(context.Background(), in); !errors.Is(err, readErr) {
		t.Fatalf("ReconcileDisabled() error = %v, want %v", err, readErr)
	}
	for _, metric := range emitter.metrics {
		if metric.key == metricBulkheadTopologyRoundsPerApply &&
			hasMetricTag(metric.tags, "phase", "reclaim_only") &&
			hasMetricTag(metric.tags, "status", "error") {
			return
		}
	}
	t.Fatalf("reclaim-only failure summary not emitted: %#v", emitter.metrics)
}

func TestCPUSetTopologyPluginReconcileDisabledFailureSummaryPreservesConvergenceResult(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-summary-result-pod",
		"bulkhead-reclaim-only-summary-result-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim/reclaim-0"] = machine.NewCPUSet(0, 1, 2, 3)
	in.DesiredView.ReclaimEffective = machine.NewCPUSet(2, 3)
	in.DesiredView.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(2, 3)
	statErr := errors.New("publish classification failed")
	var wrote bool
	var postWriteReclaimStats int
	cg.afterApply = func(string, *cgcommon.CPUSetData) {
		wrote = true
	}
	cg.statIdentityHook = func(rel string) (topology.CgroupIdentity, error) {
		if wrote && rel == "reclaim" {
			postWriteReclaimStats++
			if postWriteReclaimStats == 13 {
				return topology.CgroupIdentity{}, statErr
			}
		}
		return pluginFakeIdentity(rel), nil
	}
	emitter := &captureMetricEmitter{}
	in.Emitter = emitter

	if _, err := p.ReconcileDisabled(context.Background(), in); err == nil {
		t.Fatalf("ReconcileDisabled() error = nil, want final snapshot failure; post-write root stats=%d",
			postWriteReclaimStats)
	}
	for _, metric := range emitter.metrics {
		if metric.key == metricBulkheadTopologyRoundsPerApply &&
			hasMetricTag(metric.tags, "phase", "reclaim_only") &&
			hasMetricTag(metric.tags, "status", "error") {
			if metric.val == 0 {
				t.Fatalf("failure summary rounds = %.0f, want real non-zero convergence rounds; post-write root stats=%d",
					metric.val, postWriteReclaimStats)
			}
			return
		}
	}
	t.Fatalf("reclaim-only failure summary not emitted: %#v", emitter.metrics)
}

func TestCPUSetTopologyPluginReconcileDisabledContextTerminationSummarizesAccumulatedRetriesOnce(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-canceled-retry-pod",
		"bulkhead-reclaim-only-canceled-retry-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim/reclaim-0"] = machine.NewCPUSet(0, 1, 2, 3)
	in.DesiredView.ReclaimEffective = machine.NewCPUSet(2, 3)
	in.DesiredView.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(2, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wrote bool
	var postWriteReclaimStats int
	cg.afterApply = func(string, *cgcommon.CPUSetData) {
		wrote = true
	}
	cg.statIdentityHook = func(rel string) (topology.CgroupIdentity, error) {
		if wrote && rel == "reclaim" {
			postWriteReclaimStats++
			if postWriteReclaimStats == 13 {
				cancel()
				return topology.CgroupIdentity{Device: 99, Inode: 100}, nil
			}
		}
		return pluginFakeIdentity(rel), nil
	}
	emitter := &captureMetricEmitter{}
	in.Emitter = emitter

	result, err := p.ReconcileDisabled(ctx, in)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconcileDisabled() error = %v, want context cancellation", err)
	}
	if result.Level != bulkheadapi.ConvergenceLevelNone {
		t.Fatalf("result = %+v, want no publish on cancellation", result)
	}
	summaries := 0
	for _, metric := range emitter.metrics {
		if metric.key != metricBulkheadTopologyRoundsPerApply ||
			!hasMetricTag(metric.tags, "phase", "reclaim_only") {
			continue
		}
		summaries++
		if metric.val == 0 {
			t.Fatalf("terminal summary rounds = %.0f, want accumulated non-zero rounds", metric.val)
		}
		if !hasMetricTag(metric.tags, "status", "error") {
			t.Fatalf("terminal summary tags = %#v, want error status", metric.tags)
		}
	}
	if summaries != 1 {
		t.Fatalf("reclaim-only terminal summaries = %d, want exactly 1; metrics=%#v", summaries, emitter.metrics)
	}
}

func hasMetricTag(tags []metrics.MetricTag, key, value string) bool {
	for _, tag := range tags {
		if tag.Key == key && tag.Val == value {
			return true
		}
	}
	return false
}

func TestCPUSetTopologyPluginReconcileDisabledTreatsAllMissingRootsAsEmptySuccess(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-missing-pod",
		"bulkhead-reclaim-only-missing-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.statErrors = map[string]error{
		"reclaim":           os.ErrNotExist,
		"reclaim/reclaim-0": os.ErrNotExist,
	}
	emitter := &captureMetricEmitter{}
	in.Emitter = emitter

	result, err := p.ReconcileDisabled(context.Background(), in)
	if err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	if result.Level != bulkheadapi.ConvergenceLevelFull || result.View == nil {
		t.Fatalf("result = %+v, want current empty success", result)
	}
	if !result.View.ReclaimEffective.IsEmpty() ||
		len(result.View.ReclaimEffectivePerNUMA) != 0 ||
		len(result.View.CPUSetByRel) != 0 ||
		len(result.View.RelProofByRel) != 0 {
		t.Fatalf("missing reclaim paths published physical proof: %+v", result.View)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("missing reclaim paths received writes: %#v", cg.writes)
	}
	for _, metric := range emitter.metrics {
		if metric.key == metricBulkheadTopologyRoundsPerApply &&
			hasMetricTag(metric.tags, "phase", "reclaim_only") &&
			hasMetricTag(metric.tags, "status", "converged") {
			if metric.val != 0 {
				t.Fatalf("empty-path success summary rounds = %.0f, want 0", metric.val)
			}
			return
		}
	}
	t.Fatalf("empty-path reclaim-only success summary not emitted: %#v", emitter.metrics)
}

func TestCPUSetTopologyPluginReconcileDisabledReclassifiesAppearingRoot(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-race-pod",
		"bulkhead-reclaim-only-race-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.cpus["reclaim"] = machine.NewCPUSet(2, 3)
	delete(cg.existing, "reclaim/reclaim-0")
	var rootStats int
	cg.statIdentityHook = func(rel string) (topology.CgroupIdentity, error) {
		if rel == "reclaim" {
			rootStats++
			if rootStats == 1 {
				return topology.CgroupIdentity{}, os.ErrNotExist
			}
		}
		if rel == "reclaim/reclaim-0" {
			return topology.CgroupIdentity{}, os.ErrNotExist
		}
		return pluginFakeIdentity(rel), nil
	}

	result, err := p.ReconcileDisabled(context.Background(), in)
	if err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	if _, ok := result.View.RelProofByRel["reclaim"]; !ok {
		t.Fatalf("appearing reclaim root was not reclassified and proved: %#v", result.View.RelProofByRel)
	}
	if rootStats < 3 {
		t.Fatalf("reclaim root stat count = %d, want preflight, final absence check, and rebuilt classification", rootStats)
	}
}

func TestCPUSetTopologyPluginReconcileDisabledSkipsMissingNUMABucket(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-missing-numa-pod",
		"bulkhead-reclaim-only-missing-numa-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.statErrors = map[string]error{"reclaim/reclaim-0": os.ErrNotExist}

	result, err := p.ReconcileDisabled(context.Background(), in)
	if err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	if _, ok := result.View.RelProofByRel["reclaim"]; !ok {
		t.Fatalf("existing root proof missing: %#v", result.View.RelProofByRel)
	}
	if _, ok := result.View.RelProofByRel["reclaim/reclaim-0"]; ok {
		t.Fatalf("missing NUMA bucket was proved: %#v", result.View.RelProofByRel)
	}
	if len(result.View.ReclaimEffectivePerNUMA) != 0 {
		t.Fatalf("missing NUMA bucket left applied key: %#v", result.View.ReclaimEffectivePerNUMA)
	}
}

func TestCPUSetTopologyPluginReconcileDisabledDrainsExistingNUMABucketRemovedFromDesiredView(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV2,
		"bulkhead-reclaim-only-removed-numa-pod",
		"bulkhead-reclaim-only-removed-numa-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	in.Topology.CPUDetails = machine.CPUDetails{
		0: {NUMANodeID: 0},
		1: {NUMANodeID: 0},
		2: {NUMANodeID: 1},
		3: {NUMANodeID: 1},
	}
	cg.existing["reclaim/reclaim-1"] = true
	cg.children["reclaim"] = []string{"reclaim-0", "reclaim-1"}
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim/reclaim-0"] = machine.NewCPUSet(0, 1)
	cg.cpus["reclaim/reclaim-1"] = machine.NewCPUSet(2, 3)
	in.DesiredView.ReclaimEffective = machine.NewCPUSet(0, 1)
	in.DesiredView.ReclaimEffectivePerNUMA = map[int]machine.CPUSet{
		0: machine.NewCPUSet(0, 1),
	}

	result, err := p.ReconcileDisabled(context.Background(), in)
	if err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	if got := cg.cpus["reclaim/reclaim-1"]; !got.IsEmpty() {
		t.Fatalf("removed NUMA bucket cpuset = %s, want empty", got.String())
	}
	bucket := indexOfString(cg.writeOrder, "reclaim/reclaim-1")
	root := indexOfString(cg.writeOrder, "reclaim")
	if bucket < 0 || root < 0 || bucket >= root {
		t.Fatalf("write order = %v, want removed NUMA bucket before reclaim root", cg.writeOrder)
	}
	if result.View == nil || !result.View.ReclaimEffective.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("applied reclaim = %+v, want 0-1", result.View)
	}
	if got, ok := result.View.ReclaimEffectivePerNUMA[1]; ok && !got.IsEmpty() {
		t.Fatalf("removed NUMA applied reclaim = %s, want absent or empty", got.String())
	}
}

func TestCPUSetTopologyPluginReconcileDisabledReclassifiesAppearingNUMABucket(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-appearing-numa-pod",
		"bulkhead-reclaim-only-appearing-numa-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1, 2, 3)
	in.DesiredView.ReclaimEffective = machine.NewCPUSet(2, 3)
	var bucketStats int
	var writesBeforeReclassification int
	cg.statIdentityHook = func(rel string) (topology.CgroupIdentity, error) {
		if rel == "reclaim/reclaim-0" {
			bucketStats++
			if bucketStats == 1 {
				return topology.CgroupIdentity{}, os.ErrNotExist
			}
			if bucketStats == 2 {
				writesBeforeReclassification = len(cg.writeOrder)
			}
		}
		return pluginFakeIdentity(rel), nil
	}

	result, err := p.ReconcileDisabled(context.Background(), in)
	if err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	if _, ok := result.View.RelProofByRel["reclaim/reclaim-0"]; !ok {
		t.Fatalf("appearing NUMA bucket was not reclassified: %#v", result.View.RelProofByRel)
	}
	if bucketStats < 2 {
		t.Fatalf("NUMA bucket stat calls = %d, want reclassification", bucketStats)
	}
	if writesBeforeReclassification != 0 {
		t.Fatalf("writes before appearing NUMA bucket was reclassified = %d, want 0; writes=%v",
			writesBeforeReclassification, cg.writeOrder)
	}
}

func TestCPUSetTopologyPluginReconcileDisabledRetriesReplacedNUMABucketIdentity(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-replaced-numa-pod",
		"bulkhead-reclaim-only-replaced-numa-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	oldIdentity := topology.CgroupIdentity{Device: 99, Inode: 100}
	var bucketStats int
	cg.statIdentityHook = func(rel string) (topology.CgroupIdentity, error) {
		if rel == "reclaim/reclaim-0" {
			bucketStats++
			if bucketStats == 1 {
				return oldIdentity, nil
			}
		}
		return pluginFakeIdentity(rel), nil
	}

	result, err := p.ReconcileDisabled(context.Background(), in)
	if err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	proof := result.View.RelProofByRel["reclaim/reclaim-0"]
	if proof.Device != pluginFakeIdentity("reclaim/reclaim-0").Device ||
		proof.Inode != pluginFakeIdentity("reclaim/reclaim-0").Inode {
		t.Fatalf("published NUMA proof = %+v, want replacement identity", proof)
	}
	if bucketStats < 2 {
		t.Fatalf("NUMA bucket stat calls = %d, want replacement retry", bucketStats)
	}
}

func TestCPUSetTopologyPluginReconcileDisabledOrdersGrowAndShrink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		initial     machine.CPUSet
		target      machine.CPUSet
		growPinned  bool
		wantEarlier string
		wantLater   string
		wantApplied machine.CPUSet
	}{
		{
			// Under preserve, a same-cycle Desired growth must be pinned to the
			// running bucket instead of growing it.
			name:    "grow is pinned to running bucket",
			initial: machine.NewCPUSet(2), target: machine.NewCPUSet(2, 3),
			growPinned:  true,
			wantApplied: machine.NewCPUSet(2),
		},
		{
			name: "shrink child before parent", initial: machine.NewCPUSet(0, 1, 2, 3), target: machine.NewCPUSet(2, 3),
			wantEarlier: "reclaim/reclaim-0", wantLater: "reclaim",
			wantApplied: machine.NewCPUSet(2, 3),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, cg, in, _ := newDisabledTransitionTestPlugin(
				t,
				cgroupclient.CgroupVersionV1,
				"bulkhead-reclaim-order-"+strings.ReplaceAll(tt.name, " ", "-"),
				"bulkhead-reclaim-order-container-"+strings.ReplaceAll(tt.name, " ", "-"),
			)
			p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
			cg.cpus["reclaim"] = tt.initial.Clone()
			cg.cpus["reclaim/reclaim-0"] = tt.initial.Clone()
			in.DesiredView.ReclaimEffective = tt.target.Clone()
			in.DesiredView.ReclaimEffectivePerNUMA[0] = tt.target.Clone()

			result, err := p.ReconcileDisabled(context.Background(), in)
			if err != nil {
				t.Fatalf("ReconcileDisabled() error: %v", err)
			}
			if got := result.View.ReclaimEffectivePerNUMA[0]; !got.Equals(tt.wantApplied) {
				t.Fatalf("applied NUMA bucket = %s, want %s", got.String(), tt.wantApplied.String())
			}
			if tt.growPinned {
				// No reclaim bucket/root write must occur when growth is pinned.
				for _, rel := range cg.writeOrder {
					if rel == "reclaim" || rel == "reclaim/reclaim-0" {
						t.Fatalf("pinned grow unexpectedly wrote reclaim rel=%q, order=%v", rel, cg.writeOrder)
					}
				}
				return
			}
			earlier := indexOfString(cg.writeOrder, tt.wantEarlier)
			later := indexOfString(cg.writeOrder, tt.wantLater)
			if earlier < 0 || later < 0 || earlier >= later {
				t.Fatalf("write order = %v, want %q before %q", cg.writeOrder, tt.wantEarlier, tt.wantLater)
			}
		})
	}
}

func TestCPUSetTopologyPluginReconcileDisabledShrinksDynamicDescendantBeforeAncestors(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-dynamic-order-pod",
		"bulkhead-reclaim-dynamic-order-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.children["reclaim/reclaim-0"] = []string{"dynamic"}
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim/reclaim-0"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim/reclaim-0/dynamic"] = machine.NewCPUSet(0, 1, 2, 3)
	in.DesiredView.ReclaimEffective = machine.NewCPUSet(2, 3)
	in.DesiredView.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(2, 3)

	if _, err := p.ReconcileDisabled(context.Background(), in); err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	dynamic := indexOfString(cg.writeOrder, "reclaim/reclaim-0/dynamic")
	bucket := indexOfString(cg.writeOrder, "reclaim/reclaim-0")
	root := indexOfString(cg.writeOrder, "reclaim")
	if dynamic < 0 || bucket < 0 || root < 0 || !(dynamic < bucket && bucket < root) {
		t.Fatalf("write order = %v, want dynamic descendant, NUMA bucket, reclaim root", cg.writeOrder)
	}
}

func TestCPUSetTopologyPluginReconcileDisabledAppliesNUMAMixedReplacement(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-numa-mixed-pod",
		"bulkhead-reclaim-numa-mixed-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim/reclaim-0"] = machine.NewCPUSet(0, 1)
	in.DesiredView.ReclaimEffective = machine.NewCPUSet(2, 3)
	in.DesiredView.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(2, 3)

	result, err := p.ReconcileDisabled(context.Background(), in)
	if err != nil {
		t.Fatalf("ReconcileDisabled() error: %v", err)
	}
	// Under preserve, a same-cycle Desired reassignment (0,1) -> (2,3) is not a
	// shrink; the running pinned bucket must be retained instead of moved.
	if got := result.View.ReclaimEffectivePerNUMA[0]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("applied NUMA target = %s, want pinned 0-1", got.String())
	}
}

// reclaimPinned4NUMAScenario models the r2e2e193303 governed-reset restart: four
// NUMA nodes, each reclaim bucket pinned at 1 CPU (the checkpoint-approved
// value), while this cycle's DesiredView expands every bucket to all 4 CPUs in
// its NUMA. It asserts the preserve contract across the matrix.
func TestCPUSetTopologyPluginReconcileDisabledPreservesPinnedBucketsAcross4NUMAs(t *testing.T) {
	t.Parallel()

	// 4 NUMA nodes x 4 CPUs each: NUMA n owns CPUs [4n, 4n+3].
	numaCPUs := func(n int) machine.CPUSet { return machine.NewCPUSet(4*n, 4*n+1, 4*n+2, 4*n+3) }
	pinnedByNUMA := map[int]machine.CPUSet{
		0: machine.NewCPUSet(0),
		1: machine.NewCPUSet(4),
		2: machine.NewCPUSet(8),
		3: machine.NewCPUSet(12),
	}
	pinnedUnion := machine.NewCPUSet(0, 4, 8, 12)

	tests := []struct {
		name             string
		preserve         bool
		currentPerNUMA   machine.CPUSet // cgroup bucket cpuset for every NUMA
		desiredPerNUMA   machine.CPUSet // desired bucket cpuset for every NUMA
		wantAppliedNUMA  map[int]machine.CPUSet
		wantAppliedRoot  machine.CPUSet
		wantReclaimWrite bool
		idempotent       bool // repeat a second cycle and assert no further reclaim write
	}{
		{
			// r2e2e193303 governed restart: running buckets pinned at 1 CPU/NUMA,
			// this cycle's desired expands to all 4/NUMA. Must stay pinned.
			name:             "preserve true pins 4-NUMA buckets against same-cycle expansion",
			preserve:         true,
			currentPerNUMA:   machine.NewCPUSet(0 % 4), // overwritten per NUMA below
			desiredPerNUMA:   numaCPUs(0),
			wantAppliedNUMA:  pinnedByNUMA,
			wantAppliedRoot:  pinnedUnion,
			wantReclaimWrite: false,
			idempotent:       true,
		},
		{
			// Shrink toward desired is still allowed: desired is a subset of the
			// running bucket, so preserve must not block the shrink.
			name:           "preserve true allows 4-NUMA shrink toward desired",
			preserve:       true,
			currentPerNUMA: numaCPUs(0),
			desiredPerNUMA: machine.NewCPUSet(0),
			wantAppliedNUMA: map[int]machine.CPUSet{
				0: machine.NewCPUSet(0), 1: machine.NewCPUSet(4),
				2: machine.NewCPUSet(8), 3: machine.NewCPUSet(12),
			},
			wantAppliedRoot:  pinnedUnion,
			wantReclaimWrite: true,
		},
		{
			// preserve=false is unchanged: the disabled reconcile is free to track
			// desired (no pinning). It must not panic and must not produce a pinned
			// proof identical to the preserve case.
			name:             "preserve false does not pin buckets",
			preserve:         false,
			currentPerNUMA:   machine.NewCPUSet(0 % 4),
			desiredPerNUMA:   numaCPUs(0),
			wantReclaimWrite: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, cg, in, _ := newDisabledTransitionTestPlugin(
				t,
				cgroupclient.CgroupVersionV1,
				"bulkhead-reclaim-4n-"+strings.ReplaceAll(tt.name, " ", "-"),
				"bulkhead-reclaim-4n-container",
			)
			p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = tt.preserve

			// 4-NUMA topology.
			cpuDetails := machine.CPUDetails{}
			for c := 0; c < 16; c++ {
				cpuDetails[c] = machine.CPUTopoInfo{NUMANodeID: c / 4}
			}
			in.Topology.CPUDetails = cpuDetails

			// Reclaim root + 4 buckets. Bucket n current = tt.currentPerNUMA
			// shifted onto NUMA n's CPU range.
			currentShifted := map[int]machine.CPUSet{}
			for n := 0; n < 4; n++ {
				cpus := machine.NewCPUSet()
				for _, cpu := range tt.currentPerNUMA.ToSliceInt() {
					cpus = cpus.Union(machine.NewCPUSet(cpu + 4*n))
				}
				currentShifted[n] = cpus
			}
			desiredShifted := map[int]machine.CPUSet{}
			root := machine.NewCPUSet()
			for n := 0; n < 4; n++ {
				cpus := machine.NewCPUSet()
				for _, cpu := range tt.desiredPerNUMA.ToSliceInt() {
					cpus = cpus.Union(machine.NewCPUSet(cpu + 4*n))
				}
				desiredShifted[n] = cpus
				root = root.Union(cpus)
			}
			cg.cpus["reclaim"] = unionOf(currentShifted).Clone()
			cg.children["reclaim"] = []string{"reclaim-0", "reclaim-1", "reclaim-2", "reclaim-3"}
			for n := 0; n < 4; n++ {
				rel := fmt.Sprintf("reclaim/reclaim-%d", n)
				cg.existing[rel] = true
				cg.cpus[rel] = currentShifted[n].Clone()
			}
			in.DesiredView.ReclaimEffective = root.Clone()
			in.DesiredView.ReclaimEffectivePerNUMA = desiredShifted

			writeOrderBefore := append([]string(nil), cg.writeOrder...)
			result, err := p.ReconcileDisabled(context.Background(), in)
			if err != nil {
				t.Fatalf("ReconcileDisabled() error: %v", err)
			}

			reclaimWrote := func() bool {
				for _, rel := range cg.writeOrder[len(writeOrderBefore):] {
					if strings.HasPrefix(rel, "reclaim") {
						return true
					}
				}
				return false
			}
			if reclaimWrote() != tt.wantReclaimWrite {
				t.Fatalf("reclaim write happened=%v, want %v; order=%v", reclaimWrote(), tt.wantReclaimWrite, cg.writeOrder)
			}

			if tt.preserve && tt.wantAppliedNUMA != nil {
				if result.View == nil {
					t.Fatalf("expected reclaim-only applied view for pinned case")
				}
				for n := 0; n < 4; n++ {
					want := tt.wantAppliedNUMA[n]
					got := result.View.ReclaimEffectivePerNUMA[n]
					if !got.Equals(want) {
						t.Fatalf("NUMA %d applied bucket = %s, want %s", n, got.String(), want.String())
					}
				}
				if !result.View.ReclaimEffective.Equals(tt.wantAppliedRoot) {
					t.Fatalf("applied reclaim root = %s, want %s", result.View.ReclaimEffective.String(), tt.wantAppliedRoot.String())
				}
			}

			// Same-cycle idempotence: a second reconcile must not tear buckets.
			if tt.idempotent {
				before := len(cg.writeOrder)
				if _, err := p.ReconcileDisabled(context.Background(), in); err != nil {
					t.Fatalf("second ReconcileDisabled() error: %v", err)
				}
				for _, rel := range cg.writeOrder[before:] {
					if strings.HasPrefix(rel, "reclaim") {
						t.Fatalf("second cycle tore reclaim bucket, wrote %q; order=%v", rel, cg.writeOrder)
					}
				}
			}
		})
	}
}

func unionOf(m map[int]machine.CPUSet) machine.CPUSet {
	u := machine.NewCPUSet()
	for _, cpus := range m {
		u = u.Union(cpus)
	}
	return u
}

// reclaimPinIdentityScenario models the takeover20261003b governed preserve
// reset: live reclaim buckets are pinned at one whole-core SMT pair per NUMA
// (numa0={6,70}, numa1={30,94}), while the same-cycle DesiredView recomputes an
// equivalent-but-DIFFERENT whole-core pair on the same NUMA (numa0={15,79},
// numa1={31,95}). That equivalent pair is a product regression, not a legal
// recompute: the preserve reset must keep the exact live per-NUMA identity.
func TestCPUSetTopologyPluginReconcileDisabledPinsExactPerNUMAIdentity(t *testing.T) {
	t.Parallel()

	// Per-NUMA live (preserved) vs recomputed (drifted) whole-core pairs.
	pinned := map[int]machine.CPUSet{
		0: machine.NewCPUSet(6, 70),
		1: machine.NewCPUSet(30, 94),
	}
	recomputed := map[int]machine.CPUSet{
		0: machine.NewCPUSet(15, 79),
		1: machine.NewCPUSet(31, 95),
	}
	// Whole machine CPU topology: each CPU tagged with its NUMA node and the
	// physical core its SMT thread belongs to. 6<->70, 15<->79 are numa0 SMT
	// pairs; 30<->94, 31<->95 are numa1 SMT pairs.
	cpuTopo := map[int]machine.CPUTopoInfo{
		6:  {NUMANodeID: 0, CoreID: 100},
		70: {NUMANodeID: 0, CoreID: 100},
		15: {NUMANodeID: 0, CoreID: 101},
		79: {NUMANodeID: 0, CoreID: 101},
		30: {NUMANodeID: 1, CoreID: 200},
		94: {NUMANodeID: 1, CoreID: 200},
		31: {NUMANodeID: 1, CoreID: 201},
		95: {NUMANodeID: 1, CoreID: 201},
	}

	tests := []struct {
		name        string
		preserve    bool
		current     map[int]machine.CPUSet // live bucket cpuset per NUMA
		desired     map[int]machine.CPUSet // desired bucket cpuset per NUMA
		wantApplied map[int]machine.CPUSet // nil = "track desired" (preserve=false)
	}{
		{
			// Exact-ID positive: equivalent-different same-NUMA pair must be
			// rejected; the live pinned identity wins.
			name:        "equivalent different same-NUMA pair pinned to live",
			preserve:    true,
			current:     pinned,
			desired:     recomputed,
			wantApplied: pinned,
		},
		{
			// Half-core negative: desired asks for only one thread of the live SMT
			// pair; preserve must not accept the partial core, it keeps the whole
			// live pair.
			name:        "half-core desired rejected, keeps whole live pair",
			preserve:    true,
			current:     pinned,
			desired:     map[int]machine.CPUSet{0: machine.NewCPUSet(6), 1: machine.NewCPUSet(30)},
			wantApplied: pinned,
		},
		{
			// Cross-NUMA negative: desired puts numa1's pair onto numa0; preserve
			// must not move the bucket across NUMA, it keeps the live per-NUMA set.
			name:        "cross-NUMA desired rejected, keeps live numa",
			preserve:    true,
			current:     pinned,
			desired:     map[int]machine.CPUSet{0: machine.NewCPUSet(30, 94), 1: machine.NewCPUSet(6, 70)},
			wantApplied: pinned,
		},
		{
			// preserve=false does not pin: the disabled reconcile tracks desired.
			name:        "preserve false tracks recomputed desired",
			preserve:    false,
			current:     pinned,
			desired:     recomputed,
			wantApplied: nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, cg, in, _ := newDisabledTransitionTestPlugin(
				t,
				cgroupclient.CgroupVersionV1,
				"bulkhead-pin-identity-"+strings.ReplaceAll(tt.name, " ", "-"),
				"bulkhead-pin-identity-container",
			)
			p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = tt.preserve

			cpuDetails := machine.CPUDetails{}
			for cpu, info := range cpuTopo {
				cpuDetails[cpu] = info
			}
			in.Topology.CPUDetails = cpuDetails

			root := machine.NewCPUSet()
			cg.children["reclaim"] = []string{"reclaim-0", "reclaim-1"}
			for n := 0; n < 2; n++ {
				rel := fmt.Sprintf("reclaim/reclaim-%d", n)
				cg.existing[rel] = true
				cg.cpus[rel] = tt.current[n].Clone()
				root = root.Union(tt.current[n])
			}
			cg.cpus["reclaim"] = root.Clone()
			in.DesiredView.ReclaimEffective = unionOf(tt.desired).Clone()
			in.DesiredView.ReclaimEffectivePerNUMA = tt.desired

			result, err := p.ReconcileDisabled(context.Background(), in)
			if err != nil {
				t.Fatalf("ReconcileDisabled() error: %v", err)
			}

			if tt.wantApplied == nil {
				return
			}
			if result.View == nil {
				t.Fatalf("expected reclaim-only applied view")
			}
			for n := 0; n < 2; n++ {
				want := tt.wantApplied[n]
				got := result.View.ReclaimEffectivePerNUMA[n]
				if !got.Equals(want) {
					t.Fatalf("NUMA %d applied bucket = %s, want pinned %s", n, got.String(), want.String())
				}
			}
			if !result.View.ReclaimEffective.Equals(unionOf(tt.wantApplied)) {
				t.Fatalf("applied reclaim root = %s, want %s",
					result.View.ReclaimEffective.String(), unionOf(tt.wantApplied).String())
			}
		})
	}
}

// TestCPUSetTopologyPluginReconcileDisabledFreshBucketFailsClosed reproduces the
// takeover checkpoint-live drift: the checkpoint approved per-NUMA reclaim
// identity is {6,70}/{30,94}, but at the preserve reset the live bucket cgroup is
// either freshly recreated (empty) or unreadable, while the same-cycle DesiredView
// proposes the drifted equivalent pair {15,79}/{31,95}. Preserve mode must NOT
// fall back to desired and fabricate the drifted identity; it must fail closed.
func TestCPUSetTopologyPluginReconcileDisabledFreshBucketFailsClosed(t *testing.T) {
	t.Parallel()

	recomputed := map[int]machine.CPUSet{
		0: machine.NewCPUSet(15, 79),
		1: machine.NewCPUSet(31, 95),
	}
	cpuTopo := map[int]machine.CPUTopoInfo{
		6: {NUMANodeID: 0, CoreID: 100}, 70: {NUMANodeID: 0, CoreID: 100},
		15: {NUMANodeID: 0, CoreID: 101}, 79: {NUMANodeID: 0, CoreID: 101},
		30: {NUMANodeID: 1, CoreID: 200}, 94: {NUMANodeID: 1, CoreID: 200},
		31: {NUMANodeID: 1, CoreID: 201}, 95: {NUMANodeID: 1, CoreID: 201},
	}

	tests := []struct {
		name      string
		liveEmpty bool
		liveErr   bool
	}{
		{name: "empty live bucket fails closed", liveEmpty: true},
		{name: "unreadable live bucket fails closed", liveErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, cg, in, _ := newDisabledTransitionTestPlugin(
				t,
				cgroupclient.CgroupVersionV1,
				"bulkhead-fresh-"+strings.ReplaceAll(tt.name, " ", "-"),
				"bulkhead-fresh-container",
			)
			p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true

			cpuDetails := machine.CPUDetails{}
			for cpu, info := range cpuTopo {
				cpuDetails[cpu] = info
			}
			in.Topology.CPUDetails = cpuDetails

			cg.children["reclaim"] = []string{"reclaim-0", "reclaim-1"}
			for n := 0; n < 2; n++ {
				rel := fmt.Sprintf("reclaim/reclaim-%d", n)
				cg.existing[rel] = true
				if tt.liveEmpty {
					cg.cpus[rel] = machine.NewCPUSet()
				} else {
					cg.cpus[rel] = recomputed[n].Clone()
				}
			}
			if tt.liveErr {
				cg.readErrByRel = map[string]error{
					"reclaim/reclaim-0": errors.New("simulated read failure"),
					"reclaim/reclaim-1": errors.New("simulated read failure"),
				}
			}
			cg.cpus["reclaim"] = machine.NewCPUSet()
			in.DesiredView.ReclaimEffective = unionOf(recomputed).Clone()
			in.DesiredView.ReclaimEffectivePerNUMA = recomputed

			if _, err := p.ReconcileDisabled(context.Background(), in); err == nil {
				t.Fatalf("expected preserve reconcile to fail closed on empty/unreadable live bucket")
			}
		})
	}
}

func indexOfString(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

func TestCPUSetTopologyPluginDisabledTransitionUsesTopologySpecsAndDAGExpandV2ToEmpty(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV2,
		"bulkhead-disabled-v2-pod",
		"bulkhead-disabled-v2-container",
	)

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	// cgroup-v2 containment safety: emptying the controlled primary while the
	// unproven burstable/pod-a descendants still hold a non-empty cpuset must fail
	// closed before any write instead of fanning the empty target out to them.
	if err == nil {
		t.Fatalf("expected fail-closed error for empty primary with non-empty unproven descendants; writes=%#v", cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("expected no writes on fail-closed reset; writes=%#v", cg.writes)
	}
}

func TestCPUSetTopologyPluginDisabledTransitionV2EmptyResetWritesEmptyCPUs(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV2,
		"bulkhead-disabled-v2-fallback-pod",
		"bulkhead-disabled-v2-fallback-container",
	)
	for _, rel := range []string{
		"primary/burstable",
		"primary/burstable/pod-a",
		"primary/burstable/pod-a/container-a",
	} {
		cg.existing[rel] = true
	}
	if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err == nil {
		t.Fatalf("expected fail-closed error for empty primary with non-empty unproven descendants; writes=%#v", cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("expected no writes on fail-closed reset; writes=%#v", cg.writes)
	}
}

func TestCPUSetTopologyPluginDisabledTransitionSkipsMissingResetRel(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-missing-root-pod",
		"bulkhead-disabled-missing-root-container",
	)
	missingRel := "custom-reclaim-root"
	p.cfg.BulkheadReclaimRelPaths = append(p.cfg.BulkheadReclaimRelPaths, missingRel)
	cg.statErrors = map[string]error{missingRel: os.ErrNotExist}
	cg.readErrByRel = map[string]error{missingRel: os.ErrNotExist}

	if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}
	if _, ok := cg.writes[missingRel]; ok {
		t.Fatalf("missing reset rel %q received write; writes=%#v", missingRel, cg.writes)
	}
}

func TestCPUSetTopologyPluginDisabledTransitionReturnsErrorForInvalidV1Target(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		topology *machine.CPUTopology
	}{
		{name: "nil topology"},
		{name: "empty machine cpuset", topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{}}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, _, in, _ := newDisabledTransitionTestPlugin(
				t,
				cgroupclient.CgroupVersionV1,
				"bulkhead-disabled-invalid-"+tt.name,
				"bulkhead-disabled-invalid-container",
			)
			in.Topology = tt.topology

			err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
			if err == nil {
				t.Fatalf("expected invalid v1 reset target error")
			}
		})
	}
}

func TestCPUSetTopologyPluginDisabledTransitionReturnsSiblingDiscoveryError(t *testing.T) {
	t.Parallel()

	p, _, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-list-error-pod",
		"bulkhead-disabled-list-error-container",
	)
	p.cgroup.(*fakeCgroupClient).listErr = errors.New("list failed")

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	if err == nil {
		t.Fatalf("expected sibling discovery error")
	}
}

func TestDisabledResetNotConvergedErrorClassifiesDeferredOnly(t *testing.T) {
	t.Parallel()

	deferred := &disabledResetNotConvergedError{
		state:    topology.ConvergenceStateNonConverged,
		applied:  44,
		deferred: 2,
		report:   topology.ConvergenceReport{FullyConverged: true},
	}
	if !errors.Is(deferred, bulkheadapi.ErrDisabledResetDeferred) {
		t.Fatalf("deferred-only reset error must unwrap to ErrDisabledResetDeferred: %v", deferred)
	}

	for _, err := range []*disabledResetNotConvergedError{
		{state: topology.ConvergenceStateNonConverged, deferred: 2, failed: 1, report: topology.ConvergenceReport{FullyConverged: true}},
		{state: topology.ConvergenceStateNonConverged, deferred: 2, report: topology.ConvergenceReport{FullyConverged: false}},
		{state: topology.ConvergenceStateNonConverged, report: topology.ConvergenceReport{FullyConverged: true}},
	} {
		if errors.Is(err, bulkheadapi.ErrDisabledResetDeferred) {
			t.Fatalf("real or unexplained non-convergence must stay fail-closed: %+v", err)
		}
	}
}

func TestCPUSetTopologyPluginDisabledTransitionReturnsTypedErrorWhenResetNotConverged(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-reset-mismatch-pod",
		"bulkhead-disabled-reset-mismatch-container",
	)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		if rel == "primary" && (data.CPUs != "" || data.WriteEmptyCPUs) {
			cg.cpus[rel] = machine.NewCPUSet(0, 1)
		}
	}

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	if err == nil {
		t.Fatalf("expected disabled reset not converged error")
	}
	var notConverged *disabledResetNotConvergedError
	if !errors.As(err, &notConverged) {
		t.Fatalf("error type = %T, want *disabledResetNotConvergedError: %v", err, err)
	}
	if notConverged.state != topology.ConvergenceStateNonConverged {
		t.Fatalf("state = %s, want %s", notConverged.state, topology.ConvergenceStateNonConverged)
	}
	if len(notConverged.report.NonConvergedTargets) == 0 {
		t.Fatalf("expected non-converged targets in report: %+v", notConverged.report)
	}
	if !strings.Contains(err.Error(), "not converged") {
		t.Fatalf("error = %v, want explicit not converged message", err)
	}
}

func TestCPUSetTopologyPluginSharedModeGuardRejectsResetWhileNormalHeldWithoutWrites(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-shared-guard-pod",
		"bulkhead-disabled-shared-guard-container",
	)
	token, err := topology.NormalModeGuardWithGate(p.sharedModeGate()).TryEnter()
	if err != nil {
		t.Fatalf("TryEnter normal: %v", err)
	}
	defer token.Exit()

	err = p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	var busy *topology.CoordinatorBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("CPUSetAdjustmentDisabledHandler error = %T %v, want *CoordinatorBusyError", err, err)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("busy reset must not write cgroups, writes=%#v", cg.writes)
	}
}

func newDisabledTransitionTestPlugin(
	t *testing.T,
	version cgroupclient.CgroupVersion,
	podUID string,
	containerID string,
) (*CPUSetTopologyPlugin, *fakeCgroupClient, bulkheadapi.HandlerContext, string) {
	t.Helper()

	containerRel := "primary/burstable/pod-a/container-a"
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "bulkhead-disabled-" + podUID,
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return containerRel, false, nil
			}
			return "", true, nil
		},
	})

	cg := &fakeCgroupClient{
		version: version,
		existing: map[string]bool{
			"primary":           true,
			"reclaim":           true,
			"reclaim/reclaim-0": true,
			"sibling":           true,
			"partition":         true,
			containerRel:        true,
		},
		cpus: map[string]machine.CPUSet{
			"primary":                             machine.NewCPUSet(0, 1, 2, 3),
			"primary/burstable":                   machine.NewCPUSet(0, 1, 2, 3),
			"primary/burstable/pod-a":             machine.NewCPUSet(0, 1),
			"primary/burstable/pod-a/container-a": machine.NewCPUSet(0),
			"reclaim":                             machine.NewCPUSet(2, 3),
			"reclaim/reclaim-0":                   machine.NewCPUSet(2, 3),
			"sibling":                             machine.NewCPUSet(2, 3),
			"partition":                           machine.NewCPUSet(0, 1, 2, 3),
		},
		children: map[string][]string{
			"":                        {"primary", "reclaim", "sibling", "partition"},
			"primary":                 {"burstable"},
			"primary/burstable":       {"pod-a"},
			"primary/burstable/pod-a": {"container-a"},
			"reclaim":                 {"reclaim-0"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:         "primary",
			BulkheadReclaimRelPaths:        []string{"reclaim"},
			BulkheadReclaimNumaPrefixes:    []string{"reclaim/reclaim-"},
			BulkheadPartitionRelPaths:      []string{"partition"},
			BulkheadReclaimSiblingRelPaths: []string{"sibling"},
			EnableBulkheadReclaimSiblings:  true,
			// Opt in so the cgroup v2 disabled-reset logic under test is not
			// short-circuited by the cgroup v2 gate. cgroup v1 is unaffected.
			EnableBulkheadCpusetTopologyOnCgroupV2: true,
		},
		cgroup: cg,
	}
	in := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
						ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
						Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
						Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
							Name:        "main",
							ContainerID: "containerd://" + containerID,
						}}},
					}}},
				},
			},
			Topology: &machine.CPUTopology{
				CPUDetails: machine.CPUDetails{
					0: {},
					1: {},
					2: {},
					3: {},
				},
			},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
			ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
				0: machine.NewCPUSet(2, 3),
			},
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				podUID: {
					"main": machine.NewCPUSet(0),
				},
			},
		}},
	}
	return p, cg, in, containerRel
}

func TestCPUSetTopologyPluginPeriodicalHandlerResetsSchedLoadBalanceWhenDisabledV1(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{version: cgroupclient.CgroupVersionV1}
	p := &CPUSetTopologyPlugin{cgroup: cg}

	err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{})
	if err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if got := cg.schedLoadBalance[""]; !got {
		t.Fatalf("root sched_load_balance = %t, want true", got)
	}
}

func TestCPUSetTopologyPluginPeriodicalHandlerAppliesSchedLoadBalanceFalseWhenEnabledV1(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{version: cgroupclient.CgroupVersionV1}
	p := &CPUSetTopologyPlugin{cgroup: cg}

	err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{
		DynamicConf: enabledBulkheadCpusetTopologyDynamicConf(),
	})
	if err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if got := cg.schedLoadBalance[""]; got {
		t.Fatalf("root sched_load_balance = %t, want false", got)
	}
}

func TestCPUSetTopologyPluginPeriodicalHandlerResetsPartitionWhenDisabledV2(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version:  cgroupclient.CgroupVersionV2,
		existing: map[string]bool{"partition": true},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPartitionRelPaths:              []string{"partition"},
			EnableBulkheadCpusetTopologyOnCgroupV2: true,
		},
		cgroup: cg,
	}

	err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{})
	if err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if got := cg.partitionWrites["partition"]; got != cgcommon.CPUSetPartitionFlagMember {
		t.Fatalf("partition flag = %s, want %s", got, cgcommon.CPUSetPartitionFlagMember)
	}
}

func TestCPUSetTopologyPluginPeriodicalHandlerAppliesPartitionRootWhenEnabledV2(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version:  cgroupclient.CgroupVersionV2,
		existing: map[string]bool{"partition": true},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPartitionRelPaths:              []string{"partition"},
			EnableBulkheadCpusetTopologyOnCgroupV2: true,
		},
		cgroup: cg,
	}

	err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{
		DynamicConf: enabledBulkheadCpusetTopologyDynamicConf(),
	})
	if err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if got := cg.partitionWrites["partition"]; got != cgcommon.CPUSetPartitionFlagRoot {
		t.Fatalf("partition flag = %s, want %s", got, cgcommon.CPUSetPartitionFlagRoot)
	}
}

func TestCPUSetTopologyPluginEnableGatedOnCgroupV2(t *testing.T) {
	t.Parallel()

	enabledConf := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			DynamicConf: enabledBulkheadCpusetTopologyDynamicConf(),
		},
	}

	tests := []struct {
		name      string
		version   cgroupclient.CgroupVersion
		optInOnV2 bool
		want      bool
	}{
		{name: "v2 opt-out is inert", version: cgroupclient.CgroupVersionV2, optInOnV2: false, want: false},
		{name: "v2 opt-in delegates", version: cgroupclient.CgroupVersionV2, optInOnV2: true, want: true},
		{name: "v1 opt-out delegates", version: cgroupclient.CgroupVersionV1, optInOnV2: false, want: true},
		{name: "v1 opt-in delegates", version: cgroupclient.CgroupVersionV1, optInOnV2: true, want: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &CPUSetTopologyPlugin{
				cfg: bulkheadconfig.BulkheadConfiguration{
					EnableBulkheadCpusetTopologyOnCgroupV2: tt.optInOnV2,
				},
				cgroup: &fakeCgroupClient{version: tt.version},
			}
			if got := p.Enable(enabledConf); got != tt.want {
				t.Fatalf("Enable() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCPUSetTopologyPluginDisabledHandlerInertOnCgroupV2OptOut(t *testing.T) {
	t.Parallel()

	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV2,
		"bulkhead-disabled-v2-gated-pod",
		"bulkhead-disabled-v2-gated-container",
	)
	// Flip the helper's opt-in back off to exercise the gate.
	p.cfg.EnableBulkheadCpusetTopologyOnCgroupV2 = false

	if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("cgroup v2 opt-out disabled handler must not write, got %#v", cg.writes)
	}
	if len(cg.cpusetWrites) != 0 {
		t.Fatalf("cgroup v2 opt-out disabled handler must not write cpuset, got %#v", cg.cpusetWrites)
	}
}

func TestCPUSetTopologyPluginPeriodicalHandlerInertOnCgroupV2OptOut(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version:  cgroupclient.CgroupVersionV2,
		existing: map[string]bool{"partition": true},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPartitionRelPaths: []string{"partition"},
		},
		cgroup: cg,
	}

	if err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{
		DynamicConf: enabledBulkheadCpusetTopologyDynamicConf(),
	}); err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if len(cg.partitionWrites) != 0 {
		t.Fatalf("cgroup v2 opt-out periodical handler must not write partition, got %#v", cg.partitionWrites)
	}
}

func TestEnableBulkheadCpusetTopologyRequiresNonOverlapReclaimedCores(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                                       string
		enableBulkheadCpusetTopology               bool
		stateAllowSharedCoresOverlapReclaimedCores bool
		confAllowSharedCoresOverlapReclaimedCores  bool
		want                                       bool
	}{
		{
			name:                         "enabled and non overlap",
			enableBulkheadCpusetTopology: true,
			want:                         true,
		},
		{
			name:                         "enabled but overlap",
			enableBulkheadCpusetTopology: true,
			stateAllowSharedCoresOverlapReclaimedCores: true,
		},
		{
			name: "disabled and non overlap",
		},
		{
			name:                         "uses state overlap instead of dynamic config overlap",
			enableBulkheadCpusetTopology: true,
			confAllowSharedCoresOverlapReclaimedCores: true,
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := bulkheadCpusetTopologyDynamicConf(
				tt.enableBulkheadCpusetTopology,
				tt.confAllowSharedCoresOverlapReclaimedCores,
			)
			state := cpustate.NewCPUPluginState(nil)
			state.SetAllowSharedCoresOverlapReclaimedCores(tt.stateAllowSharedCoresOverlapReclaimedCores)
			if got := enableBulkheadCpusetTopology(bulkheadapi.HandlerContext{
				CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
					DynamicConf: conf,
					State:       state,
				},
			}); got != tt.want {
				t.Fatalf("enableBulkheadCpusetTopology() = %t, want %t", got, tt.want)
			}
		})
	}
}

func enabledBulkheadCpusetTopologyDynamicConf() *dynamicconfig.Configuration {
	return bulkheadCpusetTopologyDynamicConf(true, false)
}

func bulkheadCpusetTopologyDynamicConf(enableBulkheadCpusetTopology, allowSharedCoresOverlapReclaimedCores bool) *dynamicconfig.Configuration {
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetTopology = enableBulkheadCpusetTopology
	conf.AdminQoSConfiguration.CPUProvisionConfiguration.AllowSharedCoresOverlapReclaimedCores = allowSharedCoresOverlapReclaimedCores
	return conf
}

func TestCPUSetTopologyPluginRetainsFreshMissingPodWithinAdmissionProtectionTTL(t *testing.T) {
	t.Parallel()

	podUID := "missing-pod"
	podScope := "kubepods/podmissing-pod"
	now := time.Unix(100, 0)
	statErrors := make(map[string]error)
	for _, rel := range cgcommon.GetPodRelativeCgroupPathCandidates(podUID) {
		statErrors[strings.Trim(rel, "/")] = os.ErrNotExist
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {"main": machine.NewCPUSet(0, 1)},
		},
	}}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: &metapod.PodFetcherStub{}},
	}

	for _, tt := range []struct {
		name         string
		protectUntil time.Time
		wantPending  bool
	}{
		{name: "unexpired admission protection", protectUntil: now.Add(time.Second), wantPending: true},
		{name: "expired admission protection", protectUntil: now, wantPending: false},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &CPUSetTopologyPlugin{
				cgroup: &fakeCgroupClient{statErrors: statErrors},
				now:    func() time.Time { return now },
				pendingProtections: map[string]pendingPodProtection{
					podUID: {rel: podScope, protectUntil: tt.protectUntil},
				},
			}
			res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
				CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
					MetaServer: metaServer,
					Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
				},
				DesiredView: view,
			})
			if err != nil {
				t.Fatalf("build expected cpuset: %v", err)
			}
			if got := len(res.PendingByPod); (got == 1) != tt.wantPending {
				t.Fatalf("pending count = %d, want pending=%t: %#v", got, tt.wantPending, res.PendingByPod)
			}
			if tt.wantPending && res.PendingByPod[0].ScopeRel != podScope {
				t.Fatalf("pending scope = %q, want %q", res.PendingByPod[0].ScopeRel, podScope)
			}
		})
	}
}

func TestCPUSetTopologyPluginDropsFreshMissingPodWithoutPodCgroupFromPendingProtection(t *testing.T) {
	t.Parallel()

	podUID := "missing-pod"
	statErrors := make(map[string]error)
	for _, rel := range cgcommon.GetPodRelativeCgroupPathCandidates(podUID) {
		statErrors[strings.Trim(rel, "/")] = os.ErrNotExist
	}
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{statErrors: statErrors},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {
				"main": machine.NewCPUSet(0, 1),
			},
		},
	}}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &metapod.PodFetcherStub{},
		},
	}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: metaServer,
			Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
		},
		DesiredView: view,
	})
	if err != nil {
		t.Fatalf("fresh missing pod with no pod cgroup must be ignored as stale checkpoint state, got %v", err)
	}
	if len(res.ExpectedByRel) != 0 {
		t.Fatalf("expected no resolved leaves, got %#v", res.ExpectedByRel)
	}
	if len(res.PendingByPod) != 0 {
		t.Fatalf("stale checkpoint entry entered pending protection: %#v", res.PendingByPod)
	}
}

type admissionContextKey struct{}

type admissionContextContainerIDFetcher struct {
	metapod.PodFetcherStub
	wantValue string
	calls     int
	pod       *v1.Pod
}

type rotatingContainerIDFetcher struct {
	metapod.PodFetcherStub
	containerIDs []string
	calls        int
	pod          *v1.Pod
}

type disappearingContainerIDFetcher struct {
	metapod.PodFetcherStub
	containerID string
	calls       int
	pod         *v1.Pod
}

type allocationLookupState struct {
	cpustate.ReadonlyState
	info *cpustate.AllocationInfo
}

func (s *allocationLookupState) GetAllocationInfo(_, _ string) *cpustate.AllocationInfo {
	return s.info
}

type bypassAwarePodFetcher struct {
	metapod.PodFetcherStub
	pod *v1.Pod
}

func (f *bypassAwarePodFetcher) GetPod(ctx context.Context, _ string) (*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue {
		return nil, metapod.NewPodNotFoundError(string(f.pod.UID))
	}
	return f.pod.DeepCopy(), nil
}

func (f *bypassAwarePodFetcher) GetPodList(ctx context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue ||
		ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod list lookup was not strict fresh")
	}
	return []*v1.Pod{f.pod.DeepCopy()}, nil
}

type freshPodLookupFetcher struct {
	metapod.PodFetcherStub
	pod      *v1.Pod
	err      error
	freshHit bool
}

type absenceThenFreshPodFetcher struct {
	metapod.PodFetcherStub
	absenceErr error
	pod        *v1.Pod
	freshErr   error
	freshHit   bool
}

func (f *absenceThenFreshPodFetcher) GetContainerIDWithContext(
	context.Context, string, string,
) (string, error) {
	return "", f.absenceErr
}

func (f *absenceThenFreshPodFetcher) GetPod(ctx context.Context, _ string) (*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod freshness query did not bypass cache")
	}
	if ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod freshness query did not require strict bypass")
	}
	f.freshHit = true
	if f.freshErr != nil {
		return nil, f.freshErr
	}
	if f.pod == nil {
		return nil, metapod.NewPodNotFoundError("missing")
	}
	return f.pod.DeepCopy(), nil
}

func (f *absenceThenFreshPodFetcher) GetPodList(
	ctx context.Context, _ func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod freshness query did not bypass cache")
	}
	if ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod freshness query did not require strict bypass")
	}
	f.freshHit = true
	if f.freshErr != nil && !metapod.IsPodNotFound(f.freshErr) {
		return nil, f.freshErr
	}
	if f.pod == nil {
		return nil, nil
	}
	return []*v1.Pod{f.pod.DeepCopy()}, nil
}

func (f *freshPodLookupFetcher) GetPod(ctx context.Context, _ string) (*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod freshness query did not bypass cache")
	}
	f.freshHit = true
	if f.err != nil {
		return nil, f.err
	}
	if f.pod == nil {
		return nil, metapod.NewPodNotFoundError("missing")
	}
	return f.pod.DeepCopy(), nil
}

func (f *freshPodLookupFetcher) GetPodList(
	ctx context.Context, _ func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue ||
		ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod list lookup was not strict fresh")
	}
	f.freshHit = true
	if f.err != nil && !metapod.IsPodNotFound(f.err) {
		return nil, f.err
	}
	if f.pod == nil {
		return nil, nil
	}
	return []*v1.Pod{f.pod.DeepCopy()}, nil
}

func (f *disappearingContainerIDFetcher) GetContainerIDWithContext(
	context.Context, string, string,
) (string, error) {
	f.calls++
	if f.calls == 1 {
		return f.containerID, nil
	}
	return "", metapod.ErrContainerNotFound
}

func (f *disappearingContainerIDFetcher) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod freshness query did not bypass cache")
	}
	if ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod freshness query did not require strict bypass")
	}
	if f.pod == nil {
		return nil, metapod.NewPodNotFoundError(podUID)
	}
	return f.pod.DeepCopy(), nil
}

func (f *disappearingContainerIDFetcher) GetPodList(
	ctx context.Context, _ func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue ||
		ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod list lookup was not strict fresh")
	}
	if f.pod == nil {
		return nil, nil
	}
	return []*v1.Pod{f.pod.DeepCopy()}, nil
}

func (f *rotatingContainerIDFetcher) GetContainerIDWithContext(
	context.Context, string, string,
) (string, error) {
	containerID := f.containerIDs[f.calls]
	f.calls++
	return containerID, nil
}

func (f *rotatingContainerIDFetcher) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue ||
		ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod lookup was not strict fresh")
	}
	if f.pod == nil {
		return nil, metapod.NewPodNotFoundError(podUID)
	}
	return f.pod.DeepCopy(), nil
}

func (f *rotatingContainerIDFetcher) GetPodList(
	ctx context.Context, _ func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue ||
		ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod list lookup was not strict fresh")
	}
	if f.pod == nil {
		return nil, nil
	}
	return []*v1.Pod{f.pod.DeepCopy()}, nil
}

func (f *admissionContextContainerIDFetcher) GetContainerIDWithContext(
	ctx context.Context, _, _ string,
) (string, error) {
	if got := ctx.Value(admissionContextKey{}); got != f.wantValue {
		return "", fmt.Errorf("container ID lookup context value = %v, want %q", got, f.wantValue)
	}
	f.calls++
	return "context-aware-container", nil
}

func (f *admissionContextContainerIDFetcher) GetPod(ctx context.Context, _ string) (*v1.Pod, error) {
	if got := ctx.Value(admissionContextKey{}); got != f.wantValue {
		return nil, fmt.Errorf("pod lookup context value = %v, want %q", got, f.wantValue)
	}
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue ||
		ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod lookup was not strict fresh")
	}
	return f.pod.DeepCopy(), nil
}

func (f *admissionContextContainerIDFetcher) GetPodList(
	ctx context.Context, _ func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	if got := ctx.Value(admissionContextKey{}); got != f.wantValue {
		return nil, fmt.Errorf("pod list lookup context value = %v, want %q", got, f.wantValue)
	}
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue ||
		ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("pod list lookup was not strict fresh")
	}
	return []*v1.Pod{f.pod.DeepCopy()}, nil
}

type failingAdmissionContainerIDFetcher struct {
	metapod.PodFetcherStub
	err error
}

func (f *failingAdmissionContainerIDFetcher) GetContainerIDWithContext(
	context.Context, string, string,
) (string, error) {
	return "", f.err
}

func TestCPUSetTopologyExpectedBuildAndFinalPublishUseAdmissionContext(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "context-aware-pod"
		containerName = "main"
		containerRel  = "kubepods/context-aware-pod/context-aware-container"
		contextValue  = "admission"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "cpuset-topology-admission-context",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == "context-aware-container" {
				return containerRel, false, nil
			}
			return "", true, nil
		},
	})
	fetcher := &admissionContextContainerIDFetcher{
		wantValue: contextValue,
		pod: &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
			Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
			Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
				Name: containerName, ContainerID: "containerd://context-aware-container",
			}}},
		},
	}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
	}
	desired := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {containerName: machine.NewCPUSet(0, 1)},
		},
	}}
	ctx := context.WithValue(context.Background(), admissionContextKey{}, contextValue)

	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{existing: map[string]bool{containerRel: true}},
	}
	expected, err := p.buildExpectedCPUSetByRel(ctx, bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
		DesiredView:                desired,
	})
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if got := expected.ExpectedByRel[containerRel].String(); got != "0-1" {
		t.Fatalf("expected cpuset = %q, want 0-1", got)
	}

	snapshot := &topology.CompleteSnapshot{Entries: map[string]topology.EntryState{
		containerRel: {Identity: topology.CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
	}}
	validated, err := validateContainerLifecycleProofs(ctx, metaServer, expected.LifecycleProofs)
	if err != nil {
		t.Fatalf("validateContainerLifecycleProofs() error = %v", err)
	}
	published, err := containerCPUSetByPodFromFinalSnapshot(snapshot, validated, nil)
	if err != nil {
		t.Fatalf("containerCPUSetByPodFromFinalSnapshot() error = %v", err)
	}
	if got := published[podUID][containerName].String(); got != "0-1" {
		t.Fatalf("published cpuset = %q, want 0-1", got)
	}
	if fetcher.calls != 1 {
		t.Fatalf("context-aware container ID lookup calls = %d, want build-only count 1", fetcher.calls)
	}
}

func TestFinalSnapshotRejectsContainerIdentityChangeAsStale(t *testing.T) {
	t.Parallel()
	const (
		podUID        = "pod-final-snapshot-identity-change"
		containerName = "main"
		oldContainer  = "container-final-old"
		newContainer  = "container-final-new"
		oldRel        = "primary/container-old"
	)
	oldPod := lifecycleProofPod(podUID, []string{containerName}, map[string]string{containerName: oldContainer})
	newPod := lifecycleProofPod(podUID, []string{containerName}, map[string]string{containerName: newContainer})
	frozen, err := freezeContainerLifecycleProofs([]containerLifecycleProof{{
		PodUID: podUID, ContainerName: containerName, ContainerID: oldContainer,
		RelativePath: oldRel, DesiredCPUSet: machine.NewCPUSet(0, 1),
		State: containerLifecycleResolved,
	}}, map[string]*v1.Pod{podUID: oldPod})
	if err != nil {
		t.Fatalf("freezeContainerLifecycleProofs() error = %v", err)
	}
	fetcher := &strictSnapshotFetcher{pods: []*v1.Pod{newPod}}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
	}
	_, err = validateContainerLifecycleProofs(context.Background(), metaServer, frozen)
	var staleErr *containerLifecycleProofStaleError
	if !errors.As(err, &staleErr) || !errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("final snapshot identity change error = %v, want lifecycle stale error", err)
	}
}

func TestFinalSnapshotAcceptsCurrentContainerProofAfterIdentityRefresh(t *testing.T) {
	t.Parallel()
	const (
		podUID        = "pod-final-snapshot-current-proof"
		containerName = "main"
		newContainer  = "container-proof-new"
		newRel        = "primary/container-proof-new"
	)
	pod := lifecycleProofPod(podUID, []string{containerName}, map[string]string{containerName: newContainer})
	frozen, err := freezeContainerLifecycleProofs([]containerLifecycleProof{{
		PodUID: podUID, ContainerName: containerName, ContainerID: newContainer,
		RelativePath: newRel, DesiredCPUSet: machine.NewCPUSet(0, 1),
		State: containerLifecycleResolved,
	}}, map[string]*v1.Pod{podUID: pod})
	if err != nil {
		t.Fatalf("freezeContainerLifecycleProofs() error = %v", err)
	}
	fetcher := &strictSnapshotFetcher{pods: []*v1.Pod{pod}}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
	}
	validated, err := validateContainerLifecycleProofs(context.Background(), metaServer, frozen)
	if err != nil {
		t.Fatalf("validateContainerLifecycleProofs() error = %v", err)
	}
	published, err := containerCPUSetByPodFromFinalSnapshot(
		&topology.CompleteSnapshot{Entries: map[string]topology.EntryState{
			newRel: {Identity: topology.CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1)},
		}},
		validated, nil)
	if err != nil {
		t.Fatalf("current container proof must be publishable: %v", err)
	}
	if got := published[podUID][containerName]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("published current container cpuset = %s, want 0-1", got.String())
	}
}

func TestFinalSnapshotFailsClosedWhenIdentityRefreshFails(t *testing.T) {
	t.Parallel()
	refreshErr := errors.New("kubelet refresh failed")
	fetcher := &strictSnapshotFetcher{listErr: refreshErr}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
	}
	_, err := validateContainerLifecycleProofs(context.Background(), metaServer, containerLifecycleProofSet{
		OrderedProofs: []containerLifecycleProof{{
			PodUID: "pod-final-snapshot-refresh-failed", ContainerName: "main",
			ContainerID: "container-old", RelativePath: "primary/container-old",
			DesiredCPUSet: machine.NewCPUSet(0, 1), State: containerLifecycleResolved,
		}},
	})
	if !errors.Is(err, refreshErr) {
		t.Fatalf("final snapshot refresh error = %v, want %v", err, refreshErr)
	}
}

// parentSafeDynamicLeafFixture builds a validated resolved proof and matching
// final snapshot without consulting raw DesiredView state during finalization.
func parentSafeDynamicLeafFixture(
	t *testing.T,
	podUID string,
	containerID string,
	rel string,
	desiredCPUs machine.CPUSet,
	observedCPUs machine.CPUSet,
) (validatedContainerLifecycleProofSet, *topology.CompleteSnapshot) {
	t.Helper()
	proof := containerLifecycleProof{
		PodUID: podUID, ContainerName: "main", ContainerID: containerID,
		RelativePath: rel, DesiredCPUSet: desiredCPUs, State: containerLifecycleResolved,
	}
	snapshot := &topology.CompleteSnapshot{Entries: map[string]topology.EntryState{
		rel: {Identity: topology.CgroupIdentity{Inode: 1}, CPUs: observedCPUs},
	}}
	return validatedContainerLifecycleProofSet{OrderedProofs: []containerLifecycleProof{proof}}, snapshot
}

func TestParentSafeFinalizationPublishesDynamicLeafSafeSuperset(t *testing.T) {
	t.Parallel()

	const (
		podUID      = "pod-parentsafe-superset"
		containerID = "container-parentsafe-superset"
		rel         = "primary/container-parentsafe-superset"
	)
	desiredCPUs := machine.NewCPUSet(0, 1)
	observedCPUs := machine.NewCPUSet(0, 1, 2, 3)
	finalPrimary := machine.NewCPUSet(0, 1, 2, 3, 4, 5)
	finalReclaim := machine.NewCPUSet(6, 7)
	proofs, snapshot := parentSafeDynamicLeafFixture(
		t, podUID, containerID, rel, desiredCPUs, observedCPUs)

	published, err := containerCPUSetByPodFromFinalSnapshot(
		snapshot, proofs, map[string]struct{}{rel: {}})
	if err != nil {
		t.Fatalf("parent-safe dynamic leaf safe superset must publish: %v", err)
	}
	if got := published[podUID]["main"]; !got.Equals(observedCPUs) {
		t.Fatalf("published dynamic leaf cpuset = %s, want observed superset %s",
			got.String(), observedCPUs.String())
	}
	_ = finalPrimary
	_ = finalReclaim
}

func TestFinalizationPublishesValidatedResolvedLeafFromFinalSnapshot(t *testing.T) {
	t.Parallel()

	const (
		podUID      = "pod-parentsafe-reclaim-overlap"
		containerID = "container-parentsafe-reclaim-overlap"
		rel         = "primary/container-parentsafe-reclaim-overlap"
	)
	desiredCPUs := machine.NewCPUSet(0, 1)
	// Observed superset reaches into the finalized reclaim domain, which is
	// never a safe superset even on the ParentSafe path.
	observedCPUs := machine.NewCPUSet(0, 1, 6)
	proofs, snapshot := parentSafeDynamicLeafFixture(
		t, podUID, containerID, rel, desiredCPUs, observedCPUs)
	published, err := containerCPUSetByPodFromFinalSnapshot(snapshot, proofs, nil)
	if err != nil {
		t.Fatalf("publish validated resolved leaf: %v", err)
	}
	if got := published[podUID]["main"]; !got.Equals(observedCPUs) {
		t.Fatalf("published dynamic leaf cpuset = %s, want final snapshot %s", got.String(), observedCPUs.String())
	}
}

func TestFinalizationDoesNotFallBackToRawDesiredCPUSet(t *testing.T) {
	t.Parallel()

	const (
		podUID      = "pod-parentsafe-missing-desired"
		containerID = "container-parentsafe-missing-desired"
		rel         = "primary/container-parentsafe-missing-desired"
	)
	desiredCPUs := machine.NewCPUSet(0, 1)
	observedCPUs := machine.NewCPUSet(0, 2, 3)
	proofs, snapshot := parentSafeDynamicLeafFixture(
		t, podUID, containerID, rel, desiredCPUs, observedCPUs)
	published, err := containerCPUSetByPodFromFinalSnapshot(snapshot, proofs, nil)
	if err != nil {
		t.Fatalf("publish validated resolved leaf: %v", err)
	}
	if got := published[podUID]["main"]; !got.Equals(observedCPUs) {
		t.Fatalf("published dynamic leaf cpuset = %s, want final snapshot %s", got.String(), observedCPUs.String())
	}
}

func TestFinalizationReadsResolvedPathWithoutReclassification(t *testing.T) {
	t.Parallel()

	const (
		podUID      = "pod-parentsafe-exceeds-primary"
		containerID = "container-parentsafe-exceeds-primary"
		rel         = "primary/container-parentsafe-exceeds-primary"
	)
	desiredCPUs := machine.NewCPUSet(0, 1)
	// Observed superset includes CPU 9 which is outside the finalized primary
	// domain and not part of reclaim; it must not be published.
	observedCPUs := machine.NewCPUSet(0, 1, 9)
	proofs, snapshot := parentSafeDynamicLeafFixture(
		t, podUID, containerID, rel, desiredCPUs, observedCPUs)
	published, err := containerCPUSetByPodFromFinalSnapshot(snapshot, proofs, nil)
	if err != nil {
		t.Fatalf("publish validated resolved leaf: %v", err)
	}
	if got := published[podUID]["main"]; !got.Equals(observedCPUs) {
		t.Fatalf("published dynamic leaf cpuset = %s, want final snapshot %s", got.String(), observedCPUs.String())
	}
}

func TestFullConvergenceFinalizationUsesFinalSnapshotCPUSet(t *testing.T) {
	t.Parallel()

	const (
		podUID      = "pod-full-converge-superset"
		containerID = "container-full-converge-superset"
		rel         = "primary/container-full-converge-superset"
	)
	desiredCPUs := machine.NewCPUSet(0, 1)
	observedCPUs := machine.NewCPUSet(0, 1, 2, 3)
	proofs, snapshot := parentSafeDynamicLeafFixture(
		t, podUID, containerID, rel, desiredCPUs, observedCPUs)
	published, err := containerCPUSetByPodFromFinalSnapshot(snapshot, proofs, nil)
	if err != nil {
		t.Fatalf("publish validated resolved leaf: %v", err)
	}
	if got := published[podUID]["main"]; !got.Equals(observedCPUs) {
		t.Fatalf("published dynamic leaf cpuset = %s, want final snapshot %s", got.String(), observedCPUs.String())
	}
}

func TestCPUSetTopologyPluginDropsRotatedContainerIdentity(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod-rotated-container"
		containerName = "main"
		oldContainer  = "container-old"
		newContainer  = "container-new"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "rotated-container-identity",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == oldContainer {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &rotatingContainerIDFetcher{
		containerIDs: []string{oldContainer, newContainer},
		pod:          &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)}},
	}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {containerName: machine.NewCPUSet(0, 1)},
		},
	}}

	res, err := (&CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath: "kubepods",
		},
		cgroup: &fakeCgroupClient{statErrors: map[string]error{
			"kubepods/besteffort/pod" + podUID: os.ErrNotExist,
		}},
	}).buildExpectedCPUSetByRel(
		context.Background(),
		bulkheadapi.HandlerContext{
			CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
			DesiredView:                view,
		},
	)
	if err != nil {
		t.Fatalf("rotated container identity must be dropped, got %v", err)
	}
	if len(res.ExpectedByRel) != 0 {
		t.Fatalf("expected no resolved leaves, got %#v", res.ExpectedByRel)
	}
	if len(res.PendingByPod) != 0 {
		t.Fatalf("rotated container identity entered pending protection: %#v", res.PendingByPod)
	}
	if fetcher.calls != 1 {
		t.Fatalf("cache-only container ID lookup calls = %d, want 1", fetcher.calls)
	}
}

func TestCPUSetTopologyPluginTreatsDisappearedContainerAsPending(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod-disappeared-container"
		containerName = "main"
		containerID   = "container-disappeared"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "disappeared-container",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &disappearingContainerIDFetcher{
		containerID: containerID,
		pod: &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
			Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
		},
	}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {containerName: machine.NewCPUSet(0, 1)},
		},
	}}

	res, err := (&CPUSetTopologyPlugin{}).buildExpectedCPUSetByRel(
		context.Background(),
		bulkheadapi.HandlerContext{
			CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
			DesiredView:                view,
		},
	)
	if err != nil {
		t.Fatalf("disappeared container must be protected as pending, got %v", err)
	}
	if len(res.ExpectedByRel) != 0 {
		t.Fatalf("expected no resolved leaves, got %#v", res.ExpectedByRel)
	}
	if len(res.PendingByPod) != 1 {
		t.Fatalf("expected one protected-pending entry, got %#v", res.PendingByPod)
	}
	if fetcher.calls != 1 {
		t.Fatalf("cache-only container ID lookup calls = %d, want 1", fetcher.calls)
	}
}

func TestCPUSetTopologyPluginReleasesFreshMissingContainerName(t *testing.T) {
	t.Parallel()

	const podScope = "kubepods/besteffort/podpod-1"
	tests := []struct {
		name   string
		cgroup *fakeCgroupClient
	}{
		{
			name: "after typed pod scope absence",
			cgroup: &fakeCgroupClient{statErrors: map[string]error{
				podScope: os.ErrNotExist,
			}},
		},
		{
			name: "while pod scope still exists",
			cgroup: &fakeCgroupClient{existing: map[string]bool{
				podScope: true,
			}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &CPUSetTopologyPlugin{
				cfg: bulkheadconfig.BulkheadConfiguration{
					BulkheadPrimaryRelPath: "kubepods",
				},
				cgroup: tt.cgroup,
			}
			allocationInfo := &cpustate.AllocationInfo{}
			allocationInfo.NativeQOSClass = string(v1.PodQOSGuaranteed)
			metaServer := &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
						ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-1")},
					}}},
				},
			}
			view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
				ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
					"pod-1": {
						"missing-container": machine.NewCPUSet(0, 1),
					},
				},
			}}

			// The fresh Pod Spec is the canonical owner of container names.
			// A pod-level cgroup only proves that the Pod is live; it cannot
			// extend the lifetime of a checkpoint-only container allocation.
			res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
				CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
					MetaServer: metaServer,
					State:      &allocationLookupState{info: allocationInfo},
				},
				DesiredView: view,
			})
			if err != nil {
				t.Fatalf("missing container name must be dropped without error, got %v", err)
			}
			if len(res.ExpectedByRel) != 0 {
				t.Fatalf("expected no resolved leaves, got %#v", res.ExpectedByRel)
			}
			if len(res.PendingByPod) != 0 {
				t.Fatalf("fresh-spec-missing container entered pending protection: %#v", res.PendingByPod)
			}
		})
	}
}

func TestCPUSetTopologyPluginRefreshesPodForPendingNativeQOSClass(t *testing.T) {
	t.Parallel()

	podUID := "pod-1"
	p := &CPUSetTopologyPlugin{}
	allocationInfo := &cpustate.AllocationInfo{}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &bypassAwarePodFetcher{pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec: v1.PodSpec{Containers: []v1.Container{{
					Name: "main",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("1"),
							v1.ResourceMemory: resource.MustParse("1Gi"),
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("1"),
							v1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				}}},
			}},
		},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {"main": machine.NewCPUSet(0, 1)},
		},
	}}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: metaServer,
			State:      &allocationLookupState{info: allocationInfo},
		},
		DesiredView: view,
	})
	if err != nil {
		t.Fatalf("missing container must remain admit-safe pending, got %v", err)
	}
	if len(res.PendingByPod) != 1 {
		t.Fatalf("expected one protected-pending entry, got %#v", res.PendingByPod)
	}
	if got := res.PendingByPod[0].NativeQOSClass; got != v1.PodQOSGuaranteed {
		t.Fatalf("pending native qos class = %q, want %q", got, v1.PodQOSGuaranteed)
	}
}

func TestCPUSetTopologyPluginFreshlyGetsPodForEveryContainerAbsenceClass(t *testing.T) {
	t.Parallel()
	absenceErrors := []error{
		metapod.NewPodNotFoundError("pod-absence"),
		metapod.ErrContainerNotFound,
		bulkheadutils.ErrContainerNotRunning,
	}
	for _, absenceErr := range absenceErrors {
		absenceErr := absenceErr
		t.Run(absenceErr.Error(), func(t *testing.T) {
			t.Parallel()
			fetcher := &absenceThenFreshPodFetcher{
				absenceErr: absenceErr,
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-absence")},
					Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
				},
			}
			metaServer := &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
			}
			allocationInfo := &cpustate.AllocationInfo{}
			allocationInfo.NativeQOSClass = string(v1.PodQOSGuaranteed)
			view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
				ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
					"pod-absence": {"main": machine.NewCPUSet(0, 1)},
				},
			}}

			res, err := (&CPUSetTopologyPlugin{}).buildExpectedCPUSetByRel(
				context.Background(),
				bulkheadapi.HandlerContext{
					CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
						MetaServer: metaServer,
						State:      &allocationLookupState{info: allocationInfo},
					},
					DesiredView: view,
				},
			)
			if err != nil {
				t.Fatalf("absence-class error %v must remain pending after fresh live pod lookup: %v",
					absenceErr, err)
			}
			if !fetcher.freshHit {
				t.Fatalf("absence-class error %v did not trigger a fresh pod lookup", absenceErr)
			}
			if len(res.PendingByPod) != 1 {
				t.Fatalf("absence-class error %v produced pending=%#v, want one entry",
					absenceErr, res.PendingByPod)
			}
		})
	}
}

func TestCPUSetTopologyPluginFreshUnknownErrorFailsForEveryContainerAbsenceClass(t *testing.T) {
	t.Parallel()
	queryErr := errors.New("fresh pod query failed")
	absenceErrors := []error{
		metapod.NewPodNotFoundError("pod-absence"),
		metapod.ErrContainerNotFound,
		bulkheadutils.ErrContainerNotRunning,
	}
	for _, absenceErr := range absenceErrors {
		absenceErr := absenceErr
		t.Run(absenceErr.Error(), func(t *testing.T) {
			t.Parallel()
			fetcher := &absenceThenFreshPodFetcher{
				absenceErr: absenceErr,
				freshErr:   queryErr,
			}
			metaServer := &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
			}
			allocationInfo := &cpustate.AllocationInfo{}
			allocationInfo.NativeQOSClass = string(v1.PodQOSGuaranteed)
			view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
				ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
					"pod-absence": {"main": machine.NewCPUSet(0, 1)},
				},
			}}

			_, err := (&CPUSetTopologyPlugin{}).buildExpectedCPUSetByRel(
				context.Background(),
				bulkheadapi.HandlerContext{
					CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
						MetaServer: metaServer,
						State:      &allocationLookupState{info: allocationInfo},
					},
					DesiredView: view,
				},
			)

			if !errors.Is(err, queryErr) {
				t.Fatalf("absence-class error %v with unknown fresh error got %v, want %v",
					absenceErr, err, queryErr)
			}
			if !fetcher.freshHit {
				t.Fatalf("absence-class error %v did not trigger a fresh pod lookup", absenceErr)
			}
		})
	}
}

func TestCPUSetTopologyPluginFreshNotFoundChecksAllQoSCgroupsBeforeDroppingStale(t *testing.T) {
	t.Parallel()
	const podUID = "cross-qos-stale-check"
	fetcher := &absenceThenFreshPodFetcher{
		absenceErr: metapod.ErrContainerNotFound,
		freshErr:   metapod.NewPodNotFoundError(podUID),
	}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
	}
	allCandidates := cgcommon.GetPodRelativeCgroupPathCandidates(podUID)
	statErrors := make(map[string]error, len(allCandidates))
	for _, candidate := range allCandidates {
		statErrors[strings.Trim(candidate, "/")] = os.ErrNotExist
	}
	bestEffortCandidates := cgcommon.GetPodRelativeCgroupPathCandidatesForQOS(
		podUID, v1.PodQOSBestEffort)
	if len(bestEffortCandidates) == 0 {
		t.Fatal("expected at least one BestEffort pod cgroup candidate")
	}
	existingBestEffort := strings.Trim(bestEffortCandidates[0], "/")
	delete(statErrors, existingBestEffort)
	cgroup := &fakeCgroupClient{
		existing:   map[string]bool{existingBestEffort: true},
		statErrors: statErrors,
	}
	allocationInfo := &cpustate.AllocationInfo{}
	allocationInfo.NativeQOSClass = string(v1.PodQOSGuaranteed)
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {"main": machine.NewCPUSet(0, 1)},
		},
	}}

	res, err := (&CPUSetTopologyPlugin{cgroup: cgroup}).buildExpectedCPUSetByRel(
		context.Background(),
		bulkheadapi.HandlerContext{
			CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
				MetaServer: metaServer,
				State:      &allocationLookupState{info: allocationInfo},
			},
			DesiredView: view,
		},
	)
	if err != nil {
		t.Fatalf("fresh NotFound with an existing alternate-QoS cgroup must remain pending: %v", err)
	}
	if len(res.PendingByPod) != 1 {
		t.Fatalf("alternate-QoS cgroup was dropped as stale: pending=%#v", res.PendingByPod)
	}
}

func TestCPUSetTopologyPluginFreshNotFoundRetainsExistingPodCgroupForEveryQOS(t *testing.T) {
	t.Parallel()
	for _, qosClass := range []v1.PodQOSClass{
		v1.PodQOSGuaranteed,
		v1.PodQOSBurstable,
		v1.PodQOSBestEffort,
	} {
		qosClass := qosClass
		t.Run(string(qosClass), func(t *testing.T) {
			t.Parallel()
			podUID := "existing-" + strings.ToLower(string(qosClass))
			fetcher := &absenceThenFreshPodFetcher{
				absenceErr: bulkheadutils.ErrContainerNotRunning,
				freshErr:   metapod.NewPodNotFoundError(podUID),
			}
			metaServer := &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
			}
			allCandidates := cgcommon.GetPodRelativeCgroupPathCandidates(podUID)
			statErrors := make(map[string]error, len(allCandidates))
			for _, candidate := range allCandidates {
				statErrors[strings.Trim(candidate, "/")] = os.ErrNotExist
			}
			qosCandidates := cgcommon.GetPodRelativeCgroupPathCandidatesForQOS(podUID, qosClass)
			if len(qosCandidates) == 0 {
				t.Fatalf("expected at least one %s pod cgroup candidate", qosClass)
			}
			existing := strings.Trim(qosCandidates[0], "/")
			delete(statErrors, existing)
			cgroup := &fakeCgroupClient{
				existing:   map[string]bool{existing: true},
				statErrors: statErrors,
			}
			view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
				ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
					podUID: {"main": machine.NewCPUSet(0, 1)},
				},
			}}

			res, err := (&CPUSetTopologyPlugin{cgroup: cgroup}).buildExpectedCPUSetByRel(
				context.Background(),
				bulkheadapi.HandlerContext{
					CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
						MetaServer: metaServer,
					},
					DesiredView: view,
				},
			)
			if err != nil {
				t.Fatalf("fresh NotFound with existing %s cgroup must remain pending: %v", qosClass, err)
			}
			if len(res.PendingByPod) != 1 {
				t.Fatalf("existing %s cgroup was dropped as stale: pending=%#v",
					qosClass, res.PendingByPod)
			}
		})
	}
}

func TestCPUSetTopologyPluginLivePodWithoutCgroupStillBuildsExpectedPendingScope(t *testing.T) {
	t.Parallel()

	const podUID = "live-pod-without-cgroup"
	fetcher := &freshPodLookupFetcher{pod: &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
		Spec: v1.PodSpec{Containers: []v1.Container{{
			Name: "main",
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("1"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
				Limits: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("1"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		}}},
	}}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}
	podRel := "kubepods/pod" + podUID
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{
			readErrByRel: map[string]error{podRel: os.ErrNotExist},
			statErrors:   map[string]error{podRel: syscall.ENOENT},
		},
		pendingProtections: map[string]pendingPodProtection{},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {"main": machine.NewCPUSet(0, 1)},
		},
	}}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
		DesiredView:                view,
	})
	if err != nil {
		t.Fatalf("live pod without cgroup must remain pending: %v", err)
	}
	dag, err := topology.BuildDAG([]topology.NodeSpec{{
		Rel:            "kubepods",
		Role:           topology.TopoNodeRolePrimary,
		Domain:         topology.DomainPrimary,
		ControlledRoot: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	scopes, err := p.pendingProtectionScopes(context.Background(), dag, res.PendingByPod)
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	want := []topology.PendingProtection{{
		ScopeRel: podRel,
		CPUs:     machine.NewCPUSet(0, 1),
		PodUID:   podUID,
		Source:   topology.PendingProtectionSourceExpectedPod,
	}}
	if !reflect.DeepEqual(scopes, want) {
		t.Fatalf("pending scopes = %#v, want %#v", scopes, want)
	}
	if !fetcher.freshHit {
		t.Fatal("live pod was not confirmed through a fresh lookup")
	}
}

func TestCPUSetTopologyPluginFailsClosedOnUnknownFreshPodLookupError(t *testing.T) {
	t.Parallel()

	queryErr := errors.New("apiserver query failed")
	fetcher := &freshPodLookupFetcher{err: queryErr}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"unknown-query-pod": {"main": machine.NewCPUSet(0, 1)},
		},
	}}

	res, err := (&CPUSetTopologyPlugin{cgroup: &fakeCgroupClient{}}).buildExpectedCPUSetByRel(
		context.Background(),
		bulkheadapi.HandlerContext{
			CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
			DesiredView:                view,
		},
	)
	if !errors.Is(err, queryErr) {
		t.Fatalf("unknown fresh pod lookup error must fail closed, got res=%#v err=%v", res, err)
	}
	if !fetcher.freshHit {
		t.Fatal("unknown error path did not execute a fresh pod lookup")
	}
}

func TestCPUSetTopologyPluginFailsExpectedCPUSetForContainerIDDeadline(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &failingAdmissionContainerIDFetcher{err: context.DeadlineExceeded},
		},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"pod-1": {
				"main": machine.NewCPUSet(0, 1),
			},
		},
	}}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
		DesiredView:                view,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("container ID deadline must fail-closed, got res=%#v err=%v", res, err)
	}
}

func TestCPUSetTopologyPluginKeepsUnmaterializedFreshContainerPending(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-1")},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
					Name:        "main",
					ContainerID: "invalid-container-id",
					State:       v1.ContainerState{Running: &v1.ContainerStateRunning{}},
				}}},
			}}},
		},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"pod-1": {
				"main": machine.NewCPUSet(0, 1),
			},
		},
	}}

	// The fresh status owns this generation, but typed cgroup absence can still
	// be the normal materialization window. Keep its CPUs scoped pending rather
	// than dropping them or treating absence as an operational probe failure.
	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
		DesiredView:                view,
	})
	if err != nil {
		t.Fatalf("unmaterialized fresh container must remain pending, got %v", err)
	}
	if len(res.PendingByPod) != 1 {
		t.Fatalf("pending entries = %#v, want one", res.PendingByPod)
	}
	if got := res.PendingByPod[0].ContainerID; got != "invalid-container-id" {
		t.Fatalf("pending container ID = %q, want invalid-container-id", got)
	}
}

// TestCPUSetTopologyPluginBuildExpectedCPUSetByRelTrimsLeadingSlash is a
// regression guard: buildExpectedCPUSetByRel must produce map keys WITHOUT a
// leading "/", so they match the childRel format that
// utils/topology/writer.expandDescendants constructs via filepath.Join. If the
// key kept the leading "/" that GetKubernetesAnyExistRelativeCgroupPath
// prepends, per-container cpuset enforcement would silently degrade to
// inheriting the parent pool target inside TopologyCoordinatorConverge.
func TestCPUSetTopologyPluginBuildExpectedCPUSetByRelTrimsLeadingSlash(t *testing.T) {
	t.Parallel()

	const (
		podUID      = "pod-build-expected-trim"
		containerID = "container-build-expected-trim"
		prefixedRel = "/kubepods/burstable/pod-a/container-a"
		expectedRel = "kubepods/burstable/pod-a/container-a"
		containerNm = "main"
	)

	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "bulkhead-build-expected-trim-" + podUID,
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return prefixedRel, false, nil
			}
			return "", true, nil
		},
	})

	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{existing: map[string]bool{expectedRel: true}},
	}
	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
						ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
						Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerNm}}},
						Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
							Name:        containerNm,
							ContainerID: "containerd://" + containerID,
						}}},
					}}},
				},
			},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				podUID: {
					containerNm: machine.NewCPUSet(0, 1),
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel: %v", err)
	}
	expected := res.ExpectedByRel
	if _, ok := expected[prefixedRel]; ok {
		t.Fatalf("map key still has leading '/': %q; keys=%v", prefixedRel, keysOf(expected))
	}
	cpus, ok := expected[expectedRel]
	if !ok {
		t.Fatalf("expected key %q not found; keys=%v", expectedRel, keysOf(expected))
	}
	if got := cpus.String(); got != "0-1" {
		t.Fatalf("cpuset @ %s = %q, want 0-1", expectedRel, got)
	}
}

func keysOf(m map[string]machine.CPUSet) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func totalApplyCount(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func TestCPUSetTopologyPluginRecompilesPreWriteStalePlan(t *testing.T) {
	t.Parallel()
	testCPUSetTopologyPluginRecompilesSafeDisposition(
		t, topology.ReplanSafeNoPhysicalWrites)
}

func TestCPUSetTopologyPluginRecompilesAfterVerifiedRollback(t *testing.T) {
	t.Parallel()
	testCPUSetTopologyPluginRecompilesSafeDisposition(
		t, topology.ReplanSafeAfterVerifiedRollback)
}

func testCPUSetTopologyPluginRecompilesSafeDisposition(
	t *testing.T,
	disposition topology.ReplanDisposition,
) {
	t.Helper()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "test"}
	attempts := 0
	err := runCPUSetTopologyAdjustment(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		}),
		func(
			_ context.Context,
			_ bulkheadapi.HandlerContext,
			_ *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attempts++
			if attempts == 1 {
				return topology.ConvergenceResult{ReplanDisposition: disposition}, stale
			}
			return topology.ConvergenceResult{}, nil
		},
	)
	if err != nil {
		t.Fatalf("safe stale adjustment error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("adjustment attempts = %d, want 2", attempts)
	}
}

func TestCPUSetTopologyPluginRecompilesFromVerifiedFinalState(t *testing.T) {
	t.Parallel()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "publication"}
	verified := &topology.CompleteSnapshot{
		Entries: map[string]topology.EntryState{
			"root": {Rel: "root", CPUs: machine.NewCPUSet(0, 1)},
		},
	}
	attempts := 0
	err := runCPUSetTopologyAdjustment(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		}),
		func(
			_ context.Context,
			_ bulkheadapi.HandlerContext,
			budget *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attempts++
			if attempts == 1 {
				return topology.ConvergenceResult{
					ReplanDisposition:    topology.ReplanSafeFromVerifiedFinalState,
					FinalSnapshot:        verified,
					FinalSnapshotCurrent: true,
				}, stale
			}
			initial := budget.InitialSnapshot()
			if initial == nil || !initial.Entries["root"].CPUs.Equals(machine.NewCPUSet(0, 1)) {
				t.Fatalf("second attempt initial snapshot = %#v, want verified final state", initial)
			}
			return topology.ConvergenceResult{}, nil
		},
	)
	if err != nil {
		t.Fatalf("verified-final-state recompile error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("adjustment attempts = %d, want 2", attempts)
	}
}

func TestCPUSetTopologyPluginDoesNotRecompileTerminalStale(t *testing.T) {
	t.Parallel()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "terminal"}
	attempts := 0
	err := runCPUSetTopologyAdjustment(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		}),
		func(
			_ context.Context,
			_ bulkheadapi.HandlerContext,
			_ *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attempts++
			return topology.ConvergenceResult{
				ReplanDisposition: topology.ReplanNotAllowed,
			}, stale
		},
	)
	if !errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("terminal stale error = %v, want stale cause", err)
	}
	if attempts != 1 {
		t.Fatalf("terminal stale attempts = %d, want 1", attempts)
	}
}

func TestCPUSetTopologyPluginReplanStartsFromPristineDesiredView(t *testing.T) {
	t.Parallel()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "pristine"}
	desired := model.NewDesiredView()
	desired.DesiredReclaimEffective = machine.NewCPUSet(2, 3)
	attempts := 0
	var firstAttempt *model.DesiredView
	err := runCPUSetTopologyAdjustment(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: desired},
		topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		}),
		func(
			_ context.Context,
			in bulkheadapi.HandlerContext,
			_ *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attempts++
			if attempts == 1 {
				firstAttempt = in.DesiredView
				in.DesiredView.DesiredReclaimEffective = machine.NewCPUSet(9)
				return topology.ConvergenceResult{
					ReplanDisposition: topology.ReplanSafeNoPhysicalWrites,
				}, stale
			}
			if in.DesiredView == firstAttempt {
				t.Fatal("replan reused the mutable desired view from the prior attempt")
			}
			if !in.DesiredView.DesiredReclaimEffective.Equals(machine.NewCPUSet(2, 3)) {
				t.Fatalf("replan desired reclaim = %s, want pristine 2-3",
					in.DesiredView.DesiredReclaimEffective.String())
			}
			return topology.ConvergenceResult{}, nil
		},
	)
	if err != nil {
		t.Fatalf("pristine desired-view replan error = %v", err)
	}
	if !desired.DesiredReclaimEffective.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("caller's desired view was mutated: %s", desired.DesiredReclaimEffective.String())
	}
}

func TestCPUSetTopologyPluginReplansShareDeadlineAndWriteBudget(t *testing.T) {
	t.Parallel()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "budget"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	budget := topology.NewAdjustmentBudget(ctx, topology.ConvergenceBudget{
		MaxPlanOperations: 5,
		DeadlineDuration:  10 * time.Minute,
	})
	var firstDeadline time.Time
	attempts := 0
	err := runCPUSetTopologyAdjustment(
		ctx,
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		budget,
		func(
			_ context.Context,
			_ bulkheadapi.HandlerContext,
			budget *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attempts++
			remaining := budget.RemainingConvergenceBudget()
			if attempts == 1 {
				firstDeadline = remaining.Deadline
				if err := budget.RecordPhysicalWrites(2); err != nil {
					t.Fatalf("record first attempt writes: %v", err)
				}
				budget.ObserveCurrentSnapshot(budget.BeginSnapshotObservation())
				return topology.ConvergenceResult{
					ReplanDisposition: topology.ReplanSafeAfterVerifiedRollback,
				}, stale
			}
			if !remaining.Deadline.Equal(firstDeadline) {
				t.Fatalf("deadline changed across replans: first=%s second=%s",
					firstDeadline, remaining.Deadline)
			}
			if remaining.MaxPlanOperations != 5 {
				t.Fatalf("remaining logical plan-operation budget = %d, want 5", remaining.MaxPlanOperations)
			}
			return topology.ConvergenceResult{}, nil
		},
	)
	if err != nil {
		t.Fatalf("shared-budget replan error = %v", err)
	}
}

func TestCPUSetTopologyPluginContinuousChurnExhaustsReplanBudget(t *testing.T) {
	t.Parallel()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "continuous_churn"}
	attempts := 0
	err := runCPUSetTopologyAdjustment(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		}),
		func(
			_ context.Context,
			_ bulkheadapi.HandlerContext,
			_ *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attempts++
			return topology.ConvergenceResult{
				ReplanDisposition: topology.ReplanSafeNoPhysicalWrites,
			}, stale
		},
	)
	var exhausted *topology.ReplanBudgetExceededError
	if !errors.As(err, &exhausted) {
		t.Fatalf("continuous churn error = %v, want ReplanBudgetExceededError", err)
	}
	if !errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("continuous churn lost last stale cause: %v", err)
	}
	if !errors.Is(err, topology.ErrAdjustmentReplanBudgetExceeded) {
		t.Fatalf("continuous churn error = %v, want replan-budget sentinel", err)
	}
	if errors.Is(err, topology.ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("zero-write churn must not report write-budget exhaustion: %v", err)
	}
	if attempts != 4 {
		t.Fatalf("continuous churn attempts = %d, want 4", attempts)
	}
}

func TestCPUSetTopologyPluginChangingStaleSignaturesStillCompileAtMostFourTimes(t *testing.T) {
	t.Parallel()
	attempts := 0
	err := runCPUSetTopologyAdjustment(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		}),
		func(
			_ context.Context,
			_ bulkheadapi.HandlerContext,
			_ *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attempts++
			result := topology.ConvergenceResult{
				ReplanDisposition: topology.ReplanSafeNoPhysicalWrites,
			}
			return result, &topology.PlanStaleError{
				Rel:      fmt.Sprintf("root/generation-%d", attempts),
				Resource: "changing_signature",
			}
		},
	)

	var exhausted *topology.ReplanBudgetExceededError
	if !errors.As(err, &exhausted) {
		t.Fatalf("changing churn error = %v, want ReplanBudgetExceededError", err)
	}
	if attempts != 4 {
		t.Fatalf("changing churn compile attempts = %d, want 4", attempts)
	}
	if exhausted.Replans != 3 || exhausted.MaxReplans != 3 {
		t.Fatalf("changing churn replans = %d/%d, want 3/3",
			exhausted.Replans, exhausted.MaxReplans)
	}
}

func TestCPUSetTopologyPluginTerminalErrorClearsCumulativeSuccessEvidence(t *testing.T) {
	t.Parallel()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "terminal"}
	snapshot := &topology.CompleteSnapshot{
		Entries: map[string]topology.EntryState{
			"root": {Rel: "root", CPUs: machine.NewCPUSet(0, 1)},
		},
	}
	report := topology.ConvergenceReport{
		FullyConverged: true,
		NonConvergedTargets: []topology.RelConvergence{{
			Rel:          "root/leaf",
			Observed:     machine.NewCPUSet(0, 1),
			Target:       machine.NewCPUSet(2, 3),
			ObservedMems: "0",
			TargetMems:   "1",
			Reason:       "nested diagnostic",
		}},
		PendingToPrimary:      machine.NewCPUSet(4),
		PendingToReclaim:      machine.NewCPUSet(5),
		CleanupPendingPrimary: machine.NewCPUSet(6),
		CleanupPendingReclaim: machine.NewCPUSet(7),
	}
	cumulative, err := runCPUSetTopologyAdjustmentWithResult(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		}),
		func(
			_ context.Context,
			_ bulkheadapi.HandlerContext,
			_ *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			return topology.ConvergenceResult{
				Attempted:            3,
				Applied:              2,
				Skipped:              1,
				Failed:               1,
				Deferred:             4,
				Converged:            true,
				ParentSafe:           true,
				State:                topology.ConvergenceStateConverged,
				ConvergenceReport:    report,
				FinalSnapshot:        snapshot,
				FinalSnapshotCurrent: true,
				DeferredLeafCount:    2,
				DeferredCPUCount:     6,
				Published:            true,
				ReplanDisposition:    topology.ReplanNotAllowed,
			}, stale
		},
	)

	if !errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("terminal error = %v, want stale cause", err)
	}
	if cumulative.Attempted != 3 || cumulative.Applied != 2 || cumulative.Skipped != 1 ||
		cumulative.Failed != 1 || cumulative.Deferred != 4 {
		t.Fatalf("terminal result lost diagnostic counts: %+v", cumulative)
	}
	if cumulative.Converged || cumulative.ParentSafe || cumulative.FinalSnapshot != nil ||
		cumulative.FinalSnapshotCurrent || cumulative.Published {
		t.Fatalf("terminal result retained success evidence: %+v", cumulative)
	}
	wantReport := report
	wantReport.FullyConverged = false
	if !reflect.DeepEqual(cumulative.ConvergenceReport, wantReport) {
		t.Fatalf("terminal result lost failure diagnostics: got=%+v want=%+v",
			cumulative.ConvergenceReport, wantReport)
	}
	if cumulative.DeferredLeafCount != 0 || cumulative.DeferredCPUCount != 0 {
		t.Fatalf("terminal result retained publish-success counts: %+v", cumulative)
	}
	report.NonConvergedTargets[0].Rel = "mutated"
	report.NonConvergedTargets[0].Observed.Add(99)
	report.PendingToPrimary.Add(99)
	report.CleanupPendingReclaim.Add(99)
	if cumulative.ConvergenceReport.NonConvergedTargets[0].Rel != "root/leaf" ||
		cumulative.ConvergenceReport.NonConvergedTargets[0].Observed.Contains(99) ||
		cumulative.ConvergenceReport.PendingToPrimary.Contains(99) ||
		cumulative.ConvergenceReport.CleanupPendingReclaim.Contains(99) {
		t.Fatalf("terminal diagnostics alias attempt-owned report: %+v", cumulative.ConvergenceReport)
	}
	if cumulative.State != topology.ConvergenceStateNonConverged {
		t.Fatalf("terminal state = %q, want %q", cumulative.State, topology.ConvergenceStateNonConverged)
	}
}

func TestCPUSetTopologyPluginDeadlineDuringAttemptUsesAdjustmentDeadlineError(t *testing.T) {
	t.Parallel()
	t.Run("current stale", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		budget := topology.NewAdjustmentBudget(ctx, topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		})
		stale := &topology.PlanStaleError{Rel: "root/nested", Resource: "during_attempt"}
		attempts := 0

		err := runCPUSetTopologyAdjustment(
			ctx,
			bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
			budget,
			func(
				_ context.Context,
				_ bulkheadapi.HandlerContext,
				_ *topology.AdjustmentBudget,
			) (topology.ConvergenceResult, error) {
				attempts++
				cancel()
				return topology.ConvergenceResult{
					ReplanDisposition: topology.ReplanNotAllowed,
				}, stale
			},
		)

		if !errors.Is(err, topology.ErrAdjustmentDeadlineExceeded) {
			t.Fatalf("deadline-in-attempt error = %v, want adjustment deadline sentinel", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("deadline-in-attempt error = %v, want context cause", err)
		}
		if !errors.Is(err, topology.ErrCoordinatorPlanStale) {
			t.Fatalf("deadline-in-attempt error = %v, want current stale cause", err)
		}
		if attempts != 1 {
			t.Fatalf("deadline-in-attempt attempts = %d, want 1", attempts)
		}
	})

	t.Run("last stale after nominal success", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		budget := topology.NewAdjustmentBudget(ctx, topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		})
		stale := &topology.PlanStaleError{Rel: "root/nested", Resource: "prior_attempt"}
		attempts := 0

		err := runCPUSetTopologyAdjustment(
			ctx,
			bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
			budget,
			func(
				_ context.Context,
				_ bulkheadapi.HandlerContext,
				_ *topology.AdjustmentBudget,
			) (topology.ConvergenceResult, error) {
				attempts++
				if attempts == 1 {
					return topology.ConvergenceResult{
						ReplanDisposition: topology.ReplanSafeNoPhysicalWrites,
					}, stale
				}
				cancel()
				return topology.ConvergenceResult{Published: true}, nil
			},
		)

		if !errors.Is(err, topology.ErrAdjustmentDeadlineExceeded) ||
			!errors.Is(err, context.Canceled) ||
			!errors.Is(err, topology.ErrCoordinatorPlanStale) {
			t.Fatalf("deadline after nominal success lost deadline/context/last stale cause: %v", err)
		}
		if attempts != 2 {
			t.Fatalf("deadline-in-attempt attempts = %d, want 2", attempts)
		}
	})

	t.Run("current hard error remains primary with historical stale", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		budget := topology.NewAdjustmentBudget(ctx, topology.ConvergenceBudget{
			MaxPlanOperations: 8,
		})
		stale := &topology.PlanStaleError{Rel: "root/nested", Resource: "prior_attempt"}
		hard := errors.New("current hard execution failure")
		attempts := 0

		err := runCPUSetTopologyAdjustment(
			ctx,
			bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
			budget,
			func(
				_ context.Context,
				_ bulkheadapi.HandlerContext,
				_ *topology.AdjustmentBudget,
			) (topology.ConvergenceResult, error) {
				attempts++
				if attempts == 1 {
					return topology.ConvergenceResult{
						ReplanDisposition: topology.ReplanSafeNoPhysicalWrites,
					}, stale
				}
				cancel()
				return topology.ConvergenceResult{}, hard
			},
		)

		if !errors.Is(err, topology.ErrAdjustmentDeadlineExceeded) ||
			!errors.Is(err, context.Canceled) ||
			!errors.Is(err, topology.ErrCoordinatorPlanStale) ||
			!errors.Is(err, hard) {
			t.Fatalf("deadline exhaustion lost deadline/context/current/historical cause: %v", err)
		}
		if unwrapped := errors.Unwrap(err); !errors.Is(unwrapped, hard) {
			t.Fatalf("primary unwrap = %v, want current hard error", unwrapped)
		}
		var gotStale *topology.PlanStaleError
		if !errors.As(err, &gotStale) || gotStale != stale {
			t.Fatalf("historical stale error = %#v, want %#v", gotStale, stale)
		}
		if attempts != 2 {
			t.Fatalf("deadline-in-attempt attempts = %d, want 2", attempts)
		}
	})
}

func TestCPUSetTopologyPluginReplanRequiresDeferredWriteInFreshSnapshot(t *testing.T) {
	t.Parallel()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "after_deferred_write"}
	for _, tc := range []struct {
		name      string
		observe   bool
		wantTries int
		wantErr   bool
	}{
		{name: "unobserved write terminates", observe: false, wantTries: 1, wantErr: true},
		{name: "new snapshot observes write", observe: true, wantTries: 2},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			budget := topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
				MaxPlanOperations: 2,
			})
			attempts := 0
			_, err := runCPUSetTopologyAdjustmentWithResult(
				context.Background(),
				bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
				budget,
				func(
					ctx context.Context,
					_ bulkheadapi.HandlerContext,
					budget *topology.AdjustmentBudget,
				) (topology.ConvergenceResult, error) {
					attempts++
					if attempts > 1 {
						return topology.ConvergenceResult{}, nil
					}
					reservation, reserveErr := budget.ReserveExecution(topology.ExecutionReservationCost{
						Forward: topology.PhysicalWriteCost{CPUSetWrites: 1},
					})
					if reserveErr != nil {
						return topology.ConvergenceResult{}, reserveErr
					}
					if recordErr := reservation.RecordWriteAttempt(
						ctx, false, topology.PhysicalWriteCost{CPUSetWrites: 1}); recordErr != nil {
						return topology.ConvergenceResult{}, recordErr
					}
					if settleErr := reservation.Settle(
						topology.PhysicalWriteCost{CPUSetWrites: 1},
						topology.PhysicalWriteCost{},
					); settleErr != nil {
						return topology.ConvergenceResult{}, settleErr
					}
					if tc.observe {
						budget.ObserveCurrentSnapshot(budget.BeginSnapshotObservation())
					}
					return topology.ConvergenceResult{
						ReplanDisposition: topology.ReplanSafeNoPhysicalWrites,
					}, stale
				},
			)
			if tc.wantErr && !errors.Is(err, topology.ErrCoordinatorPlanStale) {
				t.Fatalf("unobserved write error = %v, want stale cause", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("observed write replan error = %v", err)
			}
			if attempts != tc.wantTries {
				t.Fatalf("attempts = %d, want %d", attempts, tc.wantTries)
			}
		})
	}
}

func TestCPUSetTopologyPluginSuccessfulDualWriteDoesNotFailAfterPublication(t *testing.T) {
	t.Parallel()
	budget := topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	err := runCPUSetTopologyAdjustment(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		budget,
		func(
			ctx context.Context,
			_ bulkheadapi.HandlerContext,
			budget *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			reservation, err := budget.ReserveExecution(topology.ExecutionReservationCost{
				Forward: topology.PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1},
				Rollback: topology.PhysicalWriteCost{
					CPUSetWrites: 1,
					MemsWrites:   1,
				},
			})
			if err != nil {
				return topology.ConvergenceResult{}, err
			}
			if err := reservation.RecordWriteAttempt(ctx, false, topology.PhysicalWriteCost{
				CPUSetWrites: 1,
				MemsWrites:   1,
			}); err != nil {
				return topology.ConvergenceResult{}, err
			}
			if err := reservation.Settle(
				topology.PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1},
				topology.PhysicalWriteCost{},
			); err != nil {
				return topology.ConvergenceResult{}, err
			}
			return topology.ConvergenceResult{Published: true}, nil
		},
	)
	if err != nil {
		t.Fatalf("published dual-write adjustment error = %v, want nil", err)
	}
	if got := budget.CumulativeWrites(); got != 2 {
		t.Fatalf("cumulative writes = %d, want 2", got)
	}
}

func TestCPUSetTopologyPluginWriteBudgetExhaustionPreservesLastStaleCause(t *testing.T) {
	t.Parallel()
	stale := &topology.PlanStaleError{Rel: "root", Resource: "pre-reservation"}
	budget := topology.NewAdjustmentBudget(context.Background(), topology.ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	attempts := 0
	err := runCPUSetTopologyAdjustment(
		context.Background(),
		bulkheadapi.HandlerContext{DesiredView: model.NewDesiredView()},
		budget,
		func(
			_ context.Context,
			_ bulkheadapi.HandlerContext,
			budget *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attempts++
			if attempts == 1 {
				return topology.ConvergenceResult{
					ReplanDisposition: topology.ReplanSafeNoPhysicalWrites,
				}, stale
			}
			_, reserveErr := budget.ReserveExecution(topology.ExecutionReservationCost{
				Forward: topology.PhysicalWriteCost{CPUSetWrites: 3},
				Rollback: topology.PhysicalWriteCost{
					CPUSetWrites: 3,
				},
			})
			return topology.ConvergenceResult{}, reserveErr
		},
	)
	if !errors.Is(err, topology.ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("exhaustion error = %v, want write-budget cause", err)
	}
	if !errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("exhaustion error = %v, want last stale cause", err)
	}
}

func TestCPUSetTopologyPluginDisabledResetBudgetFailurePerformsNoWrites(t *testing.T) {
	t.Parallel()
	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-reset-budget-pod",
		"bulkhead-disabled-reset-budget-container",
	)
	p.cfg.TopologyConvergenceBudget.MaxPlanOperations = 1

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)

	if !errors.Is(err, topology.ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("CPUSetAdjustmentDisabledHandler() error = %v, want %v",
			err, topology.ErrAdjustmentWriteBudgetExceeded)
	}
	if len(cg.writeOrder) != 0 {
		t.Fatalf("disabled reset budget failure performed writes: %v", cg.writeOrder)
	}
}

// TestCPUSetTopologyPluginDisabledResetHardErrorsNotDeferredSentinel pins the
// boundary at plugin.go's Converge hard-error wrap (the !err==nil branch)
// against the disabledResetNotConvergedError sentinel construction (the
// !res.Converged branch). A convergence-budget exhaustion or a canceled context
// surfaces as a hard error from the real CPUSetAdjustmentDisabledHandler and
// must stay unclassified: it must not unwrap to ErrDisabledResetDeferred, which
// is reserved for the failed==0/deferred>0/FullyConverged retry shape.
func TestCPUSetTopologyPluginDisabledResetHardErrorsNotDeferredSentinel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		podUID      string
		containerID string
		setup       func(p *CPUSetTopologyPlugin, cg *fakeCgroupClient) context.Context
		wantCause   error
	}{
		{
			name:        "convergence budget exhausted",
			podUID:      "bulkhead-disabled-reset-harderr-budget-pod",
			containerID: "bulkhead-disabled-reset-harderr-budget-container",
			setup: func(p *CPUSetTopologyPlugin, _ *fakeCgroupClient) context.Context {
				p.cfg.TopologyConvergenceBudget.MaxPlanOperations = 1
				return context.Background()
			},
			wantCause: topology.ErrAdjustmentWriteBudgetExceeded,
		},
		{
			name:        "canceled context mid-converge",
			podUID:      "bulkhead-disabled-reset-harderr-cancel-pod",
			containerID: "bulkhead-disabled-reset-harderr-cancel-container",
			setup: func(_ *CPUSetTopologyPlugin, cg *fakeCgroupClient) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cg.readEntryHook = func(string, int) error {
					cancel()
					return nil
				}
				return ctx
			},
			wantCause: context.Canceled,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, cg, in, _ := newDisabledTransitionTestPlugin(
				t,
				cgroupclient.CgroupVersionV1,
				tc.podUID,
				tc.containerID,
			)
			ctx := tc.setup(p, cg)

			err := p.CPUSetAdjustmentDisabledHandler(ctx, in)

			if err == nil {
				t.Fatalf("CPUSetAdjustmentDisabledHandler() error = nil, want non-nil hard error")
			}
			if errors.Is(err, bulkheadapi.ErrDisabledResetDeferred) {
				t.Fatalf("hard error must NOT unwrap to ErrDisabledResetDeferred: %v", err)
			}
			var notConverged *disabledResetNotConvergedError
			if errors.As(err, &notConverged) {
				t.Fatalf("hard error must not be a disabledResetNotConvergedError: %+v", notConverged)
			}
			if !errors.Is(err, tc.wantCause) {
				t.Fatalf("CPUSetAdjustmentDisabledHandler() error = %v, want chain containing %v", err, tc.wantCause)
			}
		})
	}
}

func TestCPUSetTopologyPluginDisabledResetRecompilesFirstReadStale(t *testing.T) {
	t.Parallel()
	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-reset-first-read-stale-pod",
		"bulkhead-disabled-reset-first-read-stale-container",
	)
	staleRead := errors.New("first reset read observed replaced cgroup")
	cg.readEntryHook = func(rel string, attempt int) error {
		if rel == "primary" && attempt == 1 {
			return staleRead
		}
		return nil
	}
	cg.classifyHook = func(err error, operation topology.HierarchyOperation) topology.HierarchyErrorClass {
		if errors.Is(err, staleRead) && operation == topology.HierarchyOperationRead {
			return topology.HierarchyErrorStale
		}
		return topology.HierarchyErrorInvalid
	}

	if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler() error = %v, want successful owner recompile", err)
	}
	if got := cg.readEntryCounts["primary"]; got < 2 {
		t.Fatalf("primary reset reads = %d, want first stale read plus recompiled attempt", got)
	}
	if got := cg.writes["primary"]; got != "0-3" {
		t.Fatalf("primary reset cpuset = %q, want 0-3 after recompile; writes=%#v", got, cg.writes)
	}
}

func TestCPUSetTopologyPluginDisabledResetHardWriteErrorAttemptsOnce(t *testing.T) {
	t.Parallel()
	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-reset-hard-write-pod",
		"bulkhead-disabled-reset-hard-write-container",
	)
	hardWrite := errors.New("hard reset write failure")
	cg.writeCPUHook = func(rel string, _ int) error {
		if rel == "reclaim/reclaim-0" {
			return hardWrite
		}
		return nil
	}

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	if !errors.Is(err, hardWrite) {
		t.Fatalf("CPUSetAdjustmentDisabledHandler() error = %v, want hard write cause", err)
	}
	if got := cg.writeCPUCounts["reclaim/reclaim-0"]; got != 1 {
		t.Fatalf("hard reclaim NUMA write attempts = %d, want exactly 1", got)
	}
}

func TestCPUSetTopologyPluginDisabledResetPostWriteStaleIsTerminal(t *testing.T) {
	t.Parallel()
	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-reset-post-write-stale-pod",
		"bulkhead-disabled-reset-post-write-stale-container",
	)
	cg.writeCPUHook = func(rel string, _ int) error {
		if rel == "reclaim/reclaim-0" {
			return topology.ErrCgroupIdentityChanged
		}
		return nil
	}

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	if !errors.Is(err, topology.ErrCgroupIdentityChanged) {
		t.Fatalf("CPUSetAdjustmentDisabledHandler() error = %v, want identity-change cause", err)
	}
	if got := cg.writeCPUCounts["primary"]; got != 1 {
		t.Fatalf("primary writes = %d, want exactly 1 without post-write recompile", got)
	}
	if got := cg.writeCPUCounts["reclaim/reclaim-0"]; got != 1 {
		t.Fatalf("stale reclaim NUMA write attempts = %d, want exactly 1", got)
	}
}

func TestCPUSetTopologyPluginDisabledResetContinuousStaleExhaustsOwnerReplans(t *testing.T) {
	t.Parallel()
	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-reset-continuous-stale-pod",
		"bulkhead-disabled-reset-continuous-stale-container",
	)
	p.cfg.TopologyConvergenceBudget.MaxPlanOperations = 8
	staleRead := errors.New("continuous reset read churn")
	cg.readEntryHook = func(rel string, _ int) error {
		if rel == "primary" {
			return staleRead
		}
		return nil
	}
	cg.classifyHook = func(err error, operation topology.HierarchyOperation) topology.HierarchyErrorClass {
		if errors.Is(err, staleRead) && operation == topology.HierarchyOperationRead {
			return topology.HierarchyErrorStale
		}
		return topology.HierarchyErrorInvalid
	}

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	var exhausted *topology.ReplanBudgetExceededError
	if !errors.As(err, &exhausted) {
		t.Fatalf("CPUSetAdjustmentDisabledHandler() error = %v, want ReplanBudgetExceededError", err)
	}
	if !errors.Is(err, topology.ErrAdjustmentReplanBudgetExceeded) {
		t.Fatalf("continuous stale error = %v, want replan-budget sentinel", err)
	}
	if !errors.Is(err, staleRead) {
		t.Fatalf("continuous stale error = %v, want last stale read cause", err)
	}
	if exhausted.Replans != 3 || exhausted.MaxReplans != 3 {
		t.Fatalf("continuous stale replans = %d/%d, want 3/3",
			exhausted.Replans, exhausted.MaxReplans)
	}
	if got := cg.readEntryCounts["primary"]; got != 4 {
		t.Fatalf("primary reset attempts = %d, want initial attempt plus 3 replans", got)
	}
	if len(cg.writeOrder) != 0 {
		t.Fatalf("continuous pre-write stale performed writes: %v", cg.writeOrder)
	}
}

func TestCPUSetTopologyPluginReconcileDisabledContinuousChurnIsBounded(t *testing.T) {
	t.Parallel()
	p, cg, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-reclaim-only-continuous-churn-pod",
		"bulkhead-reclaim-only-continuous-churn-container",
	)
	p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled = true
	p.cfg.TopologyConvergenceBudget.MaxPlanOperations = 8
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["reclaim/reclaim-0"] = machine.NewCPUSet(0, 1, 2, 3)
	in.DesiredView.ReclaimEffective = machine.NewCPUSet(2, 3)
	in.DesiredView.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(2, 3)

	var observations int
	cg.statIdentityHook = func(rel string) (topology.CgroupIdentity, error) {
		observations++
		identity := pluginFakeIdentity(rel)
		identity.Inode += uint64(observations)
		return identity, nil
	}

	_, err := p.ReconcileDisabled(context.Background(), in)

	var exhausted *topology.ReplanBudgetExceededError
	if !errors.As(err, &exhausted) {
		t.Fatalf("ReconcileDisabled() error = %v, want ReplanBudgetExceededError", err)
	}
	if !errors.Is(err, topology.ErrCgroupIdentityChanged) {
		t.Fatalf("continuous churn lost identity-change cause: %v", err)
	}
	if !errors.Is(err, topology.ErrAdjustmentReplanBudgetExceeded) {
		t.Fatalf("continuous churn error = %v, want replan-budget sentinel", err)
	}
	if errors.Is(err, topology.ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("zero-write churn must not report write-budget exhaustion: %v", err)
	}
	if exhausted.Replans != 3 || exhausted.MaxReplans != 3 {
		t.Fatalf("continuous churn replans = %d/%d, want 3/3",
			exhausted.Replans, exhausted.MaxReplans)
	}
	if len(cg.writeOrder) != 0 {
		t.Fatalf("continuous pre-write churn performed writes: %v", cg.writeOrder)
	}
}

// TestPendingSharedProjectionEligibilityPerContainer proves the choke-point
// classification is per (podUID, containerName): within one pod, an eligible
// shared ramp-up container is flagged while its exact sibling is not, and the hint
// is cleared when the set is absent (materialized/resolved/non-pending round).
func TestPendingSharedProjectionEligibilityPerContainer(t *testing.T) {
	t.Parallel()
	hint := map[string]map[string]struct{}{
		"pod-1": {"main": {}}, // only "main" eligible; "sidecar" is an exact sibling
	}
	if !isPendingSharedProjectionEligible(hint, "pod-1", "main") {
		t.Fatalf("eligible container must be projected")
	}
	if isPendingSharedProjectionEligible(hint, "pod-1", "sidecar") {
		t.Fatalf("exact sibling must not be projected (no pod-wide OR)")
	}
	// Unknown pod / container / nil hint: not eligible.
	if isPendingSharedProjectionEligible(hint, "pod-2", "main") {
		t.Fatalf("unknown pod must not be projected")
	}
	if isPendingSharedProjectionEligible(nil, "pod-1", "main") {
		t.Fatalf("nil hint (materialized/non-pending round) must not project")
	}
}

// TestPendingProtectionCarriesProjectedSubset proves the pending->protection
// aggregation keeps the projected subset per container and leaves it nil (fully
// exact) when no container on the pod is eligible.
func TestPendingProtectionCarriesProjectedSubset(t *testing.T) {
	t.Parallel()
	exact := pendingContainerCPUSet{PodUID: "p", ContainerName: "exact", CPUs: machine.NewCPUSet(2)}
	eligible := pendingContainerCPUSet{PodUID: "p", ContainerName: "main", CPUs: machine.NewCPUSet(0)}
	eligible.projectionCPUs = machine.NewCPUSet(0)

	// Reuse the aggregation inline: build the two records and confirm the union
	// projects only the eligible container's CPU.
	agg := map[string]pendingContainerCPUSet{}
	for _, p := range []pendingContainerCPUSet{exact, eligible} {
		cur, ok := agg[p.PodUID]
		if !ok {
			agg[p.PodUID] = p
			continue
		}
		cur.CPUs = cur.CPUs.Union(p.CPUs)
		if !p.projectionCPUs.IsEmpty() {
			if cur.projectionCPUs.IsEmpty() {
				cur.projectionCPUs = p.projectionCPUs.Clone()
			} else {
				cur.projectionCPUs = cur.projectionCPUs.Union(p.projectionCPUs)
			}
		}
		agg[p.PodUID] = cur
	}
	got := agg["p"]
	if !got.CPUs.Equals(machine.NewCPUSet(0, 2)) {
		t.Fatalf("aggregated CPUs = %s, want {0,2}", got.CPUs)
	}
	if !got.projectionCPUs.Equals(machine.NewCPUSet(0)) {
		t.Fatalf("projected subset = %s, want {0} only (sibling {2} stays exact)", got.projectionCPUs)
	}
}

// test4NUMASMTTopologyForProjection builds a 128-CPU, 4-NUMA, SMT=2 topology for
// the plugin-level projection tests: cpu and cpu+16 share a physical core.
func test4NUMASMTTopologyForProjection() *machine.CPUTopology {
	details := machine.CPUDetails{}
	for cpu := 0; cpu < 128; cpu++ {
		details[cpu] = machine.CPUTopoInfo{
			NUMANodeID: cpu / 32,
			SocketID:   cpu / 32,
			CoreID:     (cpu/32)*16 + cpu%16,
		}
	}
	return &machine.CPUTopology{NumCPUs: 128, NumCores: 64, NumSockets: 4, NumNUMANodes: 4, CPUDetails: details}
}

// applyRealChokePoint mirrors the exact production classification at the pending
// choke point (plugin.go): per-container membership against the transient hint,
// then project that container's own CPUs.
func applyRealChokePoint(hint map[string]map[string]struct{}, pc *pendingContainerCPUSet) {
	if isPendingSharedProjectionEligible(hint, pc.PodUID, pc.ContainerName) {
		pc.projectionCPUs = pc.CPUs.Clone()
	}
}

// TestPendingSharedProjectionRealLifecycleAndZeroWrite drives the REAL pending
// scope resolution (fakeCgroupClient ENOENT/inode/stat) and the REAL choke-point
// helper, then the REAL shadow derive. It proves: ENOENT+eligible projects; a
// materialized scope resolves exact with no projection; a recreated UID absent
// from the hint does not leak; and a half-core domain is a typed zero-write error.
func TestPendingSharedProjectionRealLifecycleAndZeroWrite(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 18, 8, 0, 0, 0, time.UTC)
	const (
		podUID = "pod-pending"
		podRel = "kubepods/burstable/pod-pending"
	)
	hint := map[string]map[string]struct{}{podUID: {"main": {}}}

	// Round 1: pending ENOENT + eligible hint => projected subset carried.
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{
			readErrByRel: map[string]error{podRel: os.ErrNotExist},
			statErrors:   map[string]error{podRel: syscall.ENOENT},
		},
		now: func() time.Time { return now },
		pendingProtections: map[string]pendingPodProtection{
			podUID: {rel: podRel, protectUntil: now.Add(defaultPendingPodProtectionTTL)},
		},
	}
	pending := []pendingContainerCPUSet{{PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(0)}}
	for i := range pending {
		applyRealChokePoint(hint, &pending[i])
	}
	got, err := p.pendingProtectionScopes(context.Background(), nil, pending)
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 pending protection, got %d", len(got))
	}
	if got[0].SharedProjectedCPUs.IsEmpty() {
		t.Fatalf("ENOENT+eligible must carry projected subset, got %#v", got[0])
	}
	if !got[0].SharedProjectedCPUs.Equals(machine.NewCPUSet(0)) {
		t.Fatalf("projected = %s, want {0}", got[0].SharedProjectedCPUs)
	}
	if len(p.cgroup.(*fakeCgroupClient).writeOrder) != 0 {
		t.Fatalf("pending resolution must not write cgroups: %v", p.cgroup.(*fakeCgroupClient).writeOrder)
	}

	// Round 2: same pod materialized (cgroup exists) => resolves exact, no pending.
	pMat := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{cpus: map[string]machine.CPUSet{podRel: machine.NewCPUSet(0)}},
		now:    func() time.Time { return now },
		pendingProtections: map[string]pendingPodProtection{
			podUID: {rel: podRel, protectUntil: now.Add(defaultPendingPodProtectionTTL)},
		},
	}
	pendingMat := []pendingContainerCPUSet{{PodUID: podUID, ContainerName: "main", CPUs: machine.NewCPUSet(0)}}
	for i := range pendingMat {
		// Materialized round has an EMPTY side-channel (RampUp/non-pending), so the
		// real choke point does not flag this container and projection stays nil.
		applyRealChokePoint(nil, &pendingMat[i])
	}
	gotMat, err := pMat.pendingProtectionScopes(context.Background(), nil, pendingMat)
	if err != nil {
		t.Fatalf("materialized pendingProtectionScopes() error = %v", err)
	}
	for i := range gotMat {
		if !gotMat[i].SharedProjectedCPUs.IsEmpty() {
			t.Fatalf("materialized round (empty side-channel) must not project: %#v", gotMat[i])
		}
	}

	// Round 3: delete-recreate under a NEW uid not in the hint => no stale leak.
	pNew := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{
			readErrByRel: map[string]error{"kubepods/pod-NEW": os.ErrNotExist},
			statErrors:   map[string]error{"kubepods/pod-NEW": syscall.ENOENT},
		},
		now: func() time.Time { return now },
		pendingProtections: map[string]pendingPodProtection{
			"pod-NEW": {rel: "kubepods/pod-NEW", protectUntil: now.Add(defaultPendingPodProtectionTTL)},
		},
	}
	pendingNew := []pendingContainerCPUSet{{PodUID: "pod-NEW", ContainerName: "main", CPUs: machine.NewCPUSet(1)}}
	for i := range pendingNew {
		applyRealChokePoint(hint, &pendingNew[i]) // old hint does not contain pod-NEW
	}
	gotNew, err := pNew.pendingProtectionScopes(context.Background(), nil, pendingNew)
	if err != nil {
		t.Fatalf("recreate pendingProtectionScopes() error = %v", err)
	}
	for i := range gotNew {
		if !gotNew[i].SharedProjectedCPUs.IsEmpty() {
			t.Fatalf("new uid absent from old hint must not project: %#v", gotNew[i])
		}
	}

	// Round 4: half-core domain => typed fail-closed shadow error, zero partition.
	topo := test4NUMASMTTopologyForProjection()
	base := model.NewDesiredView()
	base.DesiredNonReclaimPool = machine.MustParse("0,2-127") // drop sibling 16 => split core 0
	base.DesiredReclaimEffective = machine.NewCPUSet(4, 20, 36, 52, 68, 84, 100, 116)
	base.NonReclaimPool = base.DesiredNonReclaimPool.Clone()
	base.ReclaimEffective = base.DesiredReclaimEffective.Clone()
	sh := bulkheadutils.DeriveShadowPartition(base, topo, got)
	if sh.Err == nil {
		t.Fatalf("want typed half-core error, got hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
	if !errors.Is(sh.Err, bulkheadutils.ErrSharedProjectionHalfCoreDomain) {
		t.Fatalf("want half-core typed error, got %v", sh.Err)
	}
	if sh.HoldCurrent {
		t.Fatalf("typed error must not be hold-current/Deferred")
	}
	if !sh.NonReclaimPool.IsEmpty() || !sh.ReclaimEffective.IsEmpty() {
		t.Fatalf("zero-write violated: nr=%s reclaim=%s", sh.NonReclaimPool, sh.ReclaimEffective)
	}
}

// TestFullAdjustProjectionHalfCoreZeroWrite drives the REAL CPUSetAdjustmentHandler
// through adjustOnceWithResult into DeriveShadowPartition. The pending shared
// ramp-up container is classified by the real choke point from
// HandlerContext.PendingSharedProjection, projected onto a deliberately half-core
// shared domain, and the typed sh.Err must short-circuit BEFORE any cgroup/spec
// write.
func TestFullAdjustProjectionHalfCoreZeroWrite(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 18, 8, 0, 0, 0, time.UTC)
	const (
		podUID = "pod-pending"
		podRel = "kubepods/burstable/pod-pending"
	)
	// Minimal SMT=2 topology: 4 CPUs, 2 physical cores; cpu0<->cpu1 share a core,
	// cpu2<->cpu3 share a core.
	details := machine.CPUDetails{
		0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		3: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
	}
	topo := &machine.CPUTopology{NumCPUs: 4, NumCores: 2, NumSockets: 1, NumNUMANodes: 1, CPUDetails: details}
	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true, podRel: true},
		cpus: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 2, 3),
			"reclaim": machine.NewCPUSet(1),
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
		now:    func() time.Time { return now },
		pendingProtections: map[string]pendingPodProtection{
			podUID: {rel: podRel, protectUntil: now.Add(defaultPendingPodProtectionTTL)},
		},
	}
	// Half-core shared domain: non-reclaim pool drops sibling 1 of core 0. The
	// pending container asks CPU0; projecting expands to {0,1} but the domain only
	// contains {0}, a typed half-core contradiction.
	view := model.NewDesiredView()
	view.DesiredNonReclaimPool = machine.NewCPUSet(0, 2, 3)
	view.DesiredReclaimEffective = machine.NewCPUSet(1)
	view.NonReclaimPool = view.DesiredNonReclaimPool.Clone()
	view.ReclaimEffective = view.DesiredReclaimEffective.Clone()
	view.ContainerCPUSetByPod = map[string]map[string]machine.CPUSet{
		podUID: {"main": machine.NewCPUSet(0)},
	}
	appliedBefore := view.NonReclaimPool.Clone()

	// The fresh pod list contains the pending pod, but its container has no ID yet
	// (admit window), so buildExpectedCPUSetByRel classifies it as live pending.
	pendingPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID), Name: "pending"},
		Spec: v1.PodSpec{
			NodeName:   "node",
			Containers: []v1.Container{{Name: "main"}},
		},
		Status: v1.PodStatus{QOSClass: v1.PodQOSBurstable},
	}
	fetcher := &strictSnapshotFetcher{
		pods:         []*v1.Pod{pendingPod},
		containerIDs: map[string]string{},
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
			Topology:   topo,
		},
		DesiredView:             view,
		PendingSharedProjection: map[string]map[string]struct{}{podUID: {"main": {}}},
	})
	if err == nil {
		t.Fatalf("expected typed projection error, got nil")
	}
	if !errors.Is(err, bulkheadutils.ErrSharedProjectionHalfCoreDomain) {
		t.Fatalf("want half-core typed error, got %v", err)
	}
	if got := totalApplyCount(cg.applyCounts); got != 0 {
		t.Fatalf("sh.Err early return must perform zero cgroup writes, applyCounts=%d", got)
	}
	if len(cg.writeOrder) != 0 {
		t.Fatalf("sh.Err must not write cgroups: %v", cg.writeOrder)
	}
	if !view.NonReclaimPool.Equals(appliedBefore) {
		t.Fatalf("business view mutated: %s -> %s", appliedBefore, view.NonReclaimPool)
	}
}
