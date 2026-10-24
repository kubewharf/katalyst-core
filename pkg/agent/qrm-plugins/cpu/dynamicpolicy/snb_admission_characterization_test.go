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

package dynamicpolicy

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// snbAdmissionTestConfig captures the experiment configuration specified for
// SNB admission characterization. All knobs are set once on the immutable
// dynamicConf and never mutated during a single test case.
type snbAdmissionTestConfig struct {
	hardPartition          bool
	initialRampUpRatio     float64
	bypassCpusetAdjustment bool
	allowOverlap           bool
	maxReclaimedUtil       float64
	enableReclaim          bool
}

// approvedSNBAdmissionConfig is the approved experiment matrix:
// HardPartition=true / initial=0.05 / bypass=true / overlap=false / reclaim=0.3.
func defaultSNBAdmissionTestConfig() *snbAdmissionTestConfig {
	return &snbAdmissionTestConfig{
		hardPartition:          true,
		initialRampUpRatio:     0.05,
		bypassCpusetAdjustment: true,
		allowOverlap:           false,
		maxReclaimedUtil:       0.3,
		enableReclaim:          true,
	}
}

// Small topology: 16 CPUs / 2 sockets / 4 NUMA. NUMA0 = {0,1,8,9}. After
// reserve ({0,2}) and the reclaim pool ({1,3,9,11}), NUMA0 has exactly ONE
// allocatable CPU for share-NUMA0. This is the capacity-control topology: it
// can admit one SNB pod on NUMA0 and must fail closed on a second one.
const (
	smallCPUNum    = 16
	smallSocketNum = 2
	smallNUMANum   = 4
)

// Large topology: 64 CPUs / 1 socket / 2 NUMA. NUMA0 = {0-15,32-47} (32 CPUs),
// NUMA1 = {16-31,48-63} (32 CPUs). Per-NUMA capacity math after reserve (2
// HT-balanced CPUs) and reclaim (a handful of CPUs) leaves well over 10 free
// CPUs on each NUMA, which is enough to characterize successful 2nd/3rd
// same-NUMA members, different-NUMA pools, mixed ordinary+SNB membership, and
// restart/reload without tripping the physical capacity bound.
const (
	largeCPUNum    = 64
	largeSocketNum = 1
	largeNUMANum   = 2
)

// setupSNBAdmissionPolicy builds a CPU dynamic policy on the requested topology
// with the experiment config applied. If stateDir is non-empty the checkpoint
// in that directory is reused (restart/reload simulation); otherwise a fresh
// temp dir is created. It returns the policy, the state dir, and a cleanup func.
func setupSNBAdmissionPolicy(t *testing.T, cpuNum, socketNum, numaNum int, cfg *snbAdmissionTestConfig, stateDir string) (*DynamicPolicy, string, func()) {
	cpuTopology, err := machine.GenerateDummyCPUTopology(cpuNum, socketNum, numaNum)
	require.NoError(t, err)

	tmpDir := stateDir
	if tmpDir == "" {
		tmpDir, err = ioutil.TempDir("", "checkpoint-snb-admission")
		require.NoError(t, err)
	}

	p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.QRMPluginConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = cfg.hardPartition
	dyn.QRMPluginConfiguration.CPUPluginConfiguration.InitialRampUpReclaimCPUSetRatio = cfg.initialRampUpRatio
	dyn.QRMPluginConfiguration.CPUPluginConfiguration.EnableBypassCPUSetAdjustment = cfg.bypassCpusetAdjustment
	dyn.ReclaimedResourceConfiguration.CPUHeadroomConfiguration.MaxReclaimedCoreUtilization = cfg.maxReclaimedUtil
	p.state.SetAllowSharedCoresOverlapReclaimedCores(cfg.allowOverlap, false)
	dyn.EnableReclaim = cfg.enableReclaim

	p.config.enableCPUAdvisor = true
	p.advisor.advisorClient = &advisorPoolTestClient{}

	require.NoError(t, p.initReservePool())
	require.NoError(t, p.initReclaimPool())

	p.stopCh = make(chan struct{})

	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	return p, tmpDir, cleanup
}

