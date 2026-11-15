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

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/reclaim"
)

// serverGlobalRegistryMu serializes tests in this package that mutate
// the process-global reclaim consumer registry (or assert on cgroup
// paths derived from it). Tests hold this lock across their body so
// two concurrent tests can't observe each other's half-registered
// state or leaked entries.
var serverGlobalRegistryMu sync.Mutex

func generateTestConfiguration(t *testing.T) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	tmpStateDir, err := ioutil.TempDir("", fmt.Sprintf("sys-advisor-test.%s", rand.String(5)))
	require.NoError(t, err)
	tmpCPUAdvisorSocketDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)
	tmpCPUPluginSocketDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir
	conf.QRMAdvisorConfiguration.CPUAdvisorSocketAbsPath = tmpCPUAdvisorSocketDir + "-cpu_advisor.sock"
	conf.QRMAdvisorConfiguration.CPUPluginSocketAbsPath = tmpCPUPluginSocketDir + "-cpu_plugin.sock"

	return conf
}

func newTestCPUServer(t *testing.T, advisor subResourceAdvisor, podList []*v1.Pod) *cpuServer {
	conf := generateTestConfiguration(t)
	metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{
				PodList: podList,
			},
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: &machine.CPUTopology{
					NumSockets:   2,
					NumNUMANodes: 2,
				},
			},
		},
	}

	cpuServer, err := NewCPUServer(conf, &reporter.DummyHeadroomResourceManager{}, metaCache, metaServer, advisor, metrics.DummyMetrics{})
	cpuServer.startTime = time.Now().Add(-types.StartUpPeriod)
	require.NoError(t, err)
	require.NotNil(t, cpuServer)

	return cpuServer
}

func TestCPUServerStartAndStop(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, nil, []*v1.Pod{})

	err := cs.Start()
	assert.NoError(t, err)

	conn, err := cs.dial(cs.advisorSocketPath, cs.period)
	assert.NoError(t, err, "failed to dial check cpu server")
	assert.NotNil(t, conn, "invalid conn")
	_ = conn.Close()

	err = cs.Stop()
	assert.NoError(t, err)
}

func TestCPUServerGetAdvicePropagatesDedicatedReclaimDisjoint(t *testing.T) {
	t.Parallel()

	advisor := &mockCPUResourceAdvisor{
		provision: &types.InternalCPUCalculationResult{
			PoolEntries:                 map[string]map[int]types.CPUResource{},
			PoolOverlapInfo:             map[string]map[int]map[string]int{},
			PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
		},
		disableDedicatedOverlap: true,
	}
	cs := newTestCPUServer(t, advisor, nil)
	// The server publishes the dedicated-reclaim-disjoint flag from this cycle's
	// CPUAdvice (the advisor's single sampling), not from a fresh dynamicConf read.

	resp, err := cs.GetAdvice(context.Background(), &cpuadvisor.GetAdviceRequest{
		WantedFeatureGates: map[string]*advisorsvc.FeatureGate{
			feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
				Name:                  feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
				Type:                  finders.FeatureGateTypeCPU,
				MustMutuallySupported: true,
			},
		},
	})
	require.NoError(t, err)
	require.True(t, resp.DisableDedicatedCoresOverlapReclaimedCores)
	require.Contains(t, resp.SupportedFeatureGates, feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition)

	_, err = cs.GetAdvice(context.Background(), &cpuadvisor.GetAdviceRequest{})
	require.ErrorContains(t, err, feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition)
}

// TestCPUServerPublishesAdviceOverlapFlagsNotFreshConfig pins the P1 contract:
// the proto publish and feature-gate negotiation must use the overlap flags
// carried out on CPUAdvice (the advisor's single sampling this cycle). If a
// config swap changed cs.conf between advisor computation and the server
// publish, the published value must still match the advice, not the fresh conf.
func TestCPUServerPublishesAdviceOverlapFlagsNotFreshConfig(t *testing.T) {
	t.Parallel()

	// Mock advisor samples the cycle with dedicated-overlap ENABLED.
	advisor := &mockCPUResourceAdvisor{
		provision: &types.InternalCPUCalculationResult{
			PoolEntries:                 map[string]map[int]types.CPUResource{},
			PoolOverlapInfo:             map[string]map[int]map[string]int{},
			PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
		},
		disableDedicatedOverlap: true,
	}
	cs := newTestCPUServer(t, advisor, nil)
	// Meanwhile the live config has been swapped to the opposite value; the server
	// must ignore it when publishing / gating.
	cs.conf.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = false

	resp, err := cs.GetAdvice(context.Background(), &cpuadvisor.GetAdviceRequest{
		WantedFeatureGates: map[string]*advisorsvc.FeatureGate{
			feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
				Name:                  feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
				Type:                  finders.FeatureGateTypeCPU,
				MustMutuallySupported: true,
			},
		},
	})
	require.NoError(t, err)
	require.True(t, resp.DisableDedicatedCoresOverlapReclaimedCores, "must publish the advice sample, not the swapped config")
}

func TestConvertInternalCPUResultToListAndWatchResponsePropagatesDedicatedReclaimDisjoint(t *testing.T) {
	t.Parallel()

	resp := convertInternalCPUResultToListAndWatchResponse(&cpuInternalResult{
		DisableDedicatedCoresOverlapReclaimedCores: true,
	})

	require.True(t, resp.DisableDedicatedCoresOverlapReclaimedCores)
}

func TestCPUServerGetAndPushAdviceRejectsDedicatedReclaimDisjoint(t *testing.T) {
	t.Parallel()

	advisor := &mockCPUResourceAdvisor{
		provision: &types.InternalCPUCalculationResult{
			PoolEntries:                 map[string]map[int]types.CPUResource{},
			PoolOverlapInfo:             map[string]map[int]map[string]int{},
			PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
		},
		disableDedicatedOverlap: true,
	}
	cs := newTestCPUServer(t, advisor, nil)
	// The server gates the dedicated-reclaim-disjoint push from this cycle's
	// CPUAdvice flag, not from a fresh dynamicConf read.

	client := &mockCPUPluginClient{
		checkpoint: &cpuadvisor.GetCheckpointResponse{
			Entries: map[string]*cpuadvisor.AllocationEntries{
				commonstate.PoolNameReserve: {
					Entries: map[string]*cpuadvisor.AllocationInfo{
						commonstate.FakedContainerName: {
							OwnerPoolName: commonstate.PoolNameReserve,
						},
					},
				},
			},
		},
	}
	stream := &recordingCPUListAndWatchServer{}

	err := cs.getAndPushAdvice(client, stream)

	require.EqualError(t, err, "list and watch does not support dedicated reclaim disjoint partition")
	require.Zero(t, stream.sendCalls)
}

