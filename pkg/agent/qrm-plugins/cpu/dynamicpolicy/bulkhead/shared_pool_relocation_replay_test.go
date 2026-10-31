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
	"sync"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cpusetplugin "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/cpusettopology"
	topology "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
)

// This file is a mechanism-level replay of a generic named shared-pool relocation
// over a synthetic 96-CPU SMT topology (threads i and i+48 are paired; NUMA0 owns
// 0-47, NUMA1 owns 48-95). It drives the REAL Manager.Apply -> REAL
// cpuset_topology plugin -> REAL coordinator/budget -> in-memory hierarchy driver
// (fake IO) chain. No ConvergenceReport or AppliedView is hand-built here; every
// observed level/write comes back from the production plugin through the manager.
//
// The replay is a synthetic mechanism validation, not a byte-exact reproduction of
// any historical node: real NUMA/SMT identity is unknown, so the topology and CRI
// identities are synthetic and stable.

// replayOldAppliedReclaim28 is the previous applied reclaim partition (28 CPUs).
var replayOldAppliedReclaim28 = machine.NewCPUSet(
	0, 1, 2, 3, 4, 5, 6, 10,
	24, 25, 26, 28, 29, 30,
	48, 49, 50, 51, 52, 53, 54, 58,
	72, 73, 74, 76, 77, 78,
)

// replayPending68 is the desired non-reclaim/pending partition (68 CPUs), the
// complement of replayOldAppliedReclaim28 across 0-95.
var replayPending68 = machine.NewCPUSet(
	7, 8, 9, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 27,
	31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47,
	55, 56, 57, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 75,
	79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95,
)

// replayOldLeafCPUs is the full oversubscribed old cpuset both resolved live leaves
// carry before relocation; it seeds the physical hierarchy representation of the
// two leaves that this plugin only protects/verifies (kubelet owns their writes).
var replayOldLeafCPUs = machine.NewCPUSet(7, 11, 55, 59)

// replay96CPUTopology builds the synthetic 96-CPU machine: SMT pairs i/i+48, two
// NUMA nodes (0: 0-47, 1: 48-95).
func replay96CPUTopology() *machine.CPUTopology {
	details := machine.CPUDetails{}
	for i := 0; i < 96; i++ {
		numaID := 0
		if i >= 48 {
			numaID = 1
		}
		details[i] = machine.CPUTopoInfo{
			NUMANodeID: numaID,
			SocketID:   numaID,
			CoreID:     i % 48,
		}
	}
	return &machine.CPUTopology{
		NumCPUs:      96,
		NumCores:     48,
		NumSockets:   2,
		NumNUMANodes: 2,
		CPUDetails:   details,
	}
}

// replayWrite records one forward hierarchy write issued through the driver.
type replayWrite struct {
	Rel  string
	CPUs machine.CPUSet
}

// replayDriver is an in-memory topology.HierarchyDriver. It records every read and
// write and can inject a failure on the Nth forward write (the cross-prewrite
// failure point) to prove fail-closed no-false-Full accounting and fresh retry.
type replayDriver struct {
	mu sync.Mutex

	nodes    map[string]topology.EntryState
	children map[string][]string
	roots    []topology.RootRef

	reads      int
	listCalls  int
	writeCount int
	writes     []replayWrite

	// failOnWrite, when >= 0, makes the (failOnWrite+1)-th forward WriteCPUs fail.
	failOnWrite int
	failedAt    int
}

func newReplayDriver() *replayDriver {
	return &replayDriver{
		nodes:       map[string]topology.EntryState{},
		children:    map[string][]string{},
		failOnWrite: -1,
	}
}

func (d *replayDriver) Close() error { return nil }

func (d *replayDriver) Roots(context.Context) ([]topology.RootRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]topology.RootRef(nil), d.roots...), nil
}

func (d *replayDriver) StatIdentity(_ context.Context, rel string) (topology.CgroupIdentity, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	node, ok := d.nodes[rel]
	if !ok {
		return topology.CgroupIdentity{}, fmt.Errorf("stat %s: %w", rel, errReplayNotFound)
	}
	return node.Identity, nil
}

