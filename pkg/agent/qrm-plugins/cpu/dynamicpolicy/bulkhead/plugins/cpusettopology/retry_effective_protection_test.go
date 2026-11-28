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
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestAdmissionRelocationReleasesSourceBeforeDedicatedTakeover(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, pool, qos, failRel string
		sidecar, movingSibling   bool
	}{
		{name: "binding", pool: "binding", qos: "shared_cores"},
		{name: "package", pool: "package/binding", qos: "shared_cores"},
		{name: "shared", pool: "share", qos: "shared_cores"},
		{name: "dedicated", pool: "dedicated", qos: "dedicated_cores"},
		{name: "sidecar", pool: "binding", qos: "shared_cores", sidecar: true},
		{name: "leaf-failure", pool: "binding", qos: "shared_cores", failRel: "primary/podsource/source"},
		{name: "ancestor-failure", pool: "binding", qos: "shared_cores", failRel: "primary/podsource"},
		{name: "replacement", pool: "binding", qos: "shared_cores"},
		{name: "runtime-rewrite", pool: "binding", qos: "shared_cores"},
		{name: "missing-proof", pool: "binding", qos: "shared_cores"},
		{name: "unproven-primary-source", pool: "binding", qos: "shared_cores"},
		{name: "shared-takeover", pool: "binding", qos: "shared_cores"},
		{name: "moving-sibling", pool: "binding", qos: "shared_cores", movingSibling: true},
		{name: "sibling-missing-proof", pool: "binding", qos: "shared_cores", movingSibling: true},
		{name: "sibling-replacement", pool: "binding", qos: "shared_cores", movingSibling: true},
		{name: "sibling-write-failure", pool: "binding", qos: "shared_cores", movingSibling: true, failRel: "primary/podsource/helper"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			topo := &machine.CPUTopology{NumCPUs: 12, NumCores: 6, NumSockets: 2, NumNUMANodes: 2, CPUDetails: machine.CPUDetails{}}
			for cpu := 0; cpu < 12; cpu++ {
				topo.CPUDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: cpu / 6, SocketID: cpu / 6, CoreID: cpu / 2}
			}
			old, target, raw := machine.MustParse("4-5"), machine.MustParse("2-3"), machine.MustParse("0-3,6-7")
			if tc.name == "unproven-primary-source" || tc.name == "shared-takeover" || tc.movingSibling {
				// Destination already belongs to primary, so no reclaim handoff
				// should accidentally make an unproven source executable.
				raw = machine.MustParse("0-1,6-7")
			}
			all := topo.CPUDetails.CPUs()
			s := cpustate.NewCPUPluginState(topo)
			allocation := &cpustate.AllocationInfo{
				AllocationMeta:   commonstate.AllocationMeta{PodUid: "source", PodNamespace: "default", PodName: "source", ContainerName: "main", OwnerPoolName: tc.pool, QoSLevel: tc.qos},
				AllocationResult: target.Clone(),
			}
			if err := s.SetAllocationInfo("source", "main", allocation); err != nil {
				t.Fatal(err)
			}
			ownerID, ok := bulkheadutils.LeafPoolIdentityForAllocation(allocation)
			if !ok {
				t.Fatal("invalid test owner")
			}
			desired := model.NewDesiredView()
			desired.DesiredReclaimEffective = machine.MustParse("0-1,6-7")
			desired.DesiredNonReclaimPool = all.Difference(desired.DesiredReclaimEffective)
			desired.ReclaimEffective = desired.DesiredReclaimEffective.Clone()
			desired.NonReclaimPool = desired.DesiredNonReclaimPool.Clone()
			desired.SharePoolMap[tc.pool] = target.Clone()
			desired.Dedicated = old.Clone()
			desired.PoolOwners[ownerID] = model.DesiredPoolOwner{ProofPodUID: "source", ExpectedCPUSet: target.Clone(), ContainerCPUSetByName: map[string]machine.CPUSet{"main": target.Clone()}}
			pendingOwner := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "pending"}
			if tc.name == "shared-takeover" {
				pendingOwner = model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "other-share"}
				desired.Dedicated = machine.NewCPUSet()
				desired.SharePoolMap["other-share"] = old.Clone()
			}
			desired.PoolOwners[pendingOwner] = model.DesiredPoolOwner{ProofPodUID: "pending", ExpectedCPUSet: old.Clone()}
			desired.ContainerCPUSetByPod["source"] = map[string]machine.CPUSet{"main": target.Clone()}
			desired.ContainerCPUSetByPod["pending"] = map[string]machine.CPUSet{"main": old.Clone()}
			applied := desired.ToAppliedView()
			applied.NonReclaimPool = all.Difference(raw)
			applied.ReclaimEffective = raw.Clone()
			applied.PoolProjection.CPUSetByIdentity[ownerID] = old.Clone()
			applied.PoolProjection.CPUSetByIdentity[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}] = raw.Clone()
			if tc.name == "unproven-primary-source" || tc.name == "shared-takeover" || tc.movingSibling {
				applied.PoolProjection.CPUSetByIdentity[ownerID] = old.Union(target)
			}
			identity := pluginFakeIdentity("primary/podsource/source")
			applied.RelProofByRel["primary/podsource/source"] = model.CgroupRelProof{Device: identity.Device, Inode: identity.Inode, CPUSet: old.Clone()}
			cg := &fakeCgroupClient{existing: map[string]bool{}, cpus: map[string]machine.CPUSet{
				"primary": all.Difference(raw), "reclaim": raw.Clone(), "primary/podsource": old.Clone(),
				"primary/podsource/source": old.Clone(), "primary/podsource/sandbox": old.Clone(), "primary/podpending": old.Clone(),
			}, children: map[string][]string{"primary": {"podsource", "podpending"}, "primary/podsource": {"source", "sandbox"}}}
			pods := []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID("source")}, Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
					Status: v1.PodStatus{QOSClass: v1.PodQOSGuaranteed, ContainerStatuses: []v1.ContainerStatus{{Name: "main", ContainerID: "containerd://source"}}},
				},
				{ObjectMeta: metav1.ObjectMeta{UID: types.UID("pending")}, Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main"}}}, Status: v1.PodStatus{QOSClass: v1.PodQOSGuaranteed}},
			}
			podTarget := target.Clone()
			if tc.sidecar {
				helper := machine.MustParse("8-9")
				podTarget = podTarget.Union(helper)
				desired.ContainerCPUSetByPod["source"]["helper"] = helper
				desired.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "helper"}] = model.DesiredPoolOwner{ExpectedCPUSet: helper}
				cg.cpus["primary/podsource"] = old.Union(helper)
				cg.cpus["primary/podsource/helper"] = helper
				cg.children["primary/podsource"] = append(cg.children["primary/podsource"], "helper")
				pods[0].Spec.Containers = append(pods[0].Spec.Containers, v1.Container{Name: "helper"})
				pods[0].Status.ContainerStatuses = append(pods[0].Status.ContainerStatuses, v1.ContainerStatus{Name: "helper", ContainerID: "containerd://helper"})
			}
			if tc.movingSibling {
				helperOld := machine.MustParse("8-9")
				helperAllocation := allocation.Clone()
				helperAllocation.ContainerName = "helper"
				if err := s.SetAllocationInfo("source", "helper", helperAllocation); err != nil {
					t.Fatal(err)
				}
				desired.ContainerCPUSetByPod["source"]["helper"] = target.Clone()
				oldDesired := desired.DeepCopy()
				delete(oldDesired.PoolOwners, pendingOwner)
				oldDesired.PoolOwners[ownerID] = model.DesiredPoolOwner{ExpectedCPUSet: old.Union(target).Union(helperOld)}
				applied.PoolProjection = buildAppliedPoolProjection(model.AppliedViewLevelParentSafe, oldDesired, applied)
				if !applied.PoolProjection.CPUSetByIdentity[ownerID].Equals(old.Union(target).Union(helperOld)) ||
					!applied.PoolProjection.AmbiguousCPUs.IsEmpty() {
					t.Fatal("invalid historical owner projection")
				}
				helperIdentity := pluginFakeIdentity("primary/podsource/helper")
				applied.RelProofByRel["primary/podsource/helper"] = model.CgroupRelProof{
					Device: helperIdentity.Device, Inode: helperIdentity.Inode, CPUSet: helperOld.Clone(),
				}
				if tc.name == "sibling-missing-proof" {
					delete(applied.RelProofByRel, "primary/podsource/helper")
				}
				if tc.name == "sibling-replacement" {
					proof := applied.RelProofByRel["primary/podsource/helper"]
					proof.Inode++
					applied.RelProofByRel["primary/podsource/helper"] = proof
				}
				cg.cpus["primary/podsource"] = old.Union(helperOld)
				cg.cpus["primary/podsource/helper"] = helperOld.Clone()
				cg.children["primary/podsource"] = append(cg.children["primary/podsource"], "helper")
				pods[0].Spec.Containers = append(pods[0].Spec.Containers, v1.Container{Name: "helper"})
				pods[0].Status.ContainerStatuses = append(pods[0].Status.ContainerStatuses,
					v1.ContainerStatus{Name: "helper", ContainerID: "containerd://helper"})
				// The same pool's unrelated Pod must not join this Pod's fence closure.
				unrelated := allocation.Clone()
				unrelated.PodUid, unrelated.PodName = "unrelated", "unrelated"
				if err := s.SetAllocationInfo("unrelated", "main", unrelated); err != nil {
					t.Fatal(err)
				}
				desired.ContainerCPUSetByPod["unrelated"] = map[string]machine.CPUSet{"main": target.Clone()}
				unrelatedIdentity := pluginFakeIdentity("primary/podunrelated/unrelated")
				applied.RelProofByRel["primary/podunrelated/unrelated"] = model.CgroupRelProof{
					Device: unrelatedIdentity.Device, Inode: unrelatedIdentity.Inode, CPUSet: helperOld.Clone(),
				}
				cg.cpus["primary/podunrelated"] = helperOld.Clone()
				cg.cpus["primary/podunrelated/unrelated"] = helperOld.Clone()
				cg.children["primary"] = append(cg.children["primary"], "podunrelated")
				cg.children["primary/podunrelated"] = []string{"unrelated"}
				pods = append(pods, &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID("unrelated")},
					Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
					Status:     v1.PodStatus{QOSClass: v1.PodQOSGuaranteed, ContainerStatuses: []v1.ContainerStatus{{Name: "main", ContainerID: "containerd://unrelated"}}},
				})
			}
			for rel := range cg.cpus {
				cg.existing[rel] = true
			}
			before := model.CloneCPUSetMap(cg.cpus)
			failure := errors.New("source release rejected")
			cg.writeCPUHook = func(rel string, count int) error {
				if rel == tc.failRel && count == 2 {
					return failure
				}
				return nil
			}
			if tc.name == "replacement" {
				applied.RelProofByRel["primary/podsource/source"] = model.CgroupRelProof{Device: identity.Device, Inode: identity.Inode + 1, CPUSet: old.Clone()}
			}
			if tc.name == "missing-proof" || tc.name == "unproven-primary-source" {
				delete(applied.RelProofByRel, "primary/podsource/source")
			}
			p := &CPUSetTopologyPlugin{
				cfg: bulkheadconfig.BulkheadConfiguration{
					BulkheadPrimaryRelPath:  "primary",
					BulkheadReclaimRelPaths: []string{"reclaim"}, BulkheadReclaimNumaPrefixes: []string{"reclaim/numa-"},
				}, cgroup: cg,
				resolveContainerRel: func(uid, cid string) (string, error) { return "primary/pod" + uid + "/" + cid, nil },
			}
			runtimeRewrites := 0
			if tc.name == "runtime-rewrite" {
				cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
					if rel == "primary/podsource" && cg.cpus[rel].Equals(podTarget) {
						// A v1 stale container mask is rejected by the tightened Pod
						// parent. Do not model QRM's lease as serializing this writer.
						runtimeRewrites++
						if old.IsSubsetOf(cg.cpus[rel]) {
							cg.cpus["primary/podsource/source"] = old.Clone()
						}
					}
				}
			}
			var result bulkheadapi.TopologyResult
			err := p.runNormalAdjustment(context.Background(), bulkheadapi.HandlerContext{
				CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
					State: s, Mode: cpusetutil.CPUSetAdjustmentModeAdmission, Topology: topo,
					MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &metapod.PodFetcherStub{PodList: pods}}},
				},
				DesiredView: desired, AppliedView: applied, ProjectPendingToDesiredNonReclaim: true,
			}, func(got bulkheadapi.TopologyResult) { result = got })
			if tc.name == "replacement" || tc.name == "missing-proof" || tc.name == "unproven-primary-source" ||
				tc.name == "sibling-missing-proof" || tc.name == "sibling-replacement" {
				if err == nil || result.AppliedView != nil || len(cg.writeOrder) != 0 {
					t.Fatalf("replaced source accepted: err=%v writes=%v", err, cg.writeOrder)
				}
				return
			}
			if tc.failRel != "" {
				if !errors.Is(err, failure) || result.AppliedView != nil {
					t.Fatalf("failed release published: err=%v result=%+v", err, result)
				}
				for rel, cpus := range before {
					if !cg.cpus[rel].Equals(cpus) {
						t.Fatalf("rollback mismatch %s=%s want=%s", rel, cg.cpus[rel], cpus)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.AppliedView == nil {
				t.Fatal("missing physical proof")
			}
			if !cg.cpus["primary/podsource"].Equals(podTarget) {
				t.Fatalf("Pod fence=%s want=%s", cg.cpus["primary/podsource"], podTarget)
			}
			for _, rel := range []string{"primary/podsource/source", "primary/podsource/sandbox"} {
				want := target
				if tc.sidecar && rel == "primary/podsource/sandbox" {
					want = podTarget
				}
				if !cg.cpus[rel].Equals(want) {
					t.Fatalf("source not released/fenced before takeover: %s=%s want=%s", rel, cg.cpus[rel], want)
				}
			}
			if tc.movingSibling {
				if !cg.cpus["primary/podsource/helper"].Equals(target) {
					t.Fatal("necessary sibling release was deferred")
				}
				if !cg.cpus["primary/podunrelated/unrelated"].Equals(machine.MustParse("8-9")) {
					t.Fatal("unrelated Pod joined the exact fence closure")
				}
				for _, rel := range cg.writeOrder {
					if rel == "primary/podunrelated/unrelated" {
						t.Fatal("unrelated Pod leaf was written")
					}
				}
			}
			if tc.sidecar && !cg.cpus["primary/podsource/helper"].Equals(machine.MustParse("8-9")) {
				t.Fatal("sibling target changed")
			}
			if tc.name == "runtime-rewrite" && runtimeRewrites == 0 {
				t.Fatal("stale runtime writer was not exercised")
			}
			if !result.AppliedView.ReclaimEffective.Intersection(target).IsEmpty() {
				t.Fatal("destination remains reclaimed")
			}
		})
	}
}