func TestAssemblePoolEntriesDoesNotUseLiveReclaimWithoutRampUp(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
	cs.conf.GetDynamicConfiguration().EnableReclaim = true
	cs.conf.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	require.NoError(t, cs.metaCache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
		PoolName: commonstate.PoolNameReclaim,
		TopologyAwareAssignments: types.TopologyAwareAssignment{
			0: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		},
	}))

	entries := make(map[string]*cpuadvisor.CalculationEntries)
	bs := NewBlockSet()
	cs.assemblePoolEntries(&types.InternalCPUCalculationResult{
		PoolEntries: map[string]map[int]types.CPUResource{
			commonstate.PoolNameReclaim: {
				0: {Size: 6, Quota: -1},
			},
		},
	}, entries, bs)

	reclaimResult := entries[commonstate.PoolNameReclaim].Entries[commonstate.FakedContainerName].CalculationResultsByNumas[0]
	require.Len(t, reclaimResult.Blocks, 1)
	require.Equal(t, uint64(6), reclaimResult.Blocks[0].Result)
}

// TestAssemblePoolEntriesIsSerializerOnly verifies that the cpu server never
// modifies PoolEntry sizes during serialization. The assembler is the single
// conservation boundary; the server only converts Size to proto block.Result.
func TestAssemblePoolEntriesIsSerializerOnly(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
	require.NoError(t, cs.metaCache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
		PoolName: commonstate.PoolNameReclaim,
		TopologyAwareAssignments: types.TopologyAwareAssignment{
			0: machine.NewCPUSet(0, 1),
		},
	}))

	entries := make(map[string]*cpuadvisor.CalculationEntries)
	bs := NewBlockSet()
	cs.assemblePoolEntries(&types.InternalCPUCalculationResult{
		PoolEntries: map[string]map[int]types.CPUResource{
			commonstate.PoolNameReclaim: {
				0: {Size: 1, Quota: -1},
			},
		},
	}, entries, bs)

	reclaimResult := entries[commonstate.PoolNameReclaim].Entries[commonstate.FakedContainerName].CalculationResultsByNumas[0]
	require.Len(t, reclaimResult.Blocks, 1)
	// Server must NOT clamp Size=1 up to live=2; assembler handles live continuity.
	require.Equal(t, uint64(1), reclaimResult.Blocks[0].Result)
}

func TestAssemblePoolEntriesDoesNotOverrideAdvisorTargetWithLiveQRMReclaimSizeWhenHardPartitionEnabled(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
	cs.conf.GetDynamicConfiguration().EnableReclaim = true
	cs.conf.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	require.NoError(t, cs.metaCache.AddContainer("ramp-up-pod", "main", &types.ContainerInfo{
		PodUID:        "ramp-up-pod",
		ContainerName: "main",
		RampUp:        true,
	}))
	require.NoError(t, cs.metaCache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
		PoolName: commonstate.PoolNameReclaim,
		TopologyAwareAssignments: types.TopologyAwareAssignment{
			0: machine.NewCPUSet(0, 1),
		},
	}))

	entries := make(map[string]*cpuadvisor.CalculationEntries)
	bs := NewBlockSet()
	cs.assemblePoolEntries(&types.InternalCPUCalculationResult{
		PoolEntries: map[string]map[int]types.CPUResource{
			commonstate.PoolNameReclaim: {
				0: {Size: 1, Quota: -1},
			},
		},
	}, entries, bs)

	reclaimResult := entries[commonstate.PoolNameReclaim].Entries[commonstate.FakedContainerName].CalculationResultsByNumas[0]
	require.Len(t, reclaimResult.Blocks, 1)
	require.Equal(t, uint64(1), reclaimResult.Blocks[0].Result)
}

// TestAssemblePoolEntriesIncludesSyntheticDefaultShare pins the existing wire
// contract: even when there is no share Pod, assemblePoolEntries must serialize
// the synthesized default share (keyed by PoolNameShare / FakedNUMAID) into
// exactly one block whose Result equals the requested pool size. This is a
// characterization test guarding the serialization behavior relied upon by the
// downstream QRM consumer; it must not be "fixed" by touching the proto or the
// production assembly logic.
func TestAssemblePoolEntriesIncludesSyntheticDefaultShare(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
	entries := make(map[string]*cpuadvisor.CalculationEntries)
	bs := NewBlockSet()

	cs.assemblePoolEntries(&types.InternalCPUCalculationResult{
		PoolEntries: map[string]map[int]types.CPUResource{
			commonstate.PoolNameShare: {
				commonstate.FakedNUMAID: {Size: 134, Quota: -1},
			},
			// These reclaim entries only shape a realistic scenario where share and
			// reclaim coexist; they are intentionally not covered by the assertions below.
			commonstate.PoolNameReclaim: {
				0: {Size: 28, Quota: -1},
				1: {Size: 28, Quota: -1},
			},
		},
	}, entries, bs)

	shareResult := entries[commonstate.PoolNameShare].
		Entries[commonstate.FakedContainerName].
		CalculationResultsByNumas[commonstate.FakedNUMAID]
	require.Len(t, shareResult.Blocks, 1)
	require.Equal(t, uint64(134), shareResult.Blocks[0].Result)
}

func TestAssemblePoolEntriesKeepsAssemblerStandaloneReclaimWithinCapacity(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
	entries := make(map[string]*cpuadvisor.CalculationEntries)
	bs := NewBlockSet()
	cs.assemblePoolEntries(&types.InternalCPUCalculationResult{
		PoolEntries: map[string]map[int]types.CPUResource{
			"share": {
				0: {Size: 8, Quota: -1},
			},
			"dedicated-pod": {
				0: {Size: 8, Quota: -1},
			},
			commonstate.PoolNameReclaim: {
				0: {Size: 4, Quota: -1},
			},
		},
	}, entries, bs)

	var physicalBlockTotal uint64
	for _, blocks := range bs {
		require.NotEmpty(t, blocks)
		physicalBlockTotal += blocks[0].Block.Result
	}
	require.Equal(t, uint64(20), physicalBlockTotal)
}

func TestCPUServerAddContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		requestMetadata   metadata.MD
		request           *advisorsvc.ContainerMetadata
		want              *advisorsvc.AddContainerResponse
		wantErr           bool
		wantContainerInfo *types.ContainerInfo
	}{
		{
			name: "test1",
			request: &advisorsvc.ContainerMetadata{
				PodUid:          "testUID",
				PodNamespace:    "testPodNamespace",
				PodName:         "testPodName",
				ContainerName:   "testContainerName",
				ContainerType:   1,
				ContainerIndex:  0,
				Labels:          map[string]string{"key": "label"},
				Annotations:     map[string]string{"key": "label"},
				QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
				RequestQuantity: 1,
			},
			want:    &advisorsvc.AddContainerResponse{},
			wantErr: false,
			wantContainerInfo: &types.ContainerInfo{
				PodUID:         "testUID",
				PodNamespace:   "testPodNamespace",
				PodName:        "testPodName",
				ContainerName:  "testContainerName",
				ContainerType:  1,
				ContainerIndex: 0,
				Labels:         map[string]string{"key": "label"},
				Annotations:    map[string]string{"key": "label"},
				QoSLevel:       consts.PodAnnotationQoSLevelSharedCores,
				CPURequest:     1,
				RegionNames:    sets.NewString(),
			},
		},
		{
			name: "should ignore the request if it's sent by a plugin that supports GetAdvice",
			requestMetadata: map[string][]string{
				util.AdvisorRPCMetadataKeySupportsGetAdvice: {util.AdvisorRPCMetadataValueSupportsGetAdvice},
			},
			request: &advisorsvc.ContainerMetadata{
				PodUid:          "testUID",
				PodNamespace:    "testPodNamespace",
				PodName:         "testPodName",
				ContainerName:   "testContainerName",
				ContainerType:   1,
				ContainerIndex:  0,
				Labels:          map[string]string{"key": "label"},
				Annotations:     map[string]string{"key": "label"},
				QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
				RequestQuantity: 1,
			},
			want:              &advisorsvc.AddContainerResponse{},
			wantErr:           false,
			wantContainerInfo: nil,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cs := newTestCPUServer(t, nil, []*v1.Pod{})
			ctx := context.Background()
			if tt.requestMetadata != nil {
				ctx = metadata.NewIncomingContext(ctx, tt.requestMetadata)
			}
			got, err := cs.AddContainer(ctx, tt.request)
			if (err != nil) != tt.wantErr {
				t.Errorf("AddContainer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AddContainer() got = %v, want %v", got, tt.want)
			}

			containerInfo, ok := cs.metaCache.GetContainerInfo(tt.request.PodUid, tt.request.ContainerName)
			if tt.wantContainerInfo == nil {
				assert.False(t, ok)
				assert.Nil(t, containerInfo)
			} else {
				assert.True(t, ok)
				assert.Equal(t, tt.wantContainerInfo, containerInfo)
			}
		})
	}
}

func TestCPUServerRemovePod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		requestMetadata metadata.MD
		request         *advisorsvc.RemovePodRequest
		want            *advisorsvc.RemovePodResponse
		wantErr         bool
		wantRemoved     bool
	}{
		{
			name: "test1",
			request: &advisorsvc.RemovePodRequest{
				PodUid: "testPodUID",
			},
			want:        &advisorsvc.RemovePodResponse{},
			wantErr:     false,
			wantRemoved: true,
		},
		{
			name: "should ignore the request if it's sent by a plugin that supports GetAdvice",
			requestMetadata: map[string][]string{
				util.AdvisorRPCMetadataKeySupportsGetAdvice: {util.AdvisorRPCMetadataValueSupportsGetAdvice},
			},
			request: &advisorsvc.RemovePodRequest{
				PodUid: "testPodUID",
			},
			want:        &advisorsvc.RemovePodResponse{},
			wantErr:     false,
			wantRemoved: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cs := newTestCPUServer(t, nil, []*v1.Pod{})
			err := cs.metaCache.AddContainer(
				"testPodUID",
				"testContainerName",
				&types.ContainerInfo{
					PodUID:        "testPodUID",
					ContainerName: "testContainerName",
				})
			require.NoError(t, err)

			ctx := context.Background()
			if tt.requestMetadata != nil {
				ctx = metadata.NewIncomingContext(ctx, tt.requestMetadata)
			}
			got, err := cs.RemovePod(ctx, tt.request)
			if (err != nil) != tt.wantErr {
				t.Errorf("RemovePod() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RemovePod() got = %v, want %v", got, tt.want)
			}

			_, ok := cs.metaCache.GetContainerInfo("testPodUID", "testContainerName")
			assert.Equal(t, !tt.wantRemoved, ok)
		})
	}
}

type mockQRMCPUPluginServer struct {
	checkpoint *cpuadvisor.GetCheckpointResponse
	err        error
}

func (m *mockQRMCPUPluginServer) GetCheckpoint(ctx context.Context, request *cpuadvisor.GetCheckpointRequest) (*cpuadvisor.GetCheckpointResponse, error) {
	return m.checkpoint, m.err
}

type mockCPUResourceAdvisor struct {
	onUpdate  func()
	provision *types.InternalCPUCalculationResult
	// overlap flags mirror what the advisor sampled this cycle and are carried
	// out on CPUAdvice; the server must publish exactly these.
	allowSharedOverlap      bool
	disableDedicatedOverlap bool
	err                     error
}

func (m *mockCPUResourceAdvisor) UpdateAndGetAdvice(_ context.Context) (interface{}, error) {
	if m.onUpdate != nil {
		m.onUpdate()
	}
	return &types.CPUAdvice{
		CalculationResult:                          m.provision,
		AllowSharedCoresOverlapReclaimedCores:      m.allowSharedOverlap,
		DisableDedicatedCoresOverlapReclaimedCores: m.disableDedicatedOverlap,
	}, m.err
}

type mockCPUPluginClient struct {
	checkpoint *cpuadvisor.GetCheckpointResponse
	err        error
}

func (m *mockCPUPluginClient) GetCheckpoint(_ context.Context, _ *cpuadvisor.GetCheckpointRequest, _ ...grpc.CallOption) (*cpuadvisor.GetCheckpointResponse, error) {
	return m.checkpoint, m.err
}

type recordingCPUListAndWatchServer struct {
	grpc.ServerStream
	sendCalls int
}

func (s *recordingCPUListAndWatchServer) Send(_ *cpuadvisor.ListAndWatchResponse) error {
	s.sendCalls++
	return nil
}

func (s *recordingCPUListAndWatchServer) Context() context.Context {
	return context.Background()
}

type mockCPUServerService_ListAndWatchServer struct {
	grpc.ServerStream
	ResultsChan chan *cpuadvisor.ListAndWatchResponse
}

func (_m *mockCPUServerService_ListAndWatchServer) Send(res *cpuadvisor.ListAndWatchResponse) error {
	_m.ResultsChan <- res
	return nil
}

func (_m *mockCPUServerService_ListAndWatchServer) Context() context.Context {
	return context.TODO()
}

func DeepCopyResponse(response *cpuadvisor.ListAndWatchResponse) (*cpuadvisor.ListAndWatchResponse, error) {
	data, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	copyResponse := &cpuadvisor.ListAndWatchResponse{}
	err = json.Unmarshal(data, copyResponse)
	if err != nil {
		return nil, err
	}

	for _, entry := range copyResponse.Entries {
		for _, ci := range entry.Entries {
			for _, c := range ci.CalculationResultsByNumas {
				sort.Slice(c.Blocks, func(i, j int) bool {
					return c.Blocks[i].String() < c.Blocks[j].String()
				})
				for _, block := range c.Blocks {
					block.BlockId = ""
					sort.Slice(block.OverlapTargets, func(i, j int) bool {
						return block.OverlapTargets[i].String() < block.OverlapTargets[j].String()
					})
				}
			}
		}
	}
	return copyResponse, nil
}

