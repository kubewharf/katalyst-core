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
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cpusetplugin "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/cpusettopology"
	topology "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
)

// This file is the live common-isolation owner transfer regression. It drives the
// REAL Manager.Apply -> REAL cpuset_topology plugin -> REAL coordinator -> in-memory
// hierarchy driver chain (fake IO), reusing the synthetic 96-CPU replay fixtures and
// helpers from shared_pool_relocation_replay_test.go and the historical AppliedView
// seed pattern from applied_owner_recovery_test.go.
//
// Two resolved live leaves relocate oldA {7,11,55,59} -> targetB {0,4,48,52} inside
// the named share pool. A common-named isolation owner (isolation-0, built from the
// existing commonstate.IsIsolationPool naming) exclusively holds oldA; the share/pending
// P68 envelope happens to contain oldA, so an unproven migration must never treat the
// exclusive owner's oldA as free. A proven round must actually verify/release the live
// leaves onto targetB and project oldA onto the isolation owner identity, not merely
// report a pool-only Full.

// xoIsoOwnerUID is the stable identity of the common-named isolation exclusive owner.
const xoIsoOwnerUID = "iso-owner-pod"

// xoIsoRel is the materialized cgroup rel the isolation owner exclusively holds oldA on.
const xoIsoRel = "primary/iso-owner"

// xoIsoIdentity is the real model.Isolation pool owner identity generated from the
// isolation-<n> OwnerPoolName prefix (commonstate.IsIsolationPool) plus the pod
// namespace/name carried on the allocation meta.
var xoIsoIdentity = model.CPUSetPoolIdentity{
	Kind:         model.CPUSetPoolKindIsolation,
	PodNamespace: "iso-ns",
	PodName:      "iso-owner",
}

// xoResolvedLivePods are the two resolved live leaves relocating oldA -> targetB.
var xoResolvedLivePods = []string{"live-a-pod", "live-b-pod"}

// xoUnprovenFetcher returns all three resolved owners in the cache-only snapshot
// (running CRI identity, resolved through the injected seam) but withholds the strict
// fresh snapshot entirely. The live migration and the exclusive owner therefore carry
// NO fresh lifecycle proof this round: each owner resolves from cache yet the strict
// fresh snapshot classifies it absent/retired rather than genuinely lifecycle-resolved.
type xoUnprovenFetcher struct {
	metapod.PodFetcherStub
}

func (f *xoUnprovenFetcher) GetPodListFromCache(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	running := func(cid string) v1.ContainerStatus {
		return v1.ContainerStatus{
			Name:        "main",
			Ready:       true,
			ContainerID: "containerd://" + cid,
			State:       v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		}
	}
	return []*v1.Pod{
		{ObjectMeta: metav1.ObjectMeta{UID: "live-a-pod"}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("live-a-cid")}}},
		{ObjectMeta: metav1.ObjectMeta{UID: "live-b-pod"}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("live-b-cid")}}},
		{ObjectMeta: metav1.ObjectMeta{UID: xoIsoOwnerUID}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("iso-cid")}}},
	}, nil
}

func (f *xoUnprovenFetcher) GetPodList(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return nil, nil
}

// xoMigrationUnprovenFetcher keeps the incumbent isolation owner proven in the strict
// fresh snapshot (Spec owner + matching CRI ID) while the two migrating live leaves are
// absent from strict fresh: only the migration proof is withheld, the hard owner stays
// authenticated.
type xoMigrationUnprovenFetcher struct {
	xoUnprovenFetcher
}

func (f *xoMigrationUnprovenFetcher) GetPodList(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return []*v1.Pod{xoProvenPod(xoIsoOwnerUID, "iso-cid")}, nil
}

// xoProvenFetcher returns the cache-only snapshot for all three resolved owners and a
// strict fresh snapshot whose Spec owner name + runtime CRI ID match the frozen proof,
// so the isolation owner and both live leaves resolve as genuine lifecycle-resolved.
type xoProvenFetcher struct {
	metapod.PodFetcherStub
}

func xoProvenPod(uid, cid string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(uid), Namespace: "iso-ns"},
		Spec: v1.PodSpec{Containers: []v1.Container{
			{Name: "main"},
		}},
		Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name:        "main",
			Ready:       true,
			ContainerID: "containerd://" + cid,
			State:       v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		}}},
	}
}