func setupSNBSmall(t *testing.T, cfg *snbAdmissionTestConfig) (*DynamicPolicy, func()) {
	p, _, cleanup := setupSNBAdmissionPolicy(t, smallCPUNum, smallSocketNum, smallNUMANum, cfg, "")
	return p, cleanup
}

func setupSNBLarge(t *testing.T, cfg *snbAdmissionTestConfig) (*DynamicPolicy, func()) {
	p, _, cleanup := setupSNBAdmissionPolicy(t, largeCPUNum, largeSocketNum, largeNUMANum, cfg, "")
	return p, cleanup
}

// makeSNBResourceRequest builds a ResourceRequest for an SNB pod.
func makeSNBResourceRequest(podUID, podName, containerName string, cpuReq float64, numaNode uint64) *pluginapi.ResourceRequest {
	return &pluginapi.ResourceRequest{
		PodUid:        podUID,
		PodNamespace:  "default",
		PodName:       podName,
		ContainerName: containerName,
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): cpuReq,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
		},
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{numaNode},
			Preferred: true,
		},
	}
}

// makeOrdinaryControlResourceRequest builds a ResourceRequest for an ordinary
// (non-SNB) shared-cores pod that serves as the control baseline.
func makeOrdinaryControlResourceRequest(podUID, podName, containerName string, cpuReq float64) *pluginapi.ResourceRequest {
	return &pluginapi.ResourceRequest{
		PodUid:        podUID,
		PodNamespace:  "default",
		PodName:       podName,
		ContainerName: containerName,
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): cpuReq,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}
}

// requireSNBPodResolved asserts the allocation info is an SNB pod whose
// canonical pool resolves to the expected per-NUMA binding pool name. Ramp-up
// SNB pods intentionally leave OwnerPoolName empty, so we assert the canonical
// resolution and metadata rather than PoolNameShare.
func requireSNBPodResolved(t *testing.T, info *state.AllocationInfo, poolName string) {
	require.NotNil(t, info)
	require.True(t, info.CheckSharedNUMABinding(), "pod must be shared-NUMA-binding")
	resolved, err := info.GetSpecifiedNUMABindingPoolName()
	require.NoError(t, err)
	require.Equal(t, poolName, resolved, "pod must resolve to its canonical SNB pool")
}

// =============================================================================
// Capacity-control on the small (16/2/4) topology.
// =============================================================================

// TestSNBAdmissionSequence_SmallCapacityControlSecondSameNUMAFailsClosed proves
// the owner-accurate pool metadata removes the spurious cross-NUMA error while
// the physical capacity invariant still fails closed: on the small topology
// NUMA0 has exactly one allocatable CPU, so a second same-NUMA SNB pod must
// fail with insufficient capacity, NOT with "cross NUMA".
func TestSNBAdmissionSequence_SmallCapacityControlSecondSameNUMAFailsClosed(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBSmall(t, cfg)
	defer cleanup()

	// First same-NUMA SNB pod admits.
	_, err := p.Allocate(context.Background(), makeSNBResourceRequest("cap-1", "cap-1", "main", 1.0, 0))
	require.NoError(t, err)

	// The fake pool entry now carries owner-accurate SNB identity.
	fake := p.state.GetAllocationInfo("share-NUMA0", commonstate.FakedContainerName)
	require.NotNil(t, fake)
	require.True(t, fake.CheckSharedNUMABinding(), "fake share-NUMA0 entry must retain SNB identity")

	// Second same-NUMA pod: physically no free CPU left on NUMA0.
	_, err = p.Allocate(context.Background(), makeSNBResourceRequest("cap-2", "cap-2", "main", 1.0, 0))
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "insufficient capacity",
		"second same-NUMA pod must fail on physical capacity, not cross-NUMA: %v", err)
	require.NotContains(t, strings.ToLower(err.Error()), "cross numa",
		"the owner-inaccurate cross-NUMA error must be gone: %v", err)
}

// =============================================================================
// Successful multi-member semantics on the large (64/1/2) topology.
// =============================================================================

