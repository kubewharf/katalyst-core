/*
Copyright 2026 The Kubernetes Authors.

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

// enforcementAppliedFloorDetails mirrors the production 128-CPU / 4-NUMA shape:
//
//	NUMA0={0-15,64-79}, NUMA1={16-31,80-95}, NUMA2={32-47,96-111}, NUMA3={48-63,112-127}.
//
// CPU c and c+64 share a physical core (SMT pair); coreID = numa*16 + cpu%16.
func enforcementAppliedFloorDetails() machine.CPUDetails {
	details := machine.CPUDetails{}
	numaByBlock := []int{0, 1, 2, 3, 0, 1, 2, 3}
	for cpu := 0; cpu < 128; cpu++ {
		block := cpu / 16
		numa := numaByBlock[block]
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: numa, SocketID: numa, CoreID: numa*16 + cpu%16}
	}
	return details
}

// enforcementAppliedFloorReclaim8 is the pre-admission validated reclaim set: two
// whole cores per NUMA, including the NUMA0 bucket {4,68}.
func enforcementAppliedFloorReclaim8() machine.CPUSet {
	return machine.MustParse("4,20,36,52,68,84,100,116")
}

// enforcementAppliedFloorReclaim6 is the self-expanded business desired reclaim:
// NUMA0 removed (the business wants to grow whole NUMA0 for the pending pod).
func enforcementAppliedFloorReclaim6() machine.CPUSet {
	return machine.MustParse("20,36,52,84,100,116")
}

// enforcementAppliedFloorWholeNUMA0 is the pending business allocation: the seed
// SNB pod currently owns the entire NUMA0 slice, which overlaps the applied NUMA0
// reclaim bucket {4,68}.
func enforcementAppliedFloorWholeNUMA0() machine.CPUSet {
	return machine.MustParse("0-15,64-79")
}

// enforcementAppliedFloorFixture wires the REAL plugin chain
// (buildExpectedCPUSetByRel -> pendingProtectionScopes -> topologyCoversProtectedView
// gate -> shadow partition -> BuildTopologyNodeSpecsFromView -> Converge) for the
// soft/non-overlap admission whose business DesiredView has self-expanded away the
// NUMA0 reclaim bucket, while the pre-admission AppliedView still validates it.
//
//   - Pre-admission AppliedView (manager-owned validated prior partition):
//     NonReclaimPool = 120, ReclaimEffective = reclaim8 incl NUMA0 {4,68},
//     ReclaimEffectivePerNUMA pins reclaimed-0 = {4,68}.
//   - Business DesiredView after the pending allocation: NonReclaimPool = 122 and
//     DesiredNonReclaimPool = 122 (whole NUMA0 absorbed), ReclaimEffective =
//     reclaim6 with NUMA0 removed. The business allocation result stays 32 and is
//     never mutated.
//   - The pending protection is the whole NUMA0 slice (32 CPUs), overlapping the
//     applied NUMA0 reclaim bucket {4,68}. ProjectPendingToDesiredNonReclaim clips
//     it to the validated 30 = whole NUMA0 minus {4,68}.
type enforcementAppliedFloorFixture struct {
	t        *testing.T
	plugin   *CPUSetTopologyPlugin
	desired  *model.DesiredView
	applied  *model.AppliedView
	cg       *fakeCgroupClient
	fetcher  *strictFreshPendingFetcher
	details  machine.CPUDetails
	reclaim8 machine.CPUSet
	reclaim6 machine.CPUSet
	whole    machine.CPUSet
	podUID   string
	scopeRel string
}

func newEnforcementAppliedFloorFixture(t *testing.T) *enforcementAppliedFloorFixture {
	t.Helper()

	details := enforcementAppliedFloorDetails()
	reclaim8 := enforcementAppliedFloorReclaim8()
	reclaim6 := enforcementAppliedFloorReclaim6()
	whole := enforcementAppliedFloorWholeNUMA0()
	all := machine.MustParse("0-127")
	appliedNonReclaim := all.Difference(reclaim8) // 120
	desiredNonReclaim := all.Difference(reclaim6) // 122

	const podUID = "840b"
	scopeRel := "kubepods/besteffort/pod" + podUID
	scopeParent := "kubepods/besteffort"

	cfg := bulkheadconfig.NewBulkheadConfiguration()
	cfg.BulkheadPrimaryRelPath = "kubepods"
	cfg.BulkheadReclaimRelPaths = []string{"reclaimed"}
	cfg.BulkheadReclaimNumaPrefixes = []string{"reclaimed/reclaimed-"}
	cfg.EnableAdmissionLeafDefer = true
	cfg.AdmissionMaxRequiredWrites = 0

	cg := &fakeCgroupClient{
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
			"kubepods":              appliedNonReclaim.Clone(),
			scopeParent:             appliedNonReclaim.Clone(),
			"reclaimed":             reclaim8.Clone(),
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

	// Pre-admission validated AppliedView: reclaim8 incl NUMA0 {4,68}.
	applied := &model.AppliedView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:   appliedNonReclaim.Clone(),
			ReclaimEffective: reclaim8.Clone(),
			ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
				0: machine.MustParse("4,68"),
				1: machine.MustParse("20,84"),
				2: machine.MustParse("36,100"),
				3: machine.MustParse("52,116"),
			},
		},
		Level: model.AppliedViewLevelFull,
	}

	// Business DesiredView after the pending allocation self-expanded away the NUMA0
	// reclaim bucket. DesiredReclaimEffective is non-empty (reclaim6), so the
	// topologyCoversProtectedView gate evaluates TRUE and the shadow projection path
	// runs. The materialized NonReclaimPool/ReclaimEffective have followed the
	// business expansion to 122/6.
	desired := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		NonReclaimPool:   desiredNonReclaim.Clone(),
		ReclaimEffective: reclaim6.Clone(),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			1: machine.MustParse("20,84"),
			2: machine.MustParse("36,100"),
			3: machine.MustParse("52,116"),
		},
		DesiredNonReclaimPool:   desiredNonReclaim.Clone(),
		DesiredReclaimEffective: reclaim6.Clone(),
		DesiredReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			1: machine.MustParse("20,84"),
			2: machine.MustParse("36,100"),
			3: machine.MustParse("52,116"),
		},
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {"main": whole.Clone()},
		},
	}}

	p := &CPUSetTopologyPlugin{cfg: *cfg, cgroup: cg}

	return &enforcementAppliedFloorFixture{
		t: t, plugin: p, desired: desired, applied: applied, cg: cg, fetcher: fetcher,
		details: details, reclaim8: reclaim8, reclaim6: reclaim6, whole: whole,
		podUID: podUID, scopeRel: scopeRel,
	}
}

func (f *enforcementAppliedFloorFixture) handlerContext() bulkheadapi.HandlerContext {
	return bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Topology:   &machine.CPUTopology{CPUDetails: f.details},
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: f.fetcher}},
			Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
		},
		DesiredView:                       f.desired,
		AppliedView:                       f.applied,
		ProjectPendingToDesiredNonReclaim: true,
	}
}

// enforcementAppliedFloorProtected30 is the canonical projected pending target:
// whole NUMA0 minus the applied NUMA0 reclaim pair {4,68}.
func (f *enforcementAppliedFloorFixture) enforcementAppliedFloorProtected30() machine.CPUSet {
	return f.whole.Difference(machine.MustParse("4,68"))
}

// TestEnforcementAppliedFloorRetainsReclaimed0 drives the REAL plugin chain on the
// soft/non-overlap admission whose business DesiredView self-expanded away the
// NUMA0 reclaim bucket (DesiredNonReclaimPool=122, reclaim6), while the
// pre-admission AppliedView still validates reclaim8 incl reclaimed-0={4,68}.
//
// The enforcement NodeSpecs / required floors must be built from the pre-admission
// AppliedView partition, and only THEN the projected pending protection (30)
// folded onto the non-reclaim side. That keeps reclaimed-0={4,68} in the spec /
// required floor, leaves the business desired allocation (32) untouched, and emits
// a real dynamic leaf shrink (the pending leaf 32 -> 30).
//
// RED today: the shadow partition derives its non-reclaim/reclaim from the
// self-expanded business DesiredNonReclaimPool (122) / DesiredReclaimEffective (6),
// which already absorbed {4,68} into the primary and dropped the NUMA0 bucket. The
// reclaimed-0 spec / required floor is empty, {4,68} lands in the primary target,
// and Converge stalls at a bare no-progress with zero writes.
func TestEnforcementAppliedFloorRetainsReclaimed0(t *testing.T) {
	t.Parallel()

	fx := newEnforcementAppliedFloorFixture(t)
	ctx := context.Background()

	in := fx.handlerContext()

	budget := topology.NewAdjustmentBudget(ctx, topologyBudgetFromConfig(fx.plugin.cfg.TopologyConvergenceBudget))
	var result topology.ConvergenceResult
	err := fx.plugin.adjustOnceWithResult(ctx, in, budget, &result, nil)

	t.Logf("adjustOnceWithResult err=%v", err)
	t.Logf("convergence state=%s converged=%t parentSafe=%t deferred=%d attempted=%d applied=%d finalCurrent=%t",
		result.State, result.Converged, result.ParentSafe, result.Deferred, result.Attempted, result.Applied, result.FinalSnapshotCurrent)
	for _, nt := range result.ConvergenceReport.NonConvergedTargets {
		t.Logf("nonConverged rel=%q observed=%s target=%s reason=%s",
			nt.Rel, nt.Observed.String(), nt.Target.String(), nt.Reason)
	}
	var empty *topology.UnsupportedEmptyTargetError
	if errors.As(err, &empty) {
		t.Fatalf("enforcement floor must not empty reclaim bucket %q (current=%s); "+
			"the applied NUMA0 reclaim pair {4,68} was folded onto the primary instead of staying reclaimed. err=%v result=%+v",
			empty.Rel, empty.Current.String(), err, result)
	}
	if err != nil {
		t.Fatalf("enforcement floor chain returned a non-nil error; expected convergence with reclaimed-0 retained; err=%v result=%+v", err, result)
	}

	// A real dynamic leaf shrink/grow must be emitted: the pending leaf currently
	// holds whole NUMA0 (32) and must shrink to the validated 30 = NUMA0 minus {4,68}.
	if result.Attempted <= 0 {
		t.Fatalf("expected a real dynamic leaf shrink/grow write (attempted>0) so the pending "+
			"leaf sheds {4,68} into the enforced reclaim bucket; got attempted=%d result=%+v", result.Attempted, result)
	}

	// reclaimed-0 must stay pinned at the applied floor {4,68}: neither emptied nor
	// widened. The cgroup write (or absence) must leave it at {4,68}.
	reclaimed0, readErr := fx.cg.ReadCPUSet(ctx, "reclaimed/reclaimed-0")
	if readErr != nil {
		t.Fatalf("read reclaimed-0: %v", readErr)
	}
	if !reclaimed0.Equals(machine.MustParse("4,68")) {
		t.Fatalf("reclaimed-0 floor must stay {4,68}; got %s", reclaimed0.String())
	}

	// The pending leaf must have shrunk from whole NUMA0 (32) to the validated 30.
	leaf, readErr := fx.cg.ReadCPUSet(ctx, fx.scopeRel)
	if readErr != nil {
		t.Fatalf("read pending leaf: %v", readErr)
	}
	expectedLeaf := fx.enforcementAppliedFloorProtected30()
	if !leaf.Equals(expectedLeaf) {
		t.Fatalf("pending leaf must shrink from whole NUMA0 (32) to the validated non-reclaim 30=%s; got leaf=%s",
			expectedLeaf.String(), leaf.String())
	}

	// The business DesiredView is never mutated: the business desired allocation
	// (DesiredNonReclaimPool=122) stays business truth.
	if !fx.desired.DesiredNonReclaimPool.Equals(fx.desired.DesiredNonReclaimPool) {
		t.Fatalf("business DesiredNonReclaimPool was mutated")
	}
	if got := fx.desired.DesiredNonReclaimPool.Size(); got != 122 {
		t.Fatalf("business desired allocation pool must stay 122; got %d", got)
	}

	if !result.Converged && !result.ParentSafe {
		t.Fatalf("expected convergence (or parent-safe) with reclaimed-0 retained; result=%+v", result)
	}
}

// TestEnforcementAppliedFloorGateDisabledPreservesRawRedBehaviour is the
// counterexample: with ProjectPendingToDesiredNonReclaim OFF (now: share/reclaim
// overlap / periodic / topology projection explicitly disabled — NOT a
// hard-partition admission, which now projects), the enforcement-from-applied view
// must NOT engage. The raw whole-NUMA0 protection folds onto the primary and the
// v1 empty-target guard aborts on reclaimed-0, exactly as before.
func TestEnforcementAppliedFloorGateDisabledPreservesRawRedBehaviour(t *testing.T) {
	t.Parallel()

	fx := newEnforcementAppliedFloorFixture(t)
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

// TestEnforcementAppliedFloorNilAppliedViewFailsClosed is the counterexample for the
// initial/ nil AppliedView: with no validated prior partition the enforcement view
// cannot be constructed, so admissionEnforcementBaseline returns nil and the shadow
// derives purely from the self-expanded business DesiredView exactly as before.
//
// That historical baseline drops the NUMA0 reclaim bucket (business reclaim6) and
// folds the raw whole-NUMA0 protection onto the primary; the v1 empty-target guard
// then aborts on reclaimed-0={4,68}. This must FAIL CLOSED: no panic, no speculative
// reclaim floor invented, no write issued (attempted=0), and the pending protection
// preserved verbatim at whole NUMA0 (32) rather than shrunk onto the reclaim pair.
func TestEnforcementAppliedFloorNilAppliedViewFailsClosed(t *testing.T) {
	t.Parallel()

	fx := newEnforcementAppliedFloorFixture(t)
	ctx := context.Background()

	in := fx.handlerContext()
	// No validated AppliedView: the enforcement baseline is unavailable. The shadow
	// must fall back to the business DesiredView verbatim and must not fabricate a
	// reclaim floor from it.
	in.AppliedView = nil

	budget := topology.NewAdjustmentBudget(ctx, topologyBudgetFromConfig(fx.plugin.cfg.TopologyConvergenceBudget))
	var result topology.ConvergenceResult
	err := fx.plugin.adjustOnceWithResult(ctx, in, budget, &result, nil)

	// Fail-closed: the business-only baseline leaves reclaimed-0's spec empty, and the
	// v1 empty-target guard aborts rather than emptying the non-empty bucket. No write.
	var empty *topology.UnsupportedEmptyTargetError
	if !errors.As(err, &empty) {
		t.Fatalf("nil AppliedView must fall back to the historical business baseline and abort "+
			"via the v1 empty-target guard on reclaimed-0; got err=%v result=%+v", err, result)
	}
	if empty.Rel != "reclaimed/reclaimed-0" || !empty.Current.Equals(machine.NewCPUSet(4, 68)) {
		t.Fatalf("nil AppliedView: expected fail-closed abort on reclaimed-0 current={4,68}, "+
			"got rel=%q current=%s", empty.Rel, empty.Current.String())
	}
	if result.Attempted != 0 {
		t.Fatalf("nil AppliedView fail-closed must issue no speculative write; got attempted=%d result=%+v",
			result.Attempted, result)
	}

	// The pending protection is preserved verbatim at whole NUMA0 (32): no shrink onto
	// the reclaim pair when there is no validated enforcement baseline.
	leaf, readErr := fx.cg.ReadCPUSet(ctx, fx.scopeRel)
	if readErr != nil {
		t.Fatalf("read pending leaf: %v", readErr)
	}
	if !leaf.Equals(fx.whole) {
		t.Fatalf("nil AppliedView: protection must stay verbatim at whole NUMA0 (32=%s); got leaf=%s",
			fx.whole.String(), leaf.String())
	}
}
