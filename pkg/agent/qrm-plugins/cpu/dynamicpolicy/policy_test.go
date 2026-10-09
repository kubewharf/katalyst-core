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

package dynamicpolicy

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	utilfs "k8s.io/kubernetes/pkg/util/filesystem"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	componentagent "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/accompanyresource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicpolicyutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/validator"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/kcc"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/resourcepackage"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/reclaim"
	"github.com/kubewharf/katalyst-core/pkg/util/timemonitor"
)

const (
	podDebugAnnoKey = "qrm.katalyst.kubewharf.io/debug_pod"
)

type storeFailureState struct {
	state.State
	err        error
	storeCalls int
}

type releaseTrackingAccompanyPlugin struct {
	releaseCount int
	releaseErrs  []error
}

func (*releaseTrackingAccompanyPlugin) ResourceName() string {
	return "release-tracking"
}

func (*releaseTrackingAccompanyPlugin) GetAccompanyResourceTopologyHints(
	_ *pluginapi.ResourceRequest,
	_ *pluginapi.ListOfTopologyHints,
) error {
	return nil
}

func (*releaseTrackingAccompanyPlugin) AllocateAccompanyResource(
	_ *pluginapi.ResourceRequest,
	_ *pluginapi.ResourceAllocationResponse,
) error {
	return nil
}

func (p *releaseTrackingAccompanyPlugin) ReleaseAccompanyResource(_ *pluginapi.RemovePodRequest) error {
	p.releaseCount++
	if len(p.releaseErrs) >= p.releaseCount {
		return p.releaseErrs[p.releaseCount-1]
	}
	return nil
}

type advisorPoolTestClient struct{}

func (*advisorPoolTestClient) AddContainer(context.Context, *advisorsvc.ContainerMetadata, ...grpc.CallOption) (*advisorsvc.AddContainerResponse, error) {
	return &advisorsvc.AddContainerResponse{}, nil
}

func (*advisorPoolTestClient) RemovePod(context.Context, *advisorsvc.RemovePodRequest, ...grpc.CallOption) (*advisorsvc.RemovePodResponse, error) {
	return &advisorsvc.RemovePodResponse{}, nil
}

func (*advisorPoolTestClient) ListAndWatch(context.Context, *advisorsvc.Empty, ...grpc.CallOption) (advisorapi.CPUAdvisor_ListAndWatchClient, error) {
	return nil, fmt.Errorf("not implemented")
}

func (*advisorPoolTestClient) GetAdvice(context.Context, *advisorapi.GetAdviceRequest, ...grpc.CallOption) (*advisorapi.GetAdviceResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

type removePodTrackingAdvisorClient struct {
	advisorPoolTestClient
	removePodCount int
}

func (c *removePodTrackingAdvisorClient) RemovePod(
	context.Context,
	*advisorsvc.RemovePodRequest,
	...grpc.CallOption,
) (*advisorsvc.RemovePodResponse, error) {
	c.removePodCount++
	return &advisorsvc.RemovePodResponse{}, nil
}

func (s *storeFailureState) StoreState() error {
	s.storeCalls++
	return s.err
}

type cpuTestCase struct {
	cpuNum      int
	socketNum   int
	numaNum     int
	fakeNUMANum int
}

func TestAllocationRequestLockSerializesOnlySameContainer(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{}
	unlockFirst := p.lockAllocationRequest("pod", "main")

	sameContainerAcquired := make(chan func(), 1)
	go func() {
		sameContainerAcquired <- p.lockAllocationRequest("pod", "main")
	}()
	select {
	case unlock := <-sameContainerAcquired:
		unlock()
		t.Fatal("same-container request acquired the lock before its predecessor released it")
	case <-time.After(50 * time.Millisecond):
	}

	otherContainerAcquired := make(chan func(), 1)
	go func() {
		otherContainerAcquired <- p.lockAllocationRequest("pod", "sidecar")
	}()
	select {
	case unlock := <-otherContainerAcquired:
		unlock()
	case <-time.After(time.Second):
		t.Fatal("different-container request was blocked by an unrelated allocation lock")
	}

	unlockFirst()
	select {
	case unlock := <-sameContainerAcquired:
		unlock()
	case <-time.After(time.Second):
		t.Fatal("same-container waiter did not acquire the released allocation lock")
	}

	p.allocation.allocationRequestLocksMu.Lock()
	defer p.allocation.allocationRequestLocksMu.Unlock()
	require.Empty(t, p.allocation.allocationRequestLocks)
}

func generateSharedNumaBindingPoolAllocationMeta(poolName string) commonstate.AllocationMeta {
	meta := commonstate.GenerateGenericPoolAllocationMeta(poolName)
	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}
	meta.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
	meta.QoSLevel = consts.PodAnnotationQoSLevelSharedCores
	return meta
}

func getTestDynamicPolicyWithInitialization(
	topology *machine.CPUTopology, stateFileDirectory string,
) (*DynamicPolicy, error) {
	dynamicPolicy, err := getTestDynamicPolicyWithoutInitialization(topology, stateFileDirectory)
	if err != nil {
		return nil, err
	}

	err = dynamicPolicy.initReservePool()
	if err != nil {
		return nil, err
	}

	err = dynamicPolicy.initReclaimPool()
	if err != nil {
		return nil, err
	}

	dynamicPolicy.stopCh = make(chan struct{})
	return dynamicPolicy, nil
}

func getTestDynamicPolicyWithoutInitialization(
	topology *machine.CPUTopology, stateFileDirectory string,
) (*DynamicPolicy, error) {
	stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: stateFileDirectory,
	}
	stateImpl, err := state.NewCheckpointState(stateDirectoryConfig, cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic, topology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	if err != nil {
		return nil, err
	}

	extraTopologyInfo, _ := machine.GenerateDummyExtraTopology(topology.NumNUMANodes)
	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology:       topology,
		ExtraTopologyInfo: extraTopologyInfo,
	}

	reservedCPUs, _, err := calculator.TakeHTByNUMABalance(machineInfo, machineInfo.CPUDetails.CPUs().Clone(), 2)
	if err != nil {
		return nil, err
	}

	qosConfig := generic.NewQoSConfiguration()
	dynamicConfig := dynamic.NewDynamicAgentConfiguration()

	conf, err := options.NewOptions().Config()
	if err != nil {
		return nil, err
	}
	// The attempt layer captures the single immutable *Configuration from
	// p.config.dynamicConfig; tests set all dynamic knobs on that object, so the
	// embedded conf wrapper may stay distinct (some tests deliberately nil it).
	bulkheadManager, err := bulkhead.NewManager(conf)
	if err != nil {
		return nil, err
	}

	policyImplement := &DynamicPolicy{
		state: stateImpl,
		advisor: advisorComponent{
			advisorValidator:   validator.NewCPUAdvisorValidator(stateImpl, machineInfo),
			featureGateManager: featuregatenegotiation.NewFeatureGateManager(config.NewConfiguration()),
		},
		resourcePackageManager: resourcepackage.NewCachedResourcePackageManager(resourcepackage.NewResourcePackageManager(&npd.DummyNPDFetcher{NPD: &nodev1alpha1.NodeProfileDescriptor{}})), machine: machineComponent{machineInfo: machineInfo}, emitter: emitterComponent{emitter: metrics.DummyMetrics{}}, reclaim: reclaimComponent{reservedReclaimedCPUsSize: general.Max(reservedReclaimedCPUsSize, topology.NumNUMANodes)}, config: configComponent{conf: conf, qosConfig: qosConfig, dynamicConfig: dynamicConfig, reservedCPUs: reservedCPUs, enableReclaimNUMABinding: true, podDebugAnnoKeys: []string{podDebugAnnoKey}, numaNumberAnnotationKey: consts.PodAnnotationCPUEnhancementNumaNumber, numaIDsAnnotationKey: consts.PodAnnotationCPUEnhancementNumaIDs, topologyAllocationAnnotationKey: coreconsts.QRMPodAnnotationTopologyAllocationKey}, bulkhead: bulkheadComponent{bulkheadManager: bulkheadManager},
	}
	policyImplement.advisorPostCommitCheckpointDir = stateFileDirectory
	policyImplement.steadyFakeNUMAMigration = &steadyFakeNUMAMigrationIntentStore{
		dir: func() string { return policyImplement.advisorPostCommitCheckpointDir },
		topology: func() *machine.CPUTopology {
			if policyImplement.machine.machineInfo == nil {
				return nil
			}
			return policyImplement.machine.machineInfo.CPUTopology
		},
	}
	if err := policyImplement.steadyFakeNUMAMigration.restore(); err != nil {
		return nil, fmt.Errorf("restore steady fake-NUMA migration target: %w", err)
	}

	// Important: We must register the topologyAllocationHook and set the annotation keys
	// to ensure that the test environment correctly generates NUMA topology annotations
	// in the allocation response, matching the production behavior.
	policyImplement.RegisterAllocationHook(policyImplement.topologyAllocationHook)

	// register allocation behaviors for pods with different QoS level
	policyImplement.allocation.allocationHandlers = map[string]util.AllocationHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresAllocationHandler,
	}

	// register hint providers for pods with different QoS level
	policyImplement.allocation.hintHandlers = map[string]util.HintHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresHintHandler,
	}

	policyImplement.meta.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher:          &pod.PodFetcherStub{},
			KatalystMachineInfo: machineInfo,
			MetricsFetcher:      metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}),
		},
		ServiceProfilingManager: &spd.DummyServiceProfilingManager{},
	}

	// initialize hint optimizer
	err = policyImplement.initHintOptimizers()
	if err != nil {
		return nil, err
	}

	return policyImplement, nil
}

func TestCleanPoolsSkipsNilAllocationInfo(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestCleanPoolsSkipsNilAllocationInfo")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policyImpl, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	as.Nil(err)
	policyImpl.state.SetPodEntries(state.PodEntries{
		"pod-with-nil-entry": {
			"container-with-nil-allocation": nil,
		},
	}, false)

	as.NotPanics(func() {
		err = policyImpl.cleanPools()
	})
	as.Nil(err)
}

func TestCleanPoolsKeepsSyntheticDefaultShareWhenBackfillEnabled(t *testing.T) {
	t.Parallel()

	policy := newTestDynamicPolicy(t, "keep-synthetic-share")
	policy.config.dynamicConfig.GetDynamicConfiguration().
		FillDefaultSharePoolWithNonReclaimCPUs = true
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(
					commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(1, 2, 3),
			},
		},
	}, false)

	require.NoError(t, policy.cleanPools())
	require.False(t,
		policy.state.GetPodEntries().
			CheckPoolEmpty(commonstate.PoolNameShare))
}

func TestCleanPoolsDeletesSyntheticDefaultShareWhenBackfillDisabled(t *testing.T) {
	t.Parallel()

	policy := newTestDynamicPolicy(t, "delete-synthetic-share-gate-off")
	// gate never enabled, keep the legacy cleanup behavior.
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(
					commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(1, 2, 3),
			},
		},
	}, false)

	require.NoError(t, policy.cleanPools())
	require.True(t,
		policy.state.GetPodEntries().
			CheckPoolEmpty(commonstate.PoolNameShare))
}

func TestCleanPoolsDeletesDefaultShareAfterBackfillFlippedOff(t *testing.T) {
	t.Parallel()

	policy := newTestDynamicPolicy(t, "delete-synthetic-share-gate-flip")
	// open the gate first so the synthetic share pool would be retained.
	policy.config.dynamicConfig.GetDynamicConfiguration().
		FillDefaultSharePoolWithNonReclaimCPUs = true
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(
					commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(1, 2, 3),
			},
		},
	}, false)

	// dynamically flip the gate back to false; cleanPools should now follow the
	// legacy rules and delete the no-owner share pool.
	policy.config.dynamicConfig.GetDynamicConfiguration().
		FillDefaultSharePoolWithNonReclaimCPUs = false

	require.NoError(t, policy.cleanPools())
	require.True(t,
		policy.state.GetPodEntries().
			CheckPoolEmpty(commonstate.PoolNameShare))
}

func TestCleanPoolsFromPodEntries(t *testing.T) {
	t.Parallel()

	policy := newTestDynamicPolicy(t, "clean-pools-from-entries")
	policy.config.dynamicConfig.GetDynamicConfiguration().
		FillDefaultSharePoolWithNonReclaimCPUs = true

	newPoolEntry := func(poolName string) state.ContainerEntries {
		return state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(poolName),
			},
		}
	}

	entries := state.PodEntries{
		"share-NUMA0":               newPoolEntry("share-NUMA0"),
		"share-NUMA1":               newPoolEntry("share-NUMA1"),
		commonstate.PoolNameShare:   newPoolEntry(commonstate.PoolNameShare),
		commonstate.PoolNameReserve: newPoolEntry(commonstate.PoolNameReserve),
		"system-service":            newPoolEntry("system-service"),
		"peer-b": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "peer-b",
					ContainerName: "main",
					OwnerPoolName: "share-NUMA1",
				},
			},
		},
	}

	deletedPools := policy.cleanPoolsFromPodEntries(entries)

	require.Equal(t, []string{"share-NUMA0"}, deletedPools.List())
	require.NotContains(t, entries, "share-NUMA0")
	require.Contains(t, entries, "share-NUMA1")
	require.Contains(t, entries, commonstate.PoolNameShare)
	require.Contains(t, entries, commonstate.PoolNameReserve)
	require.Contains(t, entries, "system-service")

	legacySNB := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "legacy-snb",
			ContainerName: "main",
			QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				consts.PodAnnotationResourcePackageKey:           "legacy-package",
			},
		},
	}
	legacySNB.SetSpecifiedNUMABindingNUMAID([]uint64{0})
	machineState := policy.state.GetMachineState()
	machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
		"legacy-package": {PinnedCPUSet: machine.NewCPUSet(0)},
	}
	policy.state.SetMachineState(machineState, false)
	canonicalPool, _, err := state.GetCanonicalSharedNUMABindingPoolKey(
		machineState.GetNUMAResourcePackagePinnedCPUSet(), legacySNB)
	require.NoError(t, err)
	legacyEntries := state.PodEntries{
		canonicalPool: newPoolEntry(canonicalPool),
		"legacy-snb":  {"main": legacySNB},
	}

	deletedPools = policy.cleanPoolsFromPodEntries(legacyEntries)

	require.Empty(t, deletedPools)
	require.Contains(t, legacyEntries, canonicalPool)
}

func TestAdjustAllocationEntriesCleansOrphanInSingleDurableRevision(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, stateDir)
	require.NoError(t, err)

	const (
		podUID   = "orphan-owner"
		poolName = "share-NUMA0"
	)
	poolCPUs := machine.NewCPUSet(2, 3)
	initialEntries := state.PodEntries{
		poolName: {
			commonstate.FakedContainerName: {
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(poolName),
				AllocationResult:         poolCPUs.Clone(),
				OriginalAllocationResult: poolCPUs.Clone(),
			},
		},
		podUID: {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        podUID,
					ContainerName: "main",
					OwnerPoolName: poolName,
					QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult:         poolCPUs.Clone(),
				OriginalAllocationResult: poolCPUs.Clone(),
			},
		},
	}
	initialMachineState, err := generateMachineStateFromPodEntries(
		topology, initialEntries, policy.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, policy.state.CommitAdvisorState(
		initialEntries, initialMachineState, false, false, true))
	initialRevision := policy.state.GetRevision()

	candidate := initialEntries.Clone()
	delete(candidate, podUID)
	commit := func() error {
		return policy.adjustAllocationEntriesAtRevision(
			candidate.Clone(), initialMachineState, true, initialRevision)
	}

	require.NoError(t, os.RemoveAll(stateDir))
	require.NoError(t, os.WriteFile(stateDir, []byte("block checkpoint directory"), 0o600))
	require.Error(t, commit())
	require.Equal(t, initialRevision, policy.state.GetRevision())
	require.Equal(t, initialEntries, policy.state.GetPodEntries(),
		"checkpoint failure must roll back both owner removal and orphan cleanup")

	require.NoError(t, os.Remove(stateDir))
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, commit())
	require.Equal(t, initialRevision+1, policy.state.GetRevision(),
		"owner removal and orphan cleanup must use one revision")
	require.NotContains(t, policy.state.GetPodEntries(), podUID)
	require.NotContains(t, policy.state.GetPodEntries(), poolName)

	restarted, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
		cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic,
		topology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Equal(t, initialRevision+1, restarted.GetRevision())
	require.NotContains(t, restarted.GetPodEntries(), podUID)
	require.NotContains(t, restarted.GetPodEntries(), poolName)
}

func TestPodDeletionDefersUnownedPoolCleanupToAdjustmentBoundary(t *testing.T) {
	t.Parallel()

	const (
		podUID   = "clos-a"
		poolName = "share-NUMA0"
	)

	setup := func(t *testing.T) *DynamicPolicy {
		t.Helper()
		topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
		require.NoError(t, err)
		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)

		entries := state.PodEntries{
			poolName: {
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(poolName),
				},
			},
			podUID: {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podUID,
						ContainerName: "main",
						OwnerPoolName: poolName,
					},
				},
			},
		}
		machineState, err := generateMachineStateFromPodEntries(
			topology, entries, policy.state.GetMachineState())
		require.NoError(t, err)
		require.NoError(t, policy.state.CommitAdvisorState(entries, machineState, false, false, true))
		return policy
	}

	assertCandidate := func(t *testing.T, entries state.PodEntries) {
		require.NotContains(t, entries, podUID)
		require.Contains(t, entries, poolName)
	}

	t.Run("explicit remove", func(t *testing.T) {
		t.Parallel()
		policy := setup(t)
		adjustErr := errors.New("stop after validating target entries")
		policy.adjustAfterDeletionAtRevisionWithContextHook = func(_ context.Context, entries state.PodEntries, _ state.NUMANodeMap,
			_ bool, _ uint64,
		) error {
			assertCandidate(t, entries)
			return adjustErr
		}

		_, err := policy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: podUID})
		require.ErrorIs(t, err, adjustErr)
	})

	t.Run("residual cleanup", func(t *testing.T) {
		t.Parallel()
		policy := setup(t)
		called := false
		policy.residualHitMap = make(map[string]int64)
		policy.residualHitMap[podUID] = maxResidualTime.Nanoseconds()/stateCheckPeriod.Nanoseconds() - 1
		policy.adjustAfterDeletionAtRevisionHook = func(entries state.PodEntries, _ state.NUMANodeMap,
			_ bool, _ uint64,
		) error {
			called = true
			assertCandidate(t, entries)
			return errors.New("stop after validating target entries")
		}

		policy.clearResidualState(nil, nil, nil, nil, nil)
		require.True(t, called)
	})
}

func TestGetResourcesAllocationSkipsNilAllocationInfo(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetResourcesAllocationSkipsNilAllocationInfo")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policyImpl, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)
	policyImpl.state.SetPodEntries(state.PodEntries{
		"pod-with-nil-entry": {
			"container-with-nil-allocation": nil,
		},
	}, false)

	var resp *pluginapi.GetResourcesAllocationResponse
	as.NotPanics(func() {
		resp, err = policyImpl.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	})
	as.Nil(err)
	as.NotNil(resp)
	as.Contains(resp.PodResources, "pod-with-nil-entry")
	as.Empty(resp.PodResources["pod-with-nil-entry"].ContainerResources)
}

func TestGetResourcesAllocationLegacyRampUpExcludesReclaimFloor(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, stateDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		"legacy-shared": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid: "legacy-shared", PodNamespace: "default", PodName: "legacy-shared",
					ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN.String(),
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(2),
			},
		},
	}, false)

	_, err = p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	allocation := p.state.GetAllocationInfo("legacy-shared", "main")
	require.NotNil(t, allocation)
	require.True(t, allocation.RampUp)
	require.Empty(t, allocation.AllocationResult.Intersection(machine.NewCPUSet(0, 1)).ToSliceInt(),
		"legacy re-ramp-up allocation overlaps the hard reclaim floor: %s", allocation.AllocationResult)
}

func TestRollbackAllocationStateStoreFailureKeepsCurrentMemory(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, stateDir)
	require.NoError(t, err)

	const (
		podUID        = "rollback-store-failure"
		containerName = "main"
	)
	snapshotEntries := state.PodEntries{
		podUID: {
			containerName: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        podUID,
					ContainerName: containerName,
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: machine.NewCPUSet(1),
				RequestQuantity:  1,
			},
		},
	}
	snapshotMachineState, err := generateMachineStateFromPodEntries(
		cpuTopology, snapshotEntries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(
		snapshotEntries, snapshotMachineState, false, false, true))
	snapshot := allocationRollbackSnapshot{
		revision:                p.state.GetRevision(),
		podEntries:              snapshotEntries,
		machineState:            snapshotMachineState,
		allowOverlap:            false,
		disableDedicatedOverlap: false,
	}

	currentEntries := snapshotEntries.Clone()
	currentEntries[podUID][containerName].AllocationResult = machine.NewCPUSet(2)
	currentMachineState, err := generateMachineStateFromPodEntries(
		cpuTopology, currentEntries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(
		currentEntries, currentMachineState, false, false, false))
	require.NoError(t, os.RemoveAll(stateDir))
	require.NoError(t, os.WriteFile(stateDir, []byte("block checkpoint directory"), 0o600))

	err = p.rollbackAllocationState(&pluginapi.ResourceRequest{
		PodUid:        podUID,
		ContainerName: containerName,
	}, snapshot)
	require.Error(t, err)
	allocation := p.state.GetAllocationInfo(podUID, containerName)
	require.NotNil(t, allocation)
	require.True(t, allocation.AllocationResult.Equals(machine.NewCPUSet(2)),
		"failed rollback persistence must keep current in-memory state")
}

func TestSystemExclusivePoolSkipsNilAllocationInfo(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSystemExclusivePoolSkipsNilAllocationInfo")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policyImpl, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)
	policyImpl.state.SetPodEntries(state.PodEntries{
		"pod-with-nil-entry": {
			"container-with-nil-allocation": nil,
		},
	}, false)

	as.NotPanics(func() {
		_, _, _ = policyImpl.calculateSystemExclusivePoolChanges(map[string]*state.AllocationInfo{}, map[string]int{})
	})
	as.NotPanics(func() {
		err = policyImpl.adjustSystemCoresPodAllocation()
	})
	as.Nil(err)
}

