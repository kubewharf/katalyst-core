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
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
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
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

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
	err       error
}

func (m *mockCPUResourceAdvisor) UpdateAndGetAdvice() (interface{}, error) {
	if m.onUpdate != nil {
		m.onUpdate()
	}
	return m.provision, m.err
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

func TestCPUServerUpdate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name      string
		provision types.InternalCPUCalculationResult
		infos     []*ContainerInfo
		wantRes   *cpuadvisor.ListAndWatchResponse
	}

	tests := []testCase{
		{
			name: "reclaim pool with shared pool",
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameShare:                       {-1: 2},
					commonstate.PoolNameReclaim:                     {-1: 4},
					commonstate.PoolNamePrefixIsolation + "-test-1": {-1: 4},
				},
			},
			infos: []*ContainerInfo{
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c1",
						QosLevel:      consts.PodAnnotationQoSLevelSharedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c1",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameShare,
					},
					isolated: true,
					regions:  sets.NewString(commonstate.PoolNamePrefixIsolation + "-test-1"),
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameShare: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameShare,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},

					"pod1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"c1": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool with dedicated pod",
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReclaim: {
						0: 4,
						1: 8,
					},
				},
			},
			infos: []*ContainerInfo{
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c1",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c1",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
								},
							},
						},
					},
					"pod1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"c1": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result:         16,
												OverlapTargets: nil,
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool colocated with dedicated pod(2 containers)",
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReclaim: {
						0: 4,
						1: 8,
					},
				},
			},
			infos: []*ContainerInfo{
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c1",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c2",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c2",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c2",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
								},
							},
						},
					},
					"pod1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"c1": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c2": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool colocated with dedicated pod(3 containers)",
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReclaim: {
						0: 4,
						1: 8,
					},
				},
			},
			infos: []*ContainerInfo{
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c1",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c1",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c2",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c2",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c3",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c3",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
								},
							},
						},
					},
					"pod1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"c1": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c2": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c3": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool overlap shared pool[1]",
			provision: types.InternalCPUCalculationResult{
				AllowSharedCoresOverlapReclaimedCores: true,
				TimeStamp:                             time.Now(),
				PoolEntries: map[string]map[int]int{
					strings.Join([]string{commonstate.PoolNameShare, "1"}, "-"): {-1: 2},
					strings.Join([]string{commonstate.PoolNameShare, "2"}, "-"): {-1: 2},
					commonstate.PoolNameReclaim:                                 {-1: 4},
					commonstate.PoolNamePrefixIsolation + "-test-1":             {-1: 4},
				},
				PoolOverlapInfo: map[string]map[int]map[string]int{
					commonstate.PoolNameReclaim: {
						-1: map[string]int{
							"share-1": 2,
							"share-2": 2,
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					"share-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: "share-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "reclaim",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					"share-2": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: "share-2",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "reclaim",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "share-1",
													},
												},
											},
											{
												Result: 2,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "share-2",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool overlap shared pool[2]",
			provision: types.InternalCPUCalculationResult{
				AllowSharedCoresOverlapReclaimedCores: true,
				TimeStamp:                             time.Now(),
				PoolEntries: map[string]map[int]int{
					strings.Join([]string{commonstate.PoolNameShare, "1"}, "-"): {-1: 4},
					strings.Join([]string{commonstate.PoolNameShare, "2"}, "-"): {-1: 4},
					commonstate.PoolNameReclaim:                                 {-1: 2},
					commonstate.PoolNamePrefixIsolation + "-test-1":             {-1: 4},
				},
				PoolOverlapInfo: map[string]map[int]map[string]int{
					commonstate.PoolNameReclaim: {
						-1: map[string]int{
							"share-1": 1,
							"share-2": 1,
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					"share-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: "share-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 1,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "reclaim",
													},
												},
											},
											{
												Result: 3,
											},
										},
									},
								},
							},
						},
					},
					"share-2": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: "share-2",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 1,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "reclaim",
													},
												},
											},
											{
												Result: 3,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 1,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "share-1",
													},
												},
											},
											{
												Result: 1,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "share-2",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool with isolated pool",
			provision: types.InternalCPUCalculationResult{
				AllowSharedCoresOverlapReclaimedCores: true,
				TimeStamp:                             time.Now(),
				PoolEntries: map[string]map[int]int{
					commonstate.PoolNameReclaim:                     {-1: 2},
					commonstate.PoolNamePrefixIsolation + "-test-1": {-1: 4},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	testWithListAndWatch := func(
		t *testing.T,
		advisor *mockCPUResourceAdvisor,
		cs *cpuServer,
		tt testCase,
	) {
		// populate checkpoint
		checkpoint := &cpuadvisor.GetCheckpointResponse{
			Entries: make(map[string]*cpuadvisor.AllocationEntries),
		}
		checkpoint.Entries[commonstate.PoolNameReserve] = &cpuadvisor.AllocationEntries{
			Entries: map[string]*cpuadvisor.AllocationInfo{
				commonstate.FakedContainerName: {
					OwnerPoolName: commonstate.PoolNameReserve,
				},
			},
		}
		for _, info := range tt.infos {
			if _, ok := checkpoint.Entries[info.request.PodUid]; !ok {
				checkpoint.Entries[info.request.PodUid] = &cpuadvisor.AllocationEntries{
					Entries: make(map[string]*cpuadvisor.AllocationInfo),
				}
			}
			checkpoint.Entries[info.request.PodUid].Entries[info.request.ContainerName] = info.allocationInfo
		}

		// start mock qrm server
		qrmServer := &mockQRMCPUPluginServer{
			checkpoint: checkpoint,
			err:        nil,
		}
		server := grpc.NewServer()
		cpuadvisor.RegisterCPUPluginServer(server, qrmServer)

		sock, err := net.Listen("unix", cs.pluginSocketPath)
		require.NoError(t, err)
		defer sock.Close()
		go func() {
			server.Serve(sock)
		}()

		// populate MetaCache
		for _, info := range tt.infos {
			assert.NoError(t, cs.addContainer(info.request))
			assert.NoError(t, cs.updateContainerInfo(info.request.PodUid, info.request.ContainerName, info.podInfo, info.allocationInfo))

			nodeInfo, _ := cs.metaCache.GetContainerInfo(info.request.PodUid, info.request.ContainerName)
			nodeInfo.Isolated = info.isolated
			if info.regions.Len() > 0 {
				nodeInfo.RegionNames = info.regions
			}
			assert.NoError(t, cs.metaCache.SetContainerInfo(info.request.PodUid, info.request.ContainerName, nodeInfo))
		}

		s := &mockCPUServerService_ListAndWatchServer{ResultsChan: make(chan *cpuadvisor.ListAndWatchResponse)}
		stop := make(chan struct{})
		go func() {
			err := cs.ListAndWatch(&advisorsvc.Empty{}, s)
			assert.NoError(t, err)
			close(stop)
		}()

		res := <-s.ResultsChan
		close(cs.stopCh)
		<-stop

		res, err = DeepCopyResponse(res)
		assert.NoError(t, err)
		assert.Equal(t, tt.wantRes, res)
	}

	testWithGetAdvice := func(
		t *testing.T,
		advisor *mockCPUResourceAdvisor,
		cs *cpuServer,
		tt testCase,
	) {
		// populate GetAdviceRequest
		request := &cpuadvisor.GetAdviceRequest{
			Entries: map[string]*cpuadvisor.FullAllocationInfoEntries{},
		}
		request.Entries[commonstate.PoolNameReserve] = &cpuadvisor.FullAllocationInfoEntries{
			Entries: map[string]*cpuadvisor.FullAllocationInfo{
				commonstate.FakedContainerName: {
					OwnerPoolName: commonstate.PoolNameReserve,
				},
			},
		}
		for _, info := range tt.infos {
			if _, ok := request.Entries[info.request.PodUid]; !ok {
				request.Entries[info.request.PodUid] = &cpuadvisor.FullAllocationInfoEntries{
					Entries: make(map[string]*cpuadvisor.FullAllocationInfo),
				}
			}
			request.Entries[info.request.PodUid].Entries[info.request.ContainerName] = &cpuadvisor.FullAllocationInfo{
				PodUid:                           info.request.PodUid,
				PodNamespace:                     info.podInfo.Namespace,
				PodName:                          info.podInfo.Name,
				ContainerName:                    info.request.ContainerName,
				Annotations:                      info.request.Annotations,
				QosLevel:                         info.request.QosLevel,
				RampUp:                           info.allocationInfo.RampUp,
				OwnerPoolName:                    info.allocationInfo.OwnerPoolName,
				TopologyAwareAssignments:         info.allocationInfo.TopologyAwareAssignments,
				OriginalTopologyAwareAssignments: info.allocationInfo.OriginalTopologyAwareAssignments,
			}
		}
		advisor.onUpdate = func() {
			for _, info := range tt.infos {
				ci, ok := cs.metaCache.GetContainerInfo(info.request.PodUid, info.request.ContainerName)
				// container info should have been populated
				assert.True(t, ok)
				ci.Isolated = info.isolated
				if info.regions.Len() > 0 {
					ci.RegionNames = info.regions
				}
				assert.NoError(t, cs.metaCache.SetContainerInfo(info.request.PodUid, info.request.ContainerName, ci))
			}
		}

		resp, err := cs.GetAdvice(context.Background(), request)
		assert.NoError(t, err)
		lwResp, err := DeepCopyResponse(&cpuadvisor.ListAndWatchResponse{
			Entries:                               resp.Entries,
			AllowSharedCoresOverlapReclaimedCores: resp.AllowSharedCoresOverlapReclaimedCores,
			ExtraEntries:                          resp.ExtraEntries,
		})
		assert.NoError(t, err)
		assert.Equal(t, tt.wantRes, lwResp)
	}

	for _, tt := range tests {
		tt := tt
		// reuse the test cases and test both ListAndWatch and GetAdvice
		for apiMode, testFunc := range map[string]func(*testing.T, *mockCPUResourceAdvisor, *cpuServer, testCase){
			"ListAndWatch": testWithListAndWatch,
			"GetAdvice":    testWithGetAdvice,
		} {
			testFunc := testFunc
			t.Run(tt.name+"_"+apiMode, func(t *testing.T) {
				t.Parallel()

				advisor := &mockCPUResourceAdvisor{
					provision: &tt.provision,
					err:       nil,
				}
				var pods []*v1.Pod
				for _, info := range tt.infos {
					pods = append(pods, info.podInfo)
				}
				cs := newTestCPUServer(t, advisor, pods)

				testFunc(t, advisor, cs, tt)
			})
		}
	}
}

func TestAssemblePodEntries(t *testing.T) {
	t.Parallel()

	cs := cpuServer{}
	calcResult := map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName: "",
		QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
	}), "failed to assemble container with empty pool name")
	require.Equal(t, 0, len(calcResult), "empty pool container is added into calc results")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName: "",
		QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
	}), "failed to assemble container with empty pool name")
	require.Equal(t, 0, len(calcResult), "empty pool container is added into calc results")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName: "",
		QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
	}), "failed to assemble container with empty pool name")
	require.Equal(t, 1, len(calcResult), "dedicated pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName: "",
		QoSLevel:      consts.PodAnnotationQoSLevelSystemCores,
	}), "failed to assemble container with empty pool name")
	require.Equal(t, 1, len(calcResult), "dedicated pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName:       "non-exist",
		OriginOwnerPoolName: "non-exist",
		QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
	}), "failed to assemble container with non-exist pool name")
	require.Equal(t, 0, len(calcResult), "non-exist share pool container is added into calc results")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName:       "non-exist",
		OriginOwnerPoolName: "non-exist",
		QoSLevel:            consts.PodAnnotationQoSLevelReclaimedCores,
	}), "failed to assemble container with non-exist pool name")
	require.Equal(t, 0, len(calcResult), "non-exist reclaiemd cores pool container is added into calc results")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName:       "non-exist",
		OriginOwnerPoolName: "non-exist",
		QoSLevel:            consts.PodAnnotationQoSLevelDedicatedCores,
	}), "failed to assemble container with non-exist pool name")
	require.Equal(t, 1, len(calcResult), "non-exist dedicate cores pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName:       "non-exist",
		OriginOwnerPoolName: "non-exist",
		QoSLevel:            consts.PodAnnotationQoSLevelSystemCores,
	}), "failed to assemble container with non-exist pool name")
	require.Equal(t, 1, len(calcResult), "non-exist system cores pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{"share": {}}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
		OwnerPoolName:       "share",
		OriginOwnerPoolName: "share",
		QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
	}), "failed to assemble share container with exist pool name")
	require.Equal(t, 2, len(calcResult), "share pool container is ignored")

	calcResult = map[string]*cpuadvisor.CalculationEntries{"reclaimed": {}}
	require.NoError(t, cs.assemblePodEntries(calcResult, blockSet{}, "11", &types.ContainerInfo{
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
		Entries: map[string]*cpuadvisor.FullAllocationInfoEntries{
			"pod2": {
				Entries: map[string]*cpuadvisor.FullAllocationInfo{
					"c1": {
						PodUid:        "pod2",
						ContainerName: "c1",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
						},
						QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
						RampUp:          false,
						OwnerPoolName:   commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
					},
				},
			},
			"pod3": {
				Entries: map[string]*cpuadvisor.FullAllocationInfo{
					"c1": {
						PodUid:        "pod2",
						ContainerName: "c1",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
						},
						QosLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 4,
						RampUp:          false,
						OwnerPoolName:   commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
					},
				},
			},
			commonstate.PoolNameReserve: {
				Entries: map[string]*cpuadvisor.FullAllocationInfo{
					"": {
						PodUid:        commonstate.PoolNameReserve,
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
			commonstate.PoolNameShare: {
				Entries: map[string]*cpuadvisor.FullAllocationInfo{
					"": {
						PodUid:        commonstate.PoolNameShare,
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
			commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0": {
				Entries: map[string]*cpuadvisor.FullAllocationInfo{
					"": {
						PodUid:        commonstate.PoolNameShare + commonstate.NUMAPoolInfix + "0",
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