type ContainerInfo struct {
	request        *advisorsvc.ContainerMetadata
	podInfo        *v1.Pod
	allocationInfo *cpuadvisor.AllocationInfo
	isolated       bool
	regions        sets.String
}

func TestAssemblePodEntries(t *testing.T) {
	t.Parallel()

	cs := cpuServer{}
	calcResult := map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName: "",
		QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
	}), "failed to assemble container with empty pool name")
	require.Equal(t, 0, len(calcResult), "empty pool container is added into calc results")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName: "",
		QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
	}), "failed to assemble container with empty pool name")
	require.Equal(t, 0, len(calcResult), "empty pool container is added into calc results")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName: "",
		QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
	}), "failed to assemble container with empty pool name")
	require.Equal(t, 1, len(calcResult), "dedicated pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName: "",
		QoSLevel:      consts.PodAnnotationQoSLevelSystemCores,
	}), "failed to assemble container with empty pool name")
	require.Equal(t, 1, len(calcResult), "dedicated pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName:       "non-exist",
		OriginOwnerPoolName: "non-exist",
		QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
	}), "failed to assemble container with non-exist pool name")
	require.Equal(t, 0, len(calcResult), "non-exist share pool container is added into calc results")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName:       "non-exist",
		OriginOwnerPoolName: "non-exist",
		QoSLevel:            consts.PodAnnotationQoSLevelReclaimedCores,
	}), "failed to assemble container with non-exist pool name")
	require.Equal(t, 0, len(calcResult), "non-exist reclaiemd cores pool container is added into calc results")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName:       "non-exist",
		OriginOwnerPoolName: "non-exist",
		QoSLevel:            consts.PodAnnotationQoSLevelDedicatedCores,
	}), "failed to assemble container with non-exist pool name")
	require.Equal(t, 1, len(calcResult), "non-exist dedicate cores pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName:       "non-exist",
		OriginOwnerPoolName: "non-exist",
		QoSLevel:            consts.PodAnnotationQoSLevelSystemCores,
	}), "failed to assemble container with non-exist pool name")
	require.Equal(t, 1, len(calcResult), "non-exist system cores pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{"share": {}}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName:       "share",
		OriginOwnerPoolName: "share",
		QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
	}), "failed to assemble share container with exist pool name")
	require.Equal(t, 2, len(calcResult), "share pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{"reclaimed": {}}
	require.NoError(t, cs.assembleNormalPodEntries(calcResult, "11", &types.ContainerInfo{
		OwnerPoolName:       "reclaimed",
		OriginOwnerPoolName: "reclaimed",
		QoSLevel:            consts.PodAnnotationQoSLevelReclaimedCores,
	}), "failed to assemble reclaimed container with exist pool name")
	require.Equal(t, 2, len(calcResult), "reclaimed pool container is ignored")
}

func TestConcurrencyGetCheckpointAndAddContainer(t *testing.T) {
	t.Parallel()

	safeTime := time.Now().UnixNano()

	checkPointData := `{
    "734c8ee6-b40d-434d-bba0-5493cef2be78": {
        "entries": {
            "dp-47caf4db19": {
                "owner_pool_name": "dedicated",
                "topology_aware_assignments": {
                    "3": "48-63,112-127"
                },
                "original_topology_aware_assignments": {
                    "3": "48-63,112-127"
                }
            }
        }
    },
    "90a60749-93a7-4863-b222-8f874fb6df0a": {
        "entries": {
            "dp-ec8e2a601a": {
                "owner_pool_name": "dedicated",
                "topology_aware_assignments": {
                    "2": "32-47,96-111"
                },
                "original_topology_aware_assignments": {
                    "2": "32-47,96-111"
                }
            }
        }
    },
    "bafacfa0-72fd-47c8-a6c1-0f81a63b622a": {
        "entries": {
            "dp-0c35a803ab": {
                "owner_pool_name": "dedicated",
                "topology_aware_assignments": {
                    "0": "1-15,64-79",
                    "1": "17-31,80-95"
                },
                "original_topology_aware_assignments": {
                    "0": "1-15,64-79",
                    "1": "17-31,80-95"
                }
            }
        }
    },
    "reclaim": {
        "entries": {
            "": {
                "owner_pool_name": "reclaim",
                "topology_aware_assignments": {
                    "0": "1,65",
                    "1": "17,81",
                    "3": "48"
                },
                "original_topology_aware_assignments": {
                    "0": "1,65",
                    "1": "17,81",
                    "3": "48"
                }
            }
        }
    },
    "reserve": {
        "entries": {
            "": {
                "owner_pool_name": "reserve",
                "topology_aware_assignments": {
                    "0": "0",
                    "1": "16"
                },
                "original_topology_aware_assignments": {
                    "0": "0",
                    "1": "16"
                }
            }
        }
    }
}
`
	resp := &cpuadvisor.GetCheckpointResponse{
		Entries: map[string]*cpuadvisor.AllocationEntries{},
	}
	require.NoError(t, json.Unmarshal([]byte(checkPointData), &resp.Entries), "failed to parse checkpoint data")

	cpuServer := newTestCPUServer(t, nil, []*v1.Pod{})
	cpuServer.syncCheckpoint(context.Background(), resp, safeTime)

	containerMetaData := &advisorsvc.ContainerMetadata{
		PodUid:          "testUID",
		PodNamespace:    "testPodNamespace",
		PodName:         "testPodName",
		ContainerName:   "testContainerName",
		ContainerType:   1,
		ContainerIndex:  0,
		Labels:          map[string]string{"key": "label"},
		Annotations:     map[string]string{"key": "label"},
		QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
		RequestQuantity: 1,
	}
	cpuServer.AddContainer(context.Background(), containerMetaData)
	_, ok := cpuServer.metaCache.GetContainerInfo("testUID", "testContainerName")
	require.Equal(t, true, ok, "failed to add container")

	cpuServer.syncCheckpoint(context.Background(), resp, safeTime)
	_, ok = cpuServer.metaCache.GetContainerInfo("testUID", "testContainerName")
	require.Equal(t, true, ok, "container is removed after syncCheckpoint")

	cpuServer.syncCheckpoint(context.Background(), resp, time.Now().UnixNano())
	_, ok = cpuServer.metaCache.GetContainerInfo("testUID", "testContainerName")
	require.Equal(t, false, ok, "container is not removed after syncCheckpoint")

	newCtx, cancel := context.WithCancel(context.Background())
	go func(ctx context.Context) {
		for {
			select {
			case <-newCtx.Done():
			default:
				cpuServer.syncCheckpoint(context.Background(), resp, safeTime)
				time.Sleep(time.Millisecond * 10)
			}
		}
	}(newCtx)
	go func(ctx context.Context) {
		var containerMetaDatas []*advisorsvc.ContainerMetadata
		for i := 0; i < 100; i++ {
			containerMetaData := &advisorsvc.ContainerMetadata{
				PodUid:          fmt.Sprintf("testUID-%d", i),
				PodNamespace:    "testPodNamespace",
				PodName:         "testPodName",
				ContainerName:   "testContainerName",
				ContainerType:   1,
				ContainerIndex:  0,
				Labels:          map[string]string{"key": "label"},
				Annotations:     map[string]string{"key": "label"},
				QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
				RequestQuantity: 1,
			}

			containerMetaDatas = append(containerMetaDatas, containerMetaData)
		}

		for {
			select {
			case <-newCtx.Done():
			default:
				for i := 0; i < 10; i++ {
					cpuServer.AddContainer(ctx, containerMetaDatas[i])
				}
				time.Sleep(2 * time.Second)
				for i := 0; i < 10; i++ {
					cpuServer.metaCache.DeleteContainer(containerMetaDatas[i].PodUid, containerMetaDatas[i].ContainerName)
				}
			}
		}
	}(newCtx)

	time.Sleep(10 * time.Second)
	cancel()
}