func (f *xoProvenFetcher) GetPodListFromCache(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	running := func(cid string) v1.ContainerStatus {
		return v1.ContainerStatus{
			Name:        "main",
			Ready:       true,
			ContainerID: "containerd://" + cid,
			State:       v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		}
	}
	return []*v1.Pod{
		{ObjectMeta: metav1.ObjectMeta{UID: "live-a-pod"}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("live-a-cid")}}},
		{ObjectMeta: metav1.ObjectMeta{UID: "live-b-pod"}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("live-b-cid")}}},
		{ObjectMeta: metav1.ObjectMeta{UID: xoIsoOwnerUID}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("iso-cid")}}},
	}, nil
}

func (f *xoProvenFetcher) GetPodList(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return []*v1.Pod{
		xoProvenPod("live-a-pod", "live-a-cid"),
		xoProvenPod("live-b-pod", "live-b-cid"),
		xoProvenPod(xoIsoOwnerUID, "iso-cid"),
	}, nil
}

// xoResolverCg embeds the replay cgroup client and supplies the optional CRI->rel seam
// for the two live leaves AND the materialized isolation owner leaf.
type xoResolverCg struct {
	*replayCgroupClient
}

// resolveContainerRel is test-only fixture logic for the synthetic hierarchy;
// it is passed explicitly to the test-only constructor and is not inferred from
// the fake cgroup client.
func (c *xoResolverCg) resolveContainerRel(podUID, _ string) (string, error) {
	switch podUID {
	case "live-a-pod":
		return "primary/live-a", nil
	case "live-b-pod":
		return "primary/live-b", nil
	case xoIsoOwnerUID:
		return xoIsoRel, nil
	}
	return "", fmt.Errorf("xo: no resolved rel for pod=%s", podUID)
}

// exclusiveTransferState builds the desired CPUPluginState:
//   - common-named isolation pool entry isolation-0 exclusively holding oldA (built
//     from the commonstate.IsIsolationPool prefix, not the dedicated path);
//   - a real isolation owner pod entry whose OwnerPoolName prefix is isolation-0, which
//     the validated view turns into the model.Isolation exclusive-owner PoolOwner;
//   - named share pool pool-a owning the P68 pending envelope (which contains oldA);
//   - two resolved live leaves relocating oldA -> targetB inside pool-a.
func exclusiveTransferState() cpustate.ReadonlyState {
	state := cpustate.NewCPUPluginState(nil)

	// Common-named isolation pool entry: structural view.Isolation = oldA.
	isolationMeta := commonstate.GenerateGenericPoolAllocationMeta("isolation-0")
	_ = state.SetAllocationInfo("isolation-0", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   isolationMeta,
		AllocationResult: replayOldLeafCPUs.Clone(),
	})

	// Real isolation exclusive-owner pod entry: OwnerPoolName isolation-0 drives the
	// model.Isolation PoolOwner identity (namespace/name) in rebuildDesiredPoolOwners.
	_ = state.SetAllocationInfo(xoIsoOwnerUID, "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        xoIsoOwnerUID,
			PodNamespace:  "iso-ns",
			PodName:       "iso-owner",
			ContainerName: "main",
			OwnerPoolName: "isolation-0",
			QoSLevel:      commonstate.PoolNameShare,
			Annotations: map[string]string{
				"qosLevel": commonstate.PoolNameShare,
			},
		},
		AllocationResult: replayOldLeafCPUs.Clone(),
	})

	// Named share pool owns the P68 pending envelope; it structurally contains oldA,
	// which must NOT be borrowed as free while the isolation owner is unproven.
	shareMeta := commonstate.GenerateGenericPoolAllocationMeta("pool-a")
	shareMeta.QoSLevel = commonstate.PoolNameShare
	_ = state.SetAllocationInfo("pool-a", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   shareMeta,
		AllocationResult: replayPending68.Clone(),
	})

	// Two resolved live leaves relocate oldA -> targetB inside pool-a.
	for _, podUID := range xoResolvedLivePods {
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
	return state
}