func TestInitPoolAndCalculator(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestInitPoolAndCalculator")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policyImpl, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	err = policyImpl.initReclaimPool()
	as.Nil(err)

	reclaimPoolAllocationInfo := policyImpl.state.GetAllocationInfo(commonstate.PoolNameReclaim, "")

	as.NotNil(reclaimPoolAllocationInfo)

	as.Equal(reclaimPoolAllocationInfo.AllocationResult.Size(), reservedReclaimedCPUsSize)
}

func TestDynamicPolicyInitReclaimPoolWholeCoreAcrossNUMAs(t *testing.T) {
	t.Parallel()

	t.Run("rounds the configured floor up to complete cores", func(t *testing.T) {
		t.Parallel()

		topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
		require.NoError(t, err)

		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)
		policy.config.reservedCPUs = machine.NewCPUSet()
		policy.reclaim.reservedReclaimedCPUsSize = 5

		require.NoError(t, policy.initReclaimPool())

		reclaim := policy.state.GetAllocationInfo(
			commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		requireCoreAligned(t, topology, reclaim.AllocationResult)
		require.Equal(t, 6, reclaim.AllocationResult.Size(),
			"the configured floor must round up to complete cores")
		require.Equal(t, reclaim.AllocationResult.Size(), policy.reclaim.reservedReclaimedCPUsSize)
		require.Len(t, reclaim.TopologyAwareAssignments, 3)
	})

	t.Run("selects a numa that has an available complete core", func(t *testing.T) {
		t.Parallel()

		topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
		require.NoError(t, err)

		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)
		policy.config.reservedCPUs = topology.CPUDetails.CPUsInNUMANodes(0)
		policy.reclaim.reservedReclaimedCPUsSize = 2

		require.NoError(t, policy.initReclaimPool())

		require.True(t, policy.reclaim.reservedReclaimedCPUSet.IsSubsetOf(
			topology.CPUDetails.CPUsInNUMANodes(1)),
			"the reserved reclaim source must skip the low NUMA without an available complete core, got %s",
			policy.reclaim.reservedReclaimedCPUSet.String())
		reclaim := policy.state.GetAllocationInfo(
			commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		requireCoreAligned(t, topology, reclaim.AllocationResult)
		require.True(t, reclaim.AllocationResult.IsSubsetOf(
			topology.CPUDetails.CPUsInNUMANodes(1)),
			"reclaim should skip the low NUMA without an available complete core, got %s",
			reclaim.AllocationResult.String())
	})

	t.Run("skips a full numa while assigning the global complete-core target", func(t *testing.T) {
		t.Parallel()

		topology, err := machine.GenerateDummyCPUTopology(12, 2, 2)
		require.NoError(t, err)

		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)
		numa0CPUs := topology.CPUDetails.CPUsInNUMANodes(0)
		numa1CPUs := topology.CPUDetails.CPUsInNUMANodes(1)
		policy.config.reservedCPUs = numa0CPUs.Difference(machine.NewCPUSet(2, 8))
		policy.reclaim.reservedReclaimedCPUsSize = 6

		require.NoError(t, policy.initReclaimPool())

		requireCoreAligned(t, topology, policy.reclaim.reservedReclaimedCPUSet)
		require.Equal(t, 2, policy.reclaim.reservedReclaimedCPUSet.Intersection(numa0CPUs).Size())
		require.Equal(t, 4, policy.reclaim.reservedReclaimedCPUSet.Intersection(numa1CPUs).Size())
		require.Equal(t, 6, policy.reclaim.reservedReclaimedCPUSet.Size())
		reclaim := policy.state.GetAllocationInfo(
			commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		require.True(t, reclaim.AllocationResult.Equals(policy.reclaim.reservedReclaimedCPUSet))
	})

	t.Run("uses global capacity when one numa cannot satisfy its target", func(t *testing.T) {
		t.Parallel()

		topology, err := machine.GenerateDummyCPUTopology(8, 2, 2)
		require.NoError(t, err)

		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)
		policy.config.reservedCPUs = machine.NewCPUSet()
		policy.reclaim.reservedReclaimedCPUsSize = 4

		dedicated := topology.CPUDetails.CPUsInNUMANodes(0)
		entries := state.PodEntries{
			"dedicated-pod": {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "dedicated-pod",
						ContainerName: "main",
						OwnerPoolName: commonstate.PoolNameDedicated,
						QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
					},
					AllocationResult:                 dedicated,
					OriginalAllocationResult:         dedicated,
					TopologyAwareAssignments:         map[int]machine.CPUSet{0: dedicated},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: dedicated},
					RequestQuantity:                  float64(dedicated.Size()),
				},
			},
		}
		machineState, err := generateMachineStateFromPodEntries(
			topology, entries, policy.state.GetMachineState())
		require.NoError(t, err)
		require.NoError(t, policy.state.CommitAdvisorState(
			entries, machineState, false, false, false))

		require.NoError(t, policy.initReclaimPool())

		reclaim := policy.state.GetAllocationInfo(
			commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		requireCoreAligned(t, topology, reclaim.AllocationResult)
		require.Equal(t, policy.reclaim.reservedReclaimedCPUsSize, reclaim.AllocationResult.Size())
		require.True(t, reclaim.AllocationResult.Equals(topology.CPUDetails.CPUsInNUMANodes(1)),
			"reclaim should use the globally available complete cores, got %s",
			reclaim.AllocationResult.String())
	})
}

func TestDynamicPolicyInitReclaimPoolDoesNotReplayOverlappingReserve(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()
	policy.reclaim.reservedReclaimedCPUsSize = 2

	dedicated := topology.CPUDetails.CPUs()
	entries := state.PodEntries{
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult:         dedicated.Clone(),
				OriginalAllocationResult: dedicated.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: topology.CPUDetails.CPUsInNUMANodes(0),
					1: topology.CPUDetails.CPUsInNUMANodes(1),
				},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: topology.CPUDetails.CPUsInNUMANodes(0),
					1: topology.CPUDetails.CPUsInNUMANodes(1),
				},
				RequestQuantity: float64(dedicated.Size()),
			},
		},
	}
	machineState, err := generateMachineStateFromPodEntries(
		topology, entries, policy.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, policy.state.CommitAdvisorState(
		entries, machineState, false, false, false))

	err = policy.initReclaimPool()
	require.ErrorContains(t, err, "select core-aligned reclaim cpus")
	require.Nil(t, policy.state.GetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName))
}

func TestRemovePod(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestRemovePod")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	testName := "test"

	// test for gt
	req := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	resp, err := dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        req.PodUid,
		ContainerName: testName,
	})
	as.Nil(err)

	as.Equal(&pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       req.PodUid,
		PodNamespace: testName,
		PodName:      testName,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: testName,
			AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
				string(v1.ResourceCPU): {
					IsNodeResource:             false,
					IsScalarResource:           true,
					AggregatedQuantity:         14,
					OriginalAggregatedQuantity: 14,
					TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 3, Node: 0},
						{ResourceValue: 3, Node: 1},
						{ResourceValue: 4, Node: 2},
						{ResourceValue: 4, Node: 3},
					},
					OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 3, Node: 0},
						{ResourceValue: 3, Node: 1},
						{ResourceValue: 4, Node: 2},
						{ResourceValue: 4, Node: 3},
					},
				},
			},
		},
	}, resp)

	_, _ = dynamicPolicy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{
		PodUid: req.PodUid,
	})

	_, err = dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        req.PodUid,
		ContainerName: testName,
	})
	as.NotNil(err)
	as.True(strings.Contains(err.Error(), "is not show up in cpu plugin state"))
}

func TestAllocateAndRemovePodWaitForAdvisorOwnerToConverge(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	var reconcileCalls int32
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"record": func(_ context.Context, handlerCtx dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if handlerCtx.Mode == dynamicpolicyutil.CPUSetAdjustmentModeRetry {
				atomic.AddInt32(&reconcileCalls, 1)
			}
			return nil
		},
	}
	req := &pluginapi.ResourceRequest{
		PodUid:        string(uuid.NewUUID()),
		PodNamespace:  "pending-wal",
		PodName:       "pending-wal",
		ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}
	_, err = p.Allocate(context.Background(), req)
	require.NoError(t, err)
	trackedState := &atomicCommitTrackingState{State: p.state}
	p.state = trackedState
	revision := p.state.GetRevision()
	p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, revision)

	allocateDone := make(chan error, 1)
	go func() {
		_, allocateErr := p.Allocate(context.Background(), req)
		allocateDone <- allocateErr
	}()
	select {
	case allocateErr := <-allocateDone:
		t.Fatalf("Allocate returned before the retry owner converged pending target: %v", allocateErr)
	case <-time.After(30 * time.Millisecond):
	}
	require.Equal(t, int32(0), atomic.LoadInt32(&reconcileCalls),
		"admission must not reconcile the pending target")

	p.scheduleCPUSetAdjustmentRetry(dynamicpolicyutil.RetryReasonApplyFailed)
	require.NoError(t, <-allocateDone)
	p.adjustment.cpuSetAdjustmentRetryWG.Wait()
	require.Equal(t, int32(1), atomic.LoadInt32(&reconcileCalls))
	require.Nil(t, p.currentAdvisorPostCommitTarget())

	trackedState.commitCalls = 0
	trackedState.storeCalls = 0
	revision = p.state.GetRevision()
	p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, revision)

	removeDone := make(chan error, 1)
	go func() {
		_, removeErr := p.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: req.PodUid})
		removeDone <- removeErr
	}()
	select {
	case removeErr := <-removeDone:
		t.Fatalf("RemovePod returned before the retry owner converged pending target: %v", removeErr)
	case <-time.After(30 * time.Millisecond):
	}
	require.Equal(t, int32(1), atomic.LoadInt32(&reconcileCalls),
		"RemovePod must not reconcile the pending target")

	p.scheduleCPUSetAdjustmentRetry(dynamicpolicyutil.RetryReasonApplyFailed)
	require.NoError(t, <-removeDone)
	p.adjustment.cpuSetAdjustmentRetryWG.Wait()
	require.Equal(t, int32(2), atomic.LoadInt32(&reconcileCalls))
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
}

func TestAllocateAndRemovePodPendingWaitHonorsRequestContextWithoutStateWrites(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	req := &pluginapi.ResourceRequest{
		PodUid:        string(uuid.NewUUID()),
		PodNamespace:  "pending-wal-failure",
		PodName:       "pending-wal-failure",
		ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}
	_, err = p.Allocate(context.Background(), req)
	require.NoError(t, err)

	trackedState := &atomicCommitTrackingState{State: p.state}
	p.state = trackedState
	emitter := &recordingMetricEmitter{}
	p.emitter.emitter = emitter
	var reconcileCalls int32
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"must-not-run-from-admission": func(ctx context.Context, _ dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			atomic.AddInt32(&reconcileCalls, 1)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	revision := p.state.GetRevision()
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, revision)

	allocateCtx, cancelAllocate := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelAllocate()
	_, err = p.Allocate(allocateCtx, req)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, int32(0), atomic.LoadInt32(&reconcileCalls),
		"Allocate must wait rather than reconcile")
	require.Equal(t, 0, trackedState.commitCalls, "pre-admission failure must not rollback state")
	require.Equal(t, 0, trackedState.storeCalls, "pre-admission failure must not store state")
	require.Equal(t, revision, p.state.GetRevision())
	require.NotNil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.Equal(t, 1, countMetricRecords(emitter.records, util.MetricNameAllocateFailed),
		"pre-admission Allocate failure must retain failure metrics")

	removeCtx, cancelRemove := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelRemove()
	_, err = p.RemovePod(removeCtx, &pluginapi.RemovePodRequest{PodUid: req.PodUid})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, int32(0), atomic.LoadInt32(&reconcileCalls),
		"RemovePod must wait rather than reconcile")
	require.Equal(t, 0, trackedState.commitCalls)
	require.Equal(t, 0, trackedState.storeCalls)
	require.Equal(t, revision, p.state.GetRevision())
	require.NotNil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.Equal(t, 1, countMetricRecords(emitter.records, util.MetricNameRemovePodFailed),
		"pre-admission RemovePod failure must retain failure metrics")
}

func TestRemovePodRecoversStaleEntryBehindStuckAdvisorPostCommitTarget(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	const podUID = "stale-pod"
	entries := state.PodEntries{
		podUID: {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        podUID,
					PodNamespace:  "test-ns",
					PodName:       podUID,
					ContainerName: "main",
					ContainerType: pluginapi.ContainerType_MAIN.String(),
					QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNameShare,
				},
				AllocationResult:         machine.NewCPUSet(2),
				OriginalAllocationResult: machine.NewCPUSet(2),
				RequestQuantity:          1,
			},
		},
	}
	machineState, err := generateMachineStateFromPodEntries(topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, machineState, false, false, true))

	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	target.lastProgressAt = time.Now().Add(-advisorPostCommitStuckThreshold(p.config.conf) - time.Second)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.FileExists(t, p.advisorPostCommitCheckpointPath())

	_, err = p.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: podUID})
	require.NoError(t, err)
	require.NotContains(t, p.state.GetPodEntries(), podUID)
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.Equal(t, advisorPostCommitPhaseCanonicalReconcile,
		p.currentAdvisorPostCommitProgress().phase)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.True(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.Contains(t, p.adjustment.cpuSetAdjustmentRetryReasons, dynamicpolicyutil.RetryReasonApplyFailed)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

func TestGetResourcesAllocationWaitsForAdvisorOwnerToConverge(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"noop": func(context.Context, dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			return nil
		},
	}
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())

	done := make(chan error, 1)
	go func() {
		_, getErr := p.GetResourcesAllocation(
			context.Background(), &pluginapi.GetResourcesAllocationRequest{})
		done <- getErr
	}()
	select {
	case getErr := <-done:
		t.Fatalf("GetResourcesAllocation returned before the retry owner converged target %d: %v",
			target.revision, getErr)
	case <-time.After(30 * time.Millisecond):
	}

	p.scheduleCPUSetAdjustmentRetry(dynamicpolicyutil.RetryReasonApplyFailed)
	require.NoError(t, <-done)
	p.adjustment.cpuSetAdjustmentRetryWG.Wait()
	require.Nil(t, p.currentAdvisorPostCommitTarget())
}

func TestGetResourcesAllocationPendingWaitHonorsRequestContextWithoutStateWrites(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	trackedState := &atomicCommitTrackingState{State: p.state}
	p.state = trackedState
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	target.lastProgressAt = time.Now().Add(-advisorPostCommitStuckThreshold(p.config.conf) - time.Second)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = p.GetResourcesAllocation(ctx, &pluginapi.GetResourcesAllocationRequest{})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 0, trackedState.commitCalls)
	require.Equal(t, 0, trackedState.storeCalls)
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.Equal(t, advisorPostCommitPhasePublished, p.currentAdvisorPostCommitProgress().phase)
}

func TestAllocateExecutionLeaseRechecksAdvisorTarget(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	adjustmentEntered := make(chan struct{})
	releaseAdjustment := make(chan struct{})
	var adjustmentCalls int32
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"block": func(context.Context, dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if atomic.AddInt32(&adjustmentCalls, 1) != 1 {
				return nil
			}
			close(adjustmentEntered)
			<-releaseAdjustment
			return nil
		},
	}
	adjustmentDone := make(chan error, 1)
	go func() {
		p.Lock()
		adjustmentDone <- p.runCPUSetAdjustmentHandlers(context.Background())
		p.Unlock()
	}()
	<-adjustmentEntered

	req := &pluginapi.ResourceRequest{
		PodUid:        string(uuid.NewUUID()),
		PodNamespace:  "execution-lease",
		PodName:       "execution-lease",
		ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}
	allocateDone := make(chan error, 1)
	go func() {
		_, allocateErr := p.Allocate(context.Background(), req)
		allocateDone <- allocateErr
	}()

	select {
	case allocateErr := <-allocateDone:
		t.Fatalf("Allocate returned while cpuset adjustment still held the execution lease: %v", allocateErr)
	case <-time.After(30 * time.Millisecond):
	}
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))

	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	close(releaseAdjustment)
	require.NoError(t, <-adjustmentDone)

	select {
	case allocateErr := <-allocateDone:
		t.Fatalf("Allocate returned without rechecking target %d after acquiring the execution lease: %v",
			target.revision, allocateErr)
	case <-time.After(30 * time.Millisecond):
	}
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))

	p.scheduleCPUSetAdjustmentRetry(dynamicpolicyutil.RetryReasonApplyFailed)
	require.NoError(t, <-allocateDone)
	p.adjustment.cpuSetAdjustmentRetryWG.Wait()
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NotNil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
}

func TestPendingAdvisorPostCommitWaitWakesImmediatelyOnTargetChange(t *testing.T) {
	t.Parallel()
	p := &DynamicPolicy{}
	target := &advisorPostCommitTarget{revision: 1}
	p.publishAdvisorPostCommitTargetInMemory(target)

	current, changed := p.currentAdvisorPostCommitTargetAndChange()
	require.Same(t, target, current)
	require.NotNil(t, changed)

	p.rollbackPreparedAdvisorPostCommitTarget(target, nil)

	select {
	case <-changed:
	default:
		t.Fatal("target change must synchronously close the observed change channel")
	}
}

func TestPendingAdvisorPostCommitWaitRechecksDeadlineAfterReacquiringPolicyLock(t *testing.T) {
	t.Parallel()
	p := &DynamicPolicy{}
	target := &advisorPostCommitTarget{revision: 1}
	p.publishAdvisorPostCommitTargetInMemory(target)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	locked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		p.Lock()
		close(locked)
		err := p.waitForPendingAdvisorPostCommitTargetLocked(ctx, "test")
		p.Unlock()
		done <- err
	}()

	<-locked
	p.Lock()
	p.rollbackPreparedAdvisorPostCommitTarget(target, nil)
	<-ctx.Done()
	p.Unlock()

	require.ErrorIs(t, <-done, context.DeadlineExceeded)
}

// TestPendingAdvisorPostCommitWaitRecoversAgedTargetEvenWithoutChangeEvent is the
// regression test for delete->immediate recreate Allocate timing out behind a
// pending advisor post-commit writer fence. The waiter enters while the target is
// fresh; the target then ages into the safe writer-fence handoff window, but the
// driver emits no further target-change event (post-first physical-apply progress
// records are no-ops). Before the fix the caller slept its entire deadline and
// never re-evaluated the stuck condition, returning "blocked by pending ...
// context deadline exceeded". After the fix the bounded re-evaluation tick lets
// the caller observe the aged target and complete the safe handoff.
func TestPendingAdvisorPostCommitWaitRecoversAgedTargetEvenWithoutChangeEvent(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)

	threshold := advisorPostCommitStuckThreshold(p.config.conf)

	// Age the target past the stuck threshold shortly after the waiter has gone to
	// sleep, without recording progress, so no target-change channel is closed.
	go func() {
		time.Sleep(150 * time.Millisecond)
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		target.lastProgressAt = time.Now().Add(-threshold - time.Second)
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	p.Lock()
	err = p.waitForPendingAdvisorPostCommitTargetLocked(ctx, "Allocate")
	p.Unlock()

	require.NoError(t, err, "must recover the aged target instead of sleeping to the caller deadline")
	// Recovery must happen well before the 2s caller deadline (the buggy path
	// returned exactly at 2s). Allow headroom for scheduling.
	select {
	case <-ctx.Done():
		t.Fatal("wait must return before the caller deadline once the target is recoverable")
	default:
	}
	require.Less(t, time.Since(start), 2*time.Second, "wait returned at caller deadline instead of recovering")
	require.True(t, target.writerFenceReleased, "safe writer fence handoff must have completed")
	require.Equal(t, advisorPostCommitPhaseCanonicalReconcile, target.phase)
}

func countMetricRecords(records []metricRecord, name string) int {
	count := 0
	for _, record := range records {
		if record.key == name {
			count++
		}
	}
	return count
}

func TestInstalledStateWritePermitRejectsLegacyWriterWhileAdvisorTargetPending(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.installCPUStateWritePermit()
	revision := p.state.GetRevision()
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, revision)

	err = p.state.SetMachineState(p.state.GetMachineState(), false)

	require.Error(t, err)
	var retryable retryablePartitionCommitError
	require.ErrorAs(t, err, &retryable)
	require.True(t, retryable.Retryable())
	require.Equal(t, revision, p.state.GetRevision())
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
}

func TestAdvisorStateWritePermitBindsTargetAndRevisionAndIsSingleUse(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.installCPUStateWritePermit()
	revision := p.state.GetRevision()
	wrongRevisionTarget := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, revision+1)
	err = p.state.CommitAdvisorStateIfRevision(
		revision,
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		false,
		p.newAdvisorStateWritePermit(wrongRevisionTarget),
	)
	require.Error(t, err)
	require.Equal(t, revision, p.state.GetRevision())

	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, revision)
	permit := p.newAdvisorStateWritePermit(target)
	err = p.state.CommitAdvisorStateIfRevision(
		revision,
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		false,
		permit,
	)
	require.NoError(t, err)
	require.Equal(t, revision+1, p.state.GetRevision())

	err = p.state.CommitAdvisorStateIfRevision(
		revision+1,
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		false,
		permit,
	)
	require.Error(t, err, "a successful permit must not be reusable")

	replacedPermit := p.newAdvisorStateWritePermit(target)
	replacement := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	require.NotSame(t, target, replacement)
	err = p.state.CommitAdvisorStateIfRevision(
		p.state.GetRevision(),
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		false,
		replacedPermit,
	)
	require.Error(t, err, "a permit must not authorize a replaced target")
}

