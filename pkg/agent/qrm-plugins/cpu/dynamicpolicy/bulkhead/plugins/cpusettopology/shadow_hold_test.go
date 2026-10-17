/*
Copyright 2022 The Kubernetes Authors.

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
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestHoldCurrentOutcomeZeroWriteDefer asserts the hold-current outcome is a
// Deferred (topologyStopped) defer with no AppliedView, so the manager skips
// dependent workqueue/system consumers without seeding or persisting state.
func TestHoldCurrentOutcomeZeroWriteDefer(t *testing.T) {
	t.Parallel()
	out := holdCurrentOutcome()
	if out.Level != bulkheadapi.ConvergenceLevelDeferred {
		t.Fatalf("hold outcome level = %v, want Deferred", out.Level)
	}
	if out.View != nil {
		t.Fatalf("hold outcome must not attach AppliedView: %+v", out.View)
	}
}

// TestApplyClassifiesShadowHoldSentinel asserts a hold-current error maps to a
// nil defer error (not a terminal rejection) via errors.Is.
func TestApplyClassifiesShadowHoldSentinel(t *testing.T) {
	t.Parallel()
	wrapped := wrapHoldErr()
	if !errors.Is(wrapped, bulkheadutils.ErrShadowHoldCurrent) {
		t.Fatalf("wrapped hold err not classifiable: %v", wrapped)
	}
}

func wrapHoldErr() error {
	return bulkheadutils.ErrShadowHoldCurrent
}

// newFullPendingHoldPlugin builds the real CPUSetTopologyPlugin wired to the
// existing fakeCgroupClient/fakeSnapshotDriver, with a pending shared pod whose
// requested CPUs absorb the entire reclaim budget so the read-only shadow
// projection must short-circuit with the full_pending hold.
func newFullPendingHoldPlugin(t *testing.T) (*CPUSetTopologyPlugin, *fakeCgroupClient, *model.DesiredView, bulkheadapi.HandlerContext) {
	t.Helper()

	const podUID = "hold-full-pending"
	topo := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
		2: {NUMANodeID: 0}, 3: {NUMANodeID: 0},
	}}
	desired := &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:          machine.NewCPUSet(0, 1),
			ReclaimEffective:        machine.NewCPUSet(2, 3),
			DesiredNonReclaimPool:   machine.NewCPUSet(0, 1),
			DesiredReclaimEffective: machine.NewCPUSet(2, 3),
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				podUID: {"main": machine.NewCPUSet(2, 3)},
			},
		},
		PoolOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{},
	}

	cg := &fakeCgroupClient{
		existing: map[string]bool{"kubepods": true},
	}
	fetcher := &strictFreshPendingFetcher{
		containerErr: metapod.ErrContainerNotFound,
		pods: map[string]*v1.Pod{
			podUID: {
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
			},
		},
		podErrs:      map[string]error{},
		freshLookups: map[string]int{},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:    "kubepods",
			TopologyConvergenceBudget: bulkheadconfig.DefaultConvergenceBudget(),
		},
		cgroup:             cg,
		pendingProtections: map[string]pendingPodProtection{},
	}
	in := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Topology:   topo,
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
			Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
		},
		DesiredView: desired,
	}
	return p, cg, desired, in
}

// TestFullPendingHoldZeroWritesCompleteChain drives the REAL plugin.Apply
// through the full-pending shadow hold and proves the zero-write contract end
// to end: the hierarchy driver never performs a forward or rollback write, no
// cgroup cpuset is actually written, and no final snapshot (which would also
// drive checkpoint accept/promotion) is produced. The hold is a pure
// read-only defer that returns a Deferred outcome with no AppliedView.
func TestFullPendingHoldZeroWritesCompleteChain(t *testing.T) {
	t.Parallel()

	p, cg, desired, in := newFullPendingHoldPlugin(t)

	beforeReclaim := desired.ReclaimEffective.Clone()
	beforeNonReclaim := desired.NonReclaimPool.Clone()
	beforePending := desired.ContainerCPUSetByPod["hold-full-pending"]["main"].Clone()

	outcome, err := p.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error = %v, want nil (hold maps to a Deferred outcome)", err)
	}
	if outcome.Level != bulkheadapi.ConvergenceLevelDeferred {
		t.Fatalf("outcome level = %v, want Deferred", outcome.Level)
	}
	if outcome.View != nil {
		t.Fatalf("hold outcome must not attach an AppliedView (no final snapshot published): %+v", outcome.View)
	}
	// The hold reason must propagate into the deferred outcome Note so the
	// manager embeds it in the ErrTopologyDeferred text. This is diagnostic-only:
	// the Level/View/zero-write contract above is unchanged.
	if !strings.Contains(outcome.Note, "hold_reason=full_pending") {
		t.Fatalf("outcome.Note = %q, want it to carry hold_reason=full_pending", outcome.Note)
	}
	if !strings.Contains(outcome.Note, "reclaim_before=") || !strings.Contains(outcome.Note, "reclaim_after=") {
		t.Fatalf("outcome.Note = %q, want reclaim before/after in the diagnostic", outcome.Note)
	}

	// Hierarchy driver: forward + rollback both go through fakeSnapshotDriver.WriteCPUs.
	if len(cg.writeCPUCounts) != 0 {
		t.Fatalf("hierarchy driver WriteCPUs (forward+rollback) calls = %v, want 0", cg.writeCPUCounts)
	}
	// Actual cgroup writes (ApplyCPUSet / cpuset write materialization).
	if len(cg.applyCounts) != 0 {
		t.Fatalf("ApplyCPUSet calls = %v, want 0", cg.applyCounts)
	}
	if len(cg.cpusetWrites) != 0 {
		t.Fatalf("actual cpuset writes = %v, want 0", cg.cpusetWrites)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("cgroup writes map = %v, want 0", cg.writes)
	}
	if len(cg.writeOrder) != 0 {
		t.Fatalf("write order = %v, want empty", cg.writeOrder)
	}
	// No new cgroup directories materialized (no sibling/checkpoint setup writes).
	if len(cg.existing) != 1 || !cg.existing["kubepods"] {
		t.Fatalf("hold must not materialize any new cgroup dirs: existing=%v", cg.existing)
	}

	// Business DesiredView must be byte-identical after the read-only hold.
	if !desired.ReclaimEffective.Equals(beforeReclaim) {
		t.Fatalf("business DesiredView mutated: reclaim %s -> %s", beforeReclaim, desired.ReclaimEffective)
	}
	if !desired.NonReclaimPool.Equals(beforeNonReclaim) {
		t.Fatalf("business DesiredView mutated: non-reclaim %s -> %s", beforeNonReclaim, desired.NonReclaimPool)
	}
	if got := desired.ContainerCPUSetByPod["hold-full-pending"]["main"]; !got.Equals(beforePending) {
		t.Fatalf("pending pod desired cpuset mutated: %s -> %s", beforePending, got)
	}
}

// TestOverlapGateDisablesPluginBeforeShadowHold proves that an input which would
// otherwise short-circuit into a full_pending shadow hold (Deferred) never reaches
// the shadow path when shared cores overlap reclaimed cores: the overlap gate in
// Enable() disables the plugin up front, so the manager routes to the original
// disabled path — no shadow projection, no deferred outcome, and no physical
// write. No production counting hook is added; the gate is exercised through the
// real Enable/ShouldReconcileWhenDisabled entry points on the same fixture that
// TestFullPendingHoldZeroWritesCompleteChain drives into the hold.
func TestOverlapGateDisablesPluginBeforeShadowHold(t *testing.T) {
	t.Parallel()

	p, cg, _, in := newFullPendingHoldPlugin(t)

	// A dynamic conf that would otherwise ENABLE full topology, so the only thing
	// closing the gate is the overlap state itself.
	in.DynamicConf = enabledBulkheadCpusetTopologyDynamicConf()
	overlapState := cpustate.NewCPUPluginState(nil)
	if err := overlapState.SetAllowSharedCoresOverlapReclaimedCores(true); err != nil {
		t.Fatalf("set overlap state: %v", err)
	}
	in.State = overlapState

	// Gate closed: the same full-pending input that Deferred in
	// TestFullPendingHoldZeroWritesCompleteChain is disabled before Apply.
	if enabled := p.Enable(in); enabled {
		t.Fatalf("Enable with shared/reclaimed overlap = true, want false (gate closed before shadow hold)")
	}
	// Original disabled semantics: reclaim-preservation reconcile stays off by
	// default, so no shadow apply is scheduled and no deferred outcome arises.
	if p.ShouldReconcileWhenDisabled(context.Background(), in) {
		t.Fatalf("ShouldReconcileWhenDisabled = true under overlap gate, want false (original disabled path)")
	}
	// The gate path must not touch any hierarchy driver or cgroup state.
	if len(cg.writeCPUCounts) != 0 || len(cg.applyCounts) != 0 ||
		len(cg.cpusetWrites) != 0 || len(cg.writes) != 0 || len(cg.writeOrder) != 0 {
		t.Fatalf("overlap gate must not perform any cgroup/hierarchy write: writes=%v apply=%v cpuset=%v order=%v",
			cg.writeCPUCounts, cg.applyCounts, cg.cpusetWrites, cg.writeOrder)
	}

	// Contrast: with overlap cleared, the identical input keeps the plugin enabled,
	// which is exactly the condition under which TestFullPendingHold... reaches the
	// shadow hold. The gate, not the input, is what suppresses it here.
	clearState := cpustate.NewCPUPluginState(nil)
	if err := clearState.SetAllowSharedCoresOverlapReclaimedCores(false); err != nil {
		t.Fatalf("clear overlap state: %v", err)
	}
	in.State = clearState
	if enabled := p.Enable(in); !enabled {
		t.Fatalf("Enable without overlap should be true (gate open, shadow hold reachable)")
	}
}

// TestRenderPendingProtectionsDiagnostic proves each pending protection renders
// its rel/source/CPUs/SharedProjectedCPUs plus the eligible classification, and
// that the shared-projected vs exact split is classified per protection.
func TestRenderPendingProtectionsDiagnostic(t *testing.T) {
	t.Parallel()
	protections := []topology.PendingProtection{
		{
			ScopeRel:            "/kubepods/burstable/pod-a",
			CPUs:                machine.NewCPUSet(2, 3),
			SharedProjectedCPUs: machine.NewCPUSet(2),
			Source:              topology.PendingProtectionSourceExpectedPod,
			PodUID:              "pod-a",
		},
		{
			ScopeRel:            "/kubepods/besteffort/pod-b",
			CPUs:                machine.NewCPUSet(8),
			SharedProjectedCPUs: machine.NewCPUSet(),
			Source:              topology.PendingProtectionSourceExistingPod,
			PodUID:              "pod-b",
		},
	}
	got := renderPendingProtectionsDiagnostic(protections)
	for _, want := range []string{
		"rel=/kubepods/burstable/pod-a",
		"source=" + string(topology.PendingProtectionSourceExpectedPod),
		"cpus=2-3",
		"shared_projected=2",
		"eligible=shared_projected",
		"rel=/kubepods/besteffort/pod-b",
		"source=" + string(topology.PendingProtectionSourceExistingPod),
		"cpus=8",
		"shared_projected=",
		"eligible=exact",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic %q missing field %q", got, want)
		}
	}
	if empty := renderPendingProtectionsDiagnostic(nil); empty != "none" {
		t.Fatalf("empty protections = %q, want none", empty)
	}
}

// TestRenderNUMASetMapOrdering proves the per-NUMA floor projection renders NUMA
// ids ascending and never mutates the input.
func TestRenderNUMASetMapOrdering(t *testing.T) {
	t.Parallel()
	m := map[int]machine.CPUSet{
		1: machine.NewCPUSet(4, 5),
		0: machine.NewCPUSet(0, 1),
	}
	got := renderNUMASetMap(m)
	if got != "{0:0-1 1:4-5}" {
		t.Fatalf("renderNUMASetMap = %q, want {0:0-1 1:4-5}", got)
	}
	if len(m) != 2 {
		t.Fatalf("input map mutated: len=%d", len(m))
	}
	if empty := renderNUMASetMap(nil); empty != "{}" {
		t.Fatalf("empty map = %q, want {}", empty)
	}
}

// TestBuildShadowHoldNoteFields proves the compact note carries HoldReason, the
// protected set, and reclaim before/after, and is derived from a DesiredView
// without mutating it.
func TestBuildShadowHoldNoteFields(t *testing.T) {
	t.Parallel()
	desired := &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			DesiredReclaimEffective: machine.NewCPUSet(2, 3),
		},
	}
	in := bulkheadapi.HandlerContext{DesiredView: desired}
	before := desired.DesiredReclaimEffective.Clone()
	shadow := bulkheadutils.ShadowPartition{
		HoldReason:       bulkheadutils.HoldReasonFullPending,
		ProtectedPending: machine.NewCPUSet(2, 3),
		ReclaimEffective: machine.NewCPUSet(),
	}
	note := buildShadowHoldNote(in, shadow, []topology.PendingProtection{{PodUID: "x"}})
	for _, want := range []string{
		"hold_reason=full_pending",
		"protected=2-3",
		"reclaim_before=2-3",
		"reclaim_after=",
		"pending=1",
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q missing %q", note, want)
		}
	}
	if !desired.DesiredReclaimEffective.Equals(before) {
		t.Fatalf("DesiredView mutated by note builder: %s", desired.DesiredReclaimEffective)
	}
}

// TestShadowHoldDiagnosticErrorClassification proves the diagnostic error still
// classifies as the bounded hold-current sentinel and exposes the carried note
// via errors.As, while leaving hold classification identical.
func TestShadowHoldDiagnosticErrorClassification(t *testing.T) {
	t.Parallel()
	err := &shadowHoldDiagnosticError{reason: "floor_exhausted", note: "hold_reason=floor_exhausted"}
	if !errors.Is(err, bulkheadutils.ErrShadowHoldCurrent) {
		t.Fatalf("diagnostic error not classifiable as hold-current: %v", err)
	}
	var diag *shadowHoldDiagnosticError
	if !errors.As(err, &diag) {
		t.Fatalf("errors.As failed to extract diagnostic error: %v", err)
	}
	if diag.note != "hold_reason=floor_exhausted" {
		t.Fatalf("carried note = %q", diag.note)
	}
	if diag.reason != "floor_exhausted" {
		t.Fatalf("carried reason = %q", diag.reason)
	}
	if !strings.Contains(err.Error(), "floor_exhausted") {
		t.Fatalf("error text %q must mention the reason", err.Error())
	}
}
