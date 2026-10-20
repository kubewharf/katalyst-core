//go:build linux
// +build linux

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

package numacompact

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/handlers/compaction"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	testTHPOrder                          = 9
	testTHPUnusableIndexDegradedThreshold = 5.0
)

var numaCompactTestMu sync.Mutex

func serializeNumaCompactTest(t *testing.T) {
	t.Helper()
	numaCompactTestMu.Lock()
	t.Cleanup(numaCompactTestMu.Unlock)
}

func makeNumaMemCompactCoreConf(enable bool) (*coreconfig.Configuration, *dynamicconfig.DynamicAgentConfiguration) {
	conf := coreconfig.NewConfiguration()
	dynamicConf := conf.DynamicAgentConfiguration
	dynamicConf.GetDynamicConfiguration().NumaMemCompactConfiguration.EnableNumaMemCompact = enable

	general.RegisterHeartbeatCheck(memconsts.NumaMemCompact, 30*time.Second, general.HealthzCheckStateReady, 0)
	return conf, dynamicConf
}

func waitNumaMemCompactTask(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		numaMemCompactTaskMu.Lock()
		taskRunning := numaMemCompactTask != nil
		numaMemCompactTaskMu.Unlock()
		if !taskRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("NUMA compaction worker did not return")
		}
		time.Sleep(time.Millisecond)
	}
}

func makeMetaServer() (*metaserver.MetaServer, error) {
	server := &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{},
	}

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		return nil, err
	}

	server.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}
	return server, nil
}

// allocationInfoForQoS builds an AllocationInfo with the given QoS level bound to a NUMA node.
func allocationInfoForQoS(qosLevel string, numaID int) *state.AllocationInfo {
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "pod-" + qosLevel,
			PodName:       "pod-" + qosLevel,
			ContainerName: "container",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      qosLevel,
			Annotations: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey: qosLevel,
			},
		},
		NumaAllocationResult: machine.NewCPUSet(numaID),
	}
}

// setReadonlyStateWithPods installs a readonly state where each entry maps a NUMA id to the QoS
// level of a pod occupying it. An empty QoS string leaves that NUMA node idle.
func setReadonlyStateWithPods(numaToQoS map[int]string) {
	machineState := state.NUMANodeResourcesMap{
		v1.ResourceMemory: state.NUMANodeMap{},
	}
	for numaID, qosLevel := range numaToQoS {
		ns := &state.NUMANodeState{}
		if qosLevel != "" {
			ns.SetAllocationInfo(allocationInfoForQoS(qosLevel, numaID).PodUid, "container",
				allocationInfoForQoS(qosLevel, numaID))
		}
		machineState[v1.ResourceMemory][numaID] = ns
	}
	state.SetReadonlyState(&fakeReadonlyState{machineState: machineState})
}

// fakeReadonlyState is a minimal ReadonlyState that only serves a fixed machine state.
type fakeReadonlyState struct {
	state.ReadonlyState
	machineState state.NUMANodeResourcesMap
}

func (f *fakeReadonlyState) GetMachineState() state.NUMANodeResourcesMap {
	return f.machineState
}

type sequencedReadonlyState struct {
	state.ReadonlyState
	machineStates []state.NUMANodeResourcesMap
	next          int
}

func (f *sequencedReadonlyState) GetMachineState() state.NUMANodeResourcesMap {
	index := f.next
	if index >= len(f.machineStates) {
		index = len(f.machineStates) - 1
	}
	f.next++
	return f.machineStates[index]
}

func resetNumaCompactState() {
	clearNumaCompactStates()
}

// isNumaCompacted reports whether numaID currently has a recorded idle-compaction time.
func isNumaCompacted(numaID int) bool {
	_, ok := getNumaLastCompactState(numaID)
	return ok
}

// makeNUMANodeMap builds a memory NUMANodeMap where each entry maps a NUMA id to the QoS level of
// a pod occupying it. An empty QoS string leaves that NUMA node idle.
func makeNUMANodeMap(numaToQoS map[int]string) state.NUMANodeMap {
	nm := state.NUMANodeMap{}
	for numaID, qosLevel := range numaToQoS {
		ns := &state.NUMANodeState{}
		if qosLevel != "" {
			ns.SetAllocationInfo(allocationInfoForQoS(qosLevel, numaID).PodUid, "container",
				allocationInfoForQoS(qosLevel, numaID))
		}
		nm[numaID] = ns
	}
	return nm
}

func withFakeProcFS(t *testing.T) string {
	t.Helper()
	oldRoot := compaction.ProcFSRoot
	root := t.TempDir()
	compaction.ProcFSRoot = root
	compaction.ResetKcompactdPidsForTest()
	t.Cleanup(func() {
		compaction.ProcFSRoot = oldRoot
		compaction.ResetKcompactdPidsForTest()
	})
	return root
}