// exclusiveHistoricalAppliedView seeds the manager-owned m.appliedView with the previous
// real applied authority: R28 reclaim envelope, per-rel cset proofs bound to stable
// device/inode identities, and a pool projection that maps oldA onto the isolation
// owner identity. This is a legitimate historical input fixture; no ConvergenceReport or
// converged result is fabricated.
func exclusiveHistoricalAppliedView() *model.AppliedView {
	view := &model.AppliedView{
		CPUSetPartitionView: model.NewCPUSetPartitionView(),
		Level:               model.AppliedViewLevelFull,
		CPUSetByRel: map[string]machine.CPUSet{
			"primary":        replayOldAppliedReclaim28.Clone(),
			"primary/live-a": replayOldLeafCPUs.Clone(),
			"primary/live-b": replayOldLeafCPUs.Clone(),
			xoIsoRel:         replayOldLeafCPUs.Clone(),
		},
		RelProofByRel: map[string]model.CgroupRelProof{
			"primary":        {Device: 1, Inode: 100, CPUSet: replayOldAppliedReclaim28.Clone()},
			"primary/live-a": {Device: 1, Inode: 201, CPUSet: replayOldLeafCPUs.Clone()},
			"primary/live-b": {Device: 1, Inode: 202, CPUSet: replayOldLeafCPUs.Clone()},
			xoIsoRel:         {Device: 1, Inode: 203, CPUSet: replayOldLeafCPUs.Clone()},
		},
		PoolProjection: model.NewAppliedPoolProjection(),
	}
	view.ReclaimEffective = replayOldAppliedReclaim28.Clone()
	view.NonReclaimPool = all96CPUs().Difference(replayOldAppliedReclaim28)
	view.PoolProjection.CPUSetByIdentity[xoIsoIdentity] = replayOldLeafCPUs.Clone()
	return view
}

// xoSeedExclusiveDriver starts from the shared replay fixture (primary=R28, live leaves
// on oldA) and additionally materializes the isolation owner leaf still holding oldA.
func xoSeedExclusiveDriver() *replayDriver {
	d := newReplayDriver()
	seedReplayFixture(d)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nodes[xoIsoRel] = topology.EntryState{
		Rel:            xoIsoRel,
		Identity:       topology.CgroupIdentity{Device: 1, Inode: 203},
		CPUs:           replayOldLeafCPUs.Clone(),
		ConfiguredCPUs: replayOldLeafCPUs.Clone(),
	}
	d.children["primary"] = append(d.children["primary"], "iso-owner")
	return d
}

// xoBuildRound wires a fresh real plugin + recording wrapper + manager over the given
// driver on the shared cgroup seam; the pod fetcher is attached on the handler context.
func xoBuildRound(driver *replayDriver) (*recordingTopology, *Manager) {
	cg := &xoResolverCg{replayCgroupClient: &replayCgroupClient{driver: driver}}
	// Explicit test-only resolution binds CRI identities to the in-memory cgroup
	// tree; production wiring never passes this override.
	realPlugin := cpusetplugin.NewCPUSetTopologyPluginWithDependencies(replayCoreConf(), func() client.CgroupClient {
		return cg
	}, cg.resolveContainerRel)
	rec := &recordingTopology{TopologyPlugin: realPlugin.(bulkheadapi.TopologyPlugin)}
	m := &Manager{plugins: []bulkheadapi.Plugin{rec}}
	return rec, m
}

func xoAdmissionCtx(fetcher metapod.PodFetcher) cpusetutil.CPUSetAdjustmentHandlerCtx {
	in := replayRoundCtx()
	in.Mode = cpusetutil.CPUSetAdjustmentModeAdmission
	in.CoreConf = replayCoreConf()
	in.State = exclusiveTransferState()
	in.MetaServer = &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}
	return in
}

// projectionCPUsFor returns the projected CPUs under an identity, and whether present.
func projectionCPUsFor(view *model.AppliedView, id model.CPUSetPoolIdentity) (machine.CPUSet, bool) {
	if view == nil {
		return machine.NewCPUSet(), false
	}
	cpus, ok := view.PoolProjection.CPUSetByIdentity[id]
	return cpus, ok
}

