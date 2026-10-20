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

package cpusettopology

import (
	"context"
	"errors"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// snbShadowGateFalseDetails mirrors the production E2E 128-CPU / 4-NUMA shape:
//
//	NUMA0={0-15,64-79}, NUMA1={16-31,80-95}, NUMA2={32-47,96-111}, NUMA3={48-63,112-127}.
//
// CPU c and c+64 share a physical core (SMT pair); coreID = numa*16 + cpu%16.
func snbShadowGateFalseDetails() machine.CPUDetails {
	details := machine.CPUDetails{}
	numaByBlock := []int{0, 1, 2, 3, 0, 1, 2, 3}
	for cpu := 0; cpu < 128; cpu++ {
		block := cpu / 16
		numa := numaByBlock[block]
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: numa, SocketID: numa, CoreID: numa*16 + cpu%16}
	}
	return details
}

// snbShadowGateFalseReclaim is the steady-state reclaim set: 2 whole cores per
// NUMA, confined entirely inside one NUMA node (8 CPUs total).
func snbShadowGateFalseReclaim() machine.CPUSet {
	return machine.MustParse("4,20,36,52,68,84,100,116")
}

// snbShadowGateFalseWholeNUMA0 is the exact pending business allocation: the
// seed SNB pod currently owns the entire NUMA0 slice, which overlaps the NUMA0
// reclaim bucket {4,68}.
func snbShadowGateFalseWholeNUMA0() machine.CPUSet {
	return machine.MustParse("0-15,64-79")
}

// snbShadowGateFalseFixture wires the REAL plugin chain
// (buildExpectedCPUSetByRel -> pendingProtectionScopes -> topologyCoversProtectedView
// gate -> BuildTopologyNodeSpecsFromView -> Converge) with a 128C/4NUMA physical
// shape, a business DesiredView that leaves DesiredReclaimEffective empty (so the
// shadow gate evaluates FALSE), and a current cgroup world in which the pending
// pod scope still holds whole NUMA0.
type snbShadowGateFalseFixture struct {
	t        *testing.T
	plugin   *CPUSetTopologyPlugin
	view     *model.DesiredView
	cg       *fakeCgroupClient
	fetcher  *strictFreshPendingFetcher
	details  machine.CPUDetails
	reclaim  machine.CPUSet
	whole    machine.CPUSet
	podUID   string
	scopeRel string
}

func newSNBShadowGateFalseFixture(t *testing.T) *snbShadowGateFalseFixture {
	t.Helper()

	details := snbShadowGateFalseDetails()
	reclaim := snbShadowGateFalseReclaim()
	whole := snbShadowGateFalseWholeNUMA0()
	all := machine.MustParse("0-127")
	nonReclaim := all.Difference(reclaim)

	const podUID = "840b"
	// The fresh pod carries no resource request/limit, so the strict QOS resolves
	// to BestEffort and the unique materialized pending scope is the standard
	// besteffort pod cgroup. We make exactly that candidate materialized so the
	// scope resolves deterministically, and its current holds the business whole
	// NUMA0 slice (the pod is mid-admission and already pinned to 32 CPUs).
	scopeRel := "kubepods/besteffort/pod" + podUID
	scopeParent := "kubepods/besteffort"

	cfg := bulkheadconfig.NewBulkheadConfiguration()
	cfg.BulkheadPrimaryRelPath = "kubepods"
	cfg.BulkheadReclaimRelPaths = []string{"reclaimed"}
	cfg.BulkheadReclaimNumaPrefixes = []string{"reclaimed/reclaimed-"}
	cfg.AdmissionMaxRequiredWrites = 0

	cg := &fakeCgroupClient{
		// cgroup v1 (the E2E host): an empty cpuset.cpus is NOT a legal explicit
		// target, so when the raw pending protection forces the NUMA0 reclaim bucket
		// to shed its only CPUs the coordinator aborts on the immutable empty-target
		// guard instead of converging. That abort is the deterministic,
		// production-faithful red signal for the gate-False divergence.
		version: cgroupclient.CgroupVersionV1,
		existing: map[string]bool{
			"kubepods":              true,
			"kubepods/besteffort":   true,
			"reclaimed":             true,
			"reclaimed/reclaimed-0": true,
			"reclaimed/reclaimed-1": true,
			"reclaimed/reclaimed-2": true,
			"reclaimed/reclaimed-3": true,
			scopeRel:                true,
		},
		cpus: map[string]machine.CPUSet{
			"kubepods":              nonReclaim.Clone(),
			scopeParent:             nonReclaim.Clone(),
			"reclaimed":             reclaim.Clone(),
			"reclaimed/reclaimed-0": machine.MustParse("4,68"),
			"reclaimed/reclaimed-1": machine.MustParse("20,84"),
			"reclaimed/reclaimed-2": machine.MustParse("36,100"),
			"reclaimed/reclaimed-3": machine.MustParse("52,116"),
			scopeRel:                whole.Clone(),
		},
		mems: map[string]string{
			"kubepods":              "0",
			scopeParent:             "0",
			"reclaimed":             "0",
			"reclaimed/reclaimed-0": "0",
			"reclaimed/reclaimed-1": "0",
			"reclaimed/reclaimed-2": "0",
			"reclaimed/reclaimed-3": "0",
			scopeRel:                "0",
		},
		children: map[string][]string{
			"kubepods":  {"besteffort"},
			scopeParent: {"pod" + podUID},
		},
	}

	// The pending container is not yet resolvable (no container ID / not running),
	// exactly like a fresh SNB pod mid-admission. The fresh pod still lists the
	// container so it stays a live pending protection rather than going stale.
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

	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		NonReclaimPool:   nonReclaim.Clone(),
		ReclaimEffective: reclaim.Clone(),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.MustParse("4,68"),
			1: machine.MustParse("20,84"),
			2: machine.MustParse("36,100"),
			3: machine.MustParse("52,116"),
		},
		// DesiredReclaimEffective intentionally left EMPTY: this is the business
		// DesiredView, not a shadow-derived partition, so topologyCoversProtectedView
		// evaluates FALSE and the pending protection is never projected onto the
		// non-reclaim side by the shadow path.
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {"main": whole.Clone()},
		},
	}}

	p := &CPUSetTopologyPlugin{cfg: *cfg, cgroup: cg}

	return &snbShadowGateFalseFixture{
		t: t, plugin: p, view: view, cg: cg, fetcher: fetcher,
		details: details, reclaim: reclaim, whole: whole,
		podUID: podUID, scopeRel: scopeRel,
	}
}