// withRecordingCompactFn swaps compaction.CompactMemoryNodeFn for one that records the NUMA ids it
// is called with, restoring it afterwards. It also installs a fake procfs so the per-NUMA state
// check and buddyinfo reads never consult the host. The returned pointer accumulates the compacted
// ids.
func withRecordingCompactFn(t *testing.T) *[]int {
	t.Helper()
	oldFn := compaction.CompactMemoryNodeFn
	var compacted []int
	compaction.CompactMemoryNodeFn = func(numaID int) {
		compacted = append(compacted, numaID)
	}

	procFSRoot := withFakeProcFS(t)
	writeFakeBuddyInfo(t, procFSRoot, map[int][2]uint64{
		0: {1, 1},
		1: {1, 1},
	})
	withFakeTHPSizePath(t, uint64(os.Getpagesize())<<testTHPOrder)

	t.Cleanup(func() {
		compaction.CompactMemoryNodeFn = oldFn
	})
	return &compacted
}

func withFakeTHPSizePath(t *testing.T, thpPageSize uint64) string {
	t.Helper()
	oldPath := compaction.THPSizeFilePath
	path := filepath.Join(t.TempDir(), "hpage_pmd_size")
	compaction.THPSizeFilePath = path
	compaction.ResetTHPOrderForTest()
	assert.NoError(t, os.WriteFile(path, []byte(strconv.FormatUint(thpPageSize, 10)+"\n"), 0o644))
	t.Cleanup(func() {
		compaction.THPSizeFilePath = oldPath
		compaction.ResetTHPOrderForTest()
	})
	return path
}

func withFakeUnusableIndexPath(t *testing.T) string {
	t.Helper()
	oldPath := compaction.UnusableIndexFilePath
	path := filepath.Join(t.TempDir(), "unusable_index")
	compaction.UnusableIndexFilePath = path
	t.Cleanup(func() {
		compaction.UnusableIndexFilePath = oldPath
	})
	return path
}

func writeFakeUnusableIndex(t *testing.T, path string, numaID int, thpUnusableIndex float64) {
	t.Helper()
	writeFakeUnusableIndexesAtOrder(t, path, testTHPOrder, map[int]float64{numaID: thpUnusableIndex})
}

func writeFakeUnusableIndexes(t *testing.T, path string, numaUnusableIndexes map[int]float64) {
	t.Helper()
	writeFakeUnusableIndexesAtOrder(t, path, testTHPOrder, numaUnusableIndexes)
}

