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
	"io/ioutil"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
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

func newTestCPUServer(t *testing.T, podList []*v1.Pod) *cpuServer {
	recvCh := make(chan types.InternalCPUCalculationResult)
	sendCh := make(chan types.TriggerInfo)
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

	cpuServer, err := NewCPUServer(recvCh, sendCh, conf, metaCache, metaServer, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.NotNil(t, cpuServer)

	cpuServer.getCheckpointCalled = true

	return cpuServer
}

func TestCPUServerStartAndStop(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, []*v1.Pod{})

	err := cs.Start()
	assert.NoError(t, err)

	err = cs.Stop()
	assert.NoError(t, err)
}

func TestCPUServerAddContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newTestCPUServer(t, []*v1.Pod{})
			got, err := cs.AddContainer(context.Background(), tt.request)
			if (err != nil) != tt.wantErr {
				t.Errorf("AddContainer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AddContainer() got = %v, want %v", got, tt.want)
			}

			containerInfo, ok := cs.metaCache.GetContainerInfo(tt.request.PodUid, tt.request.ContainerName)
			assert.Equal(t, ok, true)
			if !reflect.DeepEqual(containerInfo, tt.wantContainerInfo) {
				t.Errorf("AddContainer() containerInfo got = %v, want %v", containerInfo, tt.wantContainerInfo)
			}
		})
	}
}

func TestCPUServerRemovePod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request *advisorsvc.RemovePodRequest
		want    *advisorsvc.RemovePodResponse
		wantErr bool
	}{
		{
			name: "test1",
			request: &advisorsvc.RemovePodRequest{
				PodUid: "testPodUID",
			},
			want:    &advisorsvc.RemovePodResponse{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newTestCPUServer(t, []*v1.Pod{})
			got, err := cs.RemovePod(context.Background(), tt.request)
			if (err != nil) != tt.wantErr {
				t.Errorf("RemovePod() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RemovePod() got = %v, want %v", got, tt.want)
			}
		})
	}
}

type mockCPUServerService_ListAndWatchServer struct {
	grpc.ServerStream
	ResultsChan chan *cpuadvisor.ListAndWatchResponse
}

func (_m *mockCPUServerService_ListAndWatchServer) Send(res *cpuadvisor.ListAndWatchResponse) error {
	_m.ResultsChan <- res
	return nil
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

func TestCPUServerListAndWatch(t *testing.T) {
	t.Parallel()

	type ContainerInfo struct {
		request        *advisorsvc.ContainerMetadata
		podInfo        *v1.Pod
		allocationInfo *cpuadvisor.AllocationInfo
		isolated       bool
		regions        sets.String
	}

	tests := []struct {
		name      string
		empty     *advisorsvc.Empty
		provision types.InternalCPUCalculationResult
		infos     []*ContainerInfo
		wantErr   bool
		wantRes   *cpuadvisor.ListAndWatchResponse
	}{
		{
			name:  "reclaim pool with shared pool",
			empty: &advisorsvc.Empty{},
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]int{
					state.PoolNameShare:                       {-1: 2},
					state.PoolNameReclaim:                     {-1: 4},
					state.PoolNamePrefixIsolation + "-test-1": {-1: 4},
				}},
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
						OwnerPoolName: state.PoolNameShare,
					},
					isolated: true,
					regions:  sets.NewString(state.PoolNamePrefixIsolation + "-test-1"),
				},
			},
			wantErr: false,
			wantRes: &cpuadvisor.ListAndWatchResponse{
				Entries: map[string]*cpuadvisor.CalculationEntries{
					state.PoolNameShare: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameShare,
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
					state.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameReclaim,
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
					state.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNamePrefixIsolation + "-test-1",
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
								OwnerPoolName: state.PoolNamePrefixIsolation + "-test-1",
							},
						},
					},
				},
			},
		},
		{
			name:  "reclaim pool with dedicated pod",
			empty: &advisorsvc.Empty{},
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]int{
					state.PoolNameReclaim: {
						0: 4,
						1: 8,
					},
				}},
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
						OwnerPoolName: state.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantErr: false,
			wantRes: &cpuadvisor.ListAndWatchResponse{
				Entries: map[string]*cpuadvisor.CalculationEntries{
					state.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameReclaim,
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
								OwnerPoolName: state.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPoolName: state.PoolNameReclaim,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
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
			name:  "reclaim pool colocated with dedicated pod(2 containers)",
			empty: &advisorsvc.Empty{},
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]int{
					state.PoolNameReclaim: {
						0: 4,
						1: 8,
					},
				}},
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
						OwnerPoolName: state.PoolNameDedicated,
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
						OwnerPoolName: state.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantErr: false,
			wantRes: &cpuadvisor.ListAndWatchResponse{
				Entries: map[string]*cpuadvisor.CalculationEntries{
					state.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameReclaim,
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
								OwnerPoolName: state.PoolNameDedicated,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c2": {
								OwnerPoolName: state.PoolNameDedicated,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
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
			name:  "reclaim pool colocated with dedicated pod(3 containers)",
			empty: &advisorsvc.Empty{},
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]int{
					state.PoolNameReclaim: {
						0: 4,
						1: 8,
					},
				}},
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
						OwnerPoolName: state.PoolNameDedicated,
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
						OwnerPoolName: state.PoolNameDedicated,
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
						OwnerPoolName: state.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantErr: false,
			wantRes: &cpuadvisor.ListAndWatchResponse{
				Entries: map[string]*cpuadvisor.CalculationEntries{
					state.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameReclaim,
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
								OwnerPoolName: state.PoolNameDedicated,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c2": {
								OwnerPoolName: state.PoolNameDedicated,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c3": {
								OwnerPoolName: state.PoolNameDedicated,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
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
														OverlapTargetPoolName: state.PoolNameReclaim,
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newTestCPUServer(t, []*v1.Pod{})
			s := &mockCPUServerService_ListAndWatchServer{ResultsChan: make(chan *cpuadvisor.ListAndWatchResponse)}
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
			stop := make(chan struct{})
			go func() {
				if err := cs.ListAndWatch(tt.empty, s); (err != nil) != tt.wantErr {
					t.Errorf("ListAndWatch() error = %v, wantErr %v", err, tt.wantErr)
				}
				stop <- struct{}{}
			}()
			recvCh := cs.recvCh.(chan types.InternalCPUCalculationResult)
			recvCh <- tt.provision
			res := <-s.ResultsChan
			close(cs.stopCh)
			<-stop
			copyres, err := DeepCopyResponse(res)
			assert.NoError(t, err)
			if !reflect.DeepEqual(copyres, tt.wantRes) {
				t.Errorf("ListAndWatch()\ngot = %+v, \nwant= %+v", copyres, tt.wantRes)
			}
		})
	}
}