func (f *snbShadowGateFalseFixture) handlerContext() bulkheadapi.HandlerContext {
	return bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Topology:   &machine.CPUTopology{CPUDetails: f.details},
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: f.fetcher}},
			Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
		},
		DesiredView: f.view,
		// The production soft, non-overlap admission round turns the pending
		// projection gate ON. With DesiredNonReclaimPool empty the current clipper
		// derives an empty shared domain and preserves the whole-NUMA0 protection
		// verbatim, which is exactly the gate-False divergence under test.
		ProjectPendingToDesiredNonReclaim: true,
	}
}

// TestSNBShadowGateFalseBareErrNoProgress drives the REAL plugin chain
// (buildExpectedCPUSetByRel -> pendingProtectionScopes -> the
// topologyCoversProtectedView gate -> BuildTopologyNodeSpecsFromView -> Converge)
// on the standard first-SNB shape: a 128C/4NUMA host whose fresh SNB pod owns the
// whole NUMA0 slice (32 CPUs) and overlaps the NUMA0 reclaim bucket {4,68}.
//
// The business DesiredView leaves DesiredReclaimEffective empty, so
// topologyCoversProtectedView evaluates FALSE and the shadow projection path
// (DeriveShadowPartition) never runs. Prior topology-level tests pinned the green
// behavior by hand-feeding the projected protectedPending = sharedNUMA0
// (= whole NUMA0 minus the reclaim pair {4,68}); this test instead drives the
// real gate-False wiring.
//
// RED on 4eaf01834: the raw whole-NUMA0 protection (32 CPUs, unclipped) is
// folded onto the primary kubepods target, which then widens onto the reclaim
// pair {4,68}; normalizeReclaimTargetsByPrimary deducts that pair from the NUMA0
// reclaim bucket, emptying it. On cgroup v1 the immutable empty-target guard
// aborts the plan (the same divergence that surfaces as a bare ErrNoProgress
// fixed-point stall on a v2 host). The intended green behavior projects the
// pending scope to the shared non-reclaim domain (whole NUMA0 - {4,68} = 30),
// retains the {4,68} bucket, and converges without touching ParentSafe or the
// empty guard.
func TestSNBShadowGateFalseBareErrNoProgress(t *testing.T) {
	t.Parallel()

	fx := newSNBShadowGateFalseFixture(t)
	ctx := context.Background()

	in := fx.handlerContext()

	// Pre-flight: the shadow gate must evaluate FALSE for this business view.
	if protected := fx.whole; topologyCoversProtectedView(in.Topology, in.DesiredView, protected) {
		t.Fatalf("precondition failed: shadow gate must be FALSE for the business DesiredView; "+
			"protected=%s DesiredReclaimEffective=%s", protected, fx.view.DesiredReclaimEffective)
	}

	budget := topology.NewAdjustmentBudget(ctx, topologyBudgetFromConfig(fx.plugin.cfg.TopologyConvergenceBudget))
	var result topology.ConvergenceResult
	err := fx.plugin.adjustOnceWithResult(ctx, in, budget, &result, nil)

	// Canonical projection target the gate-False path is owed: whole NUMA0 minus
	// the reclaim pair that must stay in the NUMA0 bucket.
	sharedNUMA0 := fx.whole.Difference(fx.view.ReclaimEffectivePerNUMA[0])

	// Instrumentation: expose the actual ParentSafety deficit / canonical targets
	// so the remaining gap is visible rather than collapsing to a bare error.
	t.Logf("adjustOnceWithResult err=%v", err)
	t.Logf("convergence state=%s converged=%t parentSafe=%t deferred=%d finalCurrent=%t",
		result.State, result.Converged, result.ParentSafe, result.Deferred, result.FinalSnapshotCurrent)
	t.Logf("fullyConverged=%t report=%+v", result.ConvergenceReport.FullyConverged, result.ConvergenceReport)
	if blocker := result.FirstBlocker(); blocker != "" {
		t.Logf("firstBlocker=%s", blocker)
	}
	for _, nt := range result.ConvergenceReport.NonConvergedTargets {
		t.Logf("nonConverged rel=%q observed=%s target=%s reason=%s",
			nt.Rel, nt.Observed.String(), nt.Target.String(), nt.Reason)
	}
	var stall *topology.ProtectedTransferStallError
	if errors.As(err, &stall) {
		t.Logf("ProtectedTransferStall: un-transferable protected cpus=%s", stall.CPUs.String())
	}
	var empty *topology.UnsupportedEmptyTargetError
	if errors.As(err, &empty) {
		t.Logf("UnsupportedEmptyTarget: rel=%q current=%s (raw whole-NUMA0 protection forced this bucket to shed its CPUs)",
			empty.Rel, empty.Current.String())
	}
	t.Logf("canonical: pending leaf observed=%s (whole NUMA0); reclaim bucket NUMA0=%s; overlap=%s; "+
		"shared-projected target owed=%s",
		fx.whole, fx.view.ReclaimEffectivePerNUMA[0],
		fx.whole.Intersection(fx.view.ReclaimEffectivePerNUMA[0]), sharedNUMA0)

	// RED on 4eaf01834: the real gate-False chain must project the pending
	// whole-NUMA0 scope onto the shared non-reclaim domain (whole NUMA0 minus the
	// reclaim pair {4,68} = sharedNUMA0), retain the NUMA0 reclaim bucket, and
	// converge WITHOUT aborting to empty a reclaim bucket. Today it folds the raw
	// 32-CPU protection onto the primary, widening onto {4,68}, and the planner
	// aborts trying to empty the NUMA0 reclaim bucket.
	if errors.As(err, &empty) {
		t.Fatalf("gate-False chain aborted trying to empty reclaim bucket %q (current=%s): "+
			"the raw whole-NUMA0 protection %s is not projected to the shared non-reclaim domain %q, "+
			"so {4,68} is folded onto the primary and the bucket is forced to shed its CPUs. err=%v result=%+v",
			empty.Rel, empty.Current.String(), fx.whole, sharedNUMA0, err, result)
	}
	if err != nil {
		t.Fatalf("gate-False chain returned a non-nil error after the empty-bucket abort was fixed; "+
			"expected convergence (or a parent-safe hold) with the NUMA0 bucket retained; err=%v result=%+v",
			err, result)
	}
	if !result.Converged {
		t.Fatalf("expected gate-False chain to converge once the pending whole-NUMA0 protection is "+
			"projected to the shared non-reclaim domain %q; result=%+v", sharedNUMA0, result)
	}
}