func writeFakeUnusableIndexesAtOrder(t *testing.T, path string, order int,
	numaUnusableIndexes map[int]float64,
) {
	t.Helper()
	numaIDs := make([]int, 0, len(numaUnusableIndexes))
	for numaID := range numaUnusableIndexes {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)

	lines := make([]string, 0, len(numaUnusableIndexes))
	for _, numaID := range numaIDs {
		lines = append(lines, fakeUnusableIndexLine(numaID, order, numaUnusableIndexes[numaID]))
	}
	assert.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func fakeUnusableIndexLine(numaID, order int, thpUnusableIndex float64) string {
	orderUnusableIndexes := make([]string, order+2)
	for i := range orderUnusableIndexes {
		orderUnusableIndexes[i] = "0.000"
	}
	orderUnusableIndexes[order] = fmt.Sprintf("%.3f", thpUnusableIndex/100)
	return fmt.Sprintf("Node %d, zone Normal %s", numaID, strings.Join(orderUnusableIndexes, " "))
}

func writeFakeBuddyInfo(t *testing.T, procFSRoot string, numaTHPOrderPlusBlocks map[int][2]uint64) {
	t.Helper()
	writeFakeBuddyInfoAtOrder(t, procFSRoot, testTHPOrder, numaTHPOrderPlusBlocks)
}

func writeFakeBuddyInfoAtOrder(t *testing.T, procFSRoot string, order int,
	numaTHPOrderPlusBlocks map[int][2]uint64,
) {
	t.Helper()
	numaIDs := make([]int, 0, len(numaTHPOrderPlusBlocks))
	for numaID := range numaTHPOrderPlusBlocks {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)

	lines := make([]string, 0, len(numaTHPOrderPlusBlocks))
	for _, numaID := range numaIDs {
		orderCounts := make([]string, order+2)
		for i := range orderCounts {
			orderCounts[i] = "0"
		}
		orderCounts[order] = strconv.FormatUint(numaTHPOrderPlusBlocks[numaID][0], 10)
		orderCounts[order+1] = strconv.FormatUint(numaTHPOrderPlusBlocks[numaID][1], 10)
		lines = append(lines, fmt.Sprintf("Node %d, zone Normal %s", numaID, strings.Join(orderCounts, " ")))
	}
	assert.NoError(t, os.WriteFile(filepath.Join(procFSRoot, "buddyinfo"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func TestNumaHasOnlineBusinessPods(t *testing.T) {
	t.Parallel()

	machineState := makeNUMANodeMap(map[int]string{
		0: apiconsts.PodAnnotationQoSLevelSharedCores,
		1: apiconsts.PodAnnotationQoSLevelReclaimedCores,
		2: apiconsts.PodAnnotationQoSLevelSystemCores,
		3: "",
	})

	assert.True(t, numaHasOnlineBusinessPods(machineState, 0))  // shared_cores => business
	assert.False(t, numaHasOnlineBusinessPods(machineState, 1)) // reclaimed_cores => not business
	assert.False(t, numaHasOnlineBusinessPods(machineState, 2)) // system_cores => not business
	assert.False(t, numaHasOnlineBusinessPods(machineState, 3)) // idle
}

func TestNumaMemCompactSkipsBusinessPods(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	compacted := withRecordingCompactFn(t)
	readinessPath := withFakeUnusableIndexPath(t)
	writeFakeUnusableIndex(t, readinessPath, 1, 30)

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)

	// NUMA 0 has an online business pod, NUMA 1 is idle.
	setReadonlyStateWithPods(map[int]string{
		0: apiconsts.PodAnnotationQoSLevelSharedCores,
		1: "",
	})

	doNumaMemCompact(metaServer, metrics.DummyMetrics{}, false, time.Hour, testTHPUnusableIndexDegradedThreshold)

	// Only NUMA 1 (idle) is compacted; NUMA 0 (business) has no compaction record.
	assert.Equal(t, []int{1}, *compacted)
	assert.False(t, isNumaCompacted(0))
	assert.True(t, isNumaCompacted(1))
}

func TestNumaMemCompactRefreshesStateForEachNUMA(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	compacted := withRecordingCompactFn(t)
	readinessPath := withFakeUnusableIndexPath(t)
	writeFakeUnusableIndexes(t, readinessPath, map[int]float64{0: 30, 1: 30})

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)

	state.SetReadonlyState(&sequencedReadonlyState{
		machineStates: []state.NUMANodeResourcesMap{
			{
				v1.ResourceMemory: makeNUMANodeMap(map[int]string{
					0: "",
					1: "",
				}),
			},
			{
				v1.ResourceMemory: makeNUMANodeMap(map[int]string{
					0: apiconsts.PodAnnotationQoSLevelDedicatedCores,
					1: apiconsts.PodAnnotationQoSLevelDedicatedCores,
				}),
			},
		},
	})

	doNumaMemCompact(metaServer, metrics.DummyMetrics{}, false, time.Hour, testTHPUnusableIndexDegradedThreshold)

	// NUMA iteration order is unspecified; after the first compaction either next node is busy.
	require.Len(t, *compacted, 1)
	assert.True(t, isNumaCompacted((*compacted)[0]))
	assert.False(t, isNumaCompacted(1-(*compacted)[0]))
}

func TestNumaMemCompactEdgeTriggered(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	compacted := withRecordingCompactFn(t)
	readinessPath := withFakeUnusableIndexPath(t)
	writeFakeUnusableIndexes(t, readinessPath, map[int]float64{0: 30, 1: 30})

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)

	// The periodic switch is disabled, so this is pure falling-edge behavior regardless of interval.
	const interval = 5 * time.Minute

	// Cycle 1: NUMA 0 idle from the start -> compacted once; NUMA 1 busy -> skipped.
	setReadonlyStateWithPods(map[int]string{
		0: "",
		1: apiconsts.PodAnnotationQoSLevelSharedCores,
	})
	doNumaMemCompact(metaServer, metrics.DummyMetrics{}, false, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0}, *compacted)
	_, found := getNumaLastCompactState(0)
	assert.True(t, found)

	// Cycle 2: both idle now. NUMA 0 already compacted while idle -> not compacted again;
	// NUMA 1 just became idle after hosting business -> compacted once (the falling edge).
	setReadonlyStateWithPods(map[int]string{0: "", 1: ""})
	doNumaMemCompact(metaServer, metrics.DummyMetrics{}, false, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0, 1}, *compacted)

	// Cycle 3: both still idle -> nothing compacted (interval disabled).
	doNumaMemCompact(metaServer, metrics.DummyMetrics{}, false, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0, 1}, *compacted)

	// Cycle 4: a business pod is scheduled onto NUMA 0, dropping its record -> not compacted while busy.
	setReadonlyStateWithPods(map[int]string{0: apiconsts.PodAnnotationQoSLevelDedicatedCores, 1: ""})
	doNumaMemCompact(metaServer, metrics.DummyMetrics{}, false, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0, 1}, *compacted)
	assert.False(t, isNumaCompacted(0))
	_, found = getNumaLastCompactState(0)
	assert.False(t, found)

	// Cycle 5: the business pod leaves NUMA 0 -> compacted once more on the new falling edge.
	setReadonlyStateWithPods(map[int]string{0: "", 1: ""})
	doNumaMemCompact(metaServer, metrics.DummyMetrics{}, false, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0, 1, 0}, *compacted)
}