func TestCPUServerUpdateMetaCacheInput(t *testing.T) {
	t.Parallel()

	request := &cpuadvisor.GetAdviceRequest{
		Entries: map[string]*cpuadvisor.ContainerAllocationInfoEntries{
			"pod2": {
				Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
					"c1": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid:        "pod2",
							ContainerName: "c1",
							Annotations: map[string]string{
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
							},
							QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
							RequestQuantity: 2,
						},
						AllocationInfo: &cpuadvisor.AllocationInfo{
							RampUp:        false,
							OwnerPoolName: commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
						},
					},
				},
			},
			"pod3": {
				Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
					"c1": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid:        "pod2",
							ContainerName: "c1",
							Annotations: map[string]string{
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
							},
							QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
							RequestQuantity: 4,
						},
						AllocationInfo: &cpuadvisor.AllocationInfo{
							RampUp:        false,
							OwnerPoolName: commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
						},
					},
				},
			},
			commonstate.PoolNameReserve: {
				Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
					"": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid: commonstate.PoolNameReserve,
						},
						AllocationInfo: &cpuadvisor.AllocationInfo{
							OwnerPoolName: commonstate.PoolNameReserve,
							TopologyAwareAssignments: map[uint64]string{
								0: "0",
								1: "16",
							},
							OriginalTopologyAwareAssignments: map[uint64]string{
								0: "0",
								1: "16",
							},
						},
					},
				},
			},
			commonstate.PoolNameShare: {
				Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
					"": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid: commonstate.PoolNameShare,
						},
						AllocationInfo: &cpuadvisor.AllocationInfo{
							OwnerPoolName: commonstate.PoolNameShare,
							TopologyAwareAssignments: map[uint64]string{
								1: "17-31",
							},
							OriginalTopologyAwareAssignments: map[uint64]string{
								1: "17-31",
							},
						},
					},
				},
			},
			commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0": {
				Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
					"": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid: commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
						},
						AllocationInfo: &cpuadvisor.AllocationInfo{
							OwnerPoolName: commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
							TopologyAwareAssignments: map[uint64]string{
								0: "1-15",
							},
							OriginalTopologyAwareAssignments: map[uint64]string{
								0: "1-15",
							},
						},
					},
				},
			},
		},
		ResourcePackageConfig: &cpuadvisor.ResourcePackageConfig{
			NumaResourcePackages: map[uint64]*cpuadvisor.NumaResourcePackageConfig{
				0: {
					Packages: map[string]*cpuadvisor.ResourcePackageItemConfig{
						"pkgA": {PinnedCpuset: "2-3"},
					},
				},
			},
		},
	}
	pods := []*v1.Pod{}
	for podUID, entries := range request.Entries {
		if _, ok := entries.Entries[commonstate.FakedContainerName]; ok {
			continue
		}
		pods = append(pods, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID: k8stypes.UID(podUID),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
		})
	}

	cs := newTestCPUServer(t, nil, pods)
	existingContainerInfo := []*types.ContainerInfo{
		{
			PodUID:        "pod1",
			ContainerName: "c1",
			Annotations: map[string]string{
				"a": "b",
			},
			QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
			CPURequest:          1,
			OriginOwnerPoolName: commonstate.PoolNameShare,
			RampUp:              false,
			OwnerPoolName:       commonstate.PoolNameShare,
		},
		{
			PodUID:        "pod2",
			ContainerName: "c1",
			Annotations: map[string]string{
				"a": "b",
			},
			QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
			CPURequest:          2,
			OriginOwnerPoolName: commonstate.PoolNameShare,
			RampUp:              false,
			OwnerPoolName:       commonstate.PoolNameShare,
		},
	}
	for _, info := range existingContainerInfo {
		err := cs.metaCache.SetContainerInfo(info.PodUID, info.ContainerName, info)
		require.NoError(t, err)
	}
	existingPoolInfo := map[string]*types.PoolInfo{
		commonstate.PoolNameShare: {
			PoolName: commonstate.PoolNameShare,
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("0-13"),
				1: machine.MustParse("16-31"),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("0-13"),
				1: machine.MustParse("16-31"),
			},
		},
		commonstate.PoolNamePrefixIsolation + "-test-1": {
			PoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("14-15"),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("14-15"),
			},
		},
	}
	for _, info := range existingPoolInfo {
		err := cs.metaCache.SetPoolInfo(info.PoolName, info)
		require.NoError(t, err)
	}

	err := cs.updateMetaCacheInput(context.Background(), request)
	require.NoError(t, err)

	expectedResourcePackageConfig := types.ResourcePackageConfig{
		0: map[string]*types.ResourcePackageState{
			"pkgA": {
				PinnedCPUSet: machine.MustParse("2-3"),
			},
		},
	}
	require.Equal(t, expectedResourcePackageConfig, cs.metaCache.GetResourcePackageConfig())

	expectedContainerInfo := []*types.ContainerInfo{
		{
			PodUID:        "pod2",
			ContainerName: "c1",
			Annotations: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
			},
			QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
			CPURequest:          2,
			OriginOwnerPoolName: commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
			RampUp:              false,
			OwnerPoolName:       commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("1-15"),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{},
			RegionNames:                      sets.String{},
		},
		{
			PodUID:        "pod3",
			ContainerName: "c1",
			Annotations: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
			},
			QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
			CPURequest:          4,
			OriginOwnerPoolName: commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
			RampUp:              false,
			OwnerPoolName:       commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("1-15"),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{},
			RegionNames:                      sets.String{},
		},
	}

	actualContainerInfo := []*types.ContainerInfo{}
	cs.metaCache.RangeContainer(func(podUID, containerName string, containerInfo *types.ContainerInfo) bool {
		actualContainerInfo = append(actualContainerInfo, containerInfo)
		return true
	})
	sort.Slice(actualContainerInfo, func(i, j int) bool {
		return actualContainerInfo[i].PodUID < actualContainerInfo[j].PodUID
	})
	sort.Slice(expectedContainerInfo, func(i, j int) bool {
		return expectedContainerInfo[i].PodUID < expectedContainerInfo[j].PodUID
	})
	require.Equal(t, expectedContainerInfo, actualContainerInfo)

	expectedPoolInfo := map[string]*types.PoolInfo{
		commonstate.PoolNameReserve: {
			PoolName: commonstate.PoolNameReserve,
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("0"),
				1: machine.MustParse("16"),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("0"),
				1: machine.MustParse("16"),
			},
			RegionNames: sets.String{},
		},
		commonstate.PoolNameShare: {
			PoolName: commonstate.PoolNameShare,
			TopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.MustParse("17-31"),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.MustParse("17-31"),
			},
			RegionNames: sets.String{},
		},
		commonstate.PoolNameShare + "-NUMA0": {
			PoolName: commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("1-15"),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.MustParse("1-15"),
			},
			RegionNames: sets.String{},
		},
	}
	poolNames := sets.StringKeySet(expectedPoolInfo).Union(sets.StringKeySet(existingPoolInfo)).List()
	for _, poolName := range poolNames {
		expectedPoolInfo, shouldExist := expectedPoolInfo[poolName]
		actualPoolInfo, exists := cs.metaCache.GetPoolInfo(poolName)
		require.Equal(t, shouldExist, exists)
		require.Equal(t, expectedPoolInfo, actualPoolInfo)
	}
}

