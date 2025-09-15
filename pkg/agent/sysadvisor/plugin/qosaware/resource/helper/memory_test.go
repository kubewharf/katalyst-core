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

package helper

import (
	"reflect"
	"testing"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var qosLevel2PoolName = map[string]string{
	consts.PodAnnotationQoSLevelSharedCores:    commonstate.PoolNameShare,
	consts.PodAnnotationQoSLevelReclaimedCores: commonstate.PoolNameReclaim,
	consts.PodAnnotationQoSLevelSystemCores:    commonstate.PoolNameReserve,
	consts.PodAnnotationQoSLevelDedicatedCores: commonstate.PoolNameDedicated,
}

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel string, annotations map[string]string,
	topologyAwareAssignments types.TopologyAwareAssignment, memoryRequest float64,
) *types.ContainerInfo {
	return &types.ContainerInfo{
		PodUID:                           podUID,
		PodNamespace:                     namespace,
		PodName:                          podName,
		ContainerName:                    containerName,
		ContainerType:                    v1alpha1.ContainerType_MAIN,
		ContainerIndex:                   0,
		Labels:                           nil,
		Annotations:                      annotations,
		QoSLevel:                         qoSLevel,
		CPURequest:                       0,
		MemoryRequest:                    memoryRequest,
		RampUp:                           false,
		OwnerPoolName:                    qosLevel2PoolName[qoSLevel],
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: topologyAwareAssignments,
	}
}

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf := config.NewConfiguration()
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	return conf
}

func generateTestMetaServer(t *testing.T, podList []*v1.Pod,
	metricsFetcher metrictypes.MetricsFetcher,
) *metaserver.MetaServer {
	// numa node0 cpu(s): 0-23,48-71
	// numa node1 cpu(s): 24-47,72-95
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	memoryTopology, err := machine.GenerateDummyMemoryTopology(2, 500<<30)
	require.NoError(t, err)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &info.MachineInfo{
					NumCores:       96,
					MemoryCapacity: 500 << 30,
				},
				CPUTopology:    cpuTopology,
				MemoryTopology: memoryTopology,
			},
			PodFetcher:     &pod.PodFetcherStub{PodList: podList},
			MetricsFetcher: metricsFetcher,
		},
		ServiceProfilingManager: &spd.DummyServiceProfilingManager{},
	}
	return metaServer
}