func TestNumaMemCompactPeriodicReadiness(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	compacted := withRecordingCompactFn(t)

	// Control the clock.
	base := time.Unix(1_000_000, 0)
	now := base
	oldNow := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = oldNow })

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	emitter := newRecordingEmitter()
	readinessPath := withFakeUnusableIndexPath(t)
	const (
		baselineUnusableIndex = 30.0
		degradedThreshold     = 7.5
	)
	writeFakeUnusableIndex(t, readinessPath, 0, baselineUnusableIndex)

	const interval = 5 * time.Minute

	// NUMA 0 idle throughout; NUMA 1 busy throughout.
	setReadonlyStateWithPods(map[int]string{
		0: "",
		1: apiconsts.PodAnnotationQoSLevelSharedCores,
	})

	// t=0: falling-edge compaction of NUMA 0.
	doNumaMemCompact(metaServer, emitter, true, interval, degradedThreshold)
	assert.Equal(t, []int{0}, *compacted)
	compactState, found := getNumaLastCompactState(0)
	assert.True(t, found)
	assert.InDelta(t, baselineUnusableIndex, compactState.postCompactTHPUnusableIndex, 0.001)

	// t=+2m (< interval): not re-compacted.
	now = base.Add(2 * time.Minute)
	doNumaMemCompact(metaServer, emitter, true, interval, degradedThreshold)
	assert.Equal(t, []int{0}, *compacted)

	// t=+5m (== interval): an unusable-index increase equal to the threshold is still ready.
	now = base.Add(5 * time.Minute)
	writeFakeUnusableIndex(t, readinessPath, 0, baselineUnusableIndex+degradedThreshold)
	doNumaMemCompact(metaServer, emitter, true, interval, degradedThreshold)
	assert.Equal(t, []int{0}, *compacted)
	assert.Equal(t, int64(0), emitter.values[metricNameNumaMemCompactTHPUnusableIndexDegraded])

	// The successful readiness check does not postpone the next check. On the next handler cycle,
	// a degraded unusable index triggers compaction because the minimum interval since the last actual
	// compaction has already elapsed.
	now = base.Add(5*time.Minute + 10*time.Second)
	writeFakeUnusableIndex(t, readinessPath, 0, baselineUnusableIndex+degradedThreshold+1)
	doNumaMemCompact(metaServer, emitter, true, interval, degradedThreshold)
	assert.Equal(t, []int{0, 0}, *compacted)
	assert.Equal(t, int64(0), emitter.values[metricNameNumaMemCompactTHPUnusableIndexDegraded])
	compactState, found = getNumaLastCompactState(0)
	assert.True(t, found)
	assert.InDelta(t, baselineUnusableIndex+degradedThreshold+1, compactState.postCompactTHPUnusableIndex, 0.001)
}

func TestNumaMemCompactRetriesMissingTHPUnusableIndex(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	compacted := withRecordingCompactFn(t)

	base := time.Unix(1_000_000, 0)
	now := base
	oldNow := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = oldNow })

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	emitter := newRecordingEmitter()
	readinessPath := withFakeUnusableIndexPath(t)
	const baselineUnusableIndex = 30.0
	writeFakeUnusableIndex(t, readinessPath, 0, baselineUnusableIndex)

	setReadonlyStateWithPods(map[int]string{
		0: "",
		1: apiconsts.PodAnnotationQoSLevelSharedCores,
	})

	const interval = 5 * time.Minute
	doNumaMemCompact(metaServer, emitter, true, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0}, *compacted)

	// Missing readiness data skips periodic compaction and leaves the check due for retry.
	now = base.Add(interval)
	assert.NoError(t, os.Remove(readinessPath))
	doNumaMemCompact(metaServer, emitter, true, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0}, *compacted)
	assert.Equal(t, 1, emitter.reasonCounts[metricNameNumaMemCompactError][errReasonReadTHPUnusableIndex])

	// Once unusable_index becomes available and reports poor readiness, the next scan compacts.
	writeFakeUnusableIndex(t, readinessPath, 0, baselineUnusableIndex+testTHPUnusableIndexDegradedThreshold+1)
	doNumaMemCompact(metaServer, emitter, true, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0, 0}, *compacted)
}