// xoRunUnprovenAdmission seeds the historical authority, drives one real Admission Apply
// with the given strict-fresh fetcher, and returns the observed facts for assertions/logs.
func xoRunUnprovenAdmission(t *testing.T, fetcher metapod.PodFetcher) (
	applyErr error, rec *recordingTopology, m *Manager, driver *replayDriver,
) {
	t.Helper()
	driver = xoSeedExclusiveDriver()
	rec, m = xoBuildRound(driver)

	// Seed the historical applied authority the manager owns before this admission.
	m.appliedView = exclusiveHistoricalAppliedView()
	m.appliedViewRevision = 1
	m.appliedViewValidForPeriodical = true

	in := xoAdmissionCtx(fetcher)
	_, applyErr = m.Apply(context.Background(), in)
	return applyErr, rec, m, driver
}

// xoLogUnproven prints the real error, level, projection and driver values so a RED can
// be located against production code rather than guessed from the fixture.
func xoLogUnproven(t *testing.T, label string, applyErr error, rec *recordingTopology, m *Manager, driver *replayDriver) {
	t.Helper()
	reads, lists, writes, failedAt, _ := driver.snapshot()
	liveA := driver.leafCPUs("primary/live-a")
	liveB := driver.leafCPUs("primary/live-b")
	iso := driver.leafCPUs(xoIsoRel)
	primary := driver.primaryCPUs()
	t.Logf("%s: applyErr=%v level=%v note=%q reads=%d list=%d writes=%d failedAt=%d published=%t",
		label, applyErr, rec.level, rec.note, reads, lists, writes, failedAt, m.appliedViewValidForPeriodical)
	t.Logf("%s physical: primary=%s liveA=%s liveB=%s iso=%s", label, primary.String(), liveA.String(), liveB.String(), iso.String())
	if m.appliedView != nil {
		t.Logf("%s appliedView: level=%v isolation=%s nonReclaim=%s reclaim=%s projection=%v uncovered=%s ambiguous=%s",
			label, m.appliedView.Level, m.appliedView.Isolation.String(), m.appliedView.NonReclaimPool.String(),
			m.appliedView.ReclaimEffective.String(), m.appliedView.PoolProjection.CPUSetByIdentity,
			m.appliedView.PoolProjection.UncoveredCPUs.String(), m.appliedView.PoolProjection.AmbiguousCPUs.String())
	}
}

// xoAssertShareExcludesOldA fails if any share owner in the applied projection claims
// exclusive oldA, or oldA folds into reclaim.
func xoAssertShareExcludesOldA(t *testing.T, label string, view *model.AppliedView) {
	t.Helper()
	if view == nil {
		return
	}
	for identity, cpus := range view.PoolProjection.CPUSetByIdentity {
		if identity.Kind == model.CPUSetPoolKindShare &&
			!cpus.Intersection(replayOldLeafCPUs).IsEmpty() {
			t.Fatalf("%s: share owner %v projected exclusive oldA; share=%s overlap=%s",
				label, identity, cpus.String(), cpus.Intersection(replayOldLeafCPUs).String())
		}
	}
	if overlap := view.ReclaimEffective.Intersection(replayOldLeafCPUs); !overlap.IsEmpty() {
		t.Fatalf("%s: oldA %s folded into reclaim; overlap=%s",
			label, replayOldLeafCPUs.String(), overlap.String())
	}
}

// TestExclusiveOwnerTransferIncumbentProtectsOldA keeps the incumbent isolation owner
// proven in the strict fresh snapshot while the two migrating live leaves are unproven.
// The share/pending P68 envelope contains oldA, yet oldA stays physically on the exclusive
// isolation leaf, projects onto the isolation owner identity, and is never claimed by the
// share owner; this is the safePartial/normal protection of the incumbent's oldA.
func TestExclusiveOwnerTransferIncumbentProtectsOldA(t *testing.T) {
	t.Parallel()
	applyErr, rec, m, driver := xoRunUnprovenAdmission(t, &xoMigrationUnprovenFetcher{})
	label := "incumbent"
	xoLogUnproven(t, label, applyErr, rec, m, driver)

	iso := driver.leafCPUs(xoIsoRel)
	if !iso.Equals(replayOldLeafCPUs) {
		t.Fatalf("exclusive isolation leaf = %s, must retain oldA %s", iso.String(), replayOldLeafCPUs.String())
	}
	if m.appliedView == nil {
		t.Fatalf("%s: expected a retained applied view", label)
	}
	isoProjected, ok := projectionCPUsFor(m.appliedView, xoIsoIdentity)
	if !ok {
		t.Fatalf("%s: incumbent isolation owner %v missing from projection; projection=%v",
			label, xoIsoIdentity, m.appliedView.PoolProjection.CPUSetByIdentity)
	}
	if !isoProjected.Equals(replayOldLeafCPUs) {
		t.Fatalf("%s: isolation projection = %s, want oldA %s",
			label, isoProjected.String(), replayOldLeafCPUs.String())
	}
	xoAssertShareExcludesOldA(t, label, m.appliedView)
}