func TestCPUServerUpdateMetaCacheInput_InvalidResourcePackageCPUSet(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, nil, nil)
	request := &cpuadvisor.GetAdviceRequest{
		Entries: map[string]*cpuadvisor.ContainerAllocationInfoEntries{},
		ResourcePackageConfig: &cpuadvisor.ResourcePackageConfig{
			NumaResourcePackages: map[uint64]*cpuadvisor.NumaResourcePackageConfig{
				0: {
					Packages: map[string]*cpuadvisor.ResourcePackageItemConfig{
						"pkgA": {PinnedCpuset: "bad"},
					},
				},
			},
		},
	}

	err := cs.updateMetaCacheInput(context.Background(), request)
	require.Error(t, err)
	require.Equal(t, types.ResourcePackageConfig{}, cs.metaCache.GetResourcePackageConfig())
}

func TestCPUServerUpdateMetaCacheInputUsesMetadataUntilCanonicalEntryDisappears(t *testing.T) {
	t.Parallel()

	request := &cpuadvisor.GetAdviceRequest{
		Entries: map[string]*cpuadvisor.ContainerAllocationInfoEntries{
			commonstate.PoolNameShare: {
				Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
					commonstate.FakedContainerName: {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid: commonstate.PoolNameShare,
						},
						AllocationInfo: &cpuadvisor.AllocationInfo{
							OwnerPoolName: commonstate.PoolNameShare,
							TopologyAwareAssignments: map[uint64]string{
								0: "0-3",
							},
							OriginalTopologyAwareAssignments: map[uint64]string{
								0: "0-3",
							},
						},
					},
				},
			},
			"live-pod": {
				Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
					"main": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid:          "live-pod",
							PodNamespace:    "test-ns",
							PodName:         "live-pod",
							ContainerName:   "main",
							Labels:          map[string]string{"state": "fresh"},
							Annotations:     map[string]string{"source": "request"},
							QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
							RequestQuantity: 1,
						},
						AllocationInfo: &cpuadvisor.AllocationInfo{
							RampUp:        false,
							OwnerPoolName: commonstate.PoolNameShare,
							TopologyAwareAssignments: map[uint64]string{
								0: "0",
							},
						},
					},
				},
			},
			"stale-pod": {
				Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
					"main": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid:          "stale-pod",
							PodNamespace:    "test-ns",
							PodName:         "stale-pod",
							ContainerName:   "main",
							QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
							RequestQuantity: 1,
						},
						AllocationInfo: &cpuadvisor.AllocationInfo{
							OwnerPoolName: commonstate.PoolNameShare,
							TopologyAwareAssignments: map[uint64]string{
								0: "1",
							},
						},
					},
				},
			},
		},
	}
	cs := newTestCPUServer(t, nil, []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			UID:       k8stypes.UID("live-pod"),
			Namespace: "test-ns",
			Name:      "live-pod",
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
		},
		Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
	}})
	require.NoError(t, cs.metaCache.SetContainerInfo("live-pod", "main", &types.ContainerInfo{
		PodUID:        "live-pod",
		ContainerName: "main",
		Labels:        map[string]string{"state": "stale"},
		Annotations:   map[string]string{"source": "cache"},
		QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
		CPURequest:    8,
		RampUp:        true,
		OwnerPoolName: commonstate.PoolNameDedicated,
		TopologyAwareAssignments: map[int]machine.CPUSet{
			1: machine.MustParse("1"),
		},
		OriginOwnerPoolName: commonstate.PoolNameDedicated,
	}))
	require.NoError(t, cs.metaCache.SetContainerInfo("stale-pod", "main", &types.ContainerInfo{
		PodUID:              "stale-pod",
		ContainerName:       "main",
		OriginOwnerPoolName: commonstate.PoolNameShare,
	}))
	_, ok := cs.metaCache.GetContainerInfo("stale-pod", "main")
	require.True(t, ok)

	err := cs.updateMetaCacheInput(context.Background(), request)
	require.NoError(t, err)
	liveInfo, ok := cs.metaCache.GetContainerInfo("live-pod", "main")
	require.True(t, ok)
	require.Equal(t, float64(1), liveInfo.CPURequest)
	require.Equal(t, map[string]string{"state": "fresh"}, liveInfo.Labels)
	require.Equal(t, map[string]string{"source": "request"}, liveInfo.Annotations)
	require.Equal(t, consts.PodAnnotationQoSLevelSharedCores, liveInfo.QoSLevel)
	require.False(t, liveInfo.RampUp)
	require.Equal(t, commonstate.PoolNameShare, liveInfo.OwnerPoolName)
	require.Equal(t, commonstate.PoolNameShare, liveInfo.OriginOwnerPoolName)
	require.Equal(t, machine.MustParse("0"), liveInfo.TopologyAwareAssignments[0])
	require.NotContains(t, liveInfo.TopologyAwareAssignments, 1)
	missingPodInfo, ok := cs.metaCache.GetContainerInfo("stale-pod", "main")
	require.True(t, ok, "a local Pod cache miss must not override the canonical QRM request")
	require.Equal(t, consts.PodAnnotationQoSLevelSharedCores, missingPodInfo.QoSLevel)
	require.Equal(t, machine.MustParse("1"), missingPodInfo.TopologyAwareAssignments[0])

	delete(request.Entries, "stale-pod")
	require.NoError(t, cs.updateMetaCacheInput(context.Background(), request))
	_, ok = cs.metaCache.GetContainerInfo("stale-pod", "main")
	require.False(t, ok)
}