func TestNumaMemCompactKeepsCooldownWhenBaselineRefreshFails(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	withFakeTHPSizePath(t, uint64(os.Getpagesize())<<testTHPOrder)

	base := time.Unix(1_000_000, 0)
	now := base
	oldNow := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = oldNow })

	oldFn := compaction.CompactMemoryNodeFn
	var compacted []int
	compaction.CompactMemoryNodeFn = func(numaID int) {
		compacted = append(compacted, numaID)
		if len(compacted) == 2 {
			_ = os.Remove(compaction.UnusableIndexFilePath)
		}
	}
	t.Cleanup(func() { compaction.CompactMemoryNodeFn = oldFn })

	procFSRoot := withFakeProcFS(t)
	writeFakeBuddyInfo(t, procFSRoot, map[int][2]uint64{
		0: {1, 1},
		1: {1, 1},
	})

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	emitter := newRecordingEmitter()
	readinessPath := withFakeUnusableIndexPath(t)
	const baselineUnusableIndex = 30.0
	writeFakeUnusableIndex(t, readinessPath, 0, baselineUnusableIndex)

	setReadonlyStateWithPods(map[int]string{
		0: "",
		1: apiconsts.PodAnnotationQoSLevelSharedCores,
	})

	const interval = 5 * time.Minute
	doNumaMemCompact(metaServer, emitter, true, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0}, compacted)
	compactState, found := getNumaLastCompactState(0)
	assert.True(t, found)
	assert.InDelta(t, baselineUnusableIndex, compactState.postCompactTHPUnusableIndex, 0.001)

	now = base.Add(interval)
	writeFakeUnusableIndex(t, readinessPath, 0, baselineUnusableIndex+testTHPUnusableIndexDegradedThreshold+1)
	doNumaMemCompact(metaServer, emitter, true, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0, 0}, compacted)
	compactState, found = getNumaLastCompactState(0)
	assert.True(t, found)
	assert.Equal(t, now, compactState.compactAt)
	assert.Equal(t, invalidUnusableIndex, compactState.postCompactTHPUnusableIndex)
	assert.Equal(t, 1, emitter.reasonCounts[metricNameNumaMemCompactError][errReasonReadTHPUnusableIndex])

	// The failed baseline refresh still updates compactAt, so the next handler cycle remains within
	// the cooldown and must not compact again.
	now = base.Add(interval + 10*time.Second)
	doNumaMemCompact(metaServer, emitter, true, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0, 0}, compacted)

	// Once the next compact interval elapses, an invalid baseline bypasses degradation checking and
	// allows compaction to retry capturing a post-compaction baseline.
	now = base.Add(2 * interval)
	writeFakeUnusableIndex(t, readinessPath, 0, baselineUnusableIndex)
	doNumaMemCompact(metaServer, emitter, true, interval, testTHPUnusableIndexDegradedThreshold)
	assert.Equal(t, []int{0, 0, 0}, compacted)
	compactState, found = getNumaLastCompactState(0)
	assert.True(t, found)
	assert.InDelta(t, baselineUnusableIndex, compactState.postCompactTHPUnusableIndex, 0.001)
}

func TestNumaMemCompactEmitsCompactionDiffMetrics(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	withFakeTHPSizePath(t, uint64(os.Getpagesize())<<testTHPOrder)

	readinessPath := withFakeUnusableIndexPath(t)
	const (
		preCompactUnusableIndex  = 40.0
		postCompactUnusableIndex = 25.0
	)
	writeFakeUnusableIndex(t, readinessPath, 0, preCompactUnusableIndex)

	procFSRoot := withFakeProcFS(t)
	preCompactTHPOrderPlusBlocks := [2]uint64{2, 1}
	postCompactTHPOrderPlusBlocks := [2]uint64{4, 2}
	writeFakeBuddyInfo(t, procFSRoot, map[int][2]uint64{0: preCompactTHPOrderPlusBlocks})

	oldFn := compaction.CompactMemoryNodeFn
	compaction.CompactMemoryNodeFn = func(numaID int) {
		writeFakeUnusableIndex(t, readinessPath, numaID, postCompactUnusableIndex)
		writeFakeBuddyInfo(t, procFSRoot, map[int][2]uint64{numaID: postCompactTHPOrderPlusBlocks})
	}
	t.Cleanup(func() { compaction.CompactMemoryNodeFn = oldFn })

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	emitter := newRecordingEmitter()
	setReadonlyStateWithPods(map[int]string{
		0: "",
		1: apiconsts.PodAnnotationQoSLevelSharedCores,
	})

	doNumaMemCompact(metaServer, emitter, false, time.Hour, testTHPUnusableIndexDegradedThreshold)

	assert.InDelta(t, preCompactUnusableIndex-postCompactUnusableIndex,
		emitter.floatValues[metricNameNumaMemCompactTHPUnusableIndexDiff], 0.001)
	assert.Equal(t, map[string]string{
		"numa_id": "0",
	}, emitter.floatTags[metricNameNumaMemCompactTHPUnusableIndexDiff])

	pageSize := uint64(os.Getpagesize())
	preCompactTHPOrderPlusFreeSize := pageSize *
		(preCompactTHPOrderPlusBlocks[0]*(uint64(1)<<testTHPOrder) +
			preCompactTHPOrderPlusBlocks[1]*(uint64(1)<<(testTHPOrder+1)))
	postCompactTHPOrderPlusFreeSize := pageSize *
		(postCompactTHPOrderPlusBlocks[0]*(uint64(1)<<testTHPOrder) +
			postCompactTHPOrderPlusBlocks[1]*(uint64(1)<<(testTHPOrder+1)))
	assert.Equal(t, int64(postCompactTHPOrderPlusFreeSize)-int64(preCompactTHPOrderPlusFreeSize),
		emitter.values[metricNameNumaMemCompactTHPOrderPlusFreeSizeDiffBytes])
	assert.Equal(t, map[string]string{
		"numa_id": "0",
	}, emitter.intTags[metricNameNumaMemCompactTHPOrderPlusFreeSizeDiffBytes])
}