func TestGetAvailableNUMAsAndReclaimedCores(t *testing.T) {
	t.Parallel()
	containerInfoWithTopologyAwareAssignments := makeContainerInfo("pod0", "default",
		"pod0", "container0",
		consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
			consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		nil,
		30<<30)
	containerInfoReclaimedCores := makeContainerInfo("pod1", "default",
		"pod1", "container1",
		consts.PodAnnotationQoSLevelReclaimedCores, nil,
		nil, 20<<30)
	containerInfoDedicatedCores := makeContainerInfo("pod2", "default",
		"pod2", "container2",
		consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
			consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		types.TopologyAwareAssignment{
			0: machine.NewCPUSet(0),
		}, 30<<30)
	containerInfoDedicatedCores2 := makeContainerInfo("pod3", "default",
		"pod3", "container3",
		consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
			consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		types.TopologyAwareAssignment{
			1: machine.NewCPUSet(0),
		}, 30<<30)
	containerInfoSharedCores := makeContainerInfo("pod4", "default",
		"pod4", "container4",
		consts.PodAnnotationQoSLevelSharedCores, map[string]string{
			consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
		},
		map[int]machine.CPUSet{
			0: machine.NewCPUSet(0),
		}, 20<<30)
	containerInfoSharedCores2 := makeContainerInfo("pod5", "default",
		"pod5", "container5",
		consts.PodAnnotationQoSLevelSharedCores, map[string]string{
			consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
		},
		map[int]machine.CPUSet{
			0: machine.NewCPUSet(0),
		}, 20<<30)

	tests := []struct {
		name           string
		podList        []*v1.Pod
		setup          func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer)
		wantNUMAs      machine.CPUSet
		wantContainers []*types.ContainerInfo
		wantErr        bool
	}{
		{
			name: "Empty TopologyAwareAssignments",
			setup: func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer) {
				conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
				metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
				metaServer := generateTestMetaServer(t, podList, metricsFetcher)
				metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
				require.NoError(t, err)
				err = metaReader.ClearContainers()
				require.NoError(t, err)
				infos := []*types.ContainerInfo{
					containerInfoWithTopologyAwareAssignments,
				}
				for _, info := range infos {
					err := metaReader.AddContainer(info.PodUID, info.ContainerName, info)
					assert.NoError(t, err)
				}
				return conf, metaReader, metaServer
			},
			wantErr: true,
		},
		{
			name: "No Containers",
			setup: func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer) {
				conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
				metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
				metaServer := generateTestMetaServer(t, podList, metricsFetcher)
				metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
				require.NoError(t, err)
				err = metaReader.ClearContainers()
				require.NoError(t, err)
				return conf, metaReader, metaServer
			},
			wantNUMAs:      machine.NewCPUSet(0, 1),
			wantContainers: []*types.ContainerInfo{},
			wantErr:        false,
		},
		{
			name: "One Reclaimed Containers",
			podList: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container1",
							},
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "container1",
								ContainerID: "container1",
							},
						},
					},
				},
			},
			setup: func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer) {
				conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
				metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
				metaServer := generateTestMetaServer(t, podList, metricsFetcher)
				metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
				require.NoError(t, err)
				err = metaReader.ClearContainers()
				require.NoError(t, err)
				infos := []*types.ContainerInfo{
					containerInfoReclaimedCores,
				}

				for _, info := range infos {
					err := metaReader.SetContainerInfo(info.PodUID, info.ContainerName, info)
					assert.NoError(t, err)
				}
				return conf, metaReader, metaServer
			},
			wantNUMAs:      machine.NewCPUSet(0, 1),
			wantContainers: []*types.ContainerInfo{containerInfoReclaimedCores},
			wantErr:        false,
		},
		{
			name: "One Reclaimed Containers And Dedicated Containers",
			podList: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container1",
							},
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "container1",
								ContainerID: "container1",
							},
						},
					},
				},
			},
			setup: func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer) {
				conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
				metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
				metaServer := generateTestMetaServer(t, podList, metricsFetcher)
				metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
				require.NoError(t, err)
				err = metaReader.ClearContainers()
				require.NoError(t, err)
				infos := []*types.ContainerInfo{
					containerInfoReclaimedCores,
					containerInfoDedicatedCores,
				}

				for _, info := range infos {
					err := metaReader.SetContainerInfo(info.PodUID, info.ContainerName, info)
					assert.NoError(t, err)
				}
				return conf, metaReader, metaServer
			},
			wantNUMAs:      machine.NewCPUSet(1),
			wantContainers: []*types.ContainerInfo{containerInfoReclaimedCores},
			wantErr:        false,
		},
		{
			name: "One Reclaimed Container And Dedicated Containers, but Reclaim Container Is Not Ready",
			podList: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container1",
							},
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodFailed,
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "container1",
								ContainerID: "container1",
							},
						},
					},
				},
			},
			setup: func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer) {
				conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
				metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
				metaServer := generateTestMetaServer(t, podList, metricsFetcher)
				metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
				require.NoError(t, err)
				err = metaReader.ClearContainers()
				require.NoError(t, err)
				infos := []*types.ContainerInfo{
					containerInfoReclaimedCores,
					containerInfoDedicatedCores,
				}

				for _, info := range infos {
					err := metaReader.SetContainerInfo(info.PodUID, info.ContainerName, info)
					assert.NoError(t, err)
				}
				return conf, metaReader, metaServer
			},
			wantNUMAs:      machine.NewCPUSet(1),
			wantContainers: []*types.ContainerInfo{},
			wantErr:        false,
		},
		{
			name: "One Dedicated Containers",
			setup: func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer) {
				conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
				metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
				metaServer := generateTestMetaServer(t, podList, metricsFetcher)
				metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
				require.NoError(t, err)
				err = metaReader.ClearContainers()
				require.NoError(t, err)
				infos := []*types.ContainerInfo{
					containerInfoDedicatedCores,
				}

				for _, info := range infos {
					err := metaReader.SetContainerInfo(info.PodUID, info.ContainerName, info)
					assert.NoError(t, err)
				}
				return conf, metaReader, metaServer
			},
			wantNUMAs:      machine.NewCPUSet(1),
			wantContainers: []*types.ContainerInfo{},
			wantErr:        false,
		},
		{
			name: "Two Dedicated Containers",
			setup: func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer) {
				conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
				metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
				metaServer := generateTestMetaServer(t, podList, metricsFetcher)
				metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
				require.NoError(t, err)
				err = metaReader.ClearContainers()
				require.NoError(t, err)
				infos := []*types.ContainerInfo{
					containerInfoDedicatedCores,
					containerInfoDedicatedCores2,
				}

				for _, info := range infos {
					err := metaReader.SetContainerInfo(info.PodUID, info.ContainerName, info)
					assert.NoError(t, err)
				}
				return conf, metaReader, metaServer
			},
			wantNUMAs:      machine.NewCPUSet(),
			wantContainers: []*types.ContainerInfo{},
			wantErr:        false,
		},
		{
			name: "Two Shared Containers, one disable reclaim",
			setup: func(podList []*v1.Pod) (*config.Configuration, metacache.MetaReader, *metaserver.MetaServer) {
				conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
				conf.GetDynamicConfiguration().DisableReclaimSharePools = []string{"pool1"}
				metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
				metaServer := generateTestMetaServer(t, podList, metricsFetcher)
				metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
				require.NoError(t, err)
				err = metaReader.ClearContainers()
				require.NoError(t, err)
				c1 := containerInfoSharedCores
				c2 := containerInfoSharedCores2
				c2.OriginOwnerPoolName = "pool1"
				infos := []*types.ContainerInfo{
					c1,
					c2,
				}

				for _, info := range infos {
					err := metaReader.SetContainerInfo(info.PodUID, info.ContainerName, info)
					assert.NoError(t, err)
				}
				return conf, metaReader, metaServer
			},
			wantNUMAs:      machine.NewCPUSet(1),
			wantContainers: []*types.ContainerInfo{},
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf, metaReader, metaServer := tt.setup(tt.podList)

			gotNUMAs, gotContainers, err := GetAvailableNUMAsAndReclaimedCores(conf, metaReader, metaServer)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetAvailableNUMAsAndReclaimedCores() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !reflect.DeepEqual(gotNUMAs, tt.wantNUMAs) {
				t.Errorf("GetAvailableNUMAsAndReclaimedCores() gotNUMAs = %v, want %v", gotNUMAs, tt.wantNUMAs)
			}

			if !apiequality.Semantic.DeepEqual(gotContainers, tt.wantContainers) {
				t.Errorf("GetAvailableNUMAsAndReclaimedCores() gotContainers = %v, want %v", gotContainers, tt.wantContainers)
			}
		})
	}
}

func TestReclaimedContainersFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ci       *types.ContainerInfo
		expected bool
	}{
		{
			name:     "Nil Container",
			ci:       nil,
			expected: false,
		},
		{
			name: "Reclaimed Cores QoS Level",
			ci: &types.ContainerInfo{
				QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
			},
			expected: true,
		},
		{
			name: "Different QoS Level",
			ci: &types.ContainerInfo{
				QoSLevel: "Different-QoS-Level",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := reclaimedContainersFilter(tt.ci)
			if result != tt.expected {
				t.Errorf("reclaimedContainersFilter(%v) = %v, want %v", tt.ci, result, tt.expected)
			}
		})
	}
}

func TestGetActualNUMABindingNUMAsForReclaimedCores(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		containers    []*types.ContainerInfo
		wantNUMAs     machine.CPUSet
		wantErr       bool
		expectedError string
	}{
		{
			name: "No reclaimed containers",
			containers: []*types.ContainerInfo{
				{PodUID: "pod1", ContainerName: "container1", QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores},
				{PodUID: "pod2", ContainerName: "container2", QoSLevel: consts.PodAnnotationQoSLevelSharedCores},
			},
			wantNUMAs: machine.NewCPUSet(),
			wantErr:   false,
		},
		{
			name: "Single reclaimed container with valid NUMA binding",
			containers: []*types.ContainerInfo{
				{
					PodUID:        "pod1",
					ContainerName: "container1",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "0",
					},
				},
			},
			wantNUMAs: machine.NewCPUSet(0),
			wantErr:   false,
		},
		{
			name: "Multiple reclaimed containers with different NUMA bindings",
			containers: []*types.ContainerInfo{
				{
					PodUID:        "pod1",
					ContainerName: "container1",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "0",
					},
				},
				{
					PodUID:        "pod2",
					ContainerName: "container2",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "1",
					},
				},
				{
					PodUID:        "pod3",
					ContainerName: "container3",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "0",
					},
				},
			},
			wantNUMAs: machine.NewCPUSet(0, 1),
			wantErr:   false,
		},
		{
			name: "Reclaimed container with no NUMA binding",
			containers: []*types.ContainerInfo{
				{
					PodUID:        "pod1",
					ContainerName: "container1",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			wantNUMAs: machine.NewCPUSet(),
			wantErr:   false,
		},
		{
			name: "Mixed container types with some reclaimed",
			containers: []*types.ContainerInfo{
				{PodUID: "pod1", ContainerName: "container1", QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores},
				{
					PodUID:        "pod2",
					ContainerName: "container2",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "1",
					},
				},
				{PodUID: "pod3", ContainerName: "container3", QoSLevel: consts.PodAnnotationQoSLevelSharedCores},
				{
					PodUID:        "pod4",
					ContainerName: "container4",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "2",
					},
				},
			},
			wantNUMAs: machine.NewCPUSet(1, 2),
			wantErr:   false,
		},
		{
			name: "Error in getting NUMA binding result",
			containers: []*types.ContainerInfo{
				{
					PodUID:        "pod1",
					ContainerName: "container1",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "xx",
					},
				},
			},
			wantNUMAs:     machine.CPUSet{},
			wantErr:       true,
			expectedError: "strconv.Atoi: parsing",
		},
		{
			name: "Error in getting NUMA binding result multiple numa nodes",
			containers: []*types.ContainerInfo{
				{
					PodUID:        "pod1",
					ContainerName: "container1",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "0-2",
					},
				},
			},
			wantNUMAs:     machine.CPUSet{},
			wantErr:       true,
			expectedError: "parse numaHintStr:",
		},
		{
			name: "Multiple reclaimed containers with errors",
			containers: []*types.ContainerInfo{
				{
					PodUID:        "pod1",
					ContainerName: "container1",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "0",
					},
				},
				{PodUID: "pod2", ContainerName: "container2", QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores},
				{
					PodUID:        "pod3",
					ContainerName: "container3",
					QoSLevel:      consts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						cpuconsts.CPUStateAnnotationKeyNUMAHint: "xxx",
					},
				},
			},
			wantNUMAs:     machine.CPUSet{},
			wantErr:       true,
			expectedError: "strconv.Atoi: parsing",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConfiguration(t, "/tmp/checkpoint", "/tmp/statefile")
			// Create a mock MetaReader using metacache.MetaReader interface
			metaReader, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
			require.NoError(t, err)
			err = metaReader.ClearContainers()
			require.NoError(t, err)

			for _, i := range tt.containers {
				err = metaReader.AddContainer(i.PodUID, i.ContainerName, i)
				require.NoError(t, err)
			}

			// Execute the function
			gotNUMAs, err := GetActualNUMABindingNUMAsForReclaimedCores(metaReader)

			// Assert results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.expectedError != "" {
					assert.Contains(t, err.Error(), tt.expectedError)
				}
			} else {
				assert.NoError(t, err)
			}

			if !reflect.DeepEqual(gotNUMAs, tt.wantNUMAs) {
				t.Errorf("GetActualNUMABindingNUMAsForReclaimedCores() = %v, want %v", gotNUMAs, tt.wantNUMAs)
			}
		})
	}
}