func TestNewDynamicPolicyRestoresPendingWALBeforeStartupStateWriters(t *testing.T) {
	t.Parallel()
	defer state.SetReadonlyState(nil)
	defer state.SetReadWriteState(nil)

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	stateDir := t.TempDir()
	first, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)
	require.NoError(t, first.state.CommitAdvisorStateIfRevision(
		first.state.GetRevision(),
		first.state.GetPodEntries(),
		first.state.GetMachineState(),
		first.state.GetAllowSharedCoresOverlapReclaimedCores(),
		first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
	revision := first.state.GetRevision()
	first.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision)
	require.FileExists(t, first.advisorPostCommitCheckpointPath())

	conf := config.NewConfiguration()
	conf.GenericQRMPluginConfiguration.StateDirectoryConfiguration = &statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir}
	kccConf := generateTestConfiguration(t, "pending-wal-restart", filepath.Join(stateDir, "metaserver"))
	kccMgr, err := kcc.NewDynamicConfigManager(nil, nil, nil, kccConf)
	require.NoError(t, err)
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: topology},
		},
		ConfigurationManager: kccMgr,
	}
	agentCtx := &componentagent.GenericContext{
		GenericContext: &katalystbase.GenericContext{
			EmitterPool: metricspool.DummyMetricsEmitterPool{},
		},
		MetaServer: metaServer,
	}

	ok, _, err := NewDynamicPolicy(agentCtx, conf, nil, "pending-wal-restart")
	require.NoError(t, err)
	require.True(t, ok)

	restartedState, err := state.GetReadWriteState()
	require.NoError(t, err)
	require.Equal(t, revision, restartedState.GetRevision(),
		"constructor must not advance the checkpoint revision before recovering the matching WAL")
	err = restartedState.SetMachineState(restartedState.GetMachineState(), false)
	require.Error(t, err, "the recovered WAL gate must be installed before NewDynamicPolicy returns")
	var retryable retryablePartitionCommitError
	require.ErrorAs(t, err, &retryable)
	require.True(t, retryable.Retryable())
	require.Equal(t, revision, restartedState.GetRevision())
	require.FileExists(t, first.advisorPostCommitCheckpointPath())
}

func TestRemovePodCommitsCanonicalStateAtomically(t *testing.T) {
	t.Parallel()

	const podUID = "remove-pod-atomic"
	gateErr := fmt.Errorf("adjust failed: %w", &DefaultShareResidualQuantityError{
		AdvisedQuantity: 1,
		ResidualSize:    2,
	})
	nonGateErr := errors.New("unrelated adjustment failure")

	newPolicy := func(t *testing.T) (*DynamicPolicy, string, uint64, state.NUMANodeMap) {
		t.Helper()
		topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
		require.NoError(t, err)
		stateDir := t.TempDir()
		policy, err := getTestDynamicPolicyWithoutInitialization(topology, stateDir)
		require.NoError(t, err)
		entries := state.PodEntries{
			podUID: {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podUID,
						ContainerName: "main",
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
					},
					AllocationResult: machine.NewCPUSet(1, 2),
				},
			},
		}
		machineState, err := generateMachineStateFromPodEntries(
			topology, entries, policy.state.GetMachineState())
		require.NoError(t, err)
		require.NoError(t, policy.state.CommitAdvisorState(entries, machineState, false, false, true))
		return policy, stateDir, policy.state.GetRevision(), machineState
	}

	newRegistry := func(t *testing.T, policy *DynamicPolicy) *releaseTrackingAccompanyPlugin {
		t.Helper()
		registry := accompanyresource.NewRegistry()
		plugin := &releaseTrackingAccompanyPlugin{}
		require.NoError(t, registry.RegisterPlugin(plugin))
		policy.accompanyRegistry = registry
		return plugin
	}

	t.Run("recovery persists deletion before accompany release", func(t *testing.T) {
		t.Parallel()
		policy, stateDir, initialRevision, _ := newPolicy(t)
		releasePlugin := newRegistry(t, policy)
		var (
			gotPersist  bool
			gotRevision uint64
		)
		policy.adjustAtRevisionWithContextHook = func(context.Context, state.PodEntries, state.NUMANodeMap, bool, uint64) error {
			return gateErr
		}
		policy.adjustForRecoveryAtRevisionWithContextHook = func(_ context.Context, entries state.PodEntries, machineState state.NUMANodeMap,
			persist bool, expectedRevision uint64,
		) error {
			require.NotContains(t, entries, podUID)
			require.NotNil(t, machineState)
			require.Equal(t, 0, releasePlugin.releaseCount)
			gotPersist = persist
			gotRevision = expectedRevision
			return policy.state.CommitAdvisorStateIfRevision(
				expectedRevision, entries, machineState, false, false, persist)
		}

		_, err := policy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: podUID})
		require.NoError(t, err)
		require.True(t, gotPersist)
		require.Equal(t, initialRevision, gotRevision)
		require.NotContains(t, policy.state.GetPodEntries(), podUID)
		require.Equal(t, initialRevision+1, policy.state.GetRevision())
		require.Equal(t, 1, releasePlugin.releaseCount)

		restarted, err := state.NewCheckpointState(
			&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
			cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic,
			policy.machine.machineInfo.CPUTopology, false,
			state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
		require.NoError(t, err)
		require.NotContains(t, restarted.GetPodEntries(), podUID)
	})

	t.Run("recovery error preserves canonical state and accompany resource", func(t *testing.T) {
		t.Parallel()
		policy, _, initialRevision, initialMachineState := newPolicy(t)
		releasePlugin := newRegistry(t, policy)
		policy.adjustAtRevisionWithContextHook = func(context.Context, state.PodEntries, state.NUMANodeMap, bool, uint64) error {
			return nonGateErr
		}

		_, err := policy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: podUID})
		require.ErrorIs(t, err, nonGateErr)
		require.Contains(t, policy.state.GetPodEntries(), podUID)
		require.True(t, reflect.DeepEqual(policy.state.GetMachineState(), initialMachineState))
		require.Equal(t, initialRevision, policy.state.GetRevision())
		require.Equal(t, 0, releasePlugin.releaseCount)
	})

	t.Run("checkpoint failure rolls canonical state back and preserves accompany resource", func(t *testing.T) {
		t.Parallel()
		policy, stateDir, initialRevision, initialMachineState := newPolicy(t)
		releasePlugin := newRegistry(t, policy)
		require.NoError(t, os.RemoveAll(stateDir))
		require.NoError(t, os.WriteFile(stateDir, []byte("block checkpoint directory"), 0o600))
		policy.adjustAtRevisionWithContextHook = func(context.Context, state.PodEntries, state.NUMANodeMap, bool, uint64) error {
			return gateErr
		}

		_, err := policy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: podUID})
		require.Error(t, err)
		require.Contains(t, policy.state.GetPodEntries(), podUID)
		require.True(t, reflect.DeepEqual(policy.state.GetMachineState(), initialMachineState))
		require.Equal(t, initialRevision, policy.state.GetRevision())
		require.Equal(t, 0, releasePlugin.releaseCount)
	})

	t.Run("real checkpoint failure is retryable without post CAS admission", func(t *testing.T) {
		t.Parallel()
		policy, stateDir, initialRevision, initialMachineState := newPolicy(t)
		releasePlugin := newRegistry(t, policy)
		advisorClient := &removePodTrackingAdvisorClient{}
		policy.config.enableCPUAdvisor = true
		policy.advisor.advisorClient = advisorClient

		handlerCalls := 0
		policy.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
			"must-not-run": func(context.Context, dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
				handlerCalls++
				return errors.New("admission handler must not run during recovery")
			},
		}
		policy.adjustAtRevisionWithContextHook = func(context.Context, state.PodEntries, state.NUMANodeMap, bool, uint64) error {
			return gateErr
		}

		require.NoError(t, os.RemoveAll(stateDir))
		require.NoError(t, os.WriteFile(stateDir, []byte("block checkpoint directory"), 0o600))

		req := &pluginapi.RemovePodRequest{PodUid: podUID}
		_, err := policy.RemovePod(context.Background(), req)
		require.Error(t, err)
		require.Contains(t, policy.state.GetPodEntries(), podUID)
		require.True(t, reflect.DeepEqual(initialMachineState, policy.state.GetMachineState()))
		require.Equal(t, initialRevision, policy.state.GetRevision())
		require.Equal(t, 1, advisorClient.removePodCount)
		require.Zero(t, releasePlugin.releaseCount)
		require.Zero(t, handlerCalls)
		require.False(t, policy.adjustment.cpuSetAdjustmentRetryQueued)
		require.False(t, policy.adjustment.cpuSetAdjustmentRetryAgain)
		require.False(t, policy.adjustment.cpuSetAdjustmentRetryDirty)

		require.NoError(t, os.Remove(stateDir))
		require.NoError(t, os.MkdirAll(stateDir, 0o755))

		_, err = policy.RemovePod(context.Background(), req)
		require.NoError(t, err)
		require.NotContains(t, policy.state.GetPodEntries(), podUID)
		require.Equal(t, initialRevision+1, policy.state.GetRevision())
		require.Equal(t, 2, advisorClient.removePodCount)
		require.Equal(t, 1, releasePlugin.releaseCount)
		require.Zero(t, handlerCalls)
		require.False(t, policy.adjustment.cpuSetAdjustmentRetryQueued)
		require.False(t, policy.adjustment.cpuSetAdjustmentRetryAgain)
		require.True(t, policy.adjustment.cpuSetAdjustmentRetryDirty)
		require.Contains(t, policy.adjustment.cpuSetAdjustmentRetryReasons, dynamicpolicyutil.RetryReasonRecoveryCommit)

		restarted, err := state.NewCheckpointState(
			&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
			cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic,
			policy.machine.machineInfo.CPUTopology, false,
			state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
		require.NoError(t, err)
		require.NotContains(t, restarted.GetPodEntries(), podUID)
		require.Equal(t, initialRevision+1, restarted.GetRevision())
		require.True(t, reflect.DeepEqual(policy.state.GetMachineState(), restarted.GetMachineState()))
	})

	t.Run("release failure retries without repeating state or advisor transaction", func(t *testing.T) {
		t.Parallel()
		policy, _, initialRevision, _ := newPolicy(t)
		releasePlugin := newRegistry(t, policy)
		releasePlugin.releaseErrs = []error{errors.New("release failed")}
		advisorClient := &removePodTrackingAdvisorClient{}
		policy.config.enableCPUAdvisor = true
		policy.advisor.advisorClient = advisorClient

		req := &pluginapi.RemovePodRequest{PodUid: podUID}
		_, err := policy.RemovePod(context.Background(), req)
		require.Error(t, err)
		require.True(t, strings.HasPrefix(err.Error(), "failed to release accompany resource"))
		require.NotContains(t, policy.state.GetPodEntries(), podUID)
		require.Equal(t, initialRevision+1, policy.state.GetRevision())
		require.Equal(t, 1, releasePlugin.releaseCount)
		require.Equal(t, 1, advisorClient.removePodCount)

		committedRevision := policy.state.GetRevision()
		_, err = policy.RemovePod(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, committedRevision, policy.state.GetRevision())
		require.Equal(t, 2, releasePlugin.releaseCount)
		require.Equal(t, 1, advisorClient.removePodCount)
	})
}

func TestRemovePodLastRNBExpandsNonRNBAndCleansOrphanInSingleRevision(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	stateDir := t.TempDir()
	policy, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	policy.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	policy.config.enableCPUAdvisor = true
	policy.advisor.advisorClient = &advisorPoolTestClient{}
	policy.advisor.advisorMonitor, err = timemonitor.NewTimeMonitor(
		"advisor",
		time.Second,
		time.Minute,
		time.Minute,
		"advisor_unhealthy",
		&metrics.DummyMetrics{},
		1,
		true,
	)
	require.NoError(t, err)

	const (
		removedPodUID = "last-rnb"
		orphanPool    = "share-NUMA0"
	)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	allCPUs := numa0.Union(numa1)
	initialEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 allCPUs.Clone(),
				OriginalAllocationResult:         allCPUs.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: numa0.Clone(), 1: numa1.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone(), 1: numa1.Clone()},
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: {
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:                 numa1.Clone(),
				OriginalAllocationResult:         numa1.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{1: numa1.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{1: numa1.Clone()},
			},
		},
		orphanPool: {
			commonstate.FakedContainerName: {
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(orphanPool),
				AllocationResult:                 numa0.Clone(),
				OriginalAllocationResult:         numa0.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: numa0.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone()},
			},
		},
		"non-rnb": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "non-rnb",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
				},
				RequestQuantity:                  float64(allCPUs.Size()),
				AllocationResult:                 numa1.Clone(),
				OriginalAllocationResult:         numa1.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{1: numa1.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{1: numa1.Clone()},
			},
		},
		removedPodUID: {
			"rnb": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        removedPodUID,
					ContainerName: "rnb",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
					},
				},
				RequestQuantity:                  float64(numa0.Size()),
				AllocationResult:                 numa0.Clone(),
				OriginalAllocationResult:         numa0.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: numa0.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone()},
			},
			"shared-owner": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        removedPodUID,
					ContainerName: "shared-owner",
					OwnerPoolName: orphanPool,
					QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
				},
				RequestQuantity:                  float64(numa0.Size()),
				AllocationResult:                 numa0.Clone(),
				OriginalAllocationResult:         numa0.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: numa0.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone()},
			},
		},
	}
	initialMachineState, err := generateMachineStateFromPodEntries(
		topology, initialEntries, policy.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, policy.state.CommitAdvisorState(
		initialEntries, initialMachineState, false, false, true))
	initialRevision := policy.state.GetRevision()

	_, err = policy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: removedPodUID})
	require.NoError(t, err)
	require.Equal(t, initialRevision+1, policy.state.GetRevision(),
		"pod removal, non-RNB expansion, orphan cleanup, and share deletion must share one durable revision")
	require.NotContains(t, policy.state.GetPodEntries(), removedPodUID)
	require.NotContains(t, policy.state.GetPodEntries(), orphanPool)
	require.NotContains(t, policy.state.GetPodEntries(), commonstate.PoolNameShare,
		"recovery must delete stale positive-advice share when no residual remains")
	got := policy.state.GetAllocationInfo("non-rnb", "main").AllocationResult
	require.True(t, got.Equals(allCPUs),
		"non-RNB allocation must expand across all eligible CPUs after deleting the last RNB, got %s", got)

	restarted, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
		cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic,
		topology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Equal(t, initialRevision+1, restarted.GetRevision())
	require.NotContains(t, restarted.GetPodEntries(), removedPodUID)
	require.NotContains(t, restarted.GetPodEntries(), orphanPool)
	require.NotContains(t, restarted.GetPodEntries(), commonstate.PoolNameShare)
	restartedAllocation := restarted.GetAllocationInfo("non-rnb", "main")
	require.NotNil(t, restartedAllocation)
	require.True(t, restartedAllocation.AllocationResult.Equals(allCPUs))
	require.True(t, reflect.DeepEqual(policy.state.GetMachineState(), restarted.GetMachineState()))
}

func TestRemovePodLastOwnerBackfillsOrphanCPUsInSingleRevision(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(12, 1, 3)
	require.NoError(t, err)
	stateDir := t.TempDir()
	policy, err := getTestDynamicPolicyWithInitialization(topology, stateDir)
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	policy.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	policy.config.enableCPUAdvisor = true
	policy.advisor.advisorClient = &advisorPoolTestClient{}
	policy.advisor.advisorMonitor, err = timemonitor.NewTimeMonitor(
		"advisor",
		time.Second,
		time.Minute,
		time.Minute,
		"advisor_unhealthy",
		&metrics.DummyMetrics{},
		1,
		true,
	)
	require.NoError(t, err)

	const (
		removedPodUID = "last-owner"
		orphanPool    = "share-NUMA0"
	)
	orphanCPUs := topology.CPUDetails.CPUsInNUMANodes(0)
	reclaimCPUs := topology.CPUDetails.CPUsInNUMANodes(1)
	shareCPUs := topology.CPUDetails.CPUsInNUMANodes(2)
	initialEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 reclaimCPUs.Clone(),
				OriginalAllocationResult:         reclaimCPUs.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{1: reclaimCPUs.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{1: reclaimCPUs.Clone()},
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: {
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:                 shareCPUs.Clone(),
				OriginalAllocationResult:         shareCPUs.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{2: shareCPUs.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{2: shareCPUs.Clone()},
			},
		},
		orphanPool: {
			commonstate.FakedContainerName: {
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(orphanPool),
				AllocationResult:                 orphanCPUs.Clone(),
				OriginalAllocationResult:         orphanCPUs.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: orphanCPUs.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: orphanCPUs.Clone()},
			},
		},
		removedPodUID: {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        removedPodUID,
					ContainerName: "main",
					OwnerPoolName: orphanPool,
					QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
				},
				RequestQuantity:                  float64(orphanCPUs.Size()),
				AllocationResult:                 orphanCPUs.Clone(),
				OriginalAllocationResult:         orphanCPUs.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: orphanCPUs.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: orphanCPUs.Clone()},
			},
		},
	}
	initialMachineState, err := generateMachineStateFromPodEntries(
		topology, initialEntries, policy.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, policy.state.CommitAdvisorState(
		initialEntries, initialMachineState, false, false, true))
	initialRevision := policy.state.GetRevision()

	// Exercise the revision-scoped adjustment candidate produced by RemovePod
	// before its caller-local cleanup, so the common adjustment boundary must
	// canonicalize the orphan pool before deriving quantities.
	removedEntries := policy.state.GetPodEntries()
	delete(removedEntries, removedPodUID)
	removedMachineState, err := generateMachineStateFromPodEntries(
		topology, removedEntries, policy.state.GetMachineState())
	require.NoError(t, err)
	beforeMachineState := policy.state.GetMachineState()
	err = policy.adjustAllocationEntriesAtRevision(
		removedEntries, removedMachineState, true, initialRevision)
	require.NoError(t, err)
	require.Equal(t, initialRevision+1, policy.state.GetRevision(),
		"pod removal, orphan cleanup, share backfill, and machine state must use one durable revision")

	committedEntries := policy.state.GetPodEntries()
	require.NotContains(t, committedEntries, removedPodUID)
	require.NotContains(t, committedEntries, orphanPool)
	committedShare, err := committedEntries.GetCPUSetForPool(commonstate.PoolNameShare)
	require.NoError(t, err)
	require.True(t, orphanCPUs.IsSubsetOf(committedShare),
		"orphan CPUs %s must be backfilled into committed default share %s", orphanCPUs, committedShare)
	committedMachineState := policy.state.GetMachineState()
	expectedCommittedMachineState, err := state.GenerateMachineStateFromPodEntries(
		topology, committedEntries, beforeMachineState)
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(expectedCommittedMachineState, committedMachineState),
		"committed machine state must be self-consistent with committed pod entries")

	restarted, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
		cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic,
		topology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Equal(t, initialRevision+1, restarted.GetRevision())
	require.Equal(t, committedEntries, restarted.GetPodEntries())
	require.True(t, reflect.DeepEqual(committedMachineState, restarted.GetMachineState()))
}

