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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
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
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
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
		plugin: p, desired: desired, applied: applied, cg: cg, fetcher: fetcher,
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
func TestEnforcementAppliedFloorRetainsReclaimed0(t *testing.T) {
	t.Parallel()

	fx := newEnforcementAppliedFloorFixture(t)
	ctx := context.Background()

	in := fx.handlerContext()
	desiredBefore, err := json.Marshal(fx.desired.CPUSetPartitionView)
	if err != nil {
		t.Fatal(err)
	}

	budget := topology.NewAdjustmentBudget(ctx, topologyBudgetFromConfig(fx.plugin.cfg.TopologyConvergenceBudget))
	var result topology.ConvergenceResult
	err = fx.plugin.adjustOnceWithResult(ctx, in, budget, &result, nil)

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
	desiredAfter, err := json.Marshal(fx.desired.CPUSetPartitionView)
	if err != nil {
		t.Fatal(err)
	}
	if string(desiredAfter) != string(desiredBefore) {
		t.Fatal("business DesiredView was mutated")
	}
	if got := fx.desired.DesiredNonReclaimPool.Size(); got != 122 {
		t.Fatalf("business desired allocation pool must stay 122; got %d", got)
	}

	if !result.Converged && !result.ParentSafe {
		t.Fatalf("expected convergence (or parent-safe) with reclaimed-0 retained; result=%+v", result)
	}
}