// TestAdmissionRetainsResolvedTransientOwner exercises the physical DAG and its
// final snapshot, not only the partition helper. The active SNB leaf already has
// a container ID, so it is absent from pending protections; a concurrent pending
// allocation still causes the shadow to rebuild the reclaim budget.
func TestAdmissionRetainsResolvedTransientOwner(t *testing.T) {
	t.Parallel()
	topo := &machine.CPUTopology{NumCPUs: 12, NumCores: 6, NumSockets: 2, NumNUMANodes: 2, CPUDetails: machine.CPUDetails{}}
	for cpu := 0; cpu < 12; cpu++ {
		topo.CPUDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: cpu / 6, SocketID: cpu / 6, CoreID: cpu / 2}
	}
	all, raw := machine.MustParse("0-11"), machine.MustParse("0-3,6-7")
	protected := machine.MustParse("2-3")
	desired := model.NewDesiredView()
	desired.DesiredReclaimEffective = raw.Clone()
	desired.DesiredNonReclaimPool = all.Difference(raw)
	bulkheadutils.ApplyTransientProtectedNonReclaim(desired, topo, protected)
	desired.ContainerCPUSetByPod["active"] = map[string]machine.CPUSet{"main": protected.Clone()}
	desired.ContainerCPUSetByPod["pending"] = map[string]machine.CPUSet{"main": machine.MustParse("4-5")}
	applied := desired.ToAppliedView()
	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true, "primary/active": true, "kubepods/podpending": true},
		cpus: map[string]machine.CPUSet{
			"primary": desired.NonReclaimPool.Clone(), "reclaim": desired.ReclaimEffective.Clone(),
			"primary/active": protected.Clone(), "kubepods/podpending": machine.MustParse("4-5"),
		},
		children: map[string][]string{"primary": {"active"}},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath: "primary", BulkheadReclaimRelPaths: []string{"reclaim"},
			BulkheadReclaimNumaPrefixes: []string{"reclaim/numa-"},
		}, cgroup: cg,
		resolveContainerRel: func(string, string) (string, error) { return "primary/active", nil },
	}
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("active")}, Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
			Status: v1.PodStatus{QOSClass: v1.PodQOSGuaranteed, ContainerStatuses: []v1.ContainerStatus{{Name: "main", ContainerID: "containerd://active"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("pending")}, Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
			Status: v1.PodStatus{QOSClass: v1.PodQOSGuaranteed},
		},
	}
	var result bulkheadapi.TopologyResult
	err := p.runNormalAdjustment(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Mode: cpusetutil.CPUSetAdjustmentModeAdmission, Topology: topo,
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &metapod.PodFetcherStub{PodList: pods}}},
		},
		DesiredView: desired, AppliedView: applied, ProjectPendingToDesiredNonReclaim: true,
	}, func(got bulkheadapi.TopologyResult) { result = got })
	if err != nil {
		t.Fatalf("admission error: %v", err)
	}
	if result.AppliedView == nil {
		t.Fatal("missing final snapshot proof")
	}
	view := result.AppliedView
	if !protected.IsSubsetOf(view.NonReclaimPool) || !protected.Intersection(view.ReclaimEffective).IsEmpty() {
		t.Fatalf("active owner lost from physical proof: protected=%s primary=%s reclaim=%s", protected, view.NonReclaimPool, view.ReclaimEffective)
	}
	if err := bulkheadutils.ValidateHardPartitionAppliedView(view, nil, topo, bulkheadutils.CPUSetPartitionViewOptions{
		HardPartitionEnabled: true, HardPartitionReclaimTargetPerNUMA: map[int]int{0: 4, 1: 2},
	}); err != nil {
		t.Fatalf("physical proof rejected: %v", err)
	}
	if !cg.cpus["primary/active"].Equals(protected) {
		t.Fatal("resolved active leaf changed")
	}
}