func TestAllocate(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		name                                  string
		req                                   *pluginapi.ResourceRequest
		expectedResp                          *pluginapi.ResourceAllocationResponse
		enableReclaim                         bool
		wantError                             bool
		cpuTopology                           *machine.CPUTopology
		enhancementDefaultValues              map[string]string
		allowSharedCoresOverlapReclaimedCores bool
	}{
		{
			name: "req for init container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_INIT,
				ContainerIndex:   0,
				ResourceName:     string(v1.ResourceCPU),
				AllocationResult: nil,
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for container of debug pod",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Annotations: map[string]string{
					podDebugAnnoKey: "",
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							IsNodeResource:   false,
							IsScalarResource: true,
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for shared_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 14,
							AllocationResult:  "1,3-15",
							TopologyAssignments: map[uint64]uint64{
								0: 3,
								1: 3,
								2: 4,
								3: 4,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 4,
							AllocationResult:  machine.NewCPUSet(1, 3, 9, 11).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 2,
								1: 2,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 3,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
								coreconsts.QRMPodAnnotationTopologyAllocationKey:   `{"Numa":{"0":{"allocated":{"cpu":"3"},"attributes":{"CpusetCpus":"1,8-9"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & not numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2,
							AllocationResult:  machine.NewCPUSet(1, 9).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 2,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationMemoryEnhancementNumaExclusive: "false",
								cpuconsts.CPUStateAnnotationKeyNUMAHint:            "0",
								coreconsts.QRMPodAnnotationTopologyAllocationKey:   `{"Numa":{"0":{"allocated":{"cpu":"2"},"attributes":{"CpusetCpus":"1,9"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: "false",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & default numa_exclusive true main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 3,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
								coreconsts.QRMPodAnnotationTopologyAllocationKey:   `{"Numa":{"0":{"allocated":{"cpu":"3"},"attributes":{"CpusetCpus":"1,8-9"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: cpuTopology,
			enhancementDefaultValues: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			},
		},
		{
			name: "req for dedicated_cores with numa_binding without default numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2,
							AllocationResult:  machine.NewCPUSet(1, 9).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 2,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
								coreconsts.QRMPodAnnotationTopologyAllocationKey: `{"Numa":{"0":{"allocated":{"cpu":"2"},"attributes":{"CpusetCpus":"1,9"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for shared_cores numa_binding main container with enableReclaim false",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 3,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
								coreconsts.QRMPodAnnotationTopologyAllocationKey: `{"Numa":{"0":{"allocated":{"cpu":"2"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:          "req for shared_cores numa_binding main container with enableReclaim true",
			enableReclaim: true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0.3,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 3,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
								coreconsts.QRMPodAnnotationTopologyAllocationKey: `{"Numa":{"0":{"allocated":{"cpu":"300m"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:      "req for shared_cores numa_binding main container with invalid hint",
			wantError: true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0.3,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
			expectedResp: nil,
			cpuTopology:  cpuTopology,
		},
		{
			name: "req for shared_cores with numa_binding with allowSharedCoresOverlapReclaimedCores set to true",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 3,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
								coreconsts.QRMPodAnnotationTopologyAllocationKey: `{"Numa":{"0":{"allocated":{"cpu":"1"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for system_cores with specified cpuset pool",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSystemCores,
					consts.PodAnnotationCPUEnhancementKey: `{"cpuset_pool": "reserve"}`,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2, // reserve pool
							AllocationResult:  machine.NewCPUSet(0, 2).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 1,
								1: 1,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
							Annotations: map[string]string{
								"cpuset_pool":                   "reserve",
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSystemCores,
					consts.PodAnnotationCPUEnhancementCPUSet: "reserve",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for system_cores without specified cpuset pool",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: float64(cpuTopology.CPUDetails.CPUs().Size()), // default for all cpuset
							AllocationResult:  cpuTopology.CPUDetails.CPUs().String(),
							TopologyAssignments: map[uint64]uint64{
								0: 4,
								1: 4,
								2: 4,
								3: 4,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:          "req for reclaim actual numa_binding main container with enableReclaim true",
			enableReclaim: true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0.3,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2,
							AllocationResult:  machine.NewCPUSet(1, 9).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 2,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
								coreconsts.QRMPodAnnotationTopologyAllocationKey: `{"Numa":{"0":{"allocated":{"cpu":"300m"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:          "req for reclaim non-actual numa_binding main container with enableReclaim true",
			enableReclaim: true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0.3,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 4,
							AllocationResult:  machine.NewCPUSet(1, 3, 9, 11).String(),
							TopologyAssignments: map[uint64]uint64{
								0: 2,
								1: 2,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0, 1},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated cores numa binding with distribute evenly across numa enabled and numa number more than 1",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"distribute_evenly_across_numa": "true", "katalyst.kubewharf.io/numa_number": "2"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{2, 3},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 4,
							// CPUs 4 and 12 are from NUMA 2 and CPUs 6 and 14 are from NUMA 3
							AllocationResult: machine.NewCPUSet(4, 6, 12, 14).String(),
							TopologyAssignments: map[uint64]uint64{
								2: 2,
								3: 2,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{2, 3},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
								consts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
								coreconsts.QRMPodAnnotationTopologyAllocationKey:             `{"Numa":{"2":{"allocated":{"cpu":"2"},"attributes":{"CpusetCpus":"4,12"}},"3":{"allocated":{"cpu":"2"},"attributes":{"CpusetCpus":"6,14"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated cores numa binding with full pcpus pairing enabled",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"full_pcpus_pairing": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{2},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2,
							// Allocate full physical core of CPUs 4 and 12
							AllocationResult: machine.NewCPUSet(4, 12).String(),
							TopologyAssignments: map[uint64]uint64{
								2: 2,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{2},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "2",
								"full_pcpus_pairing":                             "true",
								coreconsts.QRMPodAnnotationTopologyAllocationKey: `{"Numa":{"2":{"allocated":{"cpu":"2"},"attributes":{"CpusetCpus":"4,12"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementFullPCPUsPairing: consts.PodAnnotationCPUEnhancementFullPCPUsPairingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated cores numa binding with full pcpus pairing enabled returns error if there are not enough physical cores",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"full_pcpus_pairing": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			// Not able to take 2 physical cores from NUMA 0 because of one reserved CPU
			wantError:   true,
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated cores numa binding with full pcpus pairing enabled can allocate HT after physical cores are allocated",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 6,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"distribute_evenly_across_numa": "true", "full_pcpus_pairing": "true", "katalyst.kubewharf.io/numa_number": "2"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{2, 3},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 6,
							AllocationResult:  machine.NewCPUSet(4, 5, 6, 7, 12, 14).String(),
							TopologyAssignments: map[uint64]uint64{
								2: 3,
								3: 3,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{2, 3},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
								consts.PodAnnotationCPUEnhancementFullPCPUsPairing:           consts.PodAnnotationCPUEnhancementFullPCPUsPairingEnable,
								consts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
								coreconsts.QRMPodAnnotationTopologyAllocationKey:             `{"Numa":{"2":{"allocated":{"cpu":"3"},"attributes":{"CpusetCpus":"4-5,12"}},"3":{"allocated":{"cpu":"3"},"attributes":{"CpusetCpus":"6-7,14"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementFullPCPUsPairing:           consts.PodAnnotationCPUEnhancementFullPCPUsPairingEnable,
					consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated cores numa binding with numa number more than 1 can cross numa",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 6,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_number": "2"}`,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{2, 3},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 6,
							// Will allocate 4, 5, 12, 13 from numa 2 first and then 6, 14 from numa 3
							AllocationResult: machine.NewCPUSet(4, 5, 6, 12, 13, 14).String(),
							TopologyAssignments: map[uint64]uint64{
								2: 4,
								3: 2,
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{2, 3},
										Preferred: true,
									},
								},
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationCPUEnhancementNumaNumber:     "2",
								coreconsts.QRMPodAnnotationTopologyAllocationKey: `{"Numa":{"2":{"allocated":{"cpu":"4"},"attributes":{"CpusetCpus":"4-5,12-13"}},"3":{"allocated":{"cpu":"2"},"attributes":{"CpusetCpus":"6,14"}}}}`,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:     "2",
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocate")
			as.Nil(err)

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
			as.Nil(err)

			if tc.enhancementDefaultValues != nil {
				dynamicPolicy.config.qosConfig.QoSEnhancementDefaultValues = tc.enhancementDefaultValues
			}

			if tc.enableReclaim {
				dynamicPolicy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			}

			if tc.allowSharedCoresOverlapReclaimedCores {
				dynamicPolicy.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)
			}

			// TestAllocate's dedicated_cores expectations cover baseline NUMA
			// allocation shapes; reclaim-free preference is covered by
			// TestDynamicPolicy_allocateNumaBindingCPUs with explicit reclaim
			// inputs. Keep this fixture independent from the default reclaim pool.
			if tc.req.Labels[consts.PodAnnotationQoSLevelKey] == consts.PodAnnotationQoSLevelDedicatedCores {
				dynamicPolicy.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
					AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult:         machine.NewCPUSet(),
					OriginalAllocationResult: machine.NewCPUSet(),
				}, false)
			}

			resp, err := dynamicPolicy.Allocate(context.Background(), tc.req)
			if tc.wantError {
				as.NotNil(err)
				return
			}

			as.Nilf(err, "failed in test case: %s", tc.name)
			tc.expectedResp.PodUid = tc.req.PodUid
			as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.name)

			if tc.allowSharedCoresOverlapReclaimedCores {
				err := dynamicPolicy.adjustAllocationEntries(dynamicPolicy.state.GetPodEntries(), dynamicPolicy.state.GetMachineState(), true)
				as.NotNil(err)
			}

			_ = os.RemoveAll(tmpDir)
		})
	}
}

func TestAllocateFailureRollbackDoesNotOverwriteNewerState(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	const failedPodUID = "failed-allocate-rollback"
	const newerPodUID = "newer-allocate-submission"
	originalReclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, originalReclaim)
	originalAllocation := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        failedPodUID,
			PodNamespace:  "default",
			PodName:       failedPodUID,
			ContainerName: "main",
			OwnerPoolName: commonstate.PoolNameDedicated,
			QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
		},
		AllocationResult:         machine.NewCPUSet(6),
		OriginalAllocationResult: machine.NewCPUSet(6),
	}
	p.state.SetAllocationInfo(failedPodUID, "main", originalAllocation, false)
	p.allocation.allocationHandlers[consts.PodAnnotationQoSLevelSharedCores] = func(_ context.Context, req *pluginapi.ResourceRequest, _ bool) (*pluginapi.ResourceAllocationResponse, error) {
		p.state.SetAllocationInfo(req.PodUid, req.ContainerName, &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        req.PodUid,
				PodNamespace:  req.PodNamespace,
				PodName:       req.PodName,
				ContainerName: req.ContainerName,
				QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
			},
			AllocationResult:         machine.NewCPUSet(1),
			OriginalAllocationResult: machine.NewCPUSet(1),
		}, false)
		failedReclaim := originalReclaim.Clone()
		failedReclaim.AllocationResult = machine.NewCPUSet(7)
		failedReclaim.OriginalAllocationResult = machine.NewCPUSet(7)
		p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, failedReclaim, false)

		p.Unlock()
		p.state.SetAllocationInfo(newerPodUID, "main", &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        newerPodUID,
				PodNamespace:  "default",
				PodName:       newerPodUID,
				ContainerName: "main",
				OwnerPoolName: commonstate.PoolNameDedicated,
				QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
			},
			AllocationResult:         machine.NewCPUSet(7),
			OriginalAllocationResult: machine.NewCPUSet(7),
		}, false)
		p.Lock()

		return nil, fmt.Errorf("injected allocation failure")
	}

	req := &pluginapi.ResourceRequest{
		PodUid:        failedPodUID,
		PodNamespace:  "default",
		PodName:       failedPodUID,
		ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	_, err = p.Allocate(context.Background(), req)
	require.ErrorContains(t, err, "injected allocation failure")
	require.NotNil(t, p.state.GetAllocationInfo(newerPodUID, "main"),
		"failed allocation rollback must not overwrite newer state")
	restored := p.state.GetAllocationInfo(failedPodUID, "main")
	require.NotNil(t, restored, "failed reallocation must restore the previous allocation")
	require.True(t, restored.AllocationResult.Equals(originalAllocation.AllocationResult),
		"restored allocation=%s, want %s", restored.AllocationResult, originalAllocation.AllocationResult)
	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 3, 4, 5)),
		"reclaim allocation=%s, want latest-state replan 2-5", reclaim.AllocationResult)
}

func TestAllocateFailureRollbackDoesNotOverwriteNewerSameContainerState(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	const podUID = "same-container-rollback"
	originalAllocation := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       "original",
			ContainerName: "main",
			QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
		},
		AllocationResult:         machine.NewCPUSet(0),
		OriginalAllocationResult: machine.NewCPUSet(0),
	}
	p.state.SetAllocationInfo(podUID, "main", originalAllocation, false)

	olderStarted := make(chan struct{})
	releaseOlder := make(chan struct{})
	p.allocation.allocationHandlers[consts.PodAnnotationQoSLevelSharedCores] = func(
		_ context.Context,
		req *pluginapi.ResourceRequest,
		_ bool,
	) (*pluginapi.ResourceAllocationResponse, error) {
		if req.PodName == "older" {
			p.state.SetAllocationInfo(req.PodUid, req.ContainerName, &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        req.PodUid,
					PodNamespace:  req.PodNamespace,
					PodName:       req.PodName,
					ContainerName: req.ContainerName,
					QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult:         machine.NewCPUSet(1),
				OriginalAllocationResult: machine.NewCPUSet(1),
			}, false)
			close(olderStarted)
			p.Unlock()
			<-releaseOlder
			p.Lock()
			return nil, fmt.Errorf("injected older allocation failure")
		}

		p.state.SetAllocationInfo(req.PodUid, req.ContainerName, &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        req.PodUid,
				PodNamespace:  req.PodNamespace,
				PodName:       req.PodName,
				ContainerName: req.ContainerName,
				QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
			},
			AllocationResult:         machine.NewCPUSet(2, 3, 4),
			OriginalAllocationResult: machine.NewCPUSet(2, 3, 4),
		}, false)
		return &pluginapi.ResourceAllocationResponse{
			PodUid:        req.PodUid,
			PodName:       req.PodName,
			ContainerName: req.ContainerName,
			ResourceName:  req.ResourceName,
		}, nil
	}

	newRequest := func(podName string, quantity float64) *pluginapi.ResourceRequest {
		return &pluginapi.ResourceRequest{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       podName,
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN,
			ResourceName:  string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): quantity,
			},
			Labels: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
		}
	}

	olderResult := make(chan error, 1)
	go func() {
		_, err := p.Allocate(context.Background(), newRequest("older", 2))
		olderResult <- err
	}()
	<-olderStarted

	type allocationResult struct {
		resp *pluginapi.ResourceAllocationResponse
		err  error
	}
	newerResult := make(chan allocationResult, 1)
	go func() {
		resp, err := p.Allocate(context.Background(), newRequest("newer", 3))
		newerResult <- allocationResult{resp: resp, err: err}
	}()
	select {
	case result := <-newerResult:
		t.Fatalf("newer same-container allocation completed before the older request released ownership: resp=%v err=%v",
			result.resp, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseOlder)
	require.ErrorContains(t, <-olderResult, "injected older allocation failure")
	result := <-newerResult
	require.NoError(t, result.err)
	require.NotNil(t, result.resp)

	latest := p.state.GetAllocationInfo(podUID, "main")
	require.NotNil(t, latest)
	require.Equal(t, "newer", latest.PodName)
	require.True(t, latest.AllocationResult.Equals(machine.NewCPUSet(2, 3, 4)),
		"latest allocation=%s, want 2-4", latest.AllocationResult)
}

func TestAllocateReturnsCheckpointFailure(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, stateDir)
	require.NoError(t, err)
	failingState := &storeFailureState{
		State: p.state,
		err:   fmt.Errorf("injected checkpoint failure"),
	}
	p.state = failingState
	p.allocation.allocationHandlers[consts.PodAnnotationQoSLevelSharedCores] = func(
		_ context.Context,
		req *pluginapi.ResourceRequest,
		_ bool,
	) (*pluginapi.ResourceAllocationResponse, error) {
		p.state.SetAllocationInfo(req.PodUid, req.ContainerName, &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        req.PodUid,
				PodNamespace:  req.PodNamespace,
				PodName:       req.PodName,
				ContainerName: req.ContainerName,
				OwnerPoolName: commonstate.PoolNameDedicated,
				QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
			},
			AllocationResult:         machine.NewCPUSet(7),
			OriginalAllocationResult: machine.NewCPUSet(7),
		}, false)
		return &pluginapi.ResourceAllocationResponse{
			PodUid:        req.PodUid,
			ContainerName: req.ContainerName,
			ResourceName:  req.ResourceName,
		}, nil
	}

	req := &pluginapi.ResourceRequest{
		PodUid:        "checkpoint-failure",
		PodNamespace:  "default",
		PodName:       "checkpoint-failure",
		ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 1,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	resp, err := p.Allocate(context.Background(), req)
	require.Nil(t, resp)
	require.ErrorContains(t, err, "store allocation state")
	require.ErrorContains(t, err, "injected checkpoint failure")
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName),
		"checkpoint failure must roll back the in-memory allocation")
	require.True(t, p.state.GetMachineState()[0].AllocatedCPUSet.Intersection(machine.NewCPUSet(7)).IsEmpty(),
		"checkpoint failure must remove the failed allocation from machine state")
	require.Equal(t, 1, failingState.storeCalls,
		"failed allocation persistence must be surfaced while rollback uses atomic state commit")

	wantRestartEntries := p.state.GetPodEntries()
	restarted, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, stateDir)
	require.NoError(t, err)
	require.Equal(t, wantRestartEntries, restarted.state.GetPodEntries(),
		"failed allocation rollback must persist the canonical state used by in-memory policy")
	require.Nil(t, restarted.state.GetAllocationInfo(req.PodUid, req.ContainerName),
		"failed allocation must not appear after restart")
}

func TestAllocateForPod(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateForPod")
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	req := &pluginapi.PodResourceRequest{
		PodUid:       string(uuid.NewUUID()),
		PodNamespace: "testNamespace",
		PodName:      "testName",
		ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
	}
	_, err = dynamicPolicy.AllocateForPod(context.Background(), req)
	as.ErrorIs(err, util.ErrNotImplemented)

	_ = os.RemoveAll(tmpDir)
}

func TestAllocatePreservesOriginalPodMetaForHandler(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.podAnnotationKeptKeys = nil
	p.config.podLabelKeptKeys = nil

	const (
		spdLabelKey      = "spd.katalyst.kubewharf.io/workload"
		enhancementKey   = "enhancement.katalyst.kubewharf.io/custom"
		originalLabel    = "spd-workload"
		originalEnhance  = "enabled"
		mutatedAfterCopy = "mutated"
	)
	inputLabels := map[string]string{
		consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		spdLabelKey:                     originalLabel,
	}
	inputAnnotations := map[string]string{
		consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		enhancementKey:                  originalEnhance,
	}
	req := &pluginapi.ResourceRequest{
		PodUid:        "pod-uid",
		PodNamespace:  "pod-namespace",
		PodName:       "pod-name",
		ContainerName: "container-name",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 1,
		},
		Labels:      inputLabels,
		Annotations: inputAnnotations,
	}

	handlerCalled := false
	p.allocation.allocationHandlers[consts.PodAnnotationQoSLevelSharedCores] = func(
		ctx context.Context, filteredReq *pluginapi.ResourceRequest, _ bool,
	) (*pluginapi.ResourceAllocationResponse, error) {
		handlerCalled = true
		require.NotContains(t, filteredReq.Labels, spdLabelKey)
		require.NotContains(t, filteredReq.Annotations, enhancementKey)

		inputLabels[spdLabelKey] = mutatedAfterCopy
		inputAnnotations[enhancementKey] = mutatedAfterCopy
		originalPodMeta, ok := allocationPodMetaFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, types.UID(req.PodUid), originalPodMeta.UID)
		require.Equal(t, req.PodNamespace, originalPodMeta.Namespace)
		require.Equal(t, req.PodName, originalPodMeta.Name)
		require.Equal(t, originalLabel, originalPodMeta.Labels[spdLabelKey])
		require.Equal(t, originalEnhance, originalPodMeta.Annotations[enhancementKey])
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	_, err = p.Allocate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, handlerCalled)
}