func TestNumaMemCompact(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()

	// nil inputs are tolerated.
	conf, dynamicConf := makeNumaMemCompactCoreConf(false)
	NumaMemCompact(conf, nil, dynamicConf, nil, nil)
	NumaMemCompact(conf, nil, dynamicConf, metrics.DummyMetrics{}, nil)

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}

	// disabled: no NUMA node is compacted.
	compacted := withRecordingCompactFn(t)
	setReadonlyStateWithPods(map[int]string{0: "", 1: ""})
	conf, dynamicConf = makeNumaMemCompactCoreConf(false)
	NumaMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, metrics.DummyMetrics{}, metaServer)
	assert.Empty(t, *compacted)

	// enabled: idle NUMA nodes are compacted once.
	conf, dynamicConf = makeNumaMemCompactCoreConf(true)
	NumaMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, metrics.DummyMetrics{}, metaServer)
	waitNumaMemCompactTask(t)
	assert.ElementsMatch(t, []int{0, 1}, *compacted)
}

func TestNumaMemCompactDisabledClearsState(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	setNumaCompactState(0, 30)
	assert.True(t, isNumaCompacted(0))

	compacted := withRecordingCompactFn(t)
	readinessPath := withFakeUnusableIndexPath(t)
	writeFakeUnusableIndex(t, readinessPath, 0, 30)
	setReadonlyStateWithPods(map[int]string{
		0: "",
		1: apiconsts.PodAnnotationQoSLevelSharedCores,
	})

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	conf, dynamicConf := makeNumaMemCompactCoreConf(false)

	NumaMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, metrics.DummyMetrics{}, metaServer)
	assert.False(t, isNumaCompacted(0))

	dynamicConf.GetDynamicConfiguration().NumaMemCompactConfiguration.EnableNumaMemCompact = true
	NumaMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, metrics.DummyMetrics{}, metaServer)
	waitNumaMemCompactTask(t)
	assert.Equal(t, []int{0}, *compacted)
}

func TestNumaMemCompactPeriodicSwitch(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	compacted := withRecordingCompactFn(t)
	readinessPath := withFakeUnusableIndexPath(t)
	writeFakeUnusableIndex(t, readinessPath, 0, 36)
	setReadonlyStateWithPods(map[int]string{
		0: "",
		1: apiconsts.PodAnnotationQoSLevelSharedCores,
	})

	base := time.Unix(1_000_000, 0)
	now := base
	oldNow := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = oldNow })
	setNumaCompactState(0, 30)
	now = base.Add(30 * time.Minute)

	metaServer, err := makeMetaServer()
	require.NoError(t, err)
	conf, dynamicConf := makeNumaMemCompactCoreConf(true)

	// Periodic maintenance is enabled by default, so the due, degraded NUMA is compacted again.
	NumaMemCompact(conf, nil, dynamicConf, metrics.DummyMetrics{}, metaServer)
	waitNumaMemCompactTask(t)
	assert.Equal(t, []int{0}, *compacted)

	// Explicitly disabling periodic maintenance keeps the NUMA edge-triggered even after another
	// interval elapses and its readiness degrades further.
	dynamicConf.GetDynamicConfiguration().NumaMemCompactConfiguration.EnablePeriodicNumaMemCompact = false
	now = now.Add(30 * time.Minute)
	writeFakeUnusableIndex(t, readinessPath, 0, 42)
	NumaMemCompact(conf, nil, dynamicConf, metrics.DummyMetrics{}, metaServer)
	waitNumaMemCompactTask(t)
	assert.Equal(t, []int{0}, *compacted)
}

func TestNumaMemCompactDerivesTHPOrder(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	compacted := withRecordingCompactFn(t)
	readinessPath := withFakeUnusableIndexPath(t)
	const (
		derivedTHPOrder = 7
		unusableIndex   = 36.0
	)
	thpPageSize := uint64(os.Getpagesize()) << derivedTHPOrder
	assert.NoError(t, os.WriteFile(compaction.THPSizeFilePath,
		[]byte(strconv.FormatUint(thpPageSize, 10)+"\n"), 0o644))
	writeFakeUnusableIndexesAtOrder(t, readinessPath, derivedTHPOrder, map[int]float64{0: unusableIndex})
	writeFakeBuddyInfoAtOrder(t, compaction.ProcFSRoot, derivedTHPOrder, map[int][2]uint64{0: {1, 1}})
	setReadonlyStateWithPods(map[int]string{
		0: "",
		1: apiconsts.PodAnnotationQoSLevelSharedCores,
	})

	metaServer, err := makeMetaServer()
	require.NoError(t, err)

	conf, dynamicConf := makeNumaMemCompactCoreConf(true)
	emitter := newRecordingEmitter()
	NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	waitNumaMemCompactTask(t)

	assert.Equal(t, []int{0}, *compacted)
	compactState, found := getNumaLastCompactState(0)
	require.True(t, found)
	assert.InDelta(t, unusableIndex, compactState.postCompactTHPUnusableIndex, 0.001)
	assert.Zero(t, emitter.reasonCounts[metricNameNumaMemCompactError][errReasonReadTHPOrder])
}

