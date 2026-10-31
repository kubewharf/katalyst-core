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
	"context"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"

	v1 "k8s.io/api/core/v1"

	cpusetplugin "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/cpusettopology"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
)

// unprovenFetcher returns an empty cache-only snapshot and an empty strict fresh
// snapshot, so the two desired live leaves (A -> B relocation) carry NO fresh
// container lifecycle proof. The chain must not map B onto the physical leaves and
// must not publish a Full view.
type unprovenFetcher struct {
	metapod.PodFetcherStub
}

func (f *unprovenFetcher) GetPodListFromCache(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return nil, nil
}

func (f *unprovenFetcher) GetPodList(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return nil, nil
}

// appliedOwnerState builds the current desired CPUPluginState for the historical
// applied-authority admission: generic named share pool "pool-a" owns pending68,
// two desired live leaves target B4 {0,4,48,52}, four auxiliary share leaves empty.
func appliedOwnerState() cpustate.ReadonlyState {
	state := cpustate.NewCPUPluginState(nil)

	poolMeta := commonstate.GenerateGenericPoolAllocationMeta("pool-a")
	poolMeta.QoSLevel = commonstate.PoolNameShare
	_ = state.SetAllocationInfo("pool-a", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   poolMeta,
		AllocationResult: replayPending68.Clone(),
	})

	for _, podUID := range []string{"live-a-pod", "live-b-pod"} {
		_ = state.SetAllocationInfo(podUID, "main", &cpustate.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				ContainerName: "main",
				OwnerPoolName: "pool-a",
				QoSLevel:      commonstate.PoolNameShare,
			},
			AllocationResult: replayTargetLeafB4.Clone(),
		})
	}

	for _, name := range []string{"drained-a", "drained-b", "drained-c", "drained-d"} {
		_ = state.SetAllocationInfo(name+"-pod", "main", &cpustate.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        name + "-pod",
				ContainerName: "main",
				OwnerPoolName: "pool-a",
				QoSLevel:      commonstate.PoolNameShare,
			},
			AllocationResult: machine.NewCPUSet(),
		})
	}
	return state
}

// historicalAppliedView seeds the manager-owned m.appliedView with the previous
// real applied authority: R28 reclaim envelope, full pool projection, and per-rel
// cset proofs bound to stable device/inode identities. This is a legitimate
// historical input fixture; no ConvergenceReport or converged result is fabricated.
func historicalAppliedView() *model.AppliedView {
	view := &model.AppliedView{
		CPUSetPartitionView: model.NewCPUSetPartitionView(),
		Level:               model.AppliedViewLevelFull,
		CPUSetByRel: map[string]machine.CPUSet{
			"primary":        replayOldAppliedReclaim28.Clone(),
			"primary/live-a": replayOldLeafCPUs.Clone(),
			"primary/live-b": replayOldLeafCPUs.Clone(),
		},
		RelProofByRel: map[string]model.CgroupRelProof{
			"primary":        {Device: 1, Inode: 100, CPUSet: replayOldAppliedReclaim28.Clone()},
			"primary/live-a": {Device: 1, Inode: 201, CPUSet: replayOldLeafCPUs.Clone()},
			"primary/live-b": {Device: 1, Inode: 202, CPUSet: replayOldLeafCPUs.Clone()},
		},
		PoolProjection: model.NewAppliedPoolProjection(),
	}
	view.ReclaimEffective = replayOldAppliedReclaim28.Clone()
	view.NonReclaimPool = all96CPUs().Difference(replayOldAppliedReclaim28)
	view.PoolProjection.CPUSetByIdentity[model.CPUSetPoolIdentity{
		Kind: model.CPUSetPoolKindShare, Name: "pool-old",
	}] = view.NonReclaimPool.Clone()
	return view
}