func TestCanonicalPartitionRetryReleasesReclaimBeforeGrowingSharedLeaves(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"converge", "source-write-failure", "mixed-exact-conflict", "recorded-partition-converge"} {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			failSource := scenario == "source-write-failure"
			t.Parallel()
			fx := newEnforcementAppliedFloorFixture(t)
			// Source-release order and exact fences are independent of wall-clock speed.
			fx.plugin.cfg.TopologyConvergenceBudget.DeadlineDuration = -1
			ctx := context.Background()
			all := machine.MustParse("0-127")
			handoff := machine.MustParse("4,68")
			retained := machine.MustParse("5,69")
			oldReclaim := fx.reclaim8.Union(retained)
			newReclaim := oldReclaim.Difference(handoff)
			if scenario == "recorded-partition-converge" {
				// Recorded partition sets; the SMT/NUMA layout is a fixture, not
				// a complete replay of the node's filesystem or execution timing.
				all = machine.MustParse("0-191")
				oldReclaim = machine.MustParse("0-1,3-4,12-15,24-26,36-38,48-50,60-62,72-74,84-86,96-97,99-100,108-111,120-122,132-134,144-146,156-158,168-170,180-182")
				newReclaim = machine.MustParse("0-1,12-13,24-25,36-37,48-49,60-61,72,84,96-97,108-109,120-121,132-133,144-145,156-157,168,180")
				handoff = oldReclaim.Difference(newReclaim)
				fx.details = machine.CPUDetails{}
				for cpu := 0; cpu < 192; cpu++ {
					numa := (cpu % 96) / 12
					fx.details[cpu] = machine.CPUTopoInfo{NUMANodeID: numa, SocketID: numa, CoreID: cpu % 96}
				}
			}
			fx.applied.ReclaimEffectivePerNUMA = map[int]machine.CPUSet{}
			fx.cg.children["reclaimed"] = nil
			for _, numa := range fx.details.NUMANodes().ToSliceInt() {
				domain := fx.details.CPUsInNUMANodes(numa)
				name := fmt.Sprintf("reclaimed-%d", numa)
				rel := "reclaimed/" + name
				fx.applied.ReclaimEffectivePerNUMA[numa] = oldReclaim.Intersection(domain)
				fx.cg.cpus[rel] = oldReclaim.Intersection(domain)
				fx.cg.existing[rel] = true
				fx.cg.mems[rel] = "0"
				fx.cg.children["reclaimed"] = append(fx.cg.children["reclaimed"], name)
			}
			hard := machine.NewCPUSet(2)
			oldShare := all.Difference(oldReclaim).Difference(hard)
			newShare := all.Difference(newReclaim).Difference(hard)
			fx.applied.ReclaimEffective = oldReclaim
			fx.applied.NonReclaimPool = all.Difference(oldReclaim)
			fx.cg.cpus["reclaimed"] = oldReclaim
			fx.cg.cpus["kubepods"] = all.Difference(oldReclaim)
			fx.cg.cpus["kubepods/besteffort"] = all.Difference(oldReclaim)
			fx.cg.cpus[fx.scopeRel] = oldShare
			st := cpustate.NewCPUPluginState(&machine.CPUTopology{CPUDetails: fx.details})
			st.SetAllowSharedCoresOverlapReclaimedCores(false)
			for pool, cpus := range map[string]machine.CPUSet{
				commonstate.PoolNameShare: newShare, commonstate.PoolNameReclaim: newReclaim, "isolation-owner": hard,
			} {
				if err := st.SetAllocationInfo(pool, commonstate.FakedContainerName, &cpustate.AllocationInfo{
					AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(pool), AllocationResult: cpus,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.SetAllocationInfo(fx.podUID, "main", &cpustate.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid: fx.podUID, ContainerName: "main", OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
				}, AllocationResult: newShare,
			}); err != nil {
				t.Fatal(err)
			}
			view, hint, err := bulkheadutils.BuildValidatedCPUSetPartitionView(st,
				&machine.CPUTopology{CPUDetails: fx.details}, bulkheadutils.CPUSetPartitionViewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			fx.desired = view
			fx.fetcher.containerErr = nil
			fx.fetcher.containerErrs = map[string]error{"main": metapod.ErrContainerNotFound}
			fx.fetcher.containerIDs = map[string]string{}
			for pod := 0; pod < 4; pod++ {
				uid := fmt.Sprintf("running-%d", pod)
				rel := "kubepods/besteffort/pod" + uid
				fx.cg.existing[rel] = true
				fx.cg.cpus[rel] = all.Difference(oldReclaim)
				fx.cg.mems[rel] = "0"
				fx.cg.children["kubepods/besteffort"] = append(fx.cg.children["kubepods/besteffort"], "pod"+uid)
				fx.desired.ContainerCPUSetByPod[uid] = map[string]machine.CPUSet{}
				p := &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID(uid)}}
				for container := 0; container < 3; container++ {
					name := fmt.Sprintf("container-%d-%d", pod, container)
					id := name + "-id"
					fx.fetcher.containerIDs[name] = id
					fx.cg.children[rel] = append(fx.cg.children[rel], id)
					fx.cg.existing[rel+"/"+id] = true
					fx.cg.cpus[rel+"/"+id] = oldShare
					fx.cg.mems[rel+"/"+id] = "0"
					fx.desired.ContainerCPUSetByPod[uid][name] = newShare
					p.Spec.Containers = append(p.Spec.Containers, v1.Container{Name: name})
					p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, v1.ContainerStatus{
						Name: name, ContainerID: "containerd://" + id,
						State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
					})
				}
				fx.fetcher.pods[uid] = p
			}
			fx.plugin.resolveContainerRel = func(uid, id string) (string, error) {
				return "kubepods/besteffort/pod" + uid + "/" + id, nil
			}
			in := fx.handlerContext()
			in.Mode = cpusetutil.CPUSetAdjustmentModeRetry
			in.ProjectPendingToDesiredNonReclaim = false
			in.ReconcileCanonicalPartition = true
			in.PendingSharedProjection = hint
			if scenario == "mixed-exact-conflict" {
				// An exact sibling overlaps the eligible shared subset. It must retain
				// its strict obligation, including the hard owner's sibling.
				fx.desired.ContainerCPUSetByPod[fx.podUID]["strict"] = machine.NewCPUSet(66)
				fx.fetcher.containerErrs["strict"] = metapod.ErrContainerNotFound
				fx.fetcher.pods[fx.podUID].Spec.Containers = append(fx.fetcher.pods[fx.podUID].Spec.Containers, v1.Container{Name: "strict"})
			}
			fx.cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
				if strings.HasPrefix(rel, "kubepods") && !fx.cg.cpus[rel].Intersection(handoff).IsEmpty() {
					if !fx.cg.cpus["reclaimed"].Intersection(handoff).IsEmpty() {
						t.Fatalf("destination %s grew before physical source release", rel)
					}
					for _, child := range fx.cg.children["reclaimed"] {
						if !fx.cg.cpus["reclaimed/"+child].Intersection(handoff).IsEmpty() {
							t.Fatalf("destination %s grew before bucket %s released CPUs", rel, child)
						}
					}
				}
			}
			writeErr := errors.New("source drain failed")
			if failSource {
				fx.cg.writeCPUHook = func(rel string, _ int) error {
					if rel == "reclaimed/reclaimed-0" {
						return writeErr
					}
					return nil
				}
			}
			var result bulkheadapi.TopologyResult
			err = fx.plugin.runNormalAdjustment(ctx, in, func(r bulkheadapi.TopologyResult) { result = r })
			if scenario == "mixed-exact-conflict" {
				if !errors.Is(err, topology.ErrInvalidPendingProtection) || len(fx.cg.writeOrder) != 0 || result.Converged {
					t.Fatalf("exact mixed sibling was relaxed: err=%v writes=%v result=%+v", err, fx.cg.writeOrder, result)
				}
				return
			}
			if failSource {
				if !errors.Is(err, writeErr) || result.Converged || result.AppliedView != nil {
					t.Fatalf("failed source drain published convergence: err=%v result=%+v", err, result)
				}
				if !fx.cg.cpus["reclaimed/reclaimed-0"].Equals(oldReclaim.Intersection(fx.details.CPUsInNUMANodes(0))) {
					t.Fatal("failed source drain changed bucket ownership")
				}
				if !fx.cg.cpus["reclaimed"].Equals(oldReclaim) || !fx.cg.cpus["kubepods"].Equals(all.Difference(oldReclaim)) {
					t.Fatal("failed source drain changed source or destination ownership")
				}
				return
			}
			if err != nil {
				t.Fatalf("canonical partition retry failed: %v", err)
			}
			if !result.Converged || result.AppliedView == nil || !result.AppliedView.ReclaimEffective.Equals(newReclaim) {
				t.Fatalf("canonical reclaim did not converge: %+v", result)
			}
			for uid, containers := range fx.desired.ContainerCPUSetByPod {
				if uid == fx.podUID {
					continue
				}
				for name := range containers {
					rel := "kubepods/besteffort/pod" + uid + "/" + name + "-id"
					if !fx.cg.cpus[rel].Equals(newShare) {
						t.Fatalf("leaf %s=%s, want %s", rel, fx.cg.cpus[rel], newShare)
					}
				}
			}
			bucket := indexOfString(fx.cg.writeOrder, "reclaimed/reclaimed-0")
			root := indexOfString(fx.cg.writeOrder, "reclaimed")
			primary := indexOfString(fx.cg.writeOrder, "kubepods")
			if bucket < 0 || root < bucket || primary <= root {
				t.Fatalf("source must drain before primary grows: %v", fx.cg.writeOrder)
			}
		})
	}
}