// The release channel models an uninterruptible sysfs write. Cleanup joins the worker before
// restoring fake filesystem paths or the compaction function.
func withBlockedCompactFn(t *testing.T) (<-chan int, func(), *[]int) {
	t.Helper()
	compacted := withRecordingCompactFn(t)
	readinessPath := withFakeUnusableIndexPath(t)
	writeFakeUnusableIndexes(t, readinessPath, map[int]float64{0: 30, 1: 30})
	record := compaction.CompactMemoryNodeFn
	started := make(chan int, 4)
	released := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(released) }) }
	compaction.CompactMemoryNodeFn = func(numaID int) {
		record(numaID)
		started <- numaID
		<-released
	}
	t.Cleanup(func() {
		release()
		waitNumaMemCompactTask(t)
	})
	return started, release, compacted
}

func waitCompactStarted(t *testing.T, started <-chan int) int {
	t.Helper()
	select {
	case numaID := <-started:
		return numaID
	case <-time.After(5 * time.Second):
		t.Fatal("NUMA compaction did not start")
		return -1
	}
}

func TestNumaMemCompactTaskDurationMetric(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	started, release, compacted := withBlockedCompactFn(t)
	setReadonlyStateWithPods(map[int]string{0: "", 1: ""})
	metaServer, err := makeMetaServer()
	require.NoError(t, err)
	conf, dynamicConf := makeNumaMemCompactCoreConf(true)
	emitter := newRecordingEmitter()

	NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	numaID := waitCompactStarted(t, started)
	emitter.mu.Lock()
	assert.NotContains(t, emitter.values, metricNameNumaMemCompactTaskDurationSeconds,
		"starting a task must not emit a near-zero duration sample")
	emitter.mu.Unlock()
	numaMemCompactTaskMu.Lock()
	numaMemCompactTask.startedAt = time.Now().Add(-time.Minute)
	startedAt := numaMemCompactTask.startedAt
	numaMemCompactTaskMu.Unlock()

	// A long-running task does not block the handler heartbeat and reports its running duration.
	require.NoError(t, general.UpdateHealthzState(memconsts.NumaMemCompact,
		general.HealthzCheckStateNotReady, "previous heartbeat"))
	NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	health := general.GetRegisterReadinessCheckResult()
	assert.True(t, health[general.HealthzCheckName(memconsts.NumaMemCompact)].Ready)
	assert.GreaterOrEqual(t, emitter.intValue(metricNameNumaMemCompactTaskDurationSeconds), int64(60))
	assert.Less(t, emitter.intValue(metricNameNumaMemCompactTaskDurationSeconds), int64(120),
		"duration must be reported in seconds")
	assert.Equal(t, "ongoing", emitter.intTag(metricNameNumaMemCompactTaskDurationSeconds, "status"))

	// Subsequent handler cycles observe the same task instead of starting another one.
	for i := 0; i < 3; i++ {
		NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	}
	numaMemCompactTaskMu.Lock()
	assert.NotNil(t, numaMemCompactTask)
	assert.Equal(t, startedAt, numaMemCompactTask.startedAt)
	numaMemCompactTaskMu.Unlock()
	assert.False(t, compaction.TryCompactNUMANode(numaID))

	// Completion reports the final duration even when no handler cycle occurs at that moment.
	numaMemCompactTaskMu.Lock()
	numaMemCompactTask.startedAt = time.Now().Add(-2 * time.Minute)
	numaMemCompactTaskMu.Unlock()
	release()
	waitNumaMemCompactTask(t)
	assert.GreaterOrEqual(t, emitter.intValue(metricNameNumaMemCompactTaskDurationSeconds), int64(120))
	assert.Equal(t, "completed", emitter.intTag(metricNameNumaMemCompactTaskDurationSeconds, "status"))
	assert.ElementsMatch(t, []int{0, 1}, *compacted)
	assert.True(t, isNumaCompacted(0))
	assert.True(t, isNumaCompacted(1))
}

func TestNumaMemCompactTaskDurationMetricSkipsNoCompact(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	setReadonlyStateWithPods(map[int]string{
		0: apiconsts.PodAnnotationQoSLevelSharedCores,
		1: apiconsts.PodAnnotationQoSLevelDedicatedCores,
	})
	metaServer, err := makeMetaServer()
	require.NoError(t, err)
	conf, dynamicConf := makeNumaMemCompactCoreConf(true)
	emitter := newRecordingEmitter()

	NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	waitNumaMemCompactTask(t)

	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	assert.NotContains(t, emitter.values, metricNameNumaMemCompactTaskDurationSeconds,
		"a scan without actual compaction must not emit a completed duration")
}