// TestSNBAdmissionSequence_FirstSecondThirdSameNUMA verifies 1st, 2nd and 3rd
// same-NUMA SNB pods all admit and share one canonical SNB pool, whose fake
// entry carries owner-accurate identity.
func TestSNBAdmissionSequence_FirstSecondThirdSameNUMA(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	for i := 0; i < 3; i++ {
		uid := fmt.Sprintf("snb-numa0-%d", i)
		_, err := p.Allocate(context.Background(), makeSNBResourceRequest(uid, uid, "main", 1.0, 0))
		require.NoError(t, err, "SNB pod %d must admit", i)
	}

	for i := 0; i < 3; i++ {
		uid := fmt.Sprintf("snb-numa0-%d", i)
		info := p.state.GetAllocationInfo(uid, "main")
		requireSNBPodResolved(t, info, "share-NUMA0")
	}

	fake := p.state.GetAllocationInfo("share-NUMA0", commonstate.FakedContainerName)
	require.NotNil(t, fake)
	require.True(t, fake.CheckSharedNUMABinding(), "fake share-NUMA0 entry must be owner-accurate")
	require.NotEmpty(t, fake.TopologyAwareAssignments)
}

// TestSNBAdmissionSequence_NoKeyOverwrite verifies distinct UIDs under the same
// pool do not overwrite each other's state entries.
func TestSNBAdmissionSequence_NoKeyOverwrite(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	uids := []string{"snb-pod-a", "snb-pod-b", "snb-pod-c"}
	for _, uid := range uids {
		_, err := p.Allocate(context.Background(), makeSNBResourceRequest(uid, uid, "main", 1.0, 0))
		require.NoError(t, err, "Allocate failed for %s", uid)
	}

	for _, uid := range uids {
		info := p.state.GetAllocationInfo(uid, "main")
		require.NotNil(t, info, "%s must have its own allocation info", uid)
		require.Equal(t, uid, info.PodUid)
		requireSNBPodResolved(t, info, "share-NUMA0")
	}

	entries := p.state.GetPodEntries()
	count := 0
	for uid := range entries {
		if uid == "snb-pod-a" || uid == "snb-pod-b" || uid == "snb-pod-c" {
			count++
		}
	}
	require.Equal(t, 3, count, "all 3 distinct SNB pod UIDs must be present")
}

// TestSNBAdmissionSequence_ConcurrentPods verifies concurrent same-pool SNB
// pods admit without races or cross-NUMA errors.
func TestSNBAdmissionSequence_ConcurrentPods(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			uid := fmt.Sprintf("snb-concurrent-%d", idx)
			_, err := p.Allocate(context.Background(), makeSNBResourceRequest(uid, uid, "main", 1.0, 0))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "concurrent Allocate must not error")
	}
	for i := 0; i < n; i++ {
		info := p.state.GetAllocationInfo(fmt.Sprintf("snb-concurrent-%d", i), "main")
		require.NotNil(t, info)
		requireSNBPodResolved(t, info, "share-NUMA0")
	}
}

// TestSNBAdmissionSequence_DifferentSNBPools verifies SNB pods on different
// NUMAs land in distinct per-NUMA binding pools.
func TestSNBAdmissionSequence_DifferentSNBPools(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	_, err := p.Allocate(context.Background(), makeSNBResourceRequest("snb-numa0", "snb-numa0", "main", 1.0, 0))
	require.NoError(t, err)
	_, err = p.Allocate(context.Background(), makeSNBResourceRequest("snb-numa1", "snb-numa1", "main", 1.0, 1))
	require.NoError(t, err)

	requireSNBPodResolved(t, p.state.GetAllocationInfo("snb-numa0", "main"), "share-NUMA0")
	requireSNBPodResolved(t, p.state.GetAllocationInfo("snb-numa1", "main"), "share-NUMA1")

	fake0 := p.state.GetAllocationInfo("share-NUMA0", commonstate.FakedContainerName)
	fake1 := p.state.GetAllocationInfo("share-NUMA1", commonstate.FakedContainerName)
	require.NotNil(t, fake0)
	require.NotNil(t, fake1)
	require.True(t, fake0.CheckSharedNUMABinding())
	require.True(t, fake1.CheckSharedNUMABinding())
}