func (d *replayDriver) ReadEntry(_ context.Context, rel string) (topology.EntryState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reads++
	node, ok := d.nodes[rel]
	if !ok {
		return topology.EntryState{}, fmt.Errorf("read %s: %w", rel, errReplayNotFound)
	}
	return node, nil
}

func (d *replayDriver) ListChildren(_ context.Context, rel string) ([]topology.ChildRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.listCalls++
	out := make([]topology.ChildRef, 0, len(d.children[rel]))
	for _, name := range d.children[rel] {
		childRel := rel + "/" + name
		node := d.nodes[childRel]
		out = append(out, topology.ChildRef{Name: name, Identity: node.Identity})
	}
	return out, nil
}

func (d *replayDriver) WriteCPUs(_ context.Context, rel string, _ topology.CgroupIdentity, cpus machine.CPUSet) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writeCount++
	if d.failOnWrite >= 0 && d.writeCount == d.failOnWrite+1 {
		d.failedAt = d.writeCount
		return fmt.Errorf("injected write failure at prewrite point %d", d.writeCount)
	}
	node := d.nodes[rel]
	node.CPUs = cpus.Clone()
	node.ConfiguredCPUs = cpus.Clone()
	d.nodes[rel] = node
	d.writes = append(d.writes, replayWrite{Rel: rel, CPUs: cpus.Clone()})
	return nil
}

func (d *replayDriver) WriteMems(_ context.Context, _ string, _ topology.CgroupIdentity, _ string) error {
	return nil
}

func (d *replayDriver) Classify(error, topology.HierarchyOperation) topology.HierarchyErrorClass {
	return topology.HierarchyErrorInvalid
}

func (d *replayDriver) Capabilities() topology.HierarchyCapabilities {
	return topology.HierarchyCapabilities{
		StableIdentity:          true,
		EffectiveCPUSet:         true,
		KernelParentContainment: true,
	}
}

func (d *replayDriver) primaryCPUs() machine.CPUSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nodes["primary"].CPUs.Clone()
}

// leafCPUs reads the final effective cpuset the driver holds for a resolved leaf,
// used to prove the relocated leaf lands on B4 rather than the old A4 envelope.
func (d *replayDriver) leafCPUs(rel string) machine.CPUSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nodes[rel].CPUs.Clone()
}

func (d *replayDriver) snapshot() (reads, listCalls, writes int, failedAt int, rec []replayWrite) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads, d.listCalls, d.writeCount, d.failedAt, append([]replayWrite(nil), d.writes...)
}

var errReplayNotFound = fmt.Errorf("replay: entry not found")

// replayCgroupClient embeds the no-op FakeCgroupClient and overrides only what the
// production chain consults; SnapshotDriver hands the in-memory hierarchy driver to
// the coordinator so no real filesystem/cgroup access occurs.
type replayCgroupClient struct {
	client.FakeCgroupClient
	driver *replayDriver
}

func (c *replayCgroupClient) Version(context.Context) client.CgroupVersion {
	return client.CgroupVersionV1
}

func (c *replayCgroupClient) SnapshotDriver() topology.HierarchyDriver {
	return c.driver
}

func (c *replayCgroupClient) StatDir(_ context.Context, rel string) (time.Time, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	if _, ok := c.driver.nodes[rel]; ok {
		return time.Time{}, nil
	}
	return time.Time{}, errReplayNotFound
}

func (c *replayCgroupClient) ReadCPUSet(_ context.Context, rel string) (machine.CPUSet, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	node, ok := c.driver.nodes[rel]
	if !ok {
		return machine.NewCPUSet(), errReplayNotFound
	}
	return node.CPUs.Clone(), nil
}

func (c *replayCgroupClient) ListChildren(_ context.Context, rel string) ([]string, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	return append([]string(nil), c.driver.children[rel]...), nil
}

func (c *replayCgroupClient) EnsureDir(_ context.Context, _ string) error { return nil }

// resolveContainerRel is test-only fixture logic. It maps stable CRI identities
// onto the in-memory replay hierarchy without consulting the host cgroupfs; the
// production resolver remains exercised by non-synthetic tests.
func (c *replayCgroupClient) resolveContainerRel(podUID, containerID string) (string, error) {
	switch podUID {
	case "live-a-pod":
		return "primary/live-a", nil
	case "live-b-pod":
		return "primary/live-b", nil
	}
	return "", fmt.Errorf("replay: no resolved rel for pod=%s", podUID)
}