func TestNumaMemCompactDisableWhileTaskRunning(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	started, release, compacted := withBlockedCompactFn(t)
	setReadonlyStateWithPods(map[int]string{0: "", 1: ""})
	metaServer, err := makeMetaServer()
	require.NoError(t, err)
	conf, dynamicConf := makeNumaMemCompactCoreConf(true)
	emitter := newRecordingEmitter()

	NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	numaID := waitCompactStarted(t, started)
	numaMemCompactTaskMu.Lock()
	startedAt := numaMemCompactTask.startedAt
	numaMemCompactTaskMu.Unlock()
	setNumaCompactState(2, 30)

	dynamicConf.GetDynamicConfiguration().NumaMemCompactConfiguration.EnableNumaMemCompact = false
	NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	assert.False(t, isNumaCompacted(2))
	assert.Equal(t, int64(0), emitter.intValue(metricNameNumaMemCompactEnabled))
	assert.Equal(t, "ongoing", emitter.intTag(metricNameNumaMemCompactTaskDurationSeconds, "status"))

	dynamicConf.GetDynamicConfiguration().NumaMemCompactConfiguration.EnableNumaMemCompact = true
	for i := 0; i < 3; i++ {
		NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	}
	numaMemCompactTaskMu.Lock()
	assert.NotNil(t, numaMemCompactTask)
	assert.Equal(t, startedAt, numaMemCompactTask.startedAt)
	numaMemCompactTaskMu.Unlock()
	assert.False(t, compaction.TryCompactNUMANode(numaID))
	release()
	waitNumaMemCompactTask(t)
	assert.ElementsMatch(t, []int{0, 1}, *compacted)
	assert.False(t, isNumaCompacted(0))
	assert.False(t, isNumaCompacted(1), "the late write must not resurrect the old idle-cycle state")
	assert.Equal(t, "completed", emitter.intTag(metricNameNumaMemCompactTaskDurationSeconds, "status"))

	NumaMemCompact(conf, nil, dynamicConf, emitter, metaServer)
	waitNumaMemCompactTask(t)
	require.Len(t, *compacted, 4)
	assert.ElementsMatch(t, []int{0, 1}, (*compacted)[2:])
	assert.True(t, isNumaCompacted(0))
	assert.True(t, isNumaCompacted(1))
}

func TestSetHostMemCompact(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	compaction.CompactMemoryNodeFn(25535)
}

// recordingEmitter records the last numeric value and tags stored per key, and counts how many
// times each (key, reason-tag) pair was stored.
type recordingEmitter struct {
	metrics.DummyMetrics
	mu           sync.Mutex
	values       map[string]int64
	intTags      map[string]map[string]string
	floatValues  map[string]float64
	floatTags    map[string]map[string]string
	reasonCounts map[string]map[string]int
}

func newRecordingEmitter() *recordingEmitter {
	return &recordingEmitter{
		values:       make(map[string]int64),
		intTags:      make(map[string]map[string]string),
		floatValues:  make(map[string]float64),
		floatTags:    make(map[string]map[string]string),
		reasonCounts: make(map[string]map[string]int),
	}
}

func (e *recordingEmitter) intValue(key string) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.values[key]
}

func (e *recordingEmitter) intTag(key, tag string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.intTags[key][tag]
}

func (e *recordingEmitter) StoreInt64(key string, val int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values[key] = val
	e.intTags[key] = make(map[string]string, len(tags))
	for _, tag := range tags {
		e.intTags[key][tag.Key] = tag.Val
		if tag.Key != "reason" {
			continue
		}
		if e.reasonCounts[key] == nil {
			e.reasonCounts[key] = make(map[string]int)
		}
		e.reasonCounts[key][tag.Val]++
	}
	return nil
}

func (e *recordingEmitter) StoreFloat64(key string, val float64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.floatValues[key] = val
	e.floatTags[key] = make(map[string]string, len(tags))
	for _, tag := range tags {
		e.floatTags[key][tag.Key] = tag.Val
	}
	return nil
}

func TestNumaMemCompactEnabledMetric(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()
	withRecordingCompactFn(t)

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}
	setReadonlyStateWithPods(map[int]string{0: "", 1: ""})

	// disabled -> enabled metric is 0.
	emitter := newRecordingEmitter()
	conf, dynamicConf := makeNumaMemCompactCoreConf(false)
	NumaMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, emitter, metaServer)
	assert.Equal(t, int64(0), emitter.values[metricNameNumaMemCompactEnabled])

	// enabled -> enabled metric is 1.
	emitter = newRecordingEmitter()
	conf, dynamicConf = makeNumaMemCompactCoreConf(true)
	NumaMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, emitter, metaServer)
	waitNumaMemCompactTask(t)
	assert.Equal(t, int64(1), emitter.values[metricNameNumaMemCompactEnabled])
}

func TestNumaMemCompactErrorMetric(t *testing.T) {
	t.Parallel()
	serializeNumaCompactTest(t)

	resetNumaCompactState()

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}

	// nil metaServer -> nil_input error emitted (emitter is non-nil).
	emitter := newRecordingEmitter()
	conf, dynamicConf := makeNumaMemCompactCoreConf(true)
	NumaMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, emitter, nil)
	assert.Equal(t, 1, emitter.reasonCounts[metricNameNumaMemCompactError][errReasonNilInput])

	// read-state failure -> read_state_failed error emitted. Clearing the readonly state makes
	// getMemoryNUMAState report stateOK=false.
	state.SetReadonlyState(nil)
	emitter = newRecordingEmitter()
	withRecordingCompactFn(t)
	NumaMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, emitter, metaServer)
	waitNumaMemCompactTask(t)
	assert.Equal(t, 1, emitter.reasonCounts[metricNameNumaMemCompactError][errReasonReadStateFailed])
}