// TestSNBShadowGateDisabledPreservesRawRedBehaviour is the deliberate
// counterexample: the clipping fallback lives behind the soft/non-overlap
// admission gate (ProjectPendingToDesiredNonReclaim). With the gate OFF (now:
// share/reclaim overlap, periodic, or topology-projection explicitly disabled —
// NOT a hard-partition admission, which now projects), the pending whole-NUMA0
// scope is left exactly as today: it folds raw onto the primary and the v1
// empty-target guard aborts on the NUMA0 reclaim bucket. The narrow fix must NOT
// silently clip when the operator has not opted into soft/non-overlap projection.
func TestSNBShadowGateDisabledPreservesRawRedBehaviour(t *testing.T) {
	t.Parallel()

	fx := newSNBShadowGateFalseFixture(t)
	ctx := context.Background()

	in := fx.handlerContext()
	in.ProjectPendingToDesiredNonReclaim = false

	budget := topology.NewAdjustmentBudget(ctx, topologyBudgetFromConfig(fx.plugin.cfg.TopologyConvergenceBudget))
	var result topology.ConvergenceResult
	err := fx.plugin.adjustOnceWithResult(ctx, in, budget, &result, nil)

	var empty *topology.UnsupportedEmptyTargetError
	if !errors.As(err, &empty) {
		t.Fatalf("gate-Disabled counterexample: expected the v1 empty-target abort from the raw "+
			"unclipped whole-NUMA0 protection, got err=%v result=%+v", err, result)
	}
	if empty.Rel != "reclaimed/reclaimed-0" || !empty.Current.Equals(machine.NewCPUSet(4, 68)) {
		t.Fatalf("gate-Disabled counterexample: expected abort on reclaimed-0 current={4,68}, "+
			"got rel=%q current=%s", empty.Rel, empty.Current.String())
	}
}