func TestGetTopologyHints(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	highDensityCPUTopology, err := machine.GenerateDummyCPUTopology(384, 2, 12)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		name                     string
		req                      *pluginapi.ResourceRequest
		podEntries               state.PodEntries
		cpuNUMAHintPreferPolicy  string
		expectedResp             *pluginapi.ResourceHintsResponse
		expectErr                bool
		cpuTopology              *machine.CPUTopology
		numaHeadroom             map[int]float64
		enhancementDefaultValues map[string]string
		numaNumberAnnotationKey  string
		numaIDsAnnotationKey     string
	}{
		{
			name: "req for system_cores container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for container of debug pod",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					podDebugAnnoKey: "",
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1},
								Preferred: false,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1, 2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & not numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: "false",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & default numa_exclusive true main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1},
								Preferred: false,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1, 2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: cpuTopology,
			enhancementDefaultValues: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			},
		},
		{
			name: "req for dedicated_cores with numa_binding & without default numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for shared_cores with numa_binding main container with default cpuNUMAHintPreferPolicy(spreading)",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812kkk": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812kkk",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,8,9"),
						OriginalAllocationResult: machine.MustParse("1,8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},

						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},

						RampUp:                   false,
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 2,
					},
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 1,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1},
								Preferred: false,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:                    "req for shared_cores with numa_binding main container with packing cpuNUMAHintPreferPolicy(packing)",
			cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyPacking,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812kkk": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812kkk",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,8,9"),
						OriginalAllocationResult: machine.MustParse("1,8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 2,
					},
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 1,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:                    "req for shared_cores with numa_binding main container with dynamic_packing cpuNUMAHintPreferPolicy(apply packing)",
			cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812kkk": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812kkk",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
							},
							QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
							OwnerPoolName: commonstate.PoolNameDedicated,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,8,9"),
						OriginalAllocationResult: machine.MustParse("1,8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},

						RampUp:                   false,
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 2,
					},
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:                    "req for shared_cores with numa_binding main container with dynamic_packing cpuNUMAHintPreferPolicy(apply spreading)",
			cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812kkk": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812kkk",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,8,9"),
						OriginalAllocationResult: machine.MustParse("1,8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				"share-NUMA2": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA2"),
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"share-NUMA3": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812iii": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812iii",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA2",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						RequestQuantity: 3,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 3,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & numa_exclusive main container in high density machine",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 380,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: highDensityCPUTopology,
		},
		{
			name: "req for reclaimed_cores with actual numa_binding main container",
			numaHeadroom: map[int]float64{
				0: 2,
				1: 1,
				2: 2.5,
				3: 3,
			},
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				"share-NUMA2": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA2"),
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"share-NUMA3": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812iii": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812iii",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA2",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						RequestQuantity: 3,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 3,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for reclaimed_cores with non-actual numa_binding main container",
			numaHeadroom: map[int]float64{
				0: 2,
				1: 1,
				2: 2.5,
				3: 3,
			},
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				"share-NUMA2": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA2"),
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"share-NUMA3": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812iii": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812iii",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA2",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						RequestQuantity: 3,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 3,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for reclaimed_cores with actual numa_binding main container and an actual numa_binding container existed",
			numaHeadroom: map[int]float64{
				0: 2,
				1: 1,
				2: 2.5,
				3: 3,
			},
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9"),
						OriginalAllocationResult: machine.MustParse("9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				"share-NUMA2": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA2"),
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"share-NUMA3": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812iii": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812iii",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA2",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						RequestQuantity: 3,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 3,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated numa binding container with align by socket enabled for empty machine",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 7,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"align_by_socket": "true"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				// 2 reserved CPU will be occupied: 1 from NUMA 0 and 1 from NUMA 1, so only NUMA 2 and NUMA 3 can satisfy the
				// align by socket requirement.
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationCPUEnhancementAlignBySocket:    consts.PodAnnotationCPUEnhancementAlignBySocketEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated numa binding container with align by socket enabled for machine with existing allocations",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 7,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"align_by_socket": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4,5"),
						OriginalAllocationResult: machine.MustParse("4,5"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
						},
						RequestQuantity: 2,
					},
				},
			},
			// No hints because the container should be allocated within 1 socket.
			// Socket 1: NUMA 0 and 1 have 2 reserved CPUs and is not sufficient.
			// Socket 2: NUMA 2 and 3 have 2 allocated CPUs and is not sufficient.
			expectedResp: nil,
			expectErr:    true,
			cpuTopology:  cpuTopology,
		},
		{
			name: "req for dedicated numa binding container with align by socket enabled for across 2 sockets",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 9, // Each socket has 8 CPUs
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"align_by_socket": "true"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 2, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1, 2, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationCPUEnhancementAlignBySocket:    consts.PodAnnotationCPUEnhancementAlignBySocketEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for full pcpus pairing enabled for dedicated numa binding container with existing allocations",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 3, // needs 1 physical cores
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"full_pcpus_pairing": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("8,9"), // 2 physical cores from NUMA 0 is occupied
						OriginalAllocationResult: machine.MustParse("8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8, 9),
						},
						RequestQuantity: 2,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationCPUEnhancementFullPCPUsPairing: consts.PodAnnotationCPUEnhancementFullPCPUsPairingEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "full pcpus pairing for dedicated numa binding with numa exclusive container returns error",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 6,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"full_pcpus_pairing": "true"}`,
				},
			},
			expectErr:   true,
			cpuTopology: cpuTopology,
		},
		{
			name: "Not enough physical CPUs when full PCPUs pairing is enabled returns error",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 13, // needs 7 physical cores
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"full_pcpus_pairing": "true"}`,
				},
			},
			// No hints as there are only 6 available physical cores in this case
			expectedResp: nil,
			expectErr:    true,
			cpuTopology:  cpuTopology,
		},
		{
			name: "distribute evenly across numa with numa binding without numa exclusive can split across NUMAs when numa number is more than 1",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 12,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"distribute_evenly_across_numa": "true", "katalyst.kubewharf.io/numa_number": "4"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:                 "4",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "distribute evenly across numa with numa binding without numa exclusive cannot be distributed when numa number is less than 2",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 12,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"distribute_evenly_across_numa": "true"}`,
				},
			},
			expectedResp: nil,
			expectErr:    true,
			cpuTopology:  cpuTopology,
		},
		{
			name: "distribute evenly across numa with full pcpus pairing makes sure that physical cores can evenly spread across NUMAs",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 8, // 4 physical cores can be spread across either 2 or 4 NUMA nodes
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"distribute_evenly_across_numa": "true", "full_pcpus_pairing": "true", "katalyst.kubewharf.io/numa_number": "2"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					consts.PodAnnotationCPUEnhancementFullPCPUsPairing:           consts.PodAnnotationCPUEnhancementFullPCPUsPairingEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "distribute evenly across numa with numa exclusive returns error",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 6,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"distribute_evenly_across_numa": "true"}`,
				},
			},
			expectErr:   true,
			cpuTopology: cpuTopology,
		},
		{
			name: "req with full pcpus pairing with odd number of cpu request can still allocate full physical cores in single NUMA node",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 3, // 3 CPUs result in 1 physical cores
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"full_pcpus_pairing": "true"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationCPUEnhancementFullPCPUsPairing: consts.PodAnnotationCPUEnhancementFullPCPUsPairingEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req with full pcpus pairing with odd number of cores request can still allocate full physical cores across multiple NUMA nodes",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 6, // 6 CPUs result in 3 physical cores
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"distribute_evenly_across_numa": "true", "full_pcpus_pairing": "true", "katalyst.kubewharf.io/numa_number": "3"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9"), // 0 and 9 from NUMA 0 are allocated, which means NUMA 0 has no physical cores left
						OriginalAllocationResult: machine.MustParse("9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
						},
						RequestQuantity: 2,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1, 2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					consts.PodAnnotationCPUEnhancementFullPCPUsPairing:           consts.PodAnnotationCPUEnhancementFullPCPUsPairingEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:                 "3",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req with numa number gives valid hints when each numa has 1 CPU allocated",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 8,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_number": "4"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9"), // 0 and 9 from NUMA 0 are allocated
						OriginalAllocationResult: machine.MustParse("9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
						},
						RequestQuantity: 1,
					},
				},
				"273d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "273d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("2"), // 2 from NUMA 1 is allocated
						OriginalAllocationResult: machine.MustParse("2"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(2),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(2),
						},
						RequestQuantity: 1,
					},
				},
				"173d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "173d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4"), // 2 from NUMA 1 is allocated
						OriginalAllocationResult: machine.MustParse("4"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4),
						},
						RequestQuantity: 1,
					},
				},
				"073d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "073d08e4-7a6b-4293-aaaf-b135ff812ooo",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6"), // 2 from NUMA 1 is allocated
						OriginalAllocationResult: machine.MustParse("6"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6),
						},
						RequestQuantity: 1,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:     "4",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req with numa number makes sure that the hints only belong to those numa nodes",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_number": "2"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:       "2",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req with numa ID for one numa node only",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_ids": "1"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationCPUEnhancementNumaIDs:          "1",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req with numa ID for multiple numa nodes",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_ids": "1-3"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1, 2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationCPUEnhancementNumaIDs:          "1-3",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "numa IDs will override numa number",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_number": "2", "katalyst.kubewharf.io/numa_ids": "0-2"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationCPUEnhancementNumaIDs:          "0-2",
					consts.PodAnnotationCPUEnhancementNumaNumber:       "2",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:                    "custom numa number and numa ids annotation are supported",
			numaNumberAnnotationKey: "custom_numa_number_annotation",
			numaIDsAnnotationKey:    "custom_numa_ids_annotation",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"custom_numa_number_annotation": "2", "custom_numa_ids_annotation": "0-2"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					"custom_numa_number_annotation":                    "2",
					"custom_numa_ids_annotation":                       "0-2",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req with numa number makes sure that the hints only belong to those numa nodes",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_number": "2"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationCPUEnhancementNumaNumber:       "2",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req with numa ID for one numa node only",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_ids": "1"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationCPUEnhancementNumaIDs:          "1",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req with numa ID for multiple numa nodes",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_ids": "1-3"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1, 2, 3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationCPUEnhancementNumaIDs:          "1-3",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "numa IDs will override numa number",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"katalyst.kubewharf.io/numa_number": "2", "katalyst.kubewharf.io/numa_ids": "0-2"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationCPUEnhancementNumaIDs:          "0-2",
					consts.PodAnnotationCPUEnhancementNumaNumber:       "2",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:                    "custom numa number and numa ids annotation are supported",
			numaNumberAnnotationKey: "custom_numa_number_annotation",
			numaIDsAnnotationKey:    "custom_numa_ids_annotation",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
					consts.PodAnnotationCPUEnhancementKey:    `{"custom_numa_number_annotation": "2", "custom_numa_ids_annotation": "0-2"}`,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodName:        testName,
				PodNamespace:   testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					"custom_numa_number_annotation":                    "2",
					"custom_numa_ids_annotation":                       "0-2",
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			as := require.New(t)
			tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyHints")
			as.Nil(err)

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
			as.Nil(err)

			if tc.enhancementDefaultValues != nil {
				dynamicPolicy.config.qosConfig.QoSEnhancementDefaultValues = tc.enhancementDefaultValues
			}

			if tc.podEntries != nil {
				machineState, err := generateMachineStateFromPodEntries(tc.cpuTopology, tc.podEntries, nil)
				as.Nil(err)

				dynamicPolicy.state.SetPodEntries(tc.podEntries, true)
				dynamicPolicy.state.SetMachineState(machineState, true)
			}

			if tc.numaHeadroom != nil {
				dynamicPolicy.state.SetNUMAHeadroom(tc.numaHeadroom, true)
			}

			if tc.cpuNUMAHintPreferPolicy != "" {
				dynamicPolicy.config.conf.CPUNUMAHintPreferPolicy = tc.cpuNUMAHintPreferPolicy

				if dynamicPolicy.config.conf.CPUNUMAHintPreferPolicy == cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking {
					dynamicPolicy.config.conf.CPUNUMAHintPreferLowThreshold = 0.5
				}
			}

			if tc.numaNumberAnnotationKey != "" {
				dynamicPolicy.config.numaNumberAnnotationKey = tc.numaNumberAnnotationKey
			}

			if tc.numaIDsAnnotationKey != "" {
				dynamicPolicy.config.numaIDsAnnotationKey = tc.numaIDsAnnotationKey
			}

			dynamicPolicy.hintOpt.sharedCoresNUMABindingHintOptimizer, err = canonical.NewCanonicalHintOptimizer(dynamicPolicy.generateHintOptimizerFactoryOptions())
			as.NoError(err)

			dynamicPolicy.hintOpt.dedicatedCoresNUMABindingHintOptimizer = &hintoptimizer.DummyHintOptimizer{}

			resp, err := dynamicPolicy.GetTopologyHints(context.Background(), tc.req)
			if tc.expectErr {
				as.NotNil(err)
				return
			} else {
				as.Nil(err)
			}

			tc.expectedResp.PodUid = tc.req.PodUid
			as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.name)

			_ = os.RemoveAll(tmpDir)
		})
	}
}

func TestGetPodTopologyHints(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetPodTopologyHints")
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	req := &pluginapi.PodResourceRequest{
		PodUid:       string(uuid.NewUUID()),
		PodNamespace: "testNamespace",
		PodName:      "testName",
		ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
	}
	_, err = dynamicPolicy.GetPodTopologyHints(context.Background(), req)
	as.ErrorIs(err, util.ErrNotImplemented)

	_ = os.RemoveAll(tmpDir)
}

func TestGetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyAwareAllocatableResources")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	resp, err := dynamicPolicy.GetTopologyAwareAllocatableResources(context.Background(), &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
	as.Nil(err)

	as.Equal(&pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			string(v1.ResourceCPU): {
				IsNodeResource:   false,
				IsScalarResource: true,
				TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
					{ResourceValue: 3, Node: 0},
					{ResourceValue: 3, Node: 1},
					{ResourceValue: 4, Node: 2},
					{ResourceValue: 4, Node: 3},
				},
				TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
					{ResourceValue: 4, Node: 0},
					{ResourceValue: 4, Node: 1},
					{ResourceValue: 4, Node: 2},
					{ResourceValue: 4, Node: 3},
				},
				AggregatedAllocatableQuantity: 14,
				AggregatedCapacityQuantity:    16,
			},
		},
	}, resp)
}

func TestAddReclaimedCPUAllocatable(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestAddReclaimedCPUAllocatable")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	numaNodes := dynamicPolicy.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt()

	// headroom is expressed in cores; the reported value is millicpu (cores * 1000).
	dynamicPolicy.state.SetNUMAHeadroom(map[int]float64{
		0: 1,
		1: 2,
		2: 3,
		3: 4,
	}, false)

	// Register multiple consumers so GetReclaimedPercentage returns each
	// consumer's own percentage; the single-consumer force-100 rule only
	// applies when exactly one consumer is registered in the whole registry.
	reclaim.UnregisterConsumer("cpu-kcnr-a")
	reclaim.UnregisterConsumer("cpu-kcnr-b")
	reclaim.UnregisterConsumer("cpu-kcnr-c")
	dynamicPolicy.config.dynamicConfig.GetDynamicConfiguration().ReclaimedPercentageByConsumer = map[string]int{
		"cpu-kcnr-a": 50,
		"cpu-kcnr-b": 30,
		"cpu-kcnr-c": 20,
	}
	reg := func(name string) {
		require.NoError(t, reclaim.RegisterNamedGenericConsumer(name, config.NewConfiguration(), nil))
	}
	reg("cpu-kcnr-a")
	reg("cpu-kcnr-b")
	reg("cpu-kcnr-c")

	cpuKey := string(consts.ReclaimedResourceMilliCPU)

	newResource := func(pct float64) *pluginapi.AllocatableTopologyAwareResource {
		list := []*pluginapi.TopologyAwareQuantity{
			{ResourceValue: 1 * 1000 * pct / 100, Node: 0},
			{ResourceValue: 2 * 1000 * pct / 100, Node: 1},
			{ResourceValue: 3 * 1000 * pct / 100, Node: 2},
			{ResourceValue: 4 * 1000 * pct / 100, Node: 3},
		}
		return &pluginapi.AllocatableTopologyAwareResource{
			IsNodeResource:                       false,
			IsScalarResource:                     true,
			AggregatedAllocatableQuantity:        10 * 1000 * pct / 100,
			TopologyAwareAllocatableQuantityList: list,
			AggregatedCapacityQuantity:           10 * 1000 * pct / 100,
			TopologyAwareCapacityQuantityList:    list,
		}
	}

	testCases := []struct {
		name      string
		consumers []string
		expected  *pluginapi.AllocatableTopologyAwareResource // nil means no entry is added
	}{
		{
			name:      "empty config adds no entry",
			consumers: nil,
			expected:  nil,
		},
		{
			name:      "single configured consumer",
			consumers: []string{"cpu-kcnr-a"},
			expected:  newResource(50),
		},
		{
			name:      "multiple configured consumers are summed",
			consumers: []string{"cpu-kcnr-a", "cpu-kcnr-b"},
			expected:  newResource(80),
		},
		{
			name:      "summed percentage at 100",
			consumers: []string{"cpu-kcnr-a", "cpu-kcnr-b", "cpu-kcnr-c"},
			expected:  newResource(100),
		},
		{
			name:      "unknown consumer contributes zero",
			consumers: []string{"cpu-kcnr-a", "does-not-exist"},
			expected:  newResource(50),
		},
	}

	dynamicPolicy.config.conf.EnableReclaimedResourceAllocatableReporting = true
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)

			allocatableResources := make(map[string]*pluginapi.AllocatableTopologyAwareResource)
			dynamicPolicy.addReclaimedCPUAllocatable(allocatableResources, numaNodes, tc.consumers)

			if tc.expected == nil {
				_, ok := allocatableResources[cpuKey]
				as.False(ok, "expected no ReclaimedResourceMilliCPU entry when config is empty")
				return
			}
			as.Equal(tc.expected, allocatableResources[cpuKey])
		})
	}

	// end-to-end: the ReclaimedResourceMilliCPU entry is present in the full
	// response when ReclaimConsumersForKCNR is configured.
	dynamicPolicy.reclaim.reclaimConsumersForKCNR = []string{"cpu-kcnr-a"}
	dynamicPolicy.config.conf.EnableReclaimedResourceAllocatableReporting = false
	resp, err := dynamicPolicy.GetTopologyAwareAllocatableResources(context.Background(),
		&pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
	as.Nil(err)
	_, found := resp.AllocatableResources[cpuKey]
	as.False(found)

	dynamicPolicy.config.conf.EnableReclaimedResourceAllocatableReporting = true
	resp, err = dynamicPolicy.GetTopologyAwareAllocatableResources(context.Background(),
		&pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
	as.Nil(err)
	as.Equal(newResource(50), resp.AllocatableResources[cpuKey])
}

func TestGetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		description  string
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.GetTopologyAwareResourcesResponse
		cpuTopology  *machine.CPUTopology
		err          error
	}{
		{
			description: "req for init container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
			},
			err:         fmt.Errorf("error occurred"),
			cpuTopology: cpuTopology,
		},
		{
			description: "req for shared_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			expectedResp: &pluginapi.GetTopologyAwareResourcesResponse{
				PodNamespace: testName,
				PodName:      testName,
				ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
					ContainerName: testName,
					AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
						string(v1.ResourceCPU): {
							IsNodeResource:             false,
							IsScalarResource:           true,
							AggregatedQuantity:         14,
							OriginalAggregatedQuantity: 14,
							TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 3, Node: 0},
								{ResourceValue: 3, Node: 1},
								{ResourceValue: 4, Node: 2},
								{ResourceValue: 4, Node: 3},
							},
							OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 3, Node: 0},
								{ResourceValue: 3, Node: 1},
								{ResourceValue: 4, Node: 2},
								{ResourceValue: 4, Node: 3},
							},
						},
					},
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			description: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			expectedResp: &pluginapi.GetTopologyAwareResourcesResponse{
				PodNamespace: testName,
				PodName:      testName,
				ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
					ContainerName: testName,
					AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
						string(v1.ResourceCPU): {
							IsNodeResource:             false,
							IsScalarResource:           true,
							AggregatedQuantity:         4,
							OriginalAggregatedQuantity: 4,
							TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 2, Node: 0},
								{ResourceValue: 2, Node: 1},
							},
							OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 2, Node: 0},
								{ResourceValue: 2, Node: 1},
							},
						},
					},
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyAwareResources")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
		as.Nil(err)

		_, err = dynamicPolicy.Allocate(context.Background(), tc.req)
		as.Nil(err)

		resp, err := dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
			PodUid:        tc.req.PodUid,
			ContainerName: testName,
		})

		if tc.err != nil {
			as.NotNil(err)
			continue
		} else {
			as.Nil(err)
			tc.expectedResp.PodUid = tc.req.PodUid
		}

		as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)

		if tc.req.Annotations[consts.PodAnnotationQoSLevelKey] == consts.PodAnnotationQoSLevelSharedCores {
			originalTransitionPeriod := dynamicPolicy.config.transitionPeriod
			dynamicPolicy.config.transitionPeriod = time.Millisecond * 10
			time.Sleep(20 * time.Millisecond)
			_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
			dynamicPolicy.config.transitionPeriod = originalTransitionPeriod
			as.Nil(err)
			allocationInfo := dynamicPolicy.state.GetAllocationInfo(tc.req.PodUid, testName)
			as.NotNil(allocationInfo)
			as.Equal(false, allocationInfo.RampUp)

			resp, err = dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
				PodUid:        tc.req.PodUid,
				ContainerName: testName,
			})

			as.Nil(err)
			as.NotNil(resp)

			tc.expectedResp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
				string(v1.ResourceCPU): {
					IsNodeResource:             false,
					IsScalarResource:           true,
					AggregatedQuantity:         10,
					OriginalAggregatedQuantity: 10,
					TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 2, Node: 0},
						{ResourceValue: 2, Node: 1},
						{ResourceValue: 4, Node: 2},
						{ResourceValue: 2, Node: 3},
					},
					OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 2, Node: 0},
						{ResourceValue: 2, Node: 1},
						{ResourceValue: 4, Node: 2},
						{ResourceValue: 2, Node: 3},
					},
				},
			}

			as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)
		}

		os.Remove(tmpDir)
	}
}

func TestGetResourcesAllocation(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetResourcesAllocation")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)
	dynamicPolicy.config.transitionPeriod = time.Millisecond * 10

	testName := "test"

	// test for shared_cores
	req := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	time.Sleep(dynamicPolicy.config.transitionPeriod * 2)
	resp1, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	reclaim := dynamicPolicy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	as.NotNil(reclaim)

	as.NotNil(resp1.PodResources[req.PodUid])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 10,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.config.reservedCPUs).Difference(reclaim.AllocationResult).String(),
		// whole-core apportion balances lent cores across NUMAs, shifting the
		// per-NUMA share split while the aggregate stays 10.
		TopologyAssignments: map[uint64]uint64{
			0: 2,
			1: 2,
			2: 4,
			3: 2,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}, resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	// test after ramping up
	originalTransitionPeriod := dynamicPolicy.config.transitionPeriod
	dynamicPolicy.config.transitionPeriod = time.Millisecond * 10
	time.Sleep(20 * time.Millisecond)
	_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	dynamicPolicy.config.transitionPeriod = originalTransitionPeriod
	allocationInfo := dynamicPolicy.state.GetAllocationInfo(req.PodUid, testName)
	as.NotNil(allocationInfo)
	as.Equal(false, allocationInfo.RampUp)

	resp2, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(resp2.PodResources[req.PodUid])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 10,
		AllocationResult:  machine.NewCPUSet(1, 3, 4, 5, 6, 9, 11, 12, 13, 14).String(),
		TopologyAssignments: map[uint64]uint64{
			0: 2,
			1: 2,
			2: 4,
			3: 2,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}, resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	// test for reclaimed_cores
	req = &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
		},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	resp2, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resp2.PodResources[req.PodUid])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 4,
		AllocationResult:  machine.NewCPUSet(7, 8, 10, 15).String(),
		TopologyAssignments: map[uint64]uint64{
			uint64(0): 1,
			uint64(1): 1,
			uint64(3): 2,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
		},
	}, resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	req = &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	dynamicPolicy.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)
	dynamicPolicy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	dynamicPolicy.state.SetAllocationInfo(commonstate.PoolNameReclaim, "", &state.AllocationInfo{
		AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult:         machine.MustParse("1,3,4-5"),
		OriginalAllocationResult: machine.MustParse("1,3,4-5"),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1),
			1: machine.NewCPUSet(3),
			2: machine.NewCPUSet(4, 5),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1),
			1: machine.NewCPUSet(3),
			2: machine.NewCPUSet(4, 5),
		},
	}, true)
	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	// test after ramping up
	originalTransitionPeriod = dynamicPolicy.config.transitionPeriod
	dynamicPolicy.config.transitionPeriod = time.Millisecond * 10
	time.Sleep(20 * time.Millisecond)
	_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	dynamicPolicy.config.transitionPeriod = originalTransitionPeriod
	allocationInfo = dynamicPolicy.state.GetAllocationInfo(req.PodUid, testName)
	as.NotNil(allocationInfo)
	as.Equal(false, allocationInfo.RampUp)

	resp3, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(resp3.PodResources[req.PodUid])
	as.NotNil(resp3.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp3.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 14,
		AllocationResult:  machine.NewCPUSet(1, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15).String(),
		TopologyAssignments: map[uint64]uint64{
			uint64(0): 3,
			uint64(1): 3,
			uint64(2): 4,
			uint64(3): 4,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}, resp3.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	reclaimEntry := dynamicPolicy.state.GetAllocationInfo(commonstate.PoolNameReclaim, "")
	as.NotNil(reclaimEntry)
	as.Equal(6, reclaimEntry.AllocationResult.Size()) // ceil("14 * (4 / 10)") == 6
}

func TestRampUpDeadlineIsSharedByAllQoSPaths(t *testing.T) {
	t.Parallel()

	initTime := time.Unix(100, 0)
	for _, tc := range []struct {
		name string
		info *state.AllocationInfo
	}{
		{
			name: "non-snb",
			info: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{QoSLevel: consts.PodAnnotationQoSLevelSharedCores},
			},
		},
		{
			name: "snb",
			info: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
			},
		},
		{
			name: "dedicated",
			info: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores},
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.info.RampUp = true
			tc.info.InitTimestamp = initTime.Format(util.QRMTimeFormat)
			finished, err := shouldAllocationFinishRampUp(tc.info, rampUpTransitionPeriod, initTime.Add(29*time.Second))
			require.NoError(t, err)
			require.False(t, finished)
			finished, err = shouldAllocationFinishRampUp(tc.info, rampUpTransitionPeriod, initTime.Add(30*time.Second))
			require.NoError(t, err)
			require.True(t, finished)
		})
	}
}

type contextCapturingPodFetcher struct {
	*pod.PodFetcherStub
	contexts chan context.Context
}

func (f *contextCapturingPodFetcher) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	f.contexts <- ctx
	return f.PodFetcherStub.GetPod(ctx, podUID)
}

func TestShouldSharedCoresRampUpUsesCallerContext(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	fetcher := &contextCapturingPodFetcher{
		PodFetcherStub: &pod.PodFetcherStub{},
		contexts:       make(chan context.Context, 1),
	}
	policy.meta.metaServer.MetaAgent.PodFetcher = fetcher

	type contextKey string
	const key contextKey = "caller"
	callerCtx := context.WithValue(context.Background(), key, "preserved")
	policy.shouldSharedCoresRampUp(callerCtx, "missing-pod")

	select {
	case got := <-fetcher.contexts:
		require.Equal(t, "preserved", got.Value(key))
	case <-time.After(time.Second):
		t.Fatal("pod fetch did not receive a context")
	}
}