// TestCreateOrUpdateContainerInfoDerivesQoSFromMetadataWhenPodNil pins the
// synchronous-model fallback contract: when the pod is nil, QoS falls back to
// ContainerAllocationInfo.Metadata and allocation-derived fields are still
// populated. Under the legacy behavior GetQoSLevelForPod(nil) silently falls
// back to the default shared_cores level and overwrites the metadata QoS, which
// this test forbids.
func TestCreateOrUpdateContainerInfoDerivesQoSFromMetadataWhenPodNil(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, nil, nil)

	const (
		podUID        = "dedicated-pod"
		containerName = "main"
	)
	info := &cpuadvisor.ContainerAllocationInfo{
		Metadata: &advisorsvc.ContainerMetadata{
			PodUid:          podUID,
			PodNamespace:    "test-ns",
			PodName:         podUID,
			ContainerName:   containerName,
			QosLevel:        consts.PodAnnotationQoSLevelDedicatedCores,
			RequestQuantity: 4,
		},
		AllocationInfo: &cpuadvisor.AllocationInfo{
			RampUp:        true,
			OwnerPoolName: "dedicated",
			TopologyAwareAssignments: map[uint64]string{
				0: "0-3",
			},
		},
	}

	// pod is nil on purpose: the synchronous model resolves QoS from
	// info.Metadata, so QoS must stay dedicated and allocation fields must be
	// populated regardless of the missing pod object.
	err := cs.createOrUpdateContainerInfo(podUID, containerName, nil, info)
	require.NoError(t, err)

	ci, ok := cs.metaCache.GetContainerInfo(podUID, containerName)
	require.True(t, ok)
	require.Equal(t, consts.PodAnnotationQoSLevelDedicatedCores, ci.QoSLevel)
	require.True(t, ci.RampUp)
	require.Equal(t, "dedicated", ci.OwnerPoolName)
	require.Equal(t, machine.MustParse("0-3"), ci.TopologyAwareAssignments[0])
}

// TestCreateOrUpdateContainerInfoPodQoSOverridesMetadata pins the precedence
// contract: when the pod is non-nil, its QoS is authoritative and overrides
// the metadata fallback. Here Metadata advertises shared_cores while the pod
// annotation says dedicated_cores, so the resulting QoS must be dedicated_cores.
func TestCreateOrUpdateContainerInfoPodQoSOverridesMetadata(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "override-pod"
		containerName = "main"
	)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       k8stypes.UID(podUID),
			Namespace: "test-ns",
			Name:      podUID,
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
			},
		},
		Spec: v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
	}
	cs := newTestCPUServer(t, nil, []*v1.Pod{pod})

	info := &cpuadvisor.ContainerAllocationInfo{
		Metadata: &advisorsvc.ContainerMetadata{
			PodUid:          podUID,
			PodNamespace:    "test-ns",
			PodName:         podUID,
			ContainerName:   containerName,
			QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
			RequestQuantity: 4,
		},
		AllocationInfo: &cpuadvisor.AllocationInfo{
			OwnerPoolName: "dedicated",
			TopologyAwareAssignments: map[uint64]string{
				0: "0-3",
			},
		},
	}

	// pod is non-nil: the QoS derived from the pod (dedicated_cores) must win
	// over the metadata-advertised shared_cores.
	err := cs.createOrUpdateContainerInfo(podUID, containerName, pod, info)
	require.NoError(t, err)

	ci, ok := cs.metaCache.GetContainerInfo(podUID, containerName)
	require.True(t, ok)
	require.Equal(t, consts.PodAnnotationQoSLevelDedicatedCores, ci.QoSLevel)
	require.Equal(t, commonstate.PoolNameDedicated, ci.OriginOwnerPoolName)
}

func TestSetContainerInfoBasedOnContainerAllocationInfoUsesFinalQoSForNUMABinding(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod"
		containerName = "container"
	)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID: k8stypes.UID(podUID),
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
			},
		},
	}
	cs := newTestCPUServer(t, nil, []*v1.Pod{pod})
	ci := &types.ContainerInfo{
		PodUID:        podUID,
		ContainerName: containerName,
	}
	info := &cpuadvisor.ContainerAllocationInfo{
		Metadata: &advisorsvc.ContainerMetadata{
			QosLevel: consts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
			},
		},
		AllocationInfo: &cpuadvisor.AllocationInfo{
			OwnerPoolName: commonstate.PoolNameDedicated,
		},
	}

	require.NoError(t, cs.setContainerInfoBasedOnContainerAllocationInfo(pod, ci, info))
	require.Equal(t, consts.PodAnnotationQoSLevelDedicatedCores, ci.QoSLevel)
	require.Equal(t, commonstate.PoolNameDedicated, ci.OriginOwnerPoolName)
}

func Test_cpuServer_assembleCgroupConfig_multiReclaim(t *testing.T) {
	t.Parallel()

	// This test mutates the process-global reclaim registry and asserts on
	// paths (/kubepods/besteffort, /kubesandbox) that other parallel tests
	// in this package (e.g. TestCPUServerUpdate) also read. Serialize with
	// serverGlobalRegistryMu and clean up on exit so we don't leak state.
	serverGlobalRegistryMu.Lock()
	defer serverGlobalRegistryMu.Unlock()

	cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	cs.metaServer.KatalystMachineInfo.CPUTopology = cpuTopology
	reclaim.UnregisterConsumer("test-multi-a")
	reclaim.UnregisterConsumer("test-multi-b")
	reclaim.UnregisterConsumer(reclaim.GenericConsumerName)
	cs.conf.GetDynamicConfiguration().ReclaimedPercentageByConsumer = map[string]int{
		"test-multi-a": 60,
		"test-multi-b": 40,
	}
	reg := func(name, path string) {
		c := config.NewConfiguration()
		c.BaseConfiguration.ReclaimRelativeRootCgroupPath = path
		require.NoError(t, reclaim.RegisterNamedGenericConsumer(name, c, cs.metaServer.KatalystMachineInfo))
	}
	reg("test-multi-a", "/kubepods/besteffort")
	reg("test-multi-b", "/kubesandbox")
	t.Cleanup(func() {
		reclaim.UnregisterConsumer("test-multi-a")
		reclaim.UnregisterConsumer("test-multi-b")
	})

	provision := &types.InternalCPUCalculationResult{
		PoolEntries: map[string]map[int]types.CPUResource{
			commonstate.PoolNameReclaim: {
				-1: {Size: 40, Quota: 40.0},
				0:  {Size: 10, Quota: 10.0},
				1:  {Size: 11, Quota: 11.0},
				2:  {Size: 12, Quota: common.CPUQuotaUnlimit},
				3:  {Size: 13, Quota: 13.0},
			},
		},
	}

	buildBody := func(quota float64) string {
		q := int64(-1)
		if quota > 0 {
			q = int64(quota * DefaultCFSCPUPeriod)
		}
		bytes, err := json.Marshal(&common.CgroupResources{CpuQuota: q, CpuPeriod: DefaultCFSCPUPeriod})
		require.NoError(t, err)
		return string(bytes)
	}

	makeEntry := func(cgroupPath, body string) *advisorsvc.CalculationInfo {
		return &advisorsvc.CalculationInfo{
			CgroupPath: cgroupPath,
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					string(cpuadvisor.ControlKnobKeyCgroupConfig): body,
				},
			},
		}
	}

	got := cs.assembleCgroupConfig(provision)

	want := []*advisorsvc.CalculationInfo{
		makeEntry("/kubepods/besteffort", buildBody(40.0*60/100)),
		makeEntry("/kubesandbox", buildBody(40.0*40/100)),
		makeEntry("/kubepods/besteffort-0", buildBody(10.0*60/100)),
		makeEntry("/kubesandbox-0", buildBody(10.0*40/100)),
		makeEntry("/kubepods/besteffort-1", buildBody(11.0*60/100)),
		makeEntry("/kubesandbox-1", buildBody(11.0*40/100)),
		makeEntry("/kubepods/besteffort-2", buildBody(common.CPUQuotaUnlimit)),
		makeEntry("/kubesandbox-2", buildBody(common.CPUQuotaUnlimit)),
		makeEntry("/kubepods/besteffort-3", buildBody(13.0*60/100)),
		makeEntry("/kubesandbox-3", buildBody(13.0*40/100)),
	}
	assert.ElementsMatch(t, want, got)
}