// recordingTopology wraps the REAL topology plugin and records the production
// outcome Level/Note/error without fabricating any report.
type recordingTopology struct {
	bulkheadapi.TopologyPlugin
	level    bulkheadapi.ConvergenceLevel
	note     string
	applyErr error
	calls    int
}

func (r *recordingTopology) Apply(ctx context.Context, in bulkheadapi.HandlerContext) (bulkheadapi.TopologyOutcome, error) {
	out, err := r.TopologyPlugin.Apply(ctx, in)
	r.calls++
	r.level = out.Level
	r.note = out.Note
	r.applyErr = err
	return out, err
}

// replayMetaServer returns a minimal MetaServer with a stub fetcher. With zero
// frozen container lifecycle proofs, publication only needs a non-nil MetaServer
// and a strict GetPodList that returns no error; no business pod is materialized.
func replayMetaServer() *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &replayStubFetcher{},
		},
	}
}

type replayStubFetcher struct {
	metapod.PodFetcherStub
}

func (f *replayStubFetcher) GetPodList(context.Context, func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return replayRunningPods(), nil
}

// GetPodListFromCache satisfies CachedPodSnapshotFetcher and returns the two
// resolved live leaves with stable running container identities; the cache-only
// path then resolves their rel through the injected mock resolver.
func (f *replayStubFetcher) GetPodListFromCache(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return replayRunningPods(), nil
}