// TestSNBAdmissionSequence_OrdinarySharedPool verifies ordinary shared-cores
// pods land in the default "share" pool whose fake entry must NOT be stamped
// SNB (never blanket-tag generic pools).
func TestSNBAdmissionSequence_OrdinarySharedPool(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	_, err := p.Allocate(context.Background(), makeOrdinaryControlResourceRequest("ord-1", "ord-1", "main", 1.0))
	require.NoError(t, err)

	info := p.state.GetAllocationInfo("ord-1", "main")
	require.NotNil(t, info)
	require.False(t, info.CheckSharedNUMABinding(), "ordinary pod must NOT be SNB")
	require.Equal(t, commonstate.PoolNameShare, info.GetPoolName())

	fake := p.state.GetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName)
	require.NotNil(t, fake)
	require.False(t, fake.CheckSharedNUMABinding(), "generic share pool must NOT be stamped SNB")
}

// TestSNBAdmissionSequence_MixedOrdinaryAndSNB verifies ordinary shared and SNB
// membership coexist: ordinary in "share" (FakedNUMAID), SNB in "share-NUMA0",
// preserving the one-pool-one-NUMA contract without cross-contamination.
func TestSNBAdmissionSequence_MixedOrdinaryAndSNB(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	_, err := p.Allocate(context.Background(), makeOrdinaryControlResourceRequest("mix-ord", "mix-ord", "main", 1.0))
	require.NoError(t, err)
	_, err = p.Allocate(context.Background(), makeSNBResourceRequest("mix-snb", "mix-snb", "main", 1.0, 0))
	require.NoError(t, err)

	ord := p.state.GetAllocationInfo("mix-ord", "main")
	require.False(t, ord.CheckSharedNUMABinding())
	require.Equal(t, commonstate.PoolNameShare, ord.GetPoolName())

	snb := p.state.GetAllocationInfo("mix-snb", "main")
	requireSNBPodResolved(t, snb, "share-NUMA0")

	shareFake := p.state.GetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName)
	snbFake := p.state.GetAllocationInfo("share-NUMA0", commonstate.FakedContainerName)
	require.NotNil(t, shareFake)
	require.NotNil(t, snbFake)
	require.False(t, shareFake.CheckSharedNUMABinding(), "ordinary share pool stays generic")
	require.True(t, snbFake.CheckSharedNUMABinding(), "SNB pool stays owner-accurate")
}

// TestSNBAdmissionSequence_RestartReloadedFakePoolRetainsIdentity proves the
// committed fake pool entry survives a checkpoint reload and the next same-NUMA
// SNB pod still admits (no regressed cross-NUMA after restart).
func TestSNBAdmissionSequence_RestartReloadedFakePoolRetainsIdentity(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p1, tmpDir, cleanup := setupSNBAdmissionPolicy(t, largeCPUNum, largeSocketNum, largeNUMANum, cfg, "")
	defer cleanup()

	_, err := p1.Allocate(context.Background(), makeSNBResourceRequest("rl-1", "rl-1", "main", 1.0, 0))
	require.NoError(t, err)

	fake1 := p1.state.GetAllocationInfo("share-NUMA0", commonstate.FakedContainerName)
	require.NotNil(t, fake1)
	require.True(t, fake1.CheckSharedNUMABinding(), "pre-reload fake entry must be SNB")

	// Reload from the same checkpoint directory.
	p2, _, cleanup2 := setupSNBAdmissionPolicy(t, largeCPUNum, largeSocketNum, largeNUMANum, cfg, tmpDir)
	defer cleanup2()

	fake2 := p2.state.GetAllocationInfo("share-NUMA0", commonstate.FakedContainerName)
	require.NotNil(t, fake2, "reloaded fake share-NUMA0 entry must exist")
	require.True(t, fake2.CheckSharedNUMABinding(), "reloaded fake entry must retain SNB identity")

	// A second same-NUMA pod admits on the reloaded policy.
	_, err = p2.Allocate(context.Background(), makeSNBResourceRequest("rl-2", "rl-2", "main", 1.0, 0))
	require.NoError(t, err, "second same-NUMA pod must admit after reload")
}