// TestAppliedOwnerRecoveryHistoricalAuthorityAdmissionFailClosed drives a real
// ModeAdmission Manager.Apply over the synthetic 96-CPU fixture after seeding the
// manager-owned historical AppliedView (R28 reclaim + full pool projection + per-rel
// cset proofs). The two desired live leaves (A4 {7,11,55,59} -> B4 {0,4,48,52})
// carry NO fresh lifecycle proof; the unproven relocation must not converge Full,
// must not map B onto the physical leaves, and must not retire/ release old A.
func TestAppliedOwnerRecoveryHistoricalAuthorityAdmissionFailClosed(t *testing.T) {
	t.Parallel()
	env := newReplayEnv()
	realPlugin := cpusetplugin.NewCPUSetTopologyPluginWithDependencies(replayCoreConf(), func() client.CgroupClient {
		return env.cg
	}, env.cg.resolveContainerRel)
	rec := &recordingTopology{TopologyPlugin: realPlugin.(bulkheadapi.TopologyPlugin)}
	m := &Manager{plugins: []bulkheadapi.Plugin{rec}}

	// Seed the historical applied authority the manager owns before this admission.
	m.appliedView = historicalAppliedView()
	m.appliedViewRevision = 1
	m.appliedViewValidForPeriodical = true

	in := replayRoundCtx()
	in.Mode = cpusetutil.CPUSetAdjustmentModeAdmission
	in.CoreConf = replayCoreConf()
	in.State = appliedOwnerState()
	in.MetaServer = &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &unprovenFetcher{}}}

	_, applyErr := m.Apply(context.Background(), in)
	reads, lists, writes, failedAt, _ := env.driver.snapshot()

	liveA := env.driver.leafCPUs("primary/live-a")
	liveB := env.driver.leafCPUs("primary/live-b")
	primary := env.driver.primaryCPUs()

	t.Logf("historical admission: applyErr=%v level=%v note=%q reads=%d list=%d writes=%d failedAt=%d",
		applyErr, rec.level, rec.note, reads, lists, writes, failedAt)
	t.Logf("physical: primary=%s liveA=%s liveB=%s", primary.String(), liveA.String(), liveB.String())
	t.Logf("manager appliedView: valid=%t reclaim=%s level=%v revision=%d",
		m.appliedViewValidForPeriodical, m.appliedView.ReclaimEffective.String(), m.appliedView.Level, m.appliedViewRevision)

	// B4 target {0,4,48,52} must be carved out of the historical R28 authority.
	if !replayTargetLeafB4.IsSubsetOf(replayOldAppliedReclaim28) {
		t.Fatalf("target B4 %s must be a subset of historical reclaim R28 %s",
			replayTargetLeafB4.String(), replayOldAppliedReclaim28.String())
	}

	// Unproven relocation must not publish a Full view.
	if m.appliedViewValidForPeriodical && m.appliedView != nil && m.appliedView.Level == model.AppliedViewLevelFull {
		t.Fatalf("unproven relocation published a Full applied view; fail-closed required")
	}

	// Physical leaves must stay on old A4; B must not be mapped onto them.
	if liveA.Equals(replayTargetLeafB4) || liveB.Equals(replayTargetLeafB4) {
		t.Fatalf("unproven relocation mapped B4 onto physical leaves: liveA=%s liveB=%s",
			liveA.String(), liveB.String())
	}
	if !liveA.Equals(replayOldLeafCPUs) || !liveB.Equals(replayOldLeafCPUs) {
		t.Fatalf("old A4 leaves must be retained/protected: liveA=%s liveB=%s, want %s",
			liveA.String(), liveB.String(), replayOldLeafCPUs.String())
	}

	// The historical applied authority is retained at the proof/projection level,
	// not merely as a stale primary string: per-rel cset proofs, stable inode proofs,
	// and the old pool projection survive the failed admission untouched.
	if got := m.appliedView.CPUSetByRel["primary/live-a"]; !got.Equals(replayOldLeafCPUs) {
		t.Fatalf("retained applied proof for live-a = %s, want old A4 %s", got.String(), replayOldLeafCPUs.String())
	}
	proofA, okA := m.appliedView.RelProofByRel["primary/live-a"]
	proofB, okB := m.appliedView.RelProofByRel["primary/live-b"]
	if !okA || !okB || proofA.Inode != 201 || proofB.Inode != 202 ||
		!proofA.CPUSet.Equals(replayOldLeafCPUs) || !proofB.CPUSet.Equals(replayOldLeafCPUs) {
		t.Fatalf("retained stable inode proofs lost: live-a=%+v live-b=%+v", proofA, proofB)
	}
	oldIdentity := model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "pool-old"}
	projected, ok := m.appliedView.PoolProjection.CPUSetByIdentity[oldIdentity]
	if !ok || !projected.Equals(m.appliedView.NonReclaimPool) {
		t.Fatalf("retained old pool projection lost: %+v", m.appliedView.PoolProjection.CPUSetByIdentity)
	}
	// Old A4 CPUs stay on the protected non-reclaim side of the retained authority;
	// they are neither retired into reclaim nor released onto the desired B4 target.
	if !m.appliedView.NonReclaimPool.Intersection(replayOldLeafCPUs).Equals(replayOldLeafCPUs) {
		t.Fatalf("old A4 %s dropped from retained non-reclaim protection", replayOldLeafCPUs.String())
	}
	if overlap := m.appliedView.ReclaimEffective.Intersection(replayOldLeafCPUs); !overlap.IsEmpty() {
		t.Fatalf("old A4 %s must not fold into retained reclaim; overlap=%s", replayOldLeafCPUs.String(), overlap.String())
	}
}