// xoSeedMissingLeafDriver seeds the fixture but removes one controlled live leaf from the
// physical hierarchy, so it can neither be read back nor proven against the final snapshot.
func xoSeedMissingLeafDriver() *replayDriver {
	d := xoSeedExclusiveDriver()
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.nodes, "primary/live-a")
	children := make([]string, 0, len(d.children["primary"]))
	for _, c := range d.children["primary"] {
		if c != "live-a" {
			children = append(children, c)
		}
	}
	d.children["primary"] = children
	return d
}

// TestExclusiveOwnerTransferUnverifiableLeafMustNotPublishPoolOnlyFull is the fail-open
// negative: a controlled live leaf that is physically missing/unverifiable must not let a
// pool-only projection misjudge Full. The chain must fail closed (apply error and/or no
// published Full periodical view) instead of reporting convergence from pool coverage alone.
func TestExclusiveOwnerTransferUnverifiableLeafMustNotPublishPoolOnlyFull(t *testing.T) {
	t.Parallel()
	driver := xoSeedMissingLeafDriver()
	rec, m := xoBuildRound(driver)
	m.appliedView = exclusiveHistoricalAppliedView()
	m.appliedViewRevision = 1
	m.appliedViewValidForPeriodical = true

	in := xoAdmissionCtx(&xoProvenFetcher{})
	_, applyErr := m.Apply(context.Background(), in)
	reads, lists, writes, failedAt, _ := driver.snapshot()

	iso := driver.leafCPUs(xoIsoRel)
	t.Logf("missingLeaf: applyErr=%v level=%v note=%q reads=%d list=%d writes=%d failedAt=%d published=%t iso=%s",
		applyErr, rec.level, rec.note, reads, lists, writes, failedAt, m.appliedViewValidForPeriodical, iso.String())
	if m.appliedView != nil {
		t.Logf("missingLeaf appliedView: level=%v projection=%v",
			m.appliedView.Level, m.appliedView.PoolProjection.CPUSetByIdentity)
	}

	// Fail closed: no Full periodical view may be published when a controlled leaf cannot
	// be physically verified; pool coverage alone must not misjudge convergence.
	if m.appliedViewValidForPeriodical && m.appliedView != nil &&
		m.appliedView.Level == model.AppliedViewLevelFull {
		t.Fatalf("missingLeaf: published pool-only Full despite unverifiable controlled leaf; "+
			"level=%v note=%q projection=%v applyErr=%v",
			rec.level, rec.note, m.appliedView.PoolProjection.CPUSetByIdentity, applyErr)
	}
	// The incumbent isolation oldA stays physically protected through the failed round.
	if !iso.Equals(replayOldLeafCPUs) {
		t.Fatalf("exclusive isolation leaf = %s, must retain oldA %s", iso.String(), replayOldLeafCPUs.String())
	}
}