// TestRetryContinuesManagerOwnedEffectiveProtectionFloor drives the REAL
// CPUSetTopologyPlugin adjustment gates on a post-commit deferred-leaf Retry
// round — not the pure shadow helpers.
//
// Scenario (the live double-authority chain):
//
//  1. A soft, non-overlap ParentSafe admission for a whole-NUMA0 shared pod just
//     converged. The manager committed the validated effective reclaim to its
//     in-memory AppliedView (reclaim {1,5}, keeping the NUMA0 reclaim bucket
//     {1}), but by design a ParentSafe/Partial outcome does NOT write the
//     CommitOverride, so the persisted business state still derives the lagging
//     DesiredReclaimEffective = {5} (NUMA0 bucket dropped) and a self-expanded
//     DesiredNonReclaimPool that already swallows whole NUMA0.
//  2. The cgroup of the deferred leaf materialized, scheduling a Retry round.
//     The real manager gate computes ProjectPendingToDesiredNonReclaim=false for
//     Mode=Retry (it is admission-only). AppliedView still carries reclaim {1,5}.
//
// Before the fix the retry round had no projection (flag false) and
// admissionEnforcementBaseline returned nil (gated on the same flag), so the
// shadow rebuilt the reclaim floor from the lagging business DesiredReclaimEffective
// = {5}: the NUMA0 bucket {1} was dropped from the required ancestor floor and the
// primary could widen onto it. After the fix, projection and the enforcement
// baseline are derived from the manager-owned AppliedView whenever it carries a
// validated reclaim, so the retry keeps {1} reclaimed.
func TestRetryContinuesManagerOwnedEffectiveProtectionFloor(t *testing.T) {
	t.Parallel()

	// Minimal 2-NUMA SMT=2 shape: NUMA0={0,1,2,3}, NUMA1={4,5,6,7}; cpu c and
	// c+1 share a core. NUMA0 reclaim bucket = {1}.
	topo := &machine.CPUTopology{
		NumCPUs:      8,
		NumCores:     4,
		NumSockets:   1,
		NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			4: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			5: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			6: {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			7: {NUMANodeID: 1, SocketID: 0, CoreID: 3},
		},
	}
	all := machine.MustParse("0-7")
	numa0 := machine.MustParse("0-3")

	// Manager-owned validated ParentSafe applied view: reclaim is one whole SMT
	// core per NUMA = {0,1} (NUMA0 bucket) + {4,5} (NUMA1). Its validated
	// non-reclaim pool excludes both whole cores.
	appliedReclaim := machine.MustParse("0,1,4,5")
	applied := &model.AppliedView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:          machine.NewCPUSet(),
			NonReclaimPool:   all.Difference(appliedReclaim).Clone(), // {2,3,6,7}
			ReclaimEffective: appliedReclaim.Clone(),                 // {0,1,4,5}
		},
		Level: model.AppliedViewLevelParentSafe,
	}

	// Lagging business desired view: the pending whole-NUMA0 pod self-expanded
	// DesiredNonReclaimPool to swallow NUMA0, dropping the NUMA0 reclaim core.
	desiredReclaim := machine.MustParse("4,5")
	const podUID = "pod-retry"
	const podRel = "kubepods/pod-retry"
	desired := model.NewDesiredView()
	desired.Reserve = machine.NewCPUSet()
	desired.DesiredNonReclaimPool = all.Difference(desiredReclaim).Clone() // {0,1,2,3,6,7}
	desired.DesiredReclaimEffective = desiredReclaim.Clone()               // {4,5}
	desired.NonReclaimPool = all.Difference(appliedReclaim).Clone()        // materialized validated {2,3,6,7}
	desired.ReclaimEffective = appliedReclaim.Clone()
	desired.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}] = model.DesiredPoolOwner{
		ExpectedCPUSet: appliedReclaim.Clone(),
	}
	desired.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "share"}] = model.DesiredPoolOwner{
		ExpectedCPUSet: all.Difference(appliedReclaim).Clone(),
	}
	desired.ContainerCPUSetByPod[podUID] = map[string]machine.CPUSet{
		"main": numa0.Clone(),
	}

	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true, podRel: true},
		cpus: map[string]machine.CPUSet{
			"primary": all.Difference(appliedReclaim).Clone(), // {0,2,3,4,6,7}
			"reclaim": appliedReclaim.Clone(),                 // {1,5}
			podRel:    numa0.Clone(),
		},
		children: map[string][]string{},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
		now:    func() time.Time { return time.Now() },
		pendingProtections: map[string]pendingPodProtection{
			podUID: {rel: podRel, protectUntil: time.Now().Add(defaultPendingPodProtectionTTL)},
		},
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
		Status:     v1.PodStatus{QOSClass: v1.PodQOSBurstable},
	}

	// Retry round as the real manager produces it: Mode=Retry (admission-only
	// gate yields ProjectPendingToDesiredNonReclaim=false), but the manager-owned
	// AppliedView still carries the validated reclaim {1,5}.
	var result bulkheadapi.TopologyResult
	err := p.runNormalAdjustment(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Mode:       cpusetutil.CPUSetAdjustmentModeRetry,
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{pod}}}},
			Topology:   topo,
		},
		DesiredView:                       desired,
		AppliedView:                       applied,
		ProjectPendingToDesiredNonReclaim: false, // real manager output for Mode=Retry
		PendingSharedProjection:           map[string]map[string]struct{}{podUID: {"main": {}}},
	}, func(got bulkheadapi.TopologyResult) {
		result = got
	})
	if err != nil {
		t.Fatalf("runNormalAdjustment(retry) error = %v", err)
	}
	if result.AppliedView == nil {
		t.Fatal("retry round must publish an AppliedView")
	}
	// The NUMA0 reclaim core {0,1} must survive the retry: the manager-owned
	// effective protection is the single authority for the ancestor floor.
	if !result.AppliedView.ReclaimEffective.Contains(0) {
		t.Fatalf("retry dropped the NUMA0 reclaim ancestor floor: applied reclaim=%s, "+
			"want it to contain CPU 0 (continued from manager-owned AppliedView %s)",
			result.AppliedView.ReclaimEffective.String(), appliedReclaim.String())
	}
}

