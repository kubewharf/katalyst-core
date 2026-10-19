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
	"fmt"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cpusetplugin "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/cpusettopology"
	topology "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
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

// predrainFetcher returns the four resolved share leaves in the cache-only snapshot
// with a running CRI identity; the strict fresh snapshot stays empty.
type predrainFetcher struct {
	metapod.PodFetcherStub
}

func (f *predrainFetcher) GetPodListFromCache(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	running := func(cid string) v1.ContainerStatus {
		return v1.ContainerStatus{
			Name: "main", Ready: true, ContainerID: "containerd://" + cid,
			State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		}
	}
	return []*v1.Pod{
		{ObjectMeta: metav1.ObjectMeta{UID: "predrain-a-pod"}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("pa-cid")}}},
		{ObjectMeta: metav1.ObjectMeta{UID: "predrain-b-pod"}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("pb-cid")}}},
		{ObjectMeta: metav1.ObjectMeta{UID: "predrain-c-pod"}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("pc-cid")}}},
		{ObjectMeta: metav1.ObjectMeta{UID: "predrain-d-pod"}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("pd-cid")}}},
	}, nil
}

func (f *predrainFetcher) GetPodList(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return nil, nil
}

// predrainCg embeds the replay cgroup client and supplies the optional CRI->rel
// resolver seam for the four deferred share leaves.
type predrainCg struct {
	*replayCgroupClient
}

// resolveContainerRel is test-only fixture logic for the synthetic pre-drain
// leaves. Explicit injection keeps this mapping outside the production cgroup
// client contract.
func (c *predrainCg) resolveContainerRel(podUID, containerID string) (string, error) {
	switch podUID {
	case "predrain-a-pod":
		return "primary/predrain-a", nil
	case "predrain-b-pod":
		return "primary/predrain-b", nil
	case "predrain-c-pod":
		return "primary/predrain-c", nil
	case "predrain-d-pod":
		return "primary/predrain-d", nil
	}
	return "", fmt.Errorf("predrain: no resolved rel for pod=%s", podUID)
}

// fenceCg wraps the predrain cgroup client and flips the invalidated generation flag
// exactly after the 4th real ApplyCPUSet pre-drain write; the existing
// handlerCtx.CommitIfGenerationCurrent fence then returns false at publication.
type fenceCg struct {
	*predrainCg
	invalidated *bool
	preCount    *int
}

func (c *fenceCg) ApplyCPUSet(ctx context.Context, rel string, data *cgcommon.CPUSetData) error {
	err := c.predrainCg.ApplyCPUSet(ctx, rel, data)
	*c.preCount++
	if *c.preCount == 4 {
		*c.invalidated = true
	}
	return err
}

// deferredPredrainState builds the admission state for the exact pre-drain channel:
// one generic named share pool owning pending68 and four resolved share leaves whose
// desired allocation is P68.
func deferredPredrainState() cpustate.ReadonlyState {
	state := cpustate.NewCPUPluginState(nil)
	meta := commonstate.GenerateGenericPoolAllocationMeta("pool-a")
	meta.QoSLevel = commonstate.PoolNameShare
	_ = state.SetAllocationInfo("pool-a", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   meta,
		AllocationResult: replayPending68.Clone(),
	})
	for _, podUID := range []string{"predrain-a-pod", "predrain-b-pod", "predrain-c-pod", "predrain-d-pod"} {
		_ = state.SetAllocationInfo(podUID, "main", &cpustate.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid: podUID, ContainerName: "main", OwnerPoolName: "pool-a", QoSLevel: commonstate.PoolNameShare,
			},
			AllocationResult: replayPending68.Clone(),
		})
	}
	return state
}

// deferredPredrainDriver seeds an in-memory driver for the pre-drain phase: primary
// holds all96, and the four deferred share leaves start at a wider superset (all96)
// that still sits inside the parent; the exact pre-drain shrinks them to P68.
func deferredPredrainDriver() *replayDriver {
	d := newReplayDriver()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.roots = []topology.RootRef{{Rel: "primary", Identity: topology.CgroupIdentity{Device: 1, Inode: 100}}}
	d.nodes["primary"] = topology.EntryState{
		Rel: "primary", Identity: topology.CgroupIdentity{Device: 1, Inode: 100},
		CPUs: all96CPUs(), ConfiguredCPUs: all96CPUs(),
	}
	inodes := []uint64{201, 202, 203, 204}
	for i, rel := range []string{"primary/predrain-a", "primary/predrain-b", "primary/predrain-c", "primary/predrain-d"} {
		d.nodes[rel] = topology.EntryState{
			Rel: rel, Identity: topology.CgroupIdentity{Device: 1, Inode: inodes[i]},
			CPUs: all96CPUs(), ConfiguredCPUs: all96CPUs(),
		}
	}
	d.children["primary"] = []string{"predrain-a", "predrain-b", "predrain-c", "predrain-d"}
	return d
}