func TestShouldSharedCoresRampUpDisabledByDynamicConfig(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	pendingPodUID := "pending-pod"
	activePodUID := "active-pod"

	t.Run("default config keeps pending pod ramp up", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(pendingPodUID), Namespace: "default", Name: "pending-pod"},
				Status:     v1.PodStatus{Phase: v1.PodPending},
			},
		}}
		assert.True(t, policy.shouldSharedCoresRampUp(context.Background(), pendingPodUID))
	})

	t.Run("default config treats missing pod as admission ramp up", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: nil}
		assert.True(t, policy.shouldSharedCoresRampUp(context.Background(), "missing-pod"))
	})

	t.Run("default config skips active pod ramp up", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(activePodUID), Namespace: "default", Name: "active-pod"},
				Status:     v1.PodStatus{Phase: v1.PodRunning},
			},
		}}
		assert.False(t, policy.shouldSharedCoresRampUp(context.Background(), activePodUID))
	})

	t.Run("disabled config skips pending pod ramp up", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true
		policy.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(pendingPodUID), Namespace: "default", Name: "pending-pod"},
				Status:     v1.PodStatus{Phase: v1.PodPending},
			},
		}}
		assert.False(t, policy.shouldSharedCoresRampUp(context.Background(), pendingPodUID))
	})

	t.Run("disabled config skips ramp up before pod fetch", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true
		assert.False(t, policy.shouldSharedCoresRampUp(context.Background(), "missing-pod"))
	})
}

func TestSharedCoresRampUpDisabledAllocation(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	newSharedCoresReq := func(podUID string) *pluginapi.ResourceRequest {
		return &pluginapi.ResourceRequest{
			PodUid:         podUID,
			PodNamespace:   "default",
			PodName:        podUID,
			ContainerName:  "main",
			ContainerType:  pluginapi.ContainerType_MAIN,
			ContainerIndex: 0,
			ResourceName:   string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 2,
			},
			Labels: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
		}
	}

	t.Run("uses target pool instead of pooled cpus", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true
		policy.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID("shared-no-ramp-up"), Namespace: "default", Name: "shared-no-ramp-up"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "main",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU: resource.MustParse("2"),
								},
							},
						},
					},
				},
				Status: v1.PodStatus{Phase: v1.PodPending},
			},
		}}

		targetPoolCPUs := machine.NewCPUSet(2, 3, 4, 5)
		policy.state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &state.AllocationInfo{
			AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
			AllocationResult:         targetPoolCPUs,
			OriginalAllocationResult: targetPoolCPUs.Clone(),
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: targetPoolCPUs,
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: targetPoolCPUs.Clone(),
			},
		}, false)

		req := newSharedCoresReq("shared-no-ramp-up")
		resp, err := policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
		as.Nil(err)
		as.NotNil(resp)

		allocationInfo := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		as.NotNil(allocationInfo)
		assert.False(t, allocationInfo.RampUp)
		assert.NotEqual(t, cpuTopology.CPUDetails.CPUs().Difference(policy.config.reservedCPUs).String(), allocationInfo.AllocationResult.String())
		assert.Equal(t, targetPoolCPUs.String(), allocationInfo.AllocationResult.String())
	})

	t.Run("returns error when target pool is missing", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true

		req := newSharedCoresReq("shared-missing-pool")
		// cold-start seed path: with DisableSharedCoresRampUp=true and no pre-existing
		// target pool entry, we now seed the pool via the assembler pipeline instead of
		// returning admit failure. Verify admit succeeds, RampUp=false, and the target
		// pool entry has been persisted.
		resp, err := policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
		as.Nil(err)
		as.NotNil(resp)

		allocationInfo := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		as.NotNil(allocationInfo)
		assert.False(t, allocationInfo.RampUp)
		assert.False(t, allocationInfo.AllocationResult.IsEmpty())

		// target pool should now be present in state
		poolInfo := policy.state.GetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName)
		as.NotNil(poolInfo)
		assert.Equal(t, allocationInfo.AllocationResult.String(), poolInfo.AllocationResult.String())
	})

	t.Run("cold-start seeds specified seedpool", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true

		const specifiedPool = "seedpool-stable-2"
		req := newSharedCoresReq("shared-custom-seedpool")
		req.Annotations[consts.PodAnnotationCPUEnhancementCPUSet] = specifiedPool

		resp, err := policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
		as.Nil(err)
		as.NotNil(resp)

		allocationInfo := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		as.NotNil(allocationInfo)
		assert.False(t, allocationInfo.RampUp)
		assert.Equal(t, specifiedPool, allocationInfo.OwnerPoolName)
		assert.False(t, allocationInfo.AllocationResult.IsEmpty())

		poolInfo := policy.state.GetAllocationInfo(specifiedPool, commonstate.FakedContainerName)
		as.NotNil(poolInfo)
		assert.Equal(t, specifiedPool, poolInfo.OwnerPoolName)
		assert.Equal(t, allocationInfo.AllocationResult.String(), poolInfo.AllocationResult.String())
		assert.Equal(t, allocationInfo.OriginalAllocationResult.String(), poolInfo.OriginalAllocationResult.String())
		assert.Equal(t, allocationInfo.TopologyAwareAssignments, poolInfo.TopologyAwareAssignments)

		machineState := policy.state.GetMachineState()
		foundPod := false
		for _, numaState := range machineState {
			if numaState == nil {
				continue
			}
			for _, allocation := range numaState.PodEntries[req.PodUid] {
				if allocation != nil && allocation.ContainerName == req.ContainerName {
					foundPod = true
				}
			}
		}
		assert.True(t, foundPod)
	})

	t.Run("cold-start seed overlap-false excludes reclaim pool", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true
		policy.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

		reclaimCPUs := machine.NewCPUSet(10, 11, 12, 13, 14, 15)
		policy.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:                 reclaimCPUs,
			OriginalAllocationResult:         reclaimCPUs.Clone(),
			TopologyAwareAssignments:         map[int]machine.CPUSet{0: reclaimCPUs},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: reclaimCPUs.Clone()},
		}, false)

		req := newSharedCoresReq("shared-cold-start-overlap-false")
		resp, err := policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
		as.Nil(err)
		as.NotNil(resp)

		allocationInfo := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		as.NotNil(allocationInfo)
		assert.False(t, allocationInfo.RampUp)
		// After assembler, the reclaim pool may be reshaped; the overlap=false
		// invariant is that the pod's cpuset does not intersect the FINAL reclaim pool.
		finalReclaim := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		as.NotNil(finalReclaim)
		assert.True(t, allocationInfo.AllocationResult.Intersection(finalReclaim.AllocationResult).IsEmpty(),
			"cold-start seed cpuset should not overlap final reclaim pool when overlap=false; pod=%s reclaim=%s",
			allocationInfo.AllocationResult.String(), finalReclaim.AllocationResult.String())
	})

	t.Run("cold-start seed overlap-true allows reclaim intersection", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true
		policy.state.SetAllowSharedCoresOverlapReclaimedCores(true, false)

		reclaimCPUs := machine.NewCPUSet(10, 11, 12, 13, 14, 15)
		policy.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:                 reclaimCPUs,
			OriginalAllocationResult:         reclaimCPUs.Clone(),
			TopologyAwareAssignments:         map[int]machine.CPUSet{0: reclaimCPUs},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: reclaimCPUs.Clone()},
		}, false)

		req := newSharedCoresReq("shared-cold-start-overlap-true")
		resp, err := policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
		as.Nil(err)
		as.NotNil(resp)

		allocationInfo := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		as.NotNil(allocationInfo)
		assert.False(t, allocationInfo.RampUp)
		assert.False(t, allocationInfo.AllocationResult.IsEmpty())
	})

	t.Run("cold-start seed without reclaim pool falls back to pooled cpus", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true

		req := newSharedCoresReq("shared-cold-start-no-reclaim")
		resp, err := policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
		as.Nil(err)
		as.NotNil(resp)

		allocationInfo := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		as.NotNil(allocationInfo)
		assert.False(t, allocationInfo.RampUp)
		assert.False(t, allocationInfo.AllocationResult.IsEmpty())
	})

	t.Run("cold-start seed persists checkpoint pool entry", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		checkpointDir := t.TempDir()
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, checkpointDir)
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true

		req := newSharedCoresReq("shared-cold-start-persist")
		_, err = policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, true)
		as.Nil(err)

		reloaded, err := getTestDynamicPolicyWithInitialization(cpuTopology, checkpointDir)
		as.Nil(err)
		podEntries := reloaded.state.GetPodEntries()
		as.NotNil(podEntries)
		poolEntry := podEntries[commonstate.PoolNameShare]
		as.NotNil(poolEntry, "share pool entry should exist after cold-start seed")
		assert.NotNil(t, poolEntry[commonstate.FakedContainerName])
	})

	t.Run("cold-start seed re-entry takes clone branch on second admit", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = true

		req1 := newSharedCoresReq("shared-cold-start-first")
		_, err = policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req1, false)
		as.Nil(err)
		firstAllocation := policy.state.GetAllocationInfo(req1.PodUid, req1.ContainerName)
		as.NotNil(firstAllocation)

		poolAfterFirst := policy.state.GetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName)
		as.NotNil(poolAfterFirst)
		poolCPUsBefore := poolAfterFirst.AllocationResult.String()

		// second pod admits with pool already present - should clone pool, not re-seed
		req2 := newSharedCoresReq("shared-cold-start-second")
		_, err = policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req2, false)
		as.Nil(err)
		secondAllocation := policy.state.GetAllocationInfo(req2.PodUid, req2.ContainerName)
		as.NotNil(secondAllocation)
		assert.False(t, secondAllocation.RampUp)

		poolAfterSecond := policy.state.GetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName)
		as.NotNil(poolAfterSecond)
		// pool cpuset should remain stable across re-admits (may expand slightly if
		// assembler recomputes on second admit, but must not shrink to empty)
		assert.False(t, poolAfterSecond.AllocationResult.IsEmpty(),
			"pool should still be present after second admit; before=%s after=%s",
			poolCPUsBefore, poolAfterSecond.AllocationResult.String())
	})

	t.Run("disable=false cold-start keeps RampUp behavior", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		as.Nil(err)
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = false
		policy.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID("ramp-up-pending"), Namespace: "default", Name: "ramp-up-pending"},
				Status:     v1.PodStatus{Phase: v1.PodPending},
			},
		}}

		req := newSharedCoresReq("ramp-up-pending")
		_, err = policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
		as.Nil(err)

		allocationInfo := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		as.NotNil(allocationInfo)
		// disable=false + pending pod + no existing pool → RampUp=true broad path,
		// no cold-start seed
		assert.True(t, allocationInfo.RampUp)
	})
}

func TestSharedCoresRampUpAllocationExcludesAllNUMAReclaimFloor(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	as.NoError(err)
	policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	as.NoError(err)
	policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = false
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	policy.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0

	numa0 := policy.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0).Difference(policy.config.reservedCPUs).ToSliceInt()
	numa1 := policy.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1).Difference(policy.config.reservedCPUs).ToSliceInt()
	as.GreaterOrEqual(len(numa0), 2)
	as.GreaterOrEqual(len(numa1), 2)
	floor := machine.NewCPUSet(numa0[0], numa0[1], numa1[0], numa1[1])
	policy.reclaim.reservedReclaimedCPUSet = floor
	policy.reclaim.reservedReclaimedCPUsSize = floor.Size()
	policy.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(),
	}, false)
	tracked := &atomicCommitTrackingState{State: policy.state}
	policy.state = tracked

	const podUID = "shared-ramp-up-all-numa-floor"
	policy.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID), Namespace: "default", Name: podUID},
		Spec: v1.PodSpec{Containers: []v1.Container{{
			Name: "main",
			Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
				v1.ResourceCPU: resource.MustParse("2"),
			}},
		}}},
		Status: v1.PodStatus{Phase: v1.PodPending},
	}}}
	req := &pluginapi.ResourceRequest{
		PodUid: podUID, PodNamespace: "default", PodName: podUID,
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
		Annotations:      map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
	}

	_, err = policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
	as.NoError(err)
	as.Equal(1, tracked.commitCalls)
	allocation := policy.state.GetAllocationInfo(podUID, "main")
	as.NotNil(allocation)
	as.True(allocation.RampUp)
	as.True(allocation.AllocationResult.Intersection(floor).IsEmpty(),
		"ramp-up allocation %s contains reclaim floor %s", allocation.AllocationResult, floor)
	reclaim := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	as.NotNil(reclaim)
	as.True(floor.IsSubsetOf(reclaim.AllocationResult),
		"first shared ramp-up must commit reclaim floor atomically: reclaim=%s floor=%s",
		reclaim.AllocationResult, floor)

	_, err = policy.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
	as.NoError(err)
	replayed := policy.state.GetAllocationInfo(podUID, "main")
	as.NotNil(replayed)
	as.True(replayed.AllocationResult.Intersection(floor).IsEmpty(),
		"replayed ramp-up allocation %s contains reclaim floor %s", replayed.AllocationResult, floor)
}

func TestAllocateSNBRampUpAllowsGlobalSharePoolToShrink(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	p.config.enableCPUAdvisor = true
	p.advisor.advisorClient = &advisorPoolTestClient{}

	allocationCPUs := p.machine.machineInfo.CPUDetails.CPUs().Difference(p.config.reservedCPUs)
	assignments, err := machine.GetNumaAwareAssignments(cpuTopology, allocationCPUs)
	require.NoError(t, err)
	p.state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult:                 allocationCPUs.Clone(),
		OriginalAllocationResult:         allocationCPUs.Clone(),
		TopologyAwareAssignments:         machine.DeepcopyCPUAssignment(assignments),
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
	}, false)
	p.state.SetAllocationInfo("ordinary-shared", "main", &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "ordinary-shared",
			PodNamespace:  "default",
			PodName:       "ordinary-shared",
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			OwnerPoolName: commonstate.PoolNameShare,
			QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
		},
		AllocationResult:                 allocationCPUs.Clone(),
		OriginalAllocationResult:         allocationCPUs.Clone(),
		TopologyAwareAssignments:         machine.DeepcopyCPUAssignment(assignments),
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
		RequestQuantity:                  1,
	}, false)

	numa0 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0).Difference(p.config.reservedCPUs).ToSliceInt()
	numa1 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1).Difference(p.config.reservedCPUs).ToSliceInt()
	require.GreaterOrEqual(t, len(numa0), 2)
	require.GreaterOrEqual(t, len(numa1), 2)
	floor := machine.NewCPUSet(numa0[0], numa0[1], numa1[0], numa1[1])
	p.reclaim.reservedReclaimedCPUSet = floor
	p.reclaim.reservedReclaimedCPUsSize = floor.Size()
	p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult:                 floor.Clone(),
		OriginalAllocationResult:         floor.Clone(),
		TopologyAwareAssignments:         map[int]machine.CPUSet{0: floor.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)), 1: floor.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1))},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: floor.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)), 1: floor.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1))},
	}, false)
	entries := p.state.GetPodEntries()
	machineState, err := generateMachineStateFromPodEntries(cpuTopology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	p.state.SetMachineState(machineState, false)

	const podUID = "snb-ramp-up"
	p.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("ordinary-shared"), Namespace: "default", Name: "ordinary-shared"},
			Spec: v1.PodSpec{Containers: []v1.Container{{
				Name: "main",
				Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
					v1.ResourceCPU: resource.MustParse("1"),
				}},
			}}},
			Status: v1.PodStatus{Phase: v1.PodRunning},
		},
		{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID), Namespace: "default", Name: podUID},
			Spec: v1.PodSpec{Containers: []v1.Container{{
				Name: "main",
				Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
					v1.ResourceCPU: resource.MustParse("1"),
				}},
			}}},
			Status: v1.PodStatus{Phase: v1.PodPending},
		},
	}}
	req := &pluginapi.ResourceRequest{
		PodUid: podUID, PodNamespace: "default", PodName: podUID,
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 1},
		Labels:           map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true"}`,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}, Preferred: true},
	}

	resp, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	snb := p.state.GetAllocationInfo(podUID, "main")
	require.NotNil(t, snb)
	require.True(t, snb.RampUp)
	require.True(t, snb.CheckSharedNUMABinding())
	share := p.state.GetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName)
	require.NotNil(t, share)
	require.Less(t, share.AllocationResult.Size(), allocationCPUs.Size())
}

func TestGetResourcesAllocationRetriesFailedRampUpCompletion(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetResourcesAllocationRetriesFailedRampUpCompletion")
	as.NoError(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.NoError(err)
	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.NoError(err)
	dynamicPolicy.config.transitionPeriod = 30 * time.Second

	req := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   "test",
		PodName:        "test",
		ContainerName:  "test",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}
	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.NoError(err)

	allocationInfo := dynamicPolicy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	as.True(allocationInfo.RampUp)
	allocationInfo.InitTimestamp = time.Now().Add(-31 * time.Second).Format(util.QRMTimeFormat)
	dynamicPolicy.state.SetAllocationInfo(req.PodUid, req.ContainerName, allocationInfo, true)

	dynamicPolicy.allocation.allocationHooks = []AllocationHook{
		func(_, newAllocationInfo *state.AllocationInfo) error {
			if newAllocationInfo != nil && !newAllocationInfo.RampUp &&
				newAllocationInfo.OwnerPoolName != commonstate.EmptyOwnerPoolName {
				return fmt.Errorf("force allocation adjustment failure")
			}
			return nil
		},
	}
	_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.NoError(err)
	as.True(dynamicPolicy.state.GetAllocationInfo(req.PodUid, req.ContainerName).RampUp)
	restartedPolicy, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	as.NoError(err)
	as.True(restartedPolicy.state.GetAllocationInfo(req.PodUid, req.ContainerName).RampUp)

	dynamicPolicy.allocation.allocationHooks = nil
	dynamicPolicy.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"failing": func(context.Context, dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			return fmt.Errorf("force post-commit adjustment failure")
		},
	}
	_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.NoError(err)
	as.False(dynamicPolicy.state.GetAllocationInfo(req.PodUid, req.ContainerName).RampUp)
	restartedPolicy, err = getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	as.NoError(err)
	as.False(restartedPolicy.state.GetAllocationInfo(req.PodUid, req.ContainerName).RampUp)
}

func TestGetResourcesAllocationCommitsMainAndSidecarRampUpExitTogether(t *testing.T) {
	t.Parallel()
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetResourcesAllocationCommitsMainAndSidecarRampUpExitTogether")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	dynamicPolicy.config.transitionPeriod = 30 * time.Second

	const (
		podUID      = "ramp-up-exit-pod"
		mainName    = "main"
		sidecarName = "sidecar"
	)
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   "test",
		PodName:        "test",
		ContainerName:  mainName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}
	_, err = dynamicPolicy.Allocate(context.Background(), req)
	require.NoError(t, err)

	main := dynamicPolicy.state.GetAllocationInfo(podUID, mainName)
	require.NotNil(t, main)
	main.InitTimestamp = time.Now().Add(-31 * time.Second).Format(util.QRMTimeFormat)
	dynamicPolicy.state.SetAllocationInfo(podUID, mainName, main, true)

	sidecar := main.Clone()
	sidecar.ContainerName = sidecarName
	sidecar.ContainerType = pluginapi.ContainerType_SIDECAR.String()
	sidecar.InitTimestamp = time.Now().Format(util.QRMTimeFormat)
	dynamicPolicy.state.SetAllocationInfo(podUID, sidecarName, sidecar, true)
	startRevision := dynamicPolicy.state.GetRevision()

	_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)

	main = dynamicPolicy.state.GetAllocationInfo(podUID, mainName)
	sidecar = dynamicPolicy.state.GetAllocationInfo(podUID, sidecarName)
	require.NotNil(t, main)
	require.NotNil(t, sidecar)
	require.False(t, main.RampUp)
	require.False(t, sidecar.RampUp)
	require.Equal(t, main.OwnerPoolName, sidecar.OwnerPoolName)
	require.True(t, sidecar.AllocationResult.Equals(main.AllocationResult))
	require.True(t, sidecar.OriginalAllocationResult.Equals(main.OriginalAllocationResult))
	require.True(t, state.CheckAllocationInfoTopologyAwareAssignments(sidecar, main))
	require.True(t, state.CheckAllocationInfoOriginTopologyAwareAssignments(sidecar, main))
	require.Equal(t, startRevision+1, dynamicPolicy.state.GetRevision(),
		"main, pool, and sidecar ramp-up exit must share one revision CAS")
}