// replayRunningPods is shared by cache-only resolution and strict validation so
// the fixture models one stable pod generation instead of accidentally retiring
// the leaves between the two lifecycle snapshots.
func replayRunningPods() []*v1.Pod {
	running := func(cid string) v1.ContainerStatus {
		return v1.ContainerStatus{
			Name:        "main",
			Ready:       true,
			ContainerID: "containerd://" + cid,
			State:       v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		}
	}
	return []*v1.Pod{
		{ObjectMeta: metav1.ObjectMeta{UID: "live-a-pod"}, Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main"}}}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("live-a-cid")}}},
		{ObjectMeta: metav1.ObjectMeta{UID: "live-b-pod"}, Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main"}}}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{running("live-b-cid")}}},
	}
}

// replayCoreConf builds a core configuration with reclaim disabled (empty reclaim
// rel paths) and the synthetic primary rel path, generous convergence budget.
func replayCoreConf() *config.Configuration {
	conf := config.NewConfiguration()
	if conf.CPUQRMPluginConfig != nil && conf.CPUQRMPluginConfig.BulkheadConfiguration != nil {
		cfg := conf.CPUQRMPluginConfig.BulkheadConfiguration
		cfg.BulkheadPrimaryRelPath = "primary"
		cfg.BulkheadReclaimRelPaths = nil
		cfg.BulkheadReclaimNumaPrefixes = nil
		cfg.TopologyConvergenceBudget.MaxRounds = 16
		cfg.TopologyConvergenceBudget.MaxHierarchyIOOperations = 2048
	}
	return conf
}

// replayDynamicConf enables bulkhead topology but keeps reclaim disabled.
func replayDynamicConf() *dynamicconfig.Configuration {
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = true
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetTopology = true
	conf.EnableReclaim = false
	return conf
}

// replayEnv bundles one replay round's shared fake driver/cgroup and the fresh
// manager/plugin wiring. The driver/physical state outlives a round so a failed
// round can be retried from its verified final snapshot.
type replayEnv struct {
	driver *replayDriver
	cg     *replayCgroupClient
}

// newReplayEnv seeds the synthetic 96-CPU fixture on a fresh in-memory driver.
func newReplayEnv() *replayEnv {
	driver := newReplayDriver()
	seedReplayFixture(driver)
	return &replayEnv{driver: driver, cg: &replayCgroupClient{driver: driver}}
}

// newRound builds a fresh real plugin + recording wrapper + manager over the shared
// driver. A fresh plugin per round avoids carrying mode-gate/reset state across an
// injected failure and its fresh-full retry.
func (e *replayEnv) newRoundWithConfig(conf *config.Configuration) (*recordingTopology, *Manager) {
	// Explicit test-only resolution keeps the replay independent of host
	// cgroupfs while the real topology plugin executes unchanged.
	realPlugin := cpusetplugin.NewCPUSetTopologyPluginWithDependencies(conf, func() client.CgroupClient {
		return e.cg
	}, e.cg.resolveContainerRel)
	rec := &recordingTopology{TopologyPlugin: realPlugin.(bulkheadapi.TopologyPlugin)}
	m := &Manager{plugins: []bulkheadapi.Plugin{rec}}
	return rec, m
}

func (e *replayEnv) newRound() (*recordingTopology, *Manager) {
	return e.newRoundWithConfig(replayCoreConf())
}

func replayRoundCtxWithState(state cpustate.ReadonlyState) cpusetutil.CPUSetAdjustmentHandlerCtx {
	return cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: replayDynamicConf(),
		State:       state,
		Topology:    replay96CPUTopology(),
		MetaServer:  replayMetaServer(),
		Emitter:     metrics.DummyMetrics{},
	}
}

func replayRoundCtx() cpusetutil.CPUSetAdjustmentHandlerCtx {
	return replayRoundCtxWithState(replayState())
}

// all96CPUs is the controlled-root target envelope once reclaim is disabled and
// the reserve pool is empty (all online CPUs).
func all96CPUs() machine.CPUSet {
	set := machine.NewCPUSet()
	for i := 0; i < 96; i++ {
		set.Add(i)
	}
	return set
}

// TestSharedPoolRelocationReplayCleanRound drives a clean Manager.Apply -> real
// cpusettopology plugin -> real coordinator -> fake driver round on the 96-CPU
// synthetic fixture. It proves the production chain converges and publishes rather
// than a hand-built report, and that the controlled root reaches the new target
// rather than the old applied envelope.
func TestSharedPoolRelocationReplayCleanRound(t *testing.T) {
	t.Parallel()
	env := newReplayEnv()
	rec, m := env.newRound()

	_, applyErr := m.Apply(context.Background(), replayRoundCtx())
	reads, lists, writes, failedAt, _ := env.driver.snapshot()
	primary := env.driver.primaryCPUs()

	t.Logf("clean round: applyErr=%v outcome_level=%v note=%q reads=%d list=%d writes=%d failedAt=%d primary=%s",
		applyErr, rec.level, rec.note, reads, lists, writes, failedAt, primary.String())

	if reads == 0 {
		t.Fatalf("real plugin never read the hierarchy through the driver; clean round did not reach production coordinator")
	}
	if applyErr != nil {
		t.Fatalf("clean round Apply error = %v, want nil (full convergence published)", applyErr)
	}
	if rec.level != bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("clean round outcome level = %v, want Full", rec.level)
	}
	if !m.appliedViewValidForPeriodical || m.appliedView == nil {
		t.Fatalf("clean round did not publish a Full applied view")
	}
	if !primary.Equals(all96CPUs()) {
		t.Fatalf("controlled primary root = %s, want new target envelope %s", primary.String(), all96CPUs().String())
	}
	if primary.Equals(replayOldAppliedReclaim28) {
		t.Fatalf("controlled primary root stayed on the old applied envelope %s; it must reach the new target", primary.String())
	}
}