// fakeHeadroomResourceManager is a test double that returns configured
// results for GetAllocatable / GetNumaAllocatable. Other methods are
// unused by assembleHeadroom.
type fakeHeadroomResourceManager struct {
	allocatable    resource.Quantity
	allocatableErr error

	numaAllocatable    map[int]resource.Quantity
	numaAllocatableErr error
}

func (f *fakeHeadroomResourceManager) GetAllocatable() (resource.Quantity, error) {
	return f.allocatable, f.allocatableErr
}

func (f *fakeHeadroomResourceManager) GetCapacity() (resource.Quantity, error) {
	return resource.Quantity{}, nil
}

func (f *fakeHeadroomResourceManager) GetNumaAllocatable() (map[int]resource.Quantity, error) {
	return f.numaAllocatable, f.numaAllocatableErr
}

func (f *fakeHeadroomResourceManager) GetNumaCapacity() (map[int]resource.Quantity, error) {
	return nil, nil
}

func Test_cpuServer_assembleHeadroom(t *testing.T) {
	t.Parallel()

	// values are millicore-scaled (reportMilliValue=true for CPU).
	millicores := func(v int64) resource.Quantity {
		return *resource.NewQuantity(v, resource.DecimalSI)
	}

	t.Run("emits numa key with correct cores conversion", func(t *testing.T) {
		t.Parallel()
		cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
		cs.headroomResourceManager = &fakeHeadroomResourceManager{
			allocatable: millicores(3200),
			numaAllocatable: map[int]resource.Quantity{
				0: millicores(1500),
				1: millicores(1700),
			},
		}

		got := cs.assembleHeadroom()
		require.NotNil(t, got)
		require.Equal(t, "", got.CgroupPath)
		require.NotNil(t, got.CalculationResult)

		vals := got.CalculationResult.Values
		require.Contains(t, vals, string(cpuadvisor.ControlKnobKeyCPUNUMAHeadroom))

		var numa map[int]float64
		require.NoError(t, json.Unmarshal([]byte(vals[string(cpuadvisor.ControlKnobKeyCPUNUMAHeadroom)]), &numa))
		require.InDelta(t, 1.5, numa[0], 1e-9)
		require.InDelta(t, 1.7, numa[1], 1e-9)
	})

	t.Run("returns nil when GetNumaAllocatable errors", func(t *testing.T) {
		t.Parallel()
		cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
		cs.headroomResourceManager = &fakeHeadroomResourceManager{
			numaAllocatableErr: fmt.Errorf("boom-numa"),
			allocatable:        millicores(1000),
		}
		require.Nil(t, cs.assembleHeadroom())
	})

	t.Run("empty per-NUMA still emits map", func(t *testing.T) {
		t.Parallel()
		cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
		cs.headroomResourceManager = &fakeHeadroomResourceManager{
			allocatable:     millicores(4000),
			numaAllocatable: map[int]resource.Quantity{},
		}
		got := cs.assembleHeadroom()
		require.NotNil(t, got)

		var numa map[int]float64
		require.NoError(t, json.Unmarshal([]byte(got.CalculationResult.Values[string(cpuadvisor.ControlKnobKeyCPUNUMAHeadroom)]), &numa))
		require.Empty(t, numa)
	})
}

// TestAssemblePoolEntriesRampUpFloorPerNUMA pins the cross-domain closure: the
// live-reclaim floor must only raise reclaim on the NUMAs an active ramp-up
// domain actually owns (or every NUMA for the global domain), and must never
// lift reclaim on a NUMA that has no ramp-up of its own.
// TestAssemblePoolEntriesSerializerOnly verifies that the cpu server never
// modifies reclaim pool entry sizes, regardless of ramp-up domain state.
// Live-reclaim continuity is the assembler's responsibility; the server is a
// pure serializer. This guards against regressions where the server re-introduces
// a post-hoc clamp that breaks capacity conservation.
func TestAssemblePoolEntriesSerializerOnly(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		rampUpDomains []int
		hardPartition bool
	}{
		{"global ramp-up domain present", []int{commonstate.FakedNUMAID}, false},
		{"real NUMA ramp-up", []int{0}, false},
		{"hard partition active", []int{commonstate.FakedNUMAID}, true},
		{"no ramp-up", nil, false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cs := newTestCPUServer(t, &mockCPUResourceAdvisor{}, nil)
			// Set up metaCache with live reclaim larger than advisor target.
			require.NoError(t, cs.metaCache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
				PoolName: commonstate.PoolNameReclaim,
				TopologyAwareAssignments: types.TopologyAwareAssignment{
					0: machine.NewCPUSet(0, 1, 2, 3),
				},
			}))

			entries := make(map[string]*cpuadvisor.CalculationEntries)
			bs := NewBlockSet()
			advisorTarget := map[int]types.CPUResource{0: {Size: 1, Quota: -1}}
			cs.assemblePoolEntries(&types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReclaim: advisorTarget,
				},
			}, entries, bs)

			reclaimResult := entries[commonstate.PoolNameReclaim].Entries[commonstate.FakedContainerName].CalculationResultsByNumas[0]
			require.Len(t, reclaimResult.Blocks, 1)
			// Server must pass through advisor Size unchanged (1), never clamp to live (4).
			require.Equal(t, uint64(1), reclaimResult.Blocks[0].Result)
		})
	}
}