func TestAllocateByQoSAwareServerListAndWatchResp(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	pod1UID := string(uuid.NewUUID())
	pod2UID := string(uuid.NewUUID())
	pod3UID := string(uuid.NewUUID())
	pod4UID := string(uuid.NewUUID())
	testName := "test"

	fmt.Println("1", pod1UID)
	fmt.Println("2", pod2UID)
	fmt.Println("3", pod3UID)
	fmt.Println("4", pod4UID)

	testCases := []struct {
		name                 string
		podEntries           state.PodEntries
		expectedPodEntries   state.PodEntries
		expectedMachineState state.NUMANodeMap
		expectedNUMAHeadroom map[int]float64
		lwResp               *advisorapi.ListAndWatchResponse
		cpuTopology          *machine.CPUTopology
	}{
		{
			name: "two shared_cores containers and one reclaimed_cores container",
			podEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod1UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
						OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						RequestQuantity: 2,
					},
				},
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod2UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
						OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						RequestQuantity: 2,
					},
				},
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod3UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.NewCPUSet(7, 8, 10, 15),
						OriginalAllocationResult: machine.NewCPUSet(7, 8, 10, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
							4: machine.NewCPUSet(6),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
							4: machine.NewCPUSet(6),
						},
						RequestQuantity: 2,
					},
				},
			},
			lwResp: &advisorapi.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameShare: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameShare,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f66",
												Result:  6,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReserve: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReserve,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f65",
												Result:  2,
											},
										},
									},
								},
							},
						},
					},
				},
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{
								string(advisorapi.ControlKnobKeyCPUNUMAHeadroom): `{"0": 10.1, "1": 12.2}`,
							},
						},
					},
				},
			},
			expectedPodEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod1UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod2UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod3UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("5-8,10,13-15"),
						OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("5-8,10,13-15"),
						OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
					},
				},
				commonstate.PoolNameReserve: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
						AllocationResult:         machine.MustParse("0,2"),
						OriginalAllocationResult: machine.MustParse("0,2"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
					},
				},
			},
			expectedMachineState: state.NUMANodeMap{
				0: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								RequestQuantity: 2,
							},
						},
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								RequestQuantity: 2,
							},
						},
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("8"),
								OriginalAllocationResult: machine.MustParse("8"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				1: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								RequestQuantity: 2,
							},
						},
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								RequestQuantity: 2,
							},
						},
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("10"),
								OriginalAllocationResult: machine.MustParse("10"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				2: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("5,13"),
								OriginalAllocationResult: machine.MustParse("5,13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				3: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("6,7,14,15"),
								OriginalAllocationResult: machine.MustParse("6,7,14,15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
			},
			expectedNUMAHeadroom: map[int]float64{
				0: 10.1,
				1: 12.2,
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "two shared_cores containers and one reclaimed_cores container and one dedicated_cores",
			podEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod1UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4-5,6-7"),
						OriginalAllocationResult: machine.MustParse("4-5,6-7"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
							3: machine.NewCPUSet(6, 7),
						},
						RequestQuantity: 2,
					},
				},
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod2UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4-5,6-7"),
						OriginalAllocationResult: machine.MustParse("4-5,6-7"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
							3: machine.NewCPUSet(6, 7),
						},
						RequestQuantity: 2,
					},
				},
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod3UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.NewCPUSet(12, 13, 14, 15),
						OriginalAllocationResult: machine.NewCPUSet(12, 13, 14, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(12, 13),
							3: machine.NewCPUSet(14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(12, 13),
							3: machine.NewCPUSet(14, 15),
						},
						RequestQuantity: 2,
					},
				},
				pod4UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod4UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.NewCPUSet(1, 3, 8, 9, 10, 11),
						OriginalAllocationResult: machine.NewCPUSet(1, 3, 8, 9, 10, 11),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
							1: machine.NewCPUSet(3, 10, 11),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
							1: machine.NewCPUSet(3, 10, 11),
						},
						RequestQuantity: 2,
					},
				},
			},
			lwResp: &advisorapi.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameShare: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameShare,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f66",
												Result:  6,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReserve: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReserve,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f65",
												Result:  2,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f30",
												Result:  1,
											},
										},
									},
									1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f31",
												Result:  1,
											},
										},
									},
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f32",
												Result:  2,
											},
										},
									},
								},
							},
						},
					},
					pod4UID: {
						Entries: map[string]*advisorapi.CalculationInfo{
							testName: {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd1f20",
												Result:  2,
											},
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f30",
												Result:  1,
											},
										},
									},
									1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd1f21",
												Result:  2,
											},
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f31",
												Result:  1,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedPodEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod1UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4,5,6,7,12,13"),
						OriginalAllocationResult: machine.MustParse("4,5,6,7,12,13"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						RequestQuantity: 2,
					},
				},
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod2UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("4,5,6,7,12,13"),
						OriginalAllocationResult: machine.MustParse("4,5,6,7,12,13"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						RequestQuantity: 2,
					},
				},
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod3UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3,14-15"),
						OriginalAllocationResult: machine.MustParse("1,3,14-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1),
							1: machine.NewCPUSet(3),
							3: machine.NewCPUSet(14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1),
							1: machine.NewCPUSet(3),
							3: machine.NewCPUSet(14, 15),
						},
						RequestQuantity: 2,
					},
				},
				pod4UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod4UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.NewCPUSet(1, 3, 8, 9, 10, 11),
						OriginalAllocationResult: machine.NewCPUSet(1, 3, 8, 9, 10, 11),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
							1: machine.NewCPUSet(3, 10, 11),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
							1: machine.NewCPUSet(3, 10, 11),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("1,3,14-15"),
						OriginalAllocationResult: machine.MustParse("1,3,14-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1),
							1: machine.NewCPUSet(3),
							3: machine.NewCPUSet(14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1),
							1: machine.NewCPUSet(3),
							3: machine.NewCPUSet(14, 15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("4,5,6,7,12,13"),
						OriginalAllocationResult: machine.MustParse("4,5,6,7,12,13"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
					},
				},
				commonstate.PoolNameReserve: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
						AllocationResult:         machine.MustParse("0,2"),
						OriginalAllocationResult: machine.MustParse("0,2"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
					},
				},
			},
			expectedMachineState: state.NUMANodeMap{
				0: &state.NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(0),
					AllocatedCPUSet: machine.NewCPUSet(1, 8, 9),
					PodEntries: state.PodEntries{
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("1"),
								OriginalAllocationResult: machine.MustParse("1"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1),
								},
								RequestQuantity: 2,
							},
						},
						pod4UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod4UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameDedicated,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
										consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									},
									QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 8, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 8, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 8, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 8, 9),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				1: &state.NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(2),
					AllocatedCPUSet: machine.NewCPUSet(3, 10, 11),
					PodEntries: state.PodEntries{
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("3"),
								OriginalAllocationResult: machine.MustParse("3"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(3),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(3),
								},
								RequestQuantity: 2,
							},
						},
						pod4UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod4UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameDedicated,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
										consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									},
									QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(3, 10, 11),
								OriginalAllocationResult: machine.NewCPUSet(3, 10, 11),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 10, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 10, 11),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				2: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4-5,12-13"),
								OriginalAllocationResult: machine.MustParse("4-5,12-13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								RequestQuantity: 2,
							},
						},
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4-5,12-13"),
								OriginalAllocationResult: machine.MustParse("4-5,12-13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								RequestQuantity: 2,
							},
						},
						//pod3UID: state.ContainerEntries{
						//	testName: &state.AllocationInfo{
						//		AllocationMeta: commonstate.AllocationMeta{
						//			PodUid:                   pod3UID,
						//			PodNamespace:             testName,
						//			PodName:                  testName,
						//			ContainerName:            testName,
						//			ContainerType:            pluginapi.ContainerType_MAIN.String(),
						//			ContainerIndex:           0,
						//			OwnerPoolName:           commonstate.PoolNameReclaim,
						//			Labels: map[string]string{
						//				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						//			},
						//			Annotations: map[string]string{
						//				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						//			},
						//			QoSLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
						//		},
						//		RampUp:                   false,
						//		AllocationResult:         machine.MustParse("5,13"),
						//		OriginalAllocationResult: machine.MustParse("5,13"),
						//		TopologyAwareAssignments: map[int]machine.CPUSet{
						//			2: machine.NewCPUSet(5, 13),
						//		},
						//		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						//			2: machine.NewCPUSet(5, 13),
						//		},
						//		RequestQuantity: 2,
						//	},
						//},
					},
				},
				3: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("6,7"),
								OriginalAllocationResult: machine.MustParse("6,7"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								RequestQuantity: 2,
							},
						},
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("6,7"),
								OriginalAllocationResult: machine.MustParse("6,7"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								RequestQuantity: 2,
							},
						},
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("14,15"),
								OriginalAllocationResult: machine.MustParse("14,15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(14, 15),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)
			tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateByQoSAwareServerListAndWatchResp")
			as.Nil(err)

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
			as.Nil(err)

			dynamicPolicy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true

			machineState, err := generateMachineStateFromPodEntries(tc.cpuTopology, tc.podEntries, nil)
			as.Nil(err)

			dynamicPolicy.state.SetPodEntries(tc.podEntries, true)
			dynamicPolicy.state.SetMachineState(machineState, true)
			dynamicPolicy.initReservePool()

			emptyMap := map[string]*advisorsvc.FeatureGate{}
			err = dynamicPolicy.allocateByCPUAdvisor(nil, tc.lwResp, emptyMap)
			as.Nilf(err, "dynamicPolicy.allocateByCPUAdvisorServerListAndWatchResp got err: %v, case: %s", err, tc.name)

			getPodEntries := dynamicPolicy.state.GetPodEntries()
			match, err := entriesMatch(tc.expectedPodEntries, getPodEntries)
			as.Nilf(err, "failed in test case: %s", tc.name)
			as.Equalf(true, match, "failed in test case: %s", tc.name)

			match, err = machineStateMatch(tc.expectedMachineState, dynamicPolicy.state.GetMachineState())
			as.Nilf(err, "failed in test case: %s", tc.name)
			as.Equalf(true, match, "failed in test case: %s", tc.name)

			as.Equalf(tc.expectedNUMAHeadroom, dynamicPolicy.state.GetNUMAHeadroom(),
				"failed in test case: %s", tc.name)

			os.RemoveAll(tmpDir)
		})
	}
}

func machineStateMatch(state1, state2 state.NUMANodeMap) (bool, error) {
	if len(state1) != len(state2) {
		return false, nil
	}

	for numaId, numaState := range state1 {
		if numaState == nil {
			return false, fmt.Errorf("empty allocationInfo")
		}

		if state2[numaId] == nil {
			fmt.Printf("NUMA: %d isn't found in state2\n", numaId)
			return false, nil
		}

		if numaState.DefaultCPUSet.Size() != state2[numaId].DefaultCPUSet.Size() {
			fmt.Printf("NUMA: %d DefaultCPUSet: (state1: %d, state2: %d) not match\n",
				numaId, numaState.DefaultCPUSet.Size(), state2[numaId].DefaultCPUSet.Size())
			return false, nil
		} else if numaState.AllocatedCPUSet.Size() != state2[numaId].AllocatedCPUSet.Size() {
			fmt.Printf("NUMA: %d AllocatedCPUSet: (state1: %d, state2: %d) not match\n",
				numaId, numaState.AllocatedCPUSet.Size(), state2[numaId].AllocatedCPUSet.Size())
			return false, nil
		}
	}

	return true, nil
}

func entriesMatch(entries1, entries2 state.PodEntries) (bool, error) {
	if len(entries1) != len(entries2) {
		return false, nil
	}

	for entryName, subEntries := range entries1 {
		for subEntryName, allocationInfo := range subEntries {
			if allocationInfo == nil {
				return false, fmt.Errorf("empty allocationInfo")
			}

			if entries2[entryName][subEntryName] == nil {
				fmt.Printf("%s:%s isn't found in entries2\n", entryName, subEntryName)
				return false, nil
			}

			if allocationInfo.AllocationResult.Size() != entries2[entryName][subEntryName].AllocationResult.Size() {
				fmt.Printf("%s:%s allocationResult: (entries1: %d, entries2: %d) isn't match\n",
					entryName, subEntryName, allocationInfo.AllocationResult.Size(),
					entries2[entryName][subEntryName].AllocationResult.Size())
				return false, nil
			}

			if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				for numaId, cset := range allocationInfo.TopologyAwareAssignments {
					if cset.Size() != entries2[entryName][subEntryName].TopologyAwareAssignments[numaId].Size() {
						fmt.Printf("%s:%s NUMA: %d allocationResult: (entries1: %d, entries2: %d) not match\n",
							entryName, subEntryName, numaId, cset.Size(),
							entries2[entryName][subEntryName].TopologyAwareAssignments[numaId].Size())
						return false, nil
					}
				}
			}
		}
	}

	return true, nil
}

func TestClearResidualState(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestClearResidualState")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.clearResidualState(nil, nil, nil, nil, nil)
}

func TestClearResidualStateTreatsFailedPodAsResidual(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestClearResidualStateTreatsFailedPodAsResidual")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	const podUID = "failed-pod-uid"
	dynamicPolicy.state.SetPodEntries(state.PodEntries{
		podUID: state.ContainerEntries{
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        podUID,
					PodNamespace:  "default",
					PodName:       "failed-pod",
					ContainerName: "main",
					QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(1),
			},
		},
	}, false)
	dynamicPolicy.meta.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID:       types.UID(podUID),
						Name:      "failed-pod",
						Namespace: "default",
					},
					Status: v1.PodStatus{Phase: v1.PodFailed},
				},
			}},
		},
	}

	dynamicPolicy.clearResidualState(nil, nil, nil, nil, nil)

	as.Equal(int64(1), dynamicPolicy.residualHitMap[podUID])
}

func TestStart(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestStart")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	err = dynamicPolicy.Start()
	defer dynamicPolicy.Stop()
	as.Nil(err)
}

func TestStartWithContainerCPUIdle(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestStartWithContainerCPUIdle")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)
	dynamicPolicy.config.enableContainerCPUIdle = true

	err = dynamicPolicy.Start()
	defer dynamicPolicy.Stop()
	as.Nil(err)
}

func TestStartWithContainerCPUIdleRegisterFailure(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestStartWithContainerCPUIdleRegisterFailure")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)
	dynamicPolicy.config.enableContainerCPUIdle = true

	expectedErr := fmt.Errorf("register sync container cpu idle failed")
	dynamicPolicy.registerPeriodicalHandlerHook = func(handlerName string, _ general.HealthzCheckState, _ string, _ periodicalhandler.Handler, _ time.Duration, _ int64) error {
		if handlerName == cpuconsts.SyncContainerCPUIdle {
			return expectedErr
		}
		return nil
	}

	err = dynamicPolicy.Start()
	as.ErrorIs(err, expectedErr)
	as.False(dynamicPolicy.started)
}

func TestStop(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestStop")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	err = dynamicPolicy.Stop()
	as.Nil(err)
}

func TestCheckCPUSet(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "cpu_qrm_plugin_TestCheckCPUSet")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	adjustmentCalls := 0
	dynamicPolicy.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"must-not-run-from-check": func(context.Context, dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			adjustmentCalls++
			return nil
		},
	}
	dynamicPolicy.adjustment.cpuSetAdjustmentRetryDirty = true
	dynamicPolicy.checkCPUSet(nil, nil, nil, nil, nil)
	as.Zero(adjustmentCalls)
	as.True(dynamicPolicy.adjustment.cpuSetAdjustmentRetryDirty)
}

func TestSchedIdle(t *testing.T) {
	t.Parallel()
	// this test is used to verify the sched idle support status

	as := require.New(t)

	// check if cpu.idle file exists in cgroup mount point
	_, err1 := os.Stat("/sys/fs/cgroup/cpu/kubepods/cpu.idle")
	_, err2 := os.Stat("/sys/fs/cgroup/kubepods/cpu.idle")

	support := err1 == nil && err2 == nil

	// verify if IsCPUIdleSupported returns the correct status
	as.Equalf(support, cgroupcm.IsCPUIdleSupported(), "sched idle support status isn't correct")

	if cgroupcm.IsCPUIdleSupported() {
		// get absolute cgroup path for the given subsystem and relative path
		absCgroupPath := cgroupcm.GetAbsCgroupPath("cpu", "test")

		fs := &utilfs.DefaultFs{}
		// create cgroup directory for testing
		err := fs.MkdirAll(absCgroupPath, 0o755)

		as.Nil(err)

		var enableCPUIdle bool
		// apply cpu idle configuration
		_ = cgroupcmutils.ApplyCPUWithRelativePath("test", &cgroupcm.CPUData{CpuIdlePtr: &enableCPUIdle})

		// verify if cpu.idle file is updated correctly
		contents, err := ioutil.ReadFile(filepath.Join(absCgroupPath, "cpu.idle")) //nolint:gosec
		as.Nil(err)

		as.Equal("1", contents)
	}
}

func TestRemoveContainer(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestRemoveContainer")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	podUID := string(uuid.NewUUID())
	containerName := "testName"
	testName := "testName"

	podEntries := state.PodEntries{
		podUID: state.ContainerEntries{
			containerName: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:         podUID,
					PodNamespace:   testName,
					PodName:        testName,
					ContainerName:  containerName,
					ContainerType:  pluginapi.ContainerType_MAIN.String(),
					ContainerIndex: 0,
					OwnerPoolName:  commonstate.PoolNameShare,
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:                   false,
				AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
				OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1, 9),
					1: machine.NewCPUSet(3, 11),
					2: machine.NewCPUSet(4, 5, 11, 12),
					3: machine.NewCPUSet(6, 14),
				},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1, 9),
					1: machine.NewCPUSet(3, 11),
					2: machine.NewCPUSet(4, 5, 11, 12),
					3: machine.NewCPUSet(6, 14),
				},
				RequestQuantity: 2,
			},
		},
	}

	dynamicPolicy.state.SetPodEntries(podEntries, true)

	allocationInfo := dynamicPolicy.state.GetAllocationInfo(podUID, containerName)
	as.NotNil(allocationInfo)

	dynamicPolicy.removeContainer(podUID, containerName, true)

	allocationInfo = dynamicPolicy.state.GetAllocationInfo(podUID, containerName)
	as.Nil(allocationInfo)

	dynamicPolicy.removeContainer(podUID, containerName, true)

	allocationInfo = dynamicPolicy.state.GetAllocationInfo(podUID, containerName)
	as.Nil(allocationInfo)
}

func TestShoudSharedCoresRampUp(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestShoudSharedCoresRampUp")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.state.SetAllocationInfo(commonstate.PoolNameShare, "", &state.AllocationInfo{
		AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
		OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
	}, true)

	existPodUID := uuid.NewUUID()
	existName := "exist"
	dynamicPolicy.state.SetAllocationInfo(string(existPodUID), existName, &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:         string(existPodUID),
			PodNamespace:   existName,
			PodName:        existName,
			ContainerName:  existName,
			ContainerType:  pluginapi.ContainerType_MAIN.String(),
			ContainerIndex: 0,
			OwnerPoolName:  commonstate.PoolNameShare,
			Labels: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
			QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
		},
		RampUp:                   false,
		AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
		OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		RequestQuantity: 2,
	}, true)

	testName := "test"
	podUID := uuid.NewUUID()
	dynamicPolicy.meta.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{
				PodList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      testName,
							Namespace: testName,
							UID:       podUID,
						},
						Status: v1.PodStatus{
							Phase: v1.PodRunning,
						},
					},
				},
			},
		},
	}

	req := &pluginapi.ResourceRequest{
		PodUid:         string(podUID),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	allocationInfo := dynamicPolicy.state.GetAllocationInfo(req.PodUid, testName)
	as.NotNil(allocationInfo)
	as.Equal(false, allocationInfo.RampUp)
	as.Equal(allocationInfo.OwnerPoolName, commonstate.PoolNameShare)
}

func BenchmarkGetTopologyHints(b *testing.B) {
	klog.SetOutput(ioutil.Discard)
	klog.V(0)
	klog.LogToStderr(false)
	cpuCases := []cpuTestCase{
		{
			cpuNum:      96,
			socketNum:   2,
			numaNum:     4,
			fakeNUMANum: 4,
		},
		//{
		//	cpuNum:      128,
		//	socketNum:   2,
		//	numaNum:     4,
		//	fakeNUMANum: 4,
		//},
		//{
		//	cpuNum:      192,
		//	socketNum:   2,
		//	numaNum:     8,
		//	fakeNUMANum: 8,
		//},
		{
			cpuNum:      384,
			socketNum:   2,
			numaNum:     8,
			fakeNUMANum: 8,
		},
		{
			cpuNum:      384,
			socketNum:   2,
			numaNum:     8,
			fakeNUMANum: 16,
		},
		{
			cpuNum:      384,
			socketNum:   2,
			numaNum:     8,
			fakeNUMANum: 24,
		},
		//{
		//	cpuNum:      512,
		//	socketNum:   2,
		//	numaNum:     8,
		//	fakeNUMANum: 8,
		//},
		//{
		//	cpuNum:      512,
		//	socketNum:   2,
		//	numaNum:     8,
		//	fakeNUMANum: 16,
		//},
		{
			cpuNum:      512,
			socketNum:   2,
			numaNum:     8,
			fakeNUMANum: 32,
		},
	}

	testName := "test"

	req := &pluginapi.ResourceRequest{
		PodUid:           string(uuid.NewUUID()),
		PodNamespace:     testName,
		PodName:          testName,
		ContainerName:    testName,
		ContainerType:    pluginapi.ContainerType_MAIN,
		ContainerIndex:   0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
		},
	}

	for _, tc := range cpuCases {
		tmpDir, _ := ioutil.TempDir("", "checkpoint-BenchmarkGetTopologyHints")

		cpuTopology, _ := machine.GenerateDummyCPUTopology(tc.cpuNum, tc.socketNum, tc.fakeNUMANum)

		cpusPerNUMA := cpuTopology.NumCPUs / cpuTopology.NumNUMANodes

		dynamicPolicy, _ := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)

		for _, numaNeeded := range []int{1, 2, 4} {
			req.ResourceRequests[string(v1.ResourceCPU)] = float64(numaNeeded*cpusPerNUMA - 1)
			req.Annotations = map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
			}
			req.Labels = map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
			}

			b.Run(fmt.Sprintf("%d cpus, %d sockets, %d NUMAs, %d fake-NUMAs, %d NUMAs package",
				tc.cpuNum, tc.socketNum, tc.numaNum, tc.fakeNUMANum, numaNeeded), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					dynamicPolicy.GetTopologyHints(context.Background(), req)
				}
			})

		}

		_ = os.RemoveAll(tmpDir)
	}
}