// TestSNBAdmissionSequence_QuantityAggregationConsistency verifies
// GetTopologyAwareResources returns aggregated quantities consistent with the
// state's AllocationResult.
func TestSNBAdmissionSequence_QuantityAggregationConsistency(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	_, err := p.Allocate(context.Background(), makeSNBResourceRequest("snb-quantity", "snb-quantity", "main", 1.0, 0))
	require.NoError(t, err)

	info := p.state.GetAllocationInfo("snb-quantity", "main")
	require.NotNil(t, info)

	gtarResp, err := p.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid: "snb-quantity", ContainerName: "main",
	})
	require.NoError(t, err)
	cpuRes := gtarResp.ContainerTopologyAwareResources.AllocatedResources[string(v1.ResourceCPU)]
	require.Equal(t, float64(info.AllocationResult.Size()), cpuRes.AggregatedQuantity)
	require.Equal(t, float64(info.OriginalAllocationResult.Size()), cpuRes.OriginalAggregatedQuantity)

	var sum float64
	for _, taq := range cpuRes.TopologyAwareQuantityList {
		sum += taq.ResourceValue
	}
	require.Equal(t, cpuRes.AggregatedQuantity, sum)
}

// TestSNBAdmissionSequence_RollbackOnlyOnExplicitOuterAction verifies state
// rollback (RemovePod) only happens when explicitly invoked from outside.
func TestSNBAdmissionSequence_RollbackOnlyOnExplicitOuterAction(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	_, err := p.Allocate(context.Background(), makeSNBResourceRequest("snb-rollback", "snb-rollback", "main", 1.0, 0))
	require.NoError(t, err)
	require.NotNil(t, p.state.GetAllocationInfo("snb-rollback", "main"))

	_, err = p.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: "snb-rollback"})
	require.NoError(t, err)
	require.Nil(t, p.state.GetAllocationInfo("snb-rollback", "main"))
}

// TestSNBAdmissionSequence_OrdinaryControlConfig verifies the approved config
// admits an ordinary control pod and an SNB pod side by side.
func TestSNBAdmissionSequence_OrdinaryControlConfig(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	_, err := p.Allocate(context.Background(), makeOrdinaryControlResourceRequest("ordinary-control", "ordinary-control", "main", 1.0))
	require.NoError(t, err)
	require.False(t, p.state.GetAllocationInfo("ordinary-control", "main").CheckSharedNUMABinding())

	_, err = p.Allocate(context.Background(), makeSNBResourceRequest("snb-control", "snb-control", "main", 1.0, 0))
	require.NoError(t, err)
	require.True(t, p.state.GetAllocationInfo("snb-control", "main").CheckSharedNUMABinding())
}

// TestSNBAdmissionSequence_RampUpSeed verifies a freshly admitted SNB pod
// starts in ramp-up mode under HardPartition=true.
func TestSNBAdmissionSequence_RampUpSeed(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	_, err := p.Allocate(context.Background(), makeSNBResourceRequest("snb-rampup", "snb-rampup", "main", 1.0, 0))
	require.NoError(t, err)
	require.True(t, p.state.GetAllocationInfo("snb-rampup", "main").RampUp)
}

// TestSNBAdmissionSequence_NoPluginInternalRemovePod verifies a failed Allocate
// (genuinely invalid request) does not internally RemovePod a previously admitted
// good pod. The failing vehicle is an empty NUMA hint, which hard-fails at the
// admission entry independent of topology and of the cross-NUMA invariant.
//
// Why not a negative CPU request: under the owner-inaccurate metadata, a negative
// request only "failed" because it tripped the spurious cross-NUMA check. With
// owner-accurate metadata it simply sizes the pool to zero and returns nil, so it
// is not a valid "must fail" oracle.
func TestSNBAdmissionSequence_NoPluginInternalRemovePod(t *testing.T) {
	t.Parallel()

	cfg := defaultSNBAdmissionTestConfig()
	p, cleanup := setupSNBLarge(t, cfg)
	defer cleanup()

	_, err := p.Allocate(context.Background(), makeSNBResourceRequest("snb-good", "snb-good", "main", 1.0, 0))
	require.NoError(t, err)
	require.NotNil(t, p.state.GetAllocationInfo("snb-good", "main"))

	badReq := makeSNBResourceRequest("snb-bad", "snb-bad", "main", 1.0, 0)
	badReq.Hint = &pluginapi.TopologyHint{Preferred: true}
	_, err = p.Allocate(context.Background(), badReq)
	require.Error(t, err, "empty-hint request must fail")

	goodAfter := p.state.GetAllocationInfo("snb-good", "main")
	require.NotNil(t, goodAfter, "good pod must not be removed after a failed Allocate")
	require.Equal(t, "snb-good", goodAfter.PodUid)
}