// TestHardPartitionAdmissionRetainsValidatedReclaimFloor is the sa8 regression.
// During an active RampUp hard-partition floor (EnableReclaim &&
// EnableRampUpReclaimHardPartition, non-overlap), a fresh shared ramp-up Admission
// must project the pending shared container onto the validated non-reclaim domain
// and KEEP the manager-owned reclaim bucket. Before the fix the manager gate
// computed ProjectPendingToDesiredNonReclaim=false under hard partition, so the
// enforcement-from-AppliedView baseline engaged only on Retry, never on the first
// Admission: the shadow rebuilt reclaim from the lagging business desired, dropped
// the NUMA0 reclaim core {0,1}, and the admission could not reach an accepted state
// (diagnostic projection_disabled / eligible=exact).
func TestHardPartitionAdmissionRetainsValidatedReclaimFloor(t *testing.T) {
	t.Parallel()

	// 2-NUMA SMT=2: NUMA0={0,1,2,3}, NUMA1={4,5,6,7}; cpu c and c+1 share a core.
	topo := &machine.CPUTopology{
		NumCPUs:      8,
		NumCores:     4,
		NumSockets:   1,
		NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			4: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			5: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			6: {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			7: {NUMANodeID: 1, SocketID: 0, CoreID: 3},
		},
	}
	all := machine.MustParse("0-7")

	// Manager-owned validated ParentSafe applied view: one whole SMT core per NUMA.
	appliedReclaim := machine.MustParse("0,1,4,5")
	applied := &model.AppliedView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:          machine.NewCPUSet(),
			NonReclaimPool:   all.Difference(appliedReclaim).Clone(), // {2,3,6,7}
			ReclaimEffective: appliedReclaim.Clone(),
		},
		Level: model.AppliedViewLevelParentSafe,
	}

	// Lagging business desired: the pending whole-NUMA0 pod self-expanded
	// DesiredNonReclaimPool to swallow NUMA0, dropping the NUMA0 reclaim core {0,1}.
	desiredReclaim := machine.MustParse("4,5")
	const podUID = "pod-rampup"
	const podRel = "kubepods/burstable/pod-rampup"
	desired := model.NewDesiredView()
	desired.Reserve = machine.NewCPUSet()
	desired.DesiredNonReclaimPool = all.Difference(desiredReclaim).Clone() // {0,1,2,3,6,7}
	desired.DesiredReclaimEffective = desiredReclaim.Clone()               // {4,5}
	desired.NonReclaimPool = all.Difference(appliedReclaim).Clone()        // validated {2,3,6,7}
	desired.ReclaimEffective = appliedReclaim.Clone()
	desired.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}] = model.DesiredPoolOwner{
		ExpectedCPUSet: appliedReclaim.Clone(),
	}
	desired.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "share"}] = model.DesiredPoolOwner{
		ExpectedCPUSet: all.Difference(appliedReclaim).Clone(),
	}
	desired.ContainerCPUSetByPod[podUID] = map[string]machine.CPUSet{
		"main": machine.MustParse("2"),
	}

	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true, podRel: true},
		cpus: map[string]machine.CPUSet{
			"primary": all.Difference(appliedReclaim).Clone(), // {2,3,6,7}
			"reclaim": appliedReclaim.Clone(),                 // {0,1,4,5}
			podRel:    machine.MustParse("2"),
		},
		children: map[string][]string{},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
		now:    func() time.Time { return time.Now() },
		pendingProtections: map[string]pendingPodProtection{
			podUID: {rel: podRel, protectUntil: time.Now().Add(defaultPendingPodProtectionTTL)},
		},
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
		Status:     v1.PodStatus{QOSClass: v1.PodQOSBurstable},
	}

	// The gate value the manager supplies on a hard-partition, non-overlap Admission.
	// The manager gate lives in package bulkhead and is unit-tested there; here we
	// hand the plugin the post-fix value (true) to prove the real convergence chain
	// retains the validated reclaim floor once the gate engages. On current HEAD the
	// manager computes false under hard partition, which drops the NUMA0 floor (the
	// sa8 red state exercised by the manager/view unit tests).
	const gate = true

	var result bulkheadapi.TopologyResult
	err := p.runNormalAdjustment(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{pod}}}},
			Topology:   topo,
		},
		DesiredView:                       desired,
		AppliedView:                       applied,
		ProjectPendingToDesiredNonReclaim: gate,
		PendingSharedProjection:           map[string]map[string]struct{}{podUID: {"main": {}}},
	}, func(got bulkheadapi.TopologyResult) {
		result = got
	})
	if err != nil {
		t.Fatalf("hard-partition admission error = %v", err)
	}
	if result.AppliedView == nil {
		t.Fatal("hard-partition admission must publish an AppliedView")
	}
	// The NUMA0 reclaim core {0,1} must survive: the manager-owned validated floor
	// is the single authority on the Admission round once projection engages.
	if !result.AppliedView.ReclaimEffective.Contains(0) {
		t.Fatalf("hard-partition admission dropped the NUMA0 reclaim floor: applied reclaim=%s, "+
			"want CPU 0 retained (validated AppliedView=%s, gate=%t)",
			result.AppliedView.ReclaimEffective.String(), appliedReclaim.String(), gate)
	}
}