// TestSharedPoolRelocationMidWriteFailureFailClosedAndFreshRecover injects a failure
// at a cross-prewrite forward write and proves the production chain fails closed
// (no false Full, no published view, attempted forward writes are still counted),
// then a fresh-full retry from the verified final snapshot recovers to the NEW
// target envelope instead of rolling back to the old applied envelope (restore-old-A).
func TestSharedPoolRelocationMidWriteFailureFailClosedAndFreshRecover(t *testing.T) {
	t.Parallel()
	env := newReplayEnv()

	// Round 1: inject a failure on the 3rd forward write (the cross-prewrite point).
	env.driver.failOnWrite = 2
	rec1, m1 := env.newRound()
	_, err1 := m1.Apply(context.Background(), replayRoundCtx())
	_, _, writes1, failedAt, _ := env.driver.snapshot()

	t.Logf("failed round: err=%v level=%v writes=%d failedAt=%d published=%v",
		err1, rec1.level, writes1, failedAt, m1.appliedViewValidForPeriodical)

	// Fail-closed contract: a physical write failure must surface an error, must
	// never be reported Full, and must not publish a view.
	if err1 == nil {
		t.Fatalf("injected mid-write failure must fail closed with an error, got nil")
	}
	if rec1.level == bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("failed round falsely reported ConvergenceLevelFull (noFalseFull violated)")
	}
	if m1.appliedViewValidForPeriodical || m1.appliedView != nil {
		t.Fatalf("failed round published an applied view; fail-closed must not publish")
	}
	if writes1 == 0 {
		t.Fatalf("no forward write was attempted; shared budget total writes were not observed")
	}
	if failedAt == 0 {
		t.Fatalf("injected write failure never fired on the prewrite point")
	}

	// Fresh-full retry: disarm the fault, build a fresh plugin, retry from the
	// verified final snapshot; recovery must land on the NEW target, not old A.
	env.driver.failOnWrite = -1
	rec2, m2 := env.newRound()
	_, err2 := m2.Apply(context.Background(), replayRoundCtx())
	primary := env.driver.primaryCPUs()
	_, _, writes2, _, _ := env.driver.snapshot()

	t.Logf("fresh retry: err=%v level=%v writes=%d primary=%s", err2, rec2.level, writes2, primary.String())

	if err2 != nil {
		t.Fatalf("fresh-full retry error = %v, want nil (recoverable from verified snapshot)", err2)
	}
	if rec2.level != bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("fresh retry outcome level = %v, want Full", rec2.level)
	}
	if !m2.appliedViewValidForPeriodical || m2.appliedView == nil {
		t.Fatalf("fresh retry did not publish a Full applied view")
	}
	if !primary.Equals(all96CPUs()) {
		t.Fatalf("retry controlled primary = %s, want new target envelope %s", primary.String(), all96CPUs().String())
	}
	if primary.Equals(replayOldAppliedReclaim28) {
		t.Fatalf("retry restored the old applied envelope %s; fresh-full must converge to the new target", primary.String())
	}
}

// seedReplayFixture lays the current effective state on the in-memory hierarchy:
// the primary controlled root still holds the old applied-reclaim envelope, and the
// two resolved live leaves still carry the oversubscribed old leaf cpuset; the four
// auxiliary share leaves are pre-drained (empty).
func seedReplayFixture(d *replayDriver) {
	d.mu.Lock()
	defer d.mu.Unlock()

	id := func(n uint64) topology.CgroupIdentity { return topology.CgroupIdentity{Device: 1, Inode: n} }

	d.roots = []topology.RootRef{{Rel: "primary", Identity: id(100)}}
	d.nodes["primary"] = topology.EntryState{
		Rel:            "primary",
		Identity:       id(100),
		CPUs:           replayOldAppliedReclaim28.Clone(),
		ConfiguredCPUs: replayOldAppliedReclaim28.Clone(),
	}

	leaves := []struct {
		rel   string
		cpus  machine.CPUSet
		inode uint64
	}{
		{"primary/live-a", replayOldLeafCPUs, 201},
		{"primary/live-b", replayOldLeafCPUs, 202},
		{"primary/drained-a", machine.NewCPUSet(), 203},
		{"primary/drained-b", machine.NewCPUSet(), 204},
		{"primary/drained-c", machine.NewCPUSet(), 205},
		{"primary/drained-d", machine.NewCPUSet(), 206},
	}
	d.children["primary"] = []string{"live-a", "live-b", "drained-a", "drained-b", "drained-c", "drained-d"}
	for _, leaf := range leaves {
		d.nodes[leaf.rel] = topology.EntryState{
			Rel:            leaf.rel,
			Identity:       id(leaf.inode),
			CPUs:           leaf.cpus.Clone(),
			ConfiguredCPUs: leaf.cpus.Clone(),
		}
	}
}