func TestOwnerProvenReclaimHandoffAdmissionDrainsSourceBeforeRetryGrowsLeaf(t *testing.T) {
	t.Parallel()

	for _, pendingScope := range []bool{true, false} {
		pendingScope := pendingScope
		t.Run(fmt.Sprintf("pending_scope=%t", pendingScope), func(t *testing.T) {
			t.Parallel()
			fx := newEnforcementAppliedFloorFixture(t)
			ctx := context.Background()
			all := machine.MustParse("0-127")
			handoff := machine.MustParse("4,68")
			// Keep one whole core in NUMA0 reclaim so this test exercises the successful
			// handoff path. Draining the last v1 source CPU stays zero-write fail-closed;
			// that planner boundary is locked by TestV1PlannerRejectsExplicitOrControlledEmptyTargets.
			retainedReclaim := machine.MustParse("5,69")
			oldReclaim := fx.reclaim8.Union(retainedReclaim)
			newReclaim := fx.reclaim6.Union(retainedReclaim)
			oldNonReclaim := all.Difference(oldReclaim)
			newNonReclaim := all.Difference(newReclaim)
			current := machine.MustParse("0,64")
			target := current.Union(handoff)
			const runningPodUID = "running-handoff"
			runningPodRel := "kubepods/besteffort/pod" + runningPodUID
			leafRel := runningPodRel + "/container-id"
			ownerID := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "shared-pool"}
			reclaimOwnerID := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}

			fx.cg.existing[runningPodRel] = true
			fx.cg.existing[leafRel] = true
			fx.cg.cpus["kubepods"] = oldNonReclaim.Clone()
			fx.cg.cpus["kubepods/besteffort"] = oldNonReclaim.Clone()
			fx.cg.cpus[fx.scopeRel] = oldNonReclaim.Clone()
			fx.cg.cpus[runningPodRel] = oldNonReclaim.Clone()
			fx.cg.cpus[leafRel] = current.Clone()
			fx.cg.cpus["reclaimed"] = oldReclaim.Clone()
			fx.cg.cpus["reclaimed/reclaimed-0"] = machine.MustParse("4-5,68-69")
			fx.cg.children["kubepods/besteffort"] = []string{"pod" + fx.podUID, "pod" + runningPodUID}
			fx.cg.children[runningPodRel] = []string{"container-id"}
			fx.cg.mems[runningPodRel] = "0"

			runningPod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(runningPodUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
					Name: "main", ContainerID: "containerd://container-id",
					State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
				}}},
			}
			fx.fetcher.containerErr = nil
			fx.fetcher.containerErrs = map[string]error{"pending": metapod.ErrContainerNotFound}
			fx.fetcher.containerIDs = map[string]string{"main": "container-id"}
			fx.fetcher.pods[fx.podUID] = &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(fx.podUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "pending"}}},
			}
			fx.fetcher.pods[runningPodUID] = runningPod

			state := cpustate.NewCPUPluginState(&machine.CPUTopology{CPUDetails: fx.details})
			if err := state.SetAllocationInfo(runningPodUID, "main", &cpustate.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid: runningPodUID, ContainerName: "main",
					OwnerPoolName: ownerID.Name, QoSLevel: commonstate.PoolNameShare,
				},
				AllocationResult: target.Clone(),
			}); err != nil {
				t.Fatalf("seed allocation: %v", err)
			}

			fx.desired.NonReclaimPool = newNonReclaim.Clone()
			fx.desired.ReclaimEffective = newReclaim.Clone()
			fx.desired.ReclaimEffectivePerNUMA[0] = retainedReclaim.Clone()
			fx.desired.DesiredNonReclaimPool = newNonReclaim.Clone()
			fx.desired.DesiredReclaimEffective = newReclaim.Clone()
			fx.desired.DesiredReclaimEffectivePerNUMA[0] = retainedReclaim.Clone()
			fx.desired.ContainerCPUSetByPod = map[string]map[string]machine.CPUSet{
				fx.podUID:     {"pending": fx.whole.Clone()},
				runningPodUID: {"main": target.Clone()},
			}
			fx.desired.PoolOwners = map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{
				ownerID: {
					ExpectedCPUSet:        target.Clone(),
					ContainerCPUSetByName: map[string]machine.CPUSet{},
				},
			}

			fx.applied.NonReclaimPool = oldNonReclaim.Clone()
			fx.applied.ReclaimEffective = oldReclaim.Clone()
			fx.applied.ReclaimEffectivePerNUMA[0] = machine.MustParse("4-5,68-69")
			fx.applied.PoolProjection = model.NewAppliedPoolProjection()
			fx.applied.PoolProjection.CPUSetByIdentity[ownerID] = current.Clone()
			fx.applied.PoolProjection.CPUSetByIdentity[reclaimOwnerID] = oldReclaim.Clone()
			fx.applied.PoolProjection.UncoveredCPUs = handoff.Clone()

			fx.plugin.resolveContainerRel = func(podUID, containerID string) (string, error) {
				if podUID != runningPodUID || containerID != "container-id" {
					t.Fatalf("unexpected resolver input pod=%q container=%q", podUID, containerID)
				}
				return leafRel, nil
			}

			if !pendingScope {
				delete(fx.desired.ContainerCPUSetByPod, fx.podUID)
			}
			desiredBefore := fx.desired.DeepCopy()
			appliedBefore := fx.applied.DeepCopy()
			in := fx.handlerContext()
			in.State = state

			budget := topology.NewAdjustmentBudget(ctx, topologyBudgetFromConfig(fx.plugin.cfg.TopologyConvergenceBudget))
			var admission topology.ConvergenceResult
			var publishedAdmission bulkheadapi.TopologyResult
			err := fx.plugin.adjustOnceWithResult(ctx, in, budget, &admission, func(result bulkheadapi.TopologyResult) {
				publishedAdmission = result
			})
			if err != nil {
				t.Fatalf("Admission handoff error = %v; result=%+v", err, admission)
			}
			if !admission.ParentSafe || admission.Converged {
				t.Fatalf("Admission must publish ParentSafe but not Full before leaf grow; result=%+v", admission)
			}
			if fx.cg.applyCounts[leafRel] != 0 {
				t.Fatalf("Admission wrote deferred leaf %q %d times", leafRel, fx.cg.applyCounts[leafRel])
			}
			if got := fx.cg.cpus[leafRel]; !got.Equals(current) {
				t.Fatalf("Admission leaf=%s, want unchanged current=%s", got.String(), current.String())
			}
			if got := fx.cg.cpus["reclaimed/reclaimed-0"]; !got.Equals(retainedReclaim) {
				t.Fatalf("Admission reclaim source=%s, want retained=%s", got.String(), retainedReclaim.String())
			}
			if got := fx.cg.cpus["reclaimed"]; !got.Equals(newReclaim) {
				t.Fatalf("Admission reclaim root=%s, want carved=%s", got.String(), newReclaim.String())
			}
			wantPrimary := newNonReclaim
			if !pendingScope {
				wantPrimary = oldNonReclaim // Admission requires no primary grow without pending CPUs.
			}
			if got := fx.cg.cpus["kubepods"]; !got.Equals(wantPrimary) {
				t.Fatalf("Admission controlled primary=%s, want closure=%s", got.String(), wantPrimary.String())
			}
			if got := fx.cg.cpus["kubepods/besteffort"]; !got.Equals(oldNonReclaim) {
				t.Fatalf("Admission widened dynamic ancestor early: got=%s want=%s", got.String(), oldNonReclaim.String())
			}
			bucketWrite := indexOfString(fx.cg.writeOrder, "reclaimed/reclaimed-0")
			reclaimWrite := indexOfString(fx.cg.writeOrder, "reclaimed")
			primaryWrite := indexOfString(fx.cg.writeOrder, "kubepods")
			if bucketWrite < 0 || reclaimWrite < 0 ||
				(pendingScope && (primaryWrite < 0 || bucketWrite >= primaryWrite || reclaimWrite >= primaryWrite)) {
				t.Fatalf("Admission write order=%v, want reclaim source/root before any required primary grow", fx.cg.writeOrder)
			}
			if !model.EqualDesiredView(fx.desired.DeepCopy(), desiredBefore) {
				t.Fatal("Admission mutated canonical DesiredView content")
			}
			if !model.EqualAppliedView(fx.applied.DeepCopy(), appliedBefore) {
				t.Fatal("Admission mutated canonical AppliedView content")
			}

			if publishedAdmission.AppliedView == nil {
				t.Fatal("Admission did not publish its ParentSafe AppliedView")
			}
			if publishedAdmission.AppliedView.Level != model.AppliedViewLevelParentSafe {
				t.Fatalf("Admission published level=%s, want ParentSafe", publishedAdmission.AppliedView.Level)
			}
			if got := publishedAdmission.AppliedView.ReclaimEffective; !got.Equals(newReclaim) {
				t.Fatalf("Admission published reclaim=%s, want carved=%s", got.String(), newReclaim.String())
			}
			if got := publishedAdmission.AppliedView.ReclaimEffectivePerNUMA[0]; !got.Equals(retainedReclaim) {
				t.Fatalf("Admission published NUMA0 reclaim=%s, want retained=%s", got.String(), retainedReclaim.String())
			}
			in.AppliedView = publishedAdmission.AppliedView.DeepCopy()
			in.Mode = cpusetutil.CPUSetAdjustmentModeRetry

			beforeRetryLeafWrites := fx.cg.applyCounts[leafRel]
			beforeRetryWrites := len(fx.cg.writeOrder)
			budget = topology.NewAdjustmentBudget(ctx, topologyBudgetFromConfig(fx.plugin.cfg.TopologyConvergenceBudget))
			var retry topology.ConvergenceResult
			if err := fx.plugin.adjustOnceWithResult(ctx, in, budget, &retry, nil); err != nil {
				t.Fatalf("Retry handoff error = %v; result=%+v", err, retry)
			}
			if !retry.Converged {
				t.Fatalf("Retry must converge exact leaf target; result=%+v", retry)
			}
			if got := fx.cg.applyCounts[leafRel] - beforeRetryLeafWrites; got != 1 {
				t.Fatalf("Retry leaf writes=%d, want exactly 1", got)
			}
			if got := fx.cg.cpus[leafRel]; !got.Equals(target) {
				t.Fatalf("Retry leaf=%s, want target=%s", got.String(), target.String())
			}
			if got := fx.cg.cpus["kubepods/besteffort"]; !got.Equals(newNonReclaim) {
				t.Fatalf("Retry dynamic ancestor=%s, want closure=%s", got.String(), newNonReclaim.String())
			}
			if got := fx.cg.cpus[runningPodRel]; !got.Equals(newNonReclaim) {
				t.Fatalf("Retry pod ancestor=%s, want closure=%s", got.String(), newNonReclaim.String())
			}
			retryWrites := fx.cg.writeOrder[beforeRetryWrites:]
			qosWrite := indexOfString(retryWrites, "kubepods/besteffort")
			podWrite := indexOfString(retryWrites, runningPodRel)
			leafWrite := indexOfString(retryWrites, leafRel)
			if qosWrite < 0 || podWrite < 0 || leafWrite < 0 ||
				qosWrite >= leafWrite || podWrite >= leafWrite {
				t.Fatalf("Retry write order=%v, want dynamic ancestors before leaf", retryWrites)
			}
		})
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