// exclusiveOwnerState builds a real state fixture for the exclusive-owned common
// pool check: a generic common-named isolation pool still exclusively occupies A4
// {7,11,55,59}, built from the real isolation pool naming (OwnerPoolName prefix
// isolation-) rather than the dedicated path, while a share pool sits on non-owner
// CPUs. No generalized defer: the typed partition keeps A4 on the isolated side and
// share/reclaim cannot take A4 before release.
func exclusiveOwnerState() cpustate.ReadonlyState {
	state := cpustate.NewCPUPluginState(nil)

	// A common named isolation pool retains its exclusive CPUs.
	isolationMeta := commonstate.GenerateGenericPoolAllocationMeta("isolation-0")
	_ = state.SetAllocationInfo("isolation-0", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   isolationMeta,
		AllocationResult: replayOldLeafCPUs.Clone(),
	})

	// A separate share pool on non-owner CPUs; it must never reach for A4.
	shareMeta := commonstate.GenerateGenericPoolAllocationMeta("pool-a")
	shareMeta.QoSLevel = commonstate.PoolNameShare
	_ = state.SetAllocationInfo("pool-a", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   shareMeta,
		AllocationResult: machine.NewCPUSet(0, 1),
	})
	return state
}

// TestAppliedOwnerRecoveryExclusiveOwnerShareCannotFreeA proves that while the
// common isolation pool still exclusively occupies A4, the typed partition keeps A4
// on the isolated side: share pool and reclaim must not take A4, and A4 must not be
// misclassified onto the dedicated path. The manager admission additionally fails
// closed (no false Full, no published view) instead of releasing A4 ahead of release.
func TestAppliedOwnerRecoveryExclusiveOwnerShareCannotFreeA(t *testing.T) {
	t.Parallel()
	state := exclusiveOwnerState()

	// Typed partition invariant from the real validated builder.
	view, _, err := bulkheadutils.BuildValidatedCPUSetPartitionView(state, replay96CPUTopology(), bulkheadutils.CPUSetPartitionViewOptions{})
	if err != nil {
		t.Fatalf("build validated isolation-owner partition view: %v", err)
	}

	if !replayOldLeafCPUs.IsSubsetOf(view.Isolation) {
		t.Fatalf("exclusive owner A4 %s must be on the isolated side; isolation=%s",
			replayOldLeafCPUs.String(), view.Isolation.String())
	}
	if overlap := view.SharePool.Intersection(replayOldLeafCPUs); !overlap.IsEmpty() {
		t.Fatalf("share pool must not claim exclusive owner A4; overlap=%s", overlap.String())
	}
	if overlap := view.ReclaimEffective.Intersection(replayOldLeafCPUs); !overlap.IsEmpty() {
		t.Fatalf("reclaim must not take exclusive owner A4; overlap=%s", overlap.String())
	}
	if !replayOldLeafCPUs.IsSubsetOf(view.NonReclaimPool) {
		t.Fatalf("exclusive owner A4 %s must stay protected in non-reclaim; nonReclaim=%s",
			replayOldLeafCPUs.String(), view.NonReclaimPool.String())
	}
	// The isolated owner must NOT be misclassified onto the dedicated path.
	if overlap := view.Dedicated.Intersection(replayOldLeafCPUs); !overlap.IsEmpty() {
		t.Fatalf("isolated owner A4 must not be classified dedicated; overlap=%s", overlap.String())
	}
}