// replayTargetLeafB4 is the relocation target each resolved live leaf converges to
// (same named share pool); the leaves move off the oversubscribed old A4 set
// {7,11,55,59} onto B4.
var replayTargetLeafB4 = machine.NewCPUSet(0, 4, 48, 52)

// replayState builds the CPUPluginState for the replay: no reclaim pool (reclaim
// disabled), one generic named share pool "pool-a" owning the pending68 envelope,
// two resolved live leaves relocating A4 -> B4, and four pre-drained auxiliary
// share leaves (empty allocation result).
type replayMutableState interface {
	cpustate.ReadonlyState
	SetAllocationInfo(podUID string, containerName string, allocationInfo *cpustate.AllocationInfo) error
}

func replayStateWithLeafTarget(target machine.CPUSet) replayMutableState {
	state := cpustate.NewCPUPluginState(nil)

	// Generic named shared pool "pool-a" owns the pending68 envelope.
	poolMeta := commonstate.GenerateGenericPoolAllocationMeta("pool-a")
	poolMeta.QoSLevel = commonstate.PoolNameShare
	_ = state.SetAllocationInfo("pool-a", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   poolMeta,
		AllocationResult: replayPending68.Clone(),
	})

	// Two resolved live leaves relocate from the oversubscribed old A4 {7,11,55,59}
	// onto the B4 target {0,4,48,52} within the same named share pool.
	for _, podUID := range []string{"live-a-pod", "live-b-pod"} {
		_ = state.SetAllocationInfo(podUID, "main", &cpustate.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				ContainerName: "main",
				OwnerPoolName: "pool-a",
				QoSLevel:      commonstate.PoolNameShare,
			},
			AllocationResult: target.Clone(),
		})
	}

	// Four pre-drained auxiliary share leaves (empty allocation result).
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

func replayState() cpustate.ReadonlyState {
	return replayStateWithLeafTarget(replayTargetLeafB4)
}

func setReplayLeafTarget(t *testing.T, state replayMutableState, target machine.CPUSet) {
	t.Helper()
	for _, podUID := range []string{"live-a-pod", "live-b-pod"} {
		allocation := state.GetAllocationInfo(podUID, "main").Clone()
		allocation.AllocationResult = target.Clone()
		if err := state.SetAllocationInfo(podUID, "main", allocation); err != nil {
			t.Fatalf("update canonical leaf target for %s: %v", podUID, err)
		}
	}
}

