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
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

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