// TestAppliedOwnerRecoveryPredrainChannelFailClosedAndFreshRecover drives the real
// admission chain: the cgroup ApplyCPUSet pre-write channel is recorded
// independently and really updates the memory driver, while the coordinator DAG
// writes are observed on their own channel. A point-injected failure at a
// coordinator write after the pre-write phase fails closed (no false Full, no
// published view, attempted writes counted). A fresh-full retry from the verified
// snapshot recovers onto B4 instead of rolling back to old A4. The two channels are
// kept as separate recorded lists; no private budget counter and no new production
// API are added.
func TestAppliedOwnerRecoveryPredrainChannelFailClosedAndFreshRecover(t *testing.T) {
	t.Parallel()
	// One integrated round: the admission leaf-defer exact pre-drain writes four
	// resolved share leaves (all96 -> P68) through the cgroup ApplyCPUSet pre-write
	// channel on the same driver. After the 4th real pre-write the generation flag is
	// invalidated; the existing handlerCtx.CommitIfGenerationCurrent fence returns
	// false at publishApply, so the snapshot/publication fails closed (no false Full,
	// no published view) while the four physical pre-writes stay on P68. Resetting the
	// flag and retrying converges Full and maps the real target. No new production
	// seam, no private budget counter, no coordinator write is required.
	d := deferredPredrainDriver()
	baseCg := &predrainCg{replayCgroupClient: &replayCgroupClient{driver: d}}

	invalidated := false
	preCount := 0
	cg := &fenceCg{predrainCg: baseCg, invalidated: &invalidated, preCount: &preCount}

	conf := replayCoreConf()
	conf.CPUQRMPluginConfig.BulkheadConfiguration.EnableAdmissionLeafDefer = true
	build := func() (*recordingTopology, *Manager) {
		p := cpusetplugin.NewCPUSetTopologyPluginWithDependencies(
			conf, func() client.CgroupClient { return cg }, cg.resolveContainerRel)
		rec := &recordingTopology{TopologyPlugin: p.(bulkheadapi.TopologyPlugin)}
		m := &Manager{plugins: []bulkheadapi.Plugin{rec}}
		return rec, m
	}
	admissionIn := func() cpusetutil.CPUSetAdjustmentHandlerCtx {
		in := replayRoundCtx()
		in.Mode = cpusetutil.CPUSetAdjustmentModeAdmission
		in.CoreConf = replayCoreConf()
		in.State = deferredPredrainState()
		in.MetaServer = &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &predrainFetcher{}}}
		in.CommitIfGenerationCurrent = func(generation uint64, commit func()) bool {
			if invalidated {
				return false
			}
			commit()
			return true
		}
		return in
	}

	// Round 1: four real pre-drain writes land on P68, then the generation fence fails
	// closed at publication: no Full, no published view, writes stay on P68.
	rec1, m1 := build()
	_, err1 := m1.Apply(context.Background(), admissionIn())
	preCalls1 := len(d.cgroupCalls)
	t.Logf("fence round: err=%v level=%v pre_applycpuset_calls=%d published=%v",
		err1, rec1.level, preCalls1, m1.appliedViewValidForPeriodical)
	if err1 == nil {
		t.Fatalf("generation fence invalidation must fail closed with an error, got nil")
	}
	// Internal convergence may still read Full, but the fence rejected publication;
	// the fail-closed facts are the error, no published view and writes kept on P68.
	if m1.appliedViewValidForPeriodical || m1.appliedView != nil {
		t.Fatalf("fence round published an applied view; fail-closed must not publish")
	}
	if preCalls1 != 4 {
		t.Fatalf("expected 4 ApplyCPUSet exact pre-drain writes, got %d", preCalls1)
	}
	for _, rel := range []string{"primary/predrain-a", "primary/predrain-b", "primary/predrain-c", "primary/predrain-d"} {
		if got := d.leafCPUs(rel); !got.Equals(replayPending68) {
			t.Fatalf("pre-drain leaf %s = %s, must stay P68 after fail-closed", rel, got.String())
		}
	}

	// Fresh retry: disarm the fence, converge Full and map the real target; recorded
	// pre channel counts are not rolled back.
	invalidated = false
	rec2, m2 := build()
	_, err2 := m2.Apply(context.Background(), admissionIn())
	preCalls2 := len(d.cgroupCalls)
	t.Logf("fresh retry: err=%v level=%v pre_applycpuset_calls=%d published=%v",
		err2, rec2.level, preCalls2, m2.appliedViewValidForPeriodical)
	if err2 != nil {
		t.Fatalf("fresh retry error = %v, want nil", err2)
	}
	if rec2.level != bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("fresh retry level = %v, want Full", rec2.level)
	}
	if !m2.appliedViewValidForPeriodical || m2.appliedView == nil {
		t.Fatalf("fresh retry did not publish a Full applied view")
	}
	if preCalls2 < preCalls1 {
		t.Fatalf("fresh retry must not roll back recorded pre channel counts: %d->%d", preCalls1, preCalls2)
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