func TestSharedPoolRelocationAdmissionDefersResolvedPrimaryCleanup(t *testing.T) {
	t.Parallel()
	env := newReplayEnv()
	env.driver.mu.Lock()
	primary := env.driver.nodes["primary"]
	primary.CPUs = all96CPUs()
	primary.ConfiguredCPUs = all96CPUs()
	env.driver.nodes["primary"] = primary
	env.driver.mu.Unlock()

	conf := replayCoreConf()
	rec, m := env.newRoundWithConfig(conf)
	state := replayStateWithLeafTarget(replayOldLeafCPUs)
	pool := state.GetAllocationInfo("pool-a", commonstate.FakedContainerName).Clone()
	pool.AllocationResult = all96CPUs()
	if err := state.SetAllocationInfo("pool-a", commonstate.FakedContainerName, pool); err != nil {
		t.Fatalf("seed canonical owner envelope: %v", err)
	}

	// Publish the historical A ownership through the production Full path. The
	// following admission must rely on this AppliedView; no projection is forged.
	fullA := replayRoundCtxWithState(state)
	fullA.CoreConf = conf
	if _, err := m.Apply(context.Background(), fullA); err != nil {
		t.Fatalf("publish historical owner authority: %v", err)
	}
	if rec.level != bulkheadapi.ConvergenceLevelFull || m.appliedView == nil {
		t.Fatalf("historical authority not published: level=%v view_nil=%v", rec.level, m.appliedView == nil)
	}
	for _, rel := range []string{"primary/live-a", "primary/live-b"} {
		proof := m.appliedView.RelProofByRel[rel]
		if proof.Device == 0 || proof.Inode == 0 || !proof.CPUSet.Equals(replayOldLeafCPUs) {
			t.Fatalf("historical leaf proof %q=%+v, want stable identity and cpuset %s",
				rel, proof, replayOldLeafCPUs.String())
		}
	}
	_, _, writesBeforeAdmission, _, _ := env.driver.snapshot()

	// Runtime desired state advances A -> B while both live leaves still enforce A.
	// Admission may classify the owner-proven handoff but must perform zero writes.
	setReplayLeafTarget(t, state, replayTargetLeafB4)
	admission := replayRoundCtxWithState(state)
	admission.Mode = cpusetutil.CPUSetAdjustmentModeAdmission
	admission.CoreConf = conf
	var retryReasons []cpusetutil.CPUSetAdjustmentRetryReason
	admission.ScheduleFullRetry = func(reason cpusetutil.CPUSetAdjustmentRetryReason) {
		retryReasons = append(retryReasons, reason)
	}
	if _, err := m.Apply(context.Background(), admission); err != nil {
		t.Fatalf("owner-proven admission handoff: %v", err)
	}
	_, _, writesAfterAdmission, _, _ := env.driver.snapshot()
	liveA := env.driver.leafCPUs("primary/live-a")
	liveB := env.driver.leafCPUs("primary/live-b")
	if rec.level != bulkheadapi.ConvergenceLevelPartial {
		t.Fatalf("admission handoff level=%v, want parent-safe Partial; note=%q", rec.level, rec.note)
	}
	if writesAfterAdmission != writesBeforeAdmission {
		t.Fatalf("admission performed physical writes: before=%d after=%d", writesBeforeAdmission, writesAfterAdmission)
	}
	if len(retryReasons) != 1 || retryReasons[0] != cpusetutil.RetryReasonDeferredLeaf {
		t.Fatalf("admission retry reasons=%v, want exactly deferred_leaf", retryReasons)
	}
	if !liveA.Equals(replayOldLeafCPUs) || !liveB.Equals(replayOldLeafCPUs) {
		t.Fatalf("admission rewrote deferred leaves: %s/%s", liveA.String(), liveB.String())
	}

	// Simulate restart: the process-local AppliedView and retry queue disappear, while
	// the canonical checkpoint target B and physical leaf state A remain. A fresh
	// Manager Full pass must be sufficient to complete the exact handoff.
	restartRec, restartManager := env.newRoundWithConfig(conf)
	fullB := replayRoundCtxWithState(state)
	fullB.CoreConf = conf
	if _, err := restartManager.Apply(context.Background(), fullB); err != nil {
		t.Fatalf("full handoff retry after restart: %v", err)
	}
	if restartRec.level != bulkheadapi.ConvergenceLevelFull || !restartManager.appliedViewValidForPeriodical {
		t.Fatalf("full handoff after restart was not published: level=%v valid=%v",
			restartRec.level, restartManager.appliedViewValidForPeriodical)
	}
	if got := env.driver.leafCPUs("primary/live-a"); !got.Equals(replayTargetLeafB4) {
		t.Fatalf("live-a after Full=%s, want %s", got.String(), replayTargetLeafB4.String())
	}
	if got := env.driver.leafCPUs("primary/live-b"); !got.Equals(replayTargetLeafB4) {
		t.Fatalf("live-b after Full=%s, want %s", got.String(), replayTargetLeafB4.String())
	}
}