// TestExclusiveOwnerTransferProvenFullVerifiesLeafBAndProjection drives the proven
// legal Full round: strict fresh proofs match the frozen identities, the chain actually
// verifies/releases both live leaves onto targetB, keeps the isolation owner on oldA, and
// projects oldA onto the isolation owner identity while the share owner excludes oldA.
func TestExclusiveOwnerTransferProvenFullVerifiesLeafBAndProjection(t *testing.T) {
	t.Parallel()
	driver := xoSeedExclusiveDriver()
	rec, m := xoBuildRound(driver)

	in := xoAdmissionCtx(&xoProvenFetcher{})
	_, applyErr := m.Apply(context.Background(), in)
	reads, lists, writes, failedAt, _ := driver.snapshot()

	liveA := driver.leafCPUs("primary/live-a")
	liveB := driver.leafCPUs("primary/live-b")
	iso := driver.leafCPUs(xoIsoRel)
	primary := driver.primaryCPUs()

	t.Logf("proven: applyErr=%v level=%v note=%q reads=%d list=%d writes=%d failedAt=%d published=%t",
		applyErr, rec.level, rec.note, reads, lists, writes, failedAt, m.appliedViewValidForPeriodical)
	t.Logf("proven physical: primary=%s liveA=%s liveB=%s iso=%s", primary.String(), liveA.String(), liveB.String(), iso.String())
	if m.appliedView != nil {
		t.Logf("proven appliedView: level=%v isolation=%s nonReclaim=%s reclaim=%s projection=%v uncovered=%s ambiguous=%s",
			m.appliedView.Level, m.appliedView.Isolation.String(), m.appliedView.NonReclaimPool.String(),
			m.appliedView.ReclaimEffective.String(), m.appliedView.PoolProjection.CPUSetByIdentity,
			m.appliedView.PoolProjection.UncoveredCPUs.String(), m.appliedView.PoolProjection.AmbiguousCPUs.String())
	}

	if applyErr != nil {
		t.Fatalf("proven legal Full Apply error = %v, want nil", applyErr)
	}
	if rec.level != bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("proven round level = %v, want Full", rec.level)
	}
	if !m.appliedViewValidForPeriodical || m.appliedView == nil {
		t.Fatalf("proven round did not publish a Full applied view")
	}
	// The chain must have performed real driver writes rather than reporting Full from
	// pool coverage alone.
	if writes == 0 {
		t.Fatalf("proven Full performed zero physical writes; leafB not actually enforced")
	}
	// Both relocated leaves must actually land on targetB (real driver write + proof),
	// releasing oldA from the live leaves.
	if !liveA.Equals(replayTargetLeafB4) {
		t.Fatalf("live-a leaf = %s, want targetB %s", liveA.String(), replayTargetLeafB4.String())
	}
	if !liveB.Equals(replayTargetLeafB4) {
		t.Fatalf("live-b leaf = %s, want targetB %s", liveB.String(), replayTargetLeafB4.String())
	}
	// oldA is released from the relocated leaves: none of it remains on live-a/live-b.
	if leftover := liveA.Union(liveB).Intersection(replayOldLeafCPUs); !leftover.IsEmpty() {
		t.Fatalf("oldA not released from relocated leaves; leftover=%s on liveA=%s liveB=%s",
			leftover.String(), liveA.String(), liveB.String())
	}
	// The exclusive isolation owner stays on oldA; it is not released by the relocation.
	if !iso.Equals(replayOldLeafCPUs) {
		t.Fatalf("isolation owner leaf = %s, must stay oldA %s", iso.String(), replayOldLeafCPUs.String())
	}
	// Projection true mapping: oldA projects onto the isolation owner identity exactly,
	// and no share owner claims oldA.
	isoProjected, ok := projectionCPUsFor(m.appliedView, xoIsoIdentity)
	if !ok {
		t.Fatalf("proven projection missing isolation owner %v; projection=%v",
			xoIsoIdentity, m.appliedView.PoolProjection.CPUSetByIdentity)
	}
	if !isoProjected.Equals(replayOldLeafCPUs) {
		t.Fatalf("isolation projection = %s, want oldA %s", isoProjected.String(), replayOldLeafCPUs.String())
	}
	for identity, cpus := range m.appliedView.PoolProjection.CPUSetByIdentity {
		if identity.Kind == model.CPUSetPoolKindShare &&
			!cpus.Intersection(replayOldLeafCPUs).IsEmpty() {
			t.Fatalf("share owner %v projected exclusive oldA; overlap=%s",
				identity, cpus.Intersection(replayOldLeafCPUs).String())
		}
	}
	// oldA remains on the isolated/non-reclaim side and never enters reclaim.
	if !replayOldLeafCPUs.IsSubsetOf(m.appliedView.Isolation) &&
		!replayOldLeafCPUs.IsSubsetOf(m.appliedView.NonReclaimPool) {
		t.Fatalf("oldA %s not protected in isolation/non-reclaim; isolation=%s nonReclaim=%s",
			replayOldLeafCPUs.String(), m.appliedView.Isolation.String(), m.appliedView.NonReclaimPool.String())
	}
	if overlap := m.appliedView.ReclaimEffective.Intersection(replayOldLeafCPUs); !overlap.IsEmpty() {
		t.Fatalf("oldA %s must not enter proven reclaim; overlap=%s", replayOldLeafCPUs.String(), overlap.String())
	}
}