func TestSNBAdmitWithSidecarReallocate(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBAdmitWithSidecarReallocate")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(12, 1, 1)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.config.podAnnotationKeptKeys = []string{
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	testName := "test"
	sidecarName := "sidecar"
	podUID := string(uuid.NewUUID())
	// admit sidecar container
	sidecarReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 8}`,
		},
	}

	res, err := dynamicPolicy.GetTopologyHints(context.Background(), sidecarReq)
	as.Nil(err)
	as.Nil(res.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)

	// admit main container
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 6,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 8}`,
		},
	}

	res, err = dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	hints := res.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(hints))
	req.Hint = hints[0]

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	// we cannot GetResourceAllocation here, it will
	// not sidecar container is wait for reallocate, only main container is in state file
	sidecarAllocation := dynamicPolicy.state.GetAllocationInfo(podUID, sidecarName)
	as.Nil(sidecarAllocation)

	mainAllocation := dynamicPolicy.state.GetAllocationInfo(podUID, testName)
	as.NotNil(mainAllocation)

	// another container before reallocate with 4 core (because share-NUMA0 has only 11 core and 6 cores is allocated to podUID/test).
	anotherReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  "test1",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 4,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 4}`,
		},
	}

	// pod aggregated size is 8, the new container request is 4, 8 + 4 > 11 (share-NUMA0 size)
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), anotherReq)
	as.ErrorContains(err, cpuutil.ErrNoAvailableCPUHints.Error())

	// reallocate sidecar
	_, err = dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)
	sidecarAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, sidecarName)
	as.NotNil(sidecarAllocation)
}

func TestSNBCpuRequestWithFloat(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBCpuRequestWithFloat")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(12, 1, 1)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.config.podAnnotationKeptKeys = []string{
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	testName := "test"
	podUID := string(uuid.NewUUID())

	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  "test1",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 9.5,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 9.5}`,
		},
	}

	res, err := dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	hints := res.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(hints))
	req.Hint = hints[0]

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	// admit another pod with 0.5c
	req2 := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  "test2",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 0.5,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 0.5}`,
		},
	}

	res2, err := dynamicPolicy.GetTopologyHints(context.Background(), req2)
	as.Nil(err)
	hints2 := res2.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(hints2))
	req2.Hint = hints2[0]

	_, err = dynamicPolicy.Allocate(context.Background(), req2)
	as.Nil(err)
}

type mockCPUAdvisor struct {
	mock.Mock
	advisorapi.CPUAdvisorServer
}

func (m *mockCPUAdvisor) GetAdvice(
	ctx context.Context, in *advisorapi.GetAdviceRequest,
) (*advisorapi.GetAdviceResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*advisorapi.GetAdviceResponse), args.Error(1)
}

func (m *mockCPUAdvisor) ListAndWatch(in *advisorsvc.Empty, srv advisorapi.CPUAdvisor_ListAndWatchServer) error {
	args := m.Called(in, srv)
	return args.Error(0)
}

func (m *mockCPUAdvisor) AddContainer(
	ctx context.Context, req *advisorsvc.ContainerMetadata,
) (*advisorsvc.AddContainerResponse, error) {
	return &advisorsvc.AddContainerResponse{}, nil
}

func (m *mockCPUAdvisor) RemovePod(
	ctx context.Context, req *advisorsvc.RemovePodRequest,
) (*advisorsvc.RemovePodResponse, error) {
	return &advisorsvc.RemovePodResponse{}, nil
}

func enableAdvisorForTest(dynamicPolicy *DynamicPolicy, pluginSocketDir string) {
	dynamicPolicy.config.enableCPUAdvisor = true
	dynamicPolicy.config.getAdviceInterval = 1 * time.Second

	dynamicPolicy.config.cpuPluginSocketAbsPath = filepath.Join(pluginSocketDir, "cpu-plugin.sock")
	dynamicPolicy.config.cpuAdvisorSocketAbsPath = filepath.Join(pluginSocketDir, "cpu-advisor.sock")
}

// TestSwitchToGetAdviceRPC tests the cpu plugin can switch to GetAdvice RPC when cpu-advisor implements it,
// and switch back to ListAndWatch when cpu-advisor doesn't implement GetAdvice.
func TestSwitchBetweenAPIs(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		fn   func(t *testing.T, cpuAdvisorServer *mockCPUAdvisor, dynamicPolicy *DynamicPolicy)
	}{
		{
			name: "switch from ListAndWatch to GetAdvice",
			fn: func(t *testing.T, cpuAdvisorServer *mockCPUAdvisor, dynamicPolicy *DynamicPolicy) {
				// - start cpu-advisor that doesn't implement GetAdvice
				// - start cpu plugin and verify that it uses ListAndWatch
				// - 'upgrade' cpu-advisor to implement GetAdvice
				// - verify that cpu plugin switches to GetAdvice
				as := require.New(t)

				lwEndedChan := make(chan time.Time)
				lwStartedChan := make(chan struct{})
				unimplementedGetAdviceCall := cpuAdvisorServer.
					On("GetAdvice", mock.Anything, mock.Anything).
					Once().
					Return((*advisorapi.GetAdviceResponse)(nil), status.Error(codes.Unimplemented, "GetAdvice not implemented"))
				cpuAdvisorServer.On("ListAndWatch", mock.Anything, mock.Anything).
					Once().
					Run(func(args mock.Arguments) {
						close(lwStartedChan)
						<-lwEndedChan
					}).
					Return(nil).
					NotBefore(unimplementedGetAdviceCall)

				err := dynamicPolicy.Start()
				as.NoError(err)
				defer dynamicPolicy.Stop()

				// Wait for the plugin to call advisor
				select {
				case <-lwStartedChan:
				case <-time.After(10 * time.Second):
					t.Fatalf("ListAndWatch not called")
				}
				cpuAdvisorServer.AssertExpectations(t)

				// ListAndWatch in progress, simulate an upgrade
				unimplementedGetAdviceCall.Unset()

				getAdviceCalledChan := make(chan struct{})
				cpuAdvisorServer.
					On("GetAdvice", mock.Anything, mock.Anything).
					Run(func(args mock.Arguments) {
						getAdviceCalledChan <- struct{}{}
					}).
					Return(&advisorapi.GetAdviceResponse{}, nil)

				// terminate the old ListAndWatch
				lwEndedChan <- time.Now()

				// plugin should switch to GetAdvice and call it periodically
				for i := 0; i < 5; i++ {
					select {
					case <-getAdviceCalledChan:
					case <-time.After(dynamicPolicy.config.getAdviceInterval * 2):
						t.Fatalf("GetAdvice not called")
					}
				}
				// ListAndWatch should only be called once throughout
				cpuAdvisorServer.AssertExpectations(t)
			},
		},
		{
			name: "switch from GetAdvice to ListAndWatch",
			fn: func(t *testing.T, cpuAdvisorServer *mockCPUAdvisor, dynamicPolicy *DynamicPolicy) {
				// - start cpu-advisor that implements GetAdvice
				// - start cpu plugin and verify that it uses GetAdvice
				// - 'downgrade' cpu-advisor to a version that doesn't implement GetAdvice
				// - verify that cpu plugin switches to ListAndWatch
				as := require.New(t)

				getAdviceCalledChan := make(chan struct{})
				implementedGetAdviceCall := cpuAdvisorServer.
					On("GetAdvice", mock.Anything, mock.Anything).
					Run(func(args mock.Arguments) {
						select {
						case getAdviceCalledChan <- struct{}{}:
						default:
						}
					}).
					Return(&advisorapi.GetAdviceResponse{}, nil)

				err := dynamicPolicy.Start()
				as.NoError(err)
				defer dynamicPolicy.Stop()

				// Ensure GetAdvice is called periodically
				for i := 0; i < 5; i++ {
					select {
					case <-getAdviceCalledChan:
					case <-time.After(dynamicPolicy.config.getAdviceInterval * 2):
						t.Fatalf("GetAdvice not called")
					}
				}
				cpuAdvisorServer.AssertExpectations(t)

				// simulate a downgrade
				// lock to ensure that policy doesn't call GetAdvice after we have removed the old expectation but haven't set the new one
				dynamicPolicy.Lock()
				implementedGetAdviceCall.Unset()
				unimplementedGetAdviceCall := cpuAdvisorServer.
					On("GetAdvice", mock.Anything, mock.Anything).
					Once().
					Return((*advisorapi.GetAdviceResponse)(nil), status.Error(codes.Unimplemented, "GetAdvice not implemented"))
				cpuAdvisorServer.On("ListAndWatch", mock.Anything, mock.Anything).
					After(3 * time.Hour).
					NotBefore(unimplementedGetAdviceCall).
					Once().
					Return(nil)
				dynamicPolicy.Unlock()

				time.Sleep(3 * time.Second)
				cpuAdvisorServer.AssertExpectations(t)
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			as := require.New(t)
			tmpDir, err := ioutil.TempDir("", "*")
			as.NoError(err)
			defer func() { _ = os.RemoveAll(tmpDir) }()

			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			as.NoError(err)

			checkpointDir := filepath.Join(tmpDir, "checkpoint")
			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, checkpointDir)
			as.NoError(err)
			podEntries := state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   "default",
							PodName:        "test",
							ContainerName:  "main",
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "1",
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
					"sidecar": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   "default",
							PodName:        "test",
							ContainerName:  "sidecar",
							ContainerType:  pluginapi.ContainerType_SIDECAR.String(),
							ContainerIndex: 1,
							OwnerPoolName:  commonstate.EmptyOwnerPoolName,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
			}
			machineState, err := generateMachineStateFromPodEntries(cpuTopology, podEntries, nil)
			as.Nil(err)

			dynamicPolicy.state.SetPodEntries(podEntries, true)
			dynamicPolicy.state.SetMachineState(machineState, true)

			enableAdvisorForTest(dynamicPolicy, tmpDir)

			grpcServer := grpc.NewServer()
			cpuAdvisorServer := &mockCPUAdvisor{}
			advisorapi.RegisterCPUAdvisorServer(grpcServer, cpuAdvisorServer)
			lis, err := net.Listen("unix", dynamicPolicy.config.cpuAdvisorSocketAbsPath)
			as.NoError(err)
			defer func() { _ = lis.Close() }()
			go func() { _ = grpcServer.Serve(lis) }()
			defer grpcServer.Stop()

			testCase.fn(t, cpuAdvisorServer, dynamicPolicy)
		})
	}
}

func generateTestConfiguration(t *testing.T, nodeName string, dir string) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	testConfiguration.NodeName = nodeName
	testConfiguration.CheckpointManagerDir = dir
	return testConfiguration
}

func TestNewDynamicPolicy(t *testing.T) {
	t.Parallel()
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)
	conf := config.NewConfiguration()
	kccConf := generateTestConfiguration(t, "test", "/tmp/metaserver1/TestNewDynamicConfigManager")
	kccMgr, _ := kcc.NewDynamicConfigManager(nil, nil, nil, kccConf)
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: cpuTopology,
			},
		},
		ConfigurationManager: kccMgr,
	}
	testingDir, err := ioutil.TempDir("", "dynamic_policy_new")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testingDir)
	conf.GenericQRMPluginConfiguration.StateDirectoryConfiguration = &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: testingDir,
	}
	type args struct {
		agentCtx  *componentagent.GenericContext
		conf      *config.Configuration
		in2       interface{}
		agentName string
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "normal case",
			args: args{
				agentCtx: &componentagent.GenericContext{
					GenericContext: &katalystbase.GenericContext{
						EmitterPool: metricspool.DummyMetricsEmitterPool{},
					},
					MetaServer: metaServer,
				},
				conf:      conf,
				agentName: "test",
			},
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := NewDynamicPolicy(tt.args.agentCtx, tt.args.conf, nil, tt.args.agentName)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewDynamicPolicy() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("NewDynamicPolicy() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDynamicPolicy_AllocationHooks(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		req          *pluginapi.ResourceRequest
		hook         AllocationHook
		expectErr    bool
		errContains  string
		preRun       func(policy *DynamicPolicy)
		verifyResult func(t *testing.T, policy *DynamicPolicy)
	}{
		{
			name: "hook modifies allocation",
			req: &pluginapi.ResourceRequest{
				PodUid:        "modify-pod-uid",
				PodNamespace:  "default",
				PodName:       "modify-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "modify-pod" {
					if newAlloc.Annotations == nil {
						newAlloc.Annotations = make(map[string]string)
					}
					newAlloc.Annotations["hook-modified"] = "true"
				}
				return nil
			},
			expectErr: false,
			verifyResult: func(t *testing.T, policy *DynamicPolicy) {
				allocInfo := policy.state.GetAllocationInfo("modify-pod-uid", "c1")
				require.NotNil(t, allocInfo)
				require.Equal(t, "true", allocInfo.Annotations["hook-modified"])
			},
		},
		{
			name: "hook modifies allocation for reclaimed_cores",
			req: &pluginapi.ResourceRequest{
				PodUid:        "modify-reclaimed-pod-uid",
				PodNamespace:  "default",
				PodName:       "modify-reclaimed-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "modify-reclaimed-pod" {
					if newAlloc.Annotations == nil {
						newAlloc.Annotations = make(map[string]string)
					}
					newAlloc.Annotations["hook-modified-reclaimed"] = "true"
				}
				return nil
			},
			expectErr: false,
			verifyResult: func(t *testing.T, policy *DynamicPolicy) {
				allocInfo := policy.state.GetAllocationInfo("modify-reclaimed-pod-uid", "c1")
				require.NotNil(t, allocInfo)
				require.Equal(t, "true", allocInfo.Annotations["hook-modified-reclaimed"])
			},
		},
		{
			name: "hook modifies allocation for dedicated_cores",
			req: &pluginapi.ResourceRequest{
				PodUid:        "modify-dedicated-pod-uid",
				PodNamespace:  "default",
				PodName:       "modify-dedicated-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "modify-dedicated-pod" {
					if newAlloc.Annotations == nil {
						newAlloc.Annotations = make(map[string]string)
					}
					newAlloc.Annotations["hook-modified-dedicated"] = "true"
				}
				return nil
			},
			expectErr: false,
			verifyResult: func(t *testing.T, policy *DynamicPolicy) {
				allocInfo := policy.state.GetAllocationInfo("modify-dedicated-pod-uid", "c1")
				require.NotNil(t, allocInfo)
				require.Equal(t, "true", allocInfo.Annotations["hook-modified-dedicated"])
			},
		},
		{
			name: "hook modifies allocation for system_cores",
			req: &pluginapi.ResourceRequest{
				PodUid:        "modify-system-pod-uid",
				PodNamespace:  "default",
				PodName:       "modify-system-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "modify-system-pod" {
					if newAlloc.Annotations == nil {
						newAlloc.Annotations = make(map[string]string)
					}
					newAlloc.Annotations["hook-modified-system"] = "true"
				}
				return nil
			},
			expectErr: false,
			verifyResult: func(t *testing.T, policy *DynamicPolicy) {
				allocInfo := policy.state.GetAllocationInfo("modify-system-pod-uid", "c1")
				require.NotNil(t, allocInfo)
				require.Equal(t, "true", allocInfo.Annotations["hook-modified-system"])
			},
		},
		{
			name: "hook modifies allocation for sidecar container",
			req: &pluginapi.ResourceRequest{
				PodUid:        "modify-sidecar-pod-uid",
				PodNamespace:  "default",
				PodName:       "modify-sidecar-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_SIDECAR,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "modify-sidecar-pod" {
					if newAlloc.Annotations == nil {
						newAlloc.Annotations = make(map[string]string)
					}
					newAlloc.Annotations["hook-modified-sidecar"] = "true"
				}
				return nil
			},
			preRun: func(policy *DynamicPolicy) {
				policy.state.SetAllocationInfo("modify-sidecar-pod-uid", "main-c", &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "modify-sidecar-pod-uid",
						PodNamespace:  "default",
						PodName:       "modify-sidecar-pod",
						ContainerName: "main-c",
						ContainerType: pluginapi.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
					},
					AllocationResult: machine.NewCPUSet(0, 1),
				}, false)
			},
			expectErr: false,
			verifyResult: func(t *testing.T, policy *DynamicPolicy) {
				allocInfo := policy.state.GetAllocationInfo("modify-sidecar-pod-uid", "c1")
				require.NotNil(t, allocInfo)
				require.Equal(t, "true", allocInfo.Annotations["hook-modified-sidecar"])
			},
		},
		{
			name: "hook returns error for shared_cores",
			req: &pluginapi.ResourceRequest{
				PodUid:        "error-pod-uid",
				PodNamespace:  "default",
				PodName:       "error-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "error-pod" {
					return fmt.Errorf("hook injected error")
				}
				return nil
			},
			expectErr:   true,
			errContains: "hook injected error",
			verifyResult: func(t *testing.T, policy *DynamicPolicy) {
				allocInfo := policy.state.GetAllocationInfo("error-pod-uid", "c1")
				require.Nil(t, allocInfo)
			},
		},
		{
			name: "hook returns error for reclaimed_cores",
			req: &pluginapi.ResourceRequest{
				PodUid:        "error-reclaimed-pod-uid",
				PodNamespace:  "default",
				PodName:       "error-reclaimed-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "error-reclaimed-pod" {
					return fmt.Errorf("hook injected error for reclaimed")
				}
				return nil
			},
			expectErr:   true,
			errContains: "hook injected error for reclaimed",
		},
		{
			name: "hook returns error for dedicated_cores",
			req: &pluginapi.ResourceRequest{
				PodUid:        "error-dedicated-pod-uid",
				PodNamespace:  "default",
				PodName:       "error-dedicated-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "error-dedicated-pod" {
					return fmt.Errorf("hook injected error for dedicated")
				}
				return nil
			},
			expectErr:   true,
			errContains: "hook injected error for dedicated",
		},
		{
			name: "hook returns error for system_cores",
			req: &pluginapi.ResourceRequest{
				PodUid:        "error-system-pod-uid",
				PodNamespace:  "default",
				PodName:       "error-system-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "error-system-pod" {
					return fmt.Errorf("hook injected error for system")
				}
				return nil
			},
			expectErr:   true,
			errContains: "hook injected error for system",
		},
		{
			name: "hook returns error for sidecar container",
			req: &pluginapi.ResourceRequest{
				PodUid:        "error-sidecar-pod-uid",
				PodNamespace:  "default",
				PodName:       "error-sidecar-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_SIDECAR,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "error-sidecar-pod" {
					return fmt.Errorf("hook injected error for sidecar")
				}
				return nil
			},
			preRun: func(policy *DynamicPolicy) {
				policy.state.SetAllocationInfo("error-sidecar-pod-uid", "main-c", &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "error-sidecar-pod-uid",
						PodNamespace:  "default",
						PodName:       "error-sidecar-pod",
						ContainerName: "main-c",
						ContainerType: pluginapi.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
					},
					AllocationResult: machine.NewCPUSet(0, 1),
				}, false)
			},
			expectErr:   true,
			errContains: "hook injected error for sidecar",
		},
		{
			name: "hook returns error for shared_cores with NUMA binding",
			req: &pluginapi.ResourceRequest{
				PodUid:        "error-shared-numa-pod-uid",
				PodNamespace:  "default",
				PodName:       "error-shared-numa-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				if newAlloc.PodName == "error-shared-numa-pod" {
					return fmt.Errorf("hook injected error for shared numa")
				}
				return nil
			},
			expectErr:   true,
			errContains: "hook injected error for shared numa",
		},
		{
			name: "hook returns error for dedicated_cores without numa_binding",
			req: &pluginapi.ResourceRequest{
				PodUid:        "error-dedicated-no-numa-pod-uid",
				PodNamespace:  "default",
				PodName:       "error-dedicated-no-numa-pod",
				ContainerName: "c1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
			},
			hook: func(oldAlloc, newAlloc *state.AllocationInfo) error {
				return nil
			},
			expectErr:   true,
			errContains: "not support dedicated_cores without NUMA binding",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			require.NoError(t, err)

			policy, err := getTestDynamicPolicyWithInitialization(topology, tmpDir)
			require.NoError(t, err)
			defer func() {
				_ = policy.Stop()
			}()

			if tc.preRun != nil {
				tc.preRun(policy)
			}

			policy.RegisterAllocationHook(tc.hook)

			resp, err := policy.Allocate(context.TODO(), tc.req)
			if tc.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errContains)
				require.Nil(t, resp)
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
			}

			if tc.verifyResult != nil {
				tc.verifyResult(t, policy)
			}
		})
	}
}

func TestDynamicPolicy_InvokeAllocationHooksForPodEntries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		setupHook    func(policy *DynamicPolicy)
		verifyResult func(t *testing.T, err error, newEntries state.PodEntries)
	}{
		{
			name:      "success without hooks",
			setupHook: func(policy *DynamicPolicy) {},
			verifyResult: func(t *testing.T, err error, newEntries state.PodEntries) {
				require.NoError(t, err)
			},
		},
		{
			name: "success with hooks modifying allocation info",
			setupHook: func(policy *DynamicPolicy) {
				modifyHook := func(oldAlloc, newAlloc *state.AllocationInfo) error {
					if oldAlloc != nil {
						newAlloc.Annotations["hook-saw-old"] = oldAlloc.Annotations["old"]
					} else {
						newAlloc.Annotations["hook-saw-old"] = "false"
					}
					return nil
				}
				policy.RegisterAllocationHook(modifyHook)
			},
			verifyResult: func(t *testing.T, err error, newEntries state.PodEntries) {
				require.NoError(t, err)
				assert.Equal(t, "true", newEntries["pod-1"]["container-1"].Annotations["hook-saw-old"])
				assert.Equal(t, "false", newEntries["pod-2"]["container-2"].Annotations["hook-saw-old"])
			},
		},
		{
			name: "hook returns error",
			setupHook: func(policy *DynamicPolicy) {
				errorHook := func(oldAlloc, newAlloc *state.AllocationInfo) error {
					if newAlloc.PodUid == "pod-2" {
						return fmt.Errorf("injected error for pod-2")
					}
					return nil
				}
				policy.RegisterAllocationHook(errorHook)
			},
			verifyResult: func(t *testing.T, err error, newEntries state.PodEntries) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invokeAllocationHooks failed for pod: pod-2")
				assert.Contains(t, err.Error(), "injected error for pod-2")
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			require.NoError(t, err)

			policy, err := getTestDynamicPolicyWithInitialization(topology, tmpDir)
			require.NoError(t, err)
			defer func() {
				_ = policy.Stop()
			}()

			curEntries := state.PodEntries{
				"pod-1": state.ContainerEntries{
					"container-1": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "pod-1",
							ContainerName: "container-1",
							Annotations:   map[string]string{"old": "true"},
						},
					},
				},
				"pool-1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "pool-1",
							ContainerName: "",
						},
					},
				},
			}

			newEntries := state.PodEntries{
				"pod-1": state.ContainerEntries{
					"container-1": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "pod-1",
							ContainerName: "container-1",
							Annotations:   map[string]string{"new": "true"},
						},
					},
				},
				"pod-2": state.ContainerEntries{
					"container-2": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "pod-2",
							ContainerName: "container-2",
							Annotations:   map[string]string{"new": "true"},
						},
					},
				},
				"pool-1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "pool-1",
							ContainerName: "",
						},
					},
				},
			}

			tc.setupHook(policy)
			err = policy.invokeAllocationHooksForPodEntries(curEntries, newEntries)
			tc.verifyResult(t, err, newEntries)
		})
	}
}