// TestSharedPoolRelocationLiveLeafRelocationCleanAndRecover drives the real chain over
// the two resolved live leaves (A4 -> B4, same named share pool) and proves the leaves
// converge onto B4 with observable writes, the controlled root reaches the new target,
// a mid-write failure fails closed without a false Full, and a fresh-full retry recovers
// onto B4 instead of rolling back to the old A4 envelope.
func TestSharedPoolRelocationLiveLeafRelocationCleanAndRecover(t *testing.T) {
	t.Parallel()
	env := newReplayEnv()

	// Clean round: live leaves relocate A4 -> B4 and the chain converges Full.
	rec, m := env.newRound()
	_, applyErr := m.Apply(context.Background(), replayRoundCtx())
	reads, lists, writes, failedAt, _ := env.driver.snapshot()
	primary := env.driver.primaryCPUs()
	liveA := env.driver.leafCPUs("primary/live-a")
	liveB := env.driver.leafCPUs("primary/live-b")

	t.Logf("clean: err=%v level=%v reads=%d list=%d writes=%d failedAt=%d primary=%s liveA=%s liveB=%s",
		applyErr, rec.level, reads, lists, writes, failedAt, primary.String(), liveA.String(), liveB.String())

	if reads == 0 {
		t.Fatalf("real plugin never read the hierarchy through the driver")
	}
	if applyErr != nil {
		t.Fatalf("clean Apply error = %v, want nil", applyErr)
	}
	if rec.level != bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("clean level = %v, want Full", rec.level)
	}
	if !m.appliedViewValidForPeriodical || m.appliedView == nil {
		t.Fatalf("clean did not publish a Full applied view")
	}
	// Both relocated leaves must land on the B4 target, not the old A4 envelope.
	if !liveA.Equals(replayTargetLeafB4) {
		t.Fatalf("live-a leaf = %s, want B4 %s", liveA.String(), replayTargetLeafB4.String())
	}
	if !liveB.Equals(replayTargetLeafB4) {
		t.Fatalf("live-b leaf = %s, want B4 %s", liveB.String(), replayTargetLeafB4.String())
	}
	if !primary.Equals(all96CPUs()) {
		t.Fatalf("controlled primary = %s, want new target 0-95", primary.String())
	}
	if primary.Equals(replayOldAppliedReclaim28) {
		t.Fatalf("primary stayed on old applied envelope; must reach new target")
	}

	// Fresh fail-closed round on a freshly seeded driver (the clean round above is
	// idempotent/no-op on an already-matched snapshot, so it cannot fire a fault);
	// inject a mid-write failure and assert fail-closed: error, no false Full, no
	// published view, attempted writes counted, fault fired.
	failEnv := newReplayEnv()
	failEnv.driver.failOnWrite = 3
	rec1, m1 := failEnv.newRound()
	_, err1 := m1.Apply(context.Background(), replayRoundCtx())
	_, _, writes1, fired1, _ := failEnv.driver.snapshot()
	t.Logf("failed: err=%v level=%v writes=%d fired=%d published=%v",
		err1, rec1.level, writes1, fired1, m1.appliedViewValidForPeriodical)
	if err1 == nil {
		t.Fatalf("injected mid-write failure must fail closed with an error")
	}
	if rec1.level == bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("failed round falsely reported Full (noFalseFull violated)")
	}
	if m1.appliedViewValidForPeriodical || m1.appliedView != nil {
		t.Fatalf("failed round published a view; fail-closed must not")
	}
	if writes1 == 0 || fired1 == 0 {
		t.Fatalf("no attempted write or injected failure never fired")
	}

	// Fresh-full retry on a freshly seeded driver: recover onto B4, converge Full,
	// never restore the old A4 envelope.
	retryEnv := newReplayEnv()
	rec2, m2 := retryEnv.newRound()
	_, err2 := m2.Apply(context.Background(), replayRoundCtx())
	retryA := retryEnv.driver.leafCPUs("primary/live-a")
	retryB := retryEnv.driver.leafCPUs("primary/live-b")
	retryPrimary := retryEnv.driver.primaryCPUs()
	t.Logf("retry: err=%v level=%v primary=%s retryA=%s retryB=%s",
		err2, rec2.level, retryPrimary.String(), retryA.String(), retryB.String())
	if err2 != nil {
		t.Fatalf("fresh-full retry error = %v, want nil", err2)
	}
	if rec2.level != bulkheadapi.ConvergenceLevelFull {
		t.Fatalf("retry level = %v, want Full", rec2.level)
	}
	if !m2.appliedViewValidForPeriodical || m2.appliedView == nil {
		t.Fatalf("retry did not publish a Full applied view")
	}
	if !retryA.Equals(replayTargetLeafB4) || !retryB.Equals(replayTargetLeafB4) {
		t.Fatalf("retry leaves = %s/%s, both want B4 %s", retryA.String(), retryB.String(), replayTargetLeafB4.String())
	}
	if !retryPrimary.Equals(all96CPUs()) || retryPrimary.Equals(replayOldAppliedReclaim28) {
		t.Fatalf("retry primary = %s, want new target 0-95 and not old R28", retryPrimary.String())
	}
}