// =============================================================================
// Direct characterization of derivePoolOwnerAllocationMeta (mixed/inconsistent
// membership, nil/empty annotations, restart identity, no blanket tagging).
// =============================================================================

func snbMember(uid, ownerPool string) *state.AllocationInfo {
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        uid,
			OwnerPoolName: ownerPool,
			QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
			},
		},
	}
}

func ordinaryMember(uid, ownerPool string, annotations map[string]string) *state.AllocationInfo {
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        uid,
			OwnerPoolName: ownerPool,
			QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
			Annotations:   annotations,
		},
	}
}

func TestDerivePoolOwnerAllocationMeta_ConsistentSNBMembers(t *testing.T) {
	t.Parallel()

	entries := state.PodEntries{
		"pod-a": {"main": snbMember("pod-a", "share-NUMA0")},
		"pod-b": {"main": snbMember("pod-b", "share-NUMA0")},
	}
	meta := derivePoolOwnerAllocationMeta("share-NUMA0", entries)
	require.Equal(t, consts.PodAnnotationQoSLevelSharedCores, meta.QoSLevel)
	require.Equal(t, consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		meta.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding])
	require.True(t, (&meta).CheckSharedNUMABinding())
}

// Mixed ordinary + SNB membership must stay generic (fail closed), never
// silently relabeled as SNB.
func TestDerivePoolOwnerAllocationMeta_MixedMembersFailClosed(t *testing.T) {
	t.Parallel()

	entries := state.PodEntries{
		"pod-snb": {"main": snbMember("pod-snb", "share-NUMA0")},
		// an ordinary member that nominally resolves into the same pool name
		"pod-ord": {"main": ordinaryMember("pod-ord", "share-NUMA0", map[string]string{})},
	}
	meta := derivePoolOwnerAllocationMeta("share-NUMA0", entries)
	require.Empty(t, meta.QoSLevel, "mixed membership must not be stamped SNB")
	require.False(t, (&meta).CheckSharedNUMABinding())
}

// Ordinary-only members must not be blanket-tagged.
func TestDerivePoolOwnerAllocationMeta_OrdinaryOnlyMembersGeneric(t *testing.T) {
	t.Parallel()

	entries := state.PodEntries{
		"pod-ord": {"main": ordinaryMember("pod-ord", "share", map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		})},
	}
	meta := derivePoolOwnerAllocationMeta("share", entries)
	require.False(t, (&meta).CheckSharedNUMABinding())
}

// Nil/empty annotations are not SNB.
func TestDerivePoolOwnerAllocationMeta_NilEmptyAnnotationsNotSNB(t *testing.T) {
	t.Parallel()

	entries := state.PodEntries{
		"pod-nil":   {"main": ordinaryMember("pod-nil", "share-NUMA0", nil)},
		"pod-empty": {"main": ordinaryMember("pod-empty", "share-NUMA0", map[string]string{})},
	}
	meta := derivePoolOwnerAllocationMeta("share-NUMA0", entries)
	require.False(t, (&meta).CheckSharedNUMABinding())
}

// No resident members: a previously committed SNB fake entry keeps its identity
// (restart/reload path); otherwise generic.
func TestDerivePoolOwnerAllocationMeta_NoMembersPreservesCommittedSNB(t *testing.T) {
	t.Parallel()

	// With a committed SNB fake entry and no members, preserve it.
	committed := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta("share-NUMA0"),
	}
	committed.QoSLevel = consts.PodAnnotationQoSLevelSharedCores
	committed.Annotations = map[string]string{
		consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}
	entries := state.PodEntries{
		"share-NUMA0": {commonstate.FakedContainerName: committed},
	}
	meta := derivePoolOwnerAllocationMeta("share-NUMA0", entries)
	require.True(t, (&meta).CheckSharedNUMABinding(), "committed SNB identity must survive reload")

	// No committed entry at all: generic.
	meta2 := derivePoolOwnerAllocationMeta("share-NUMA0", state.PodEntries{})
	require.False(t, (&meta2).CheckSharedNUMABinding())
}
