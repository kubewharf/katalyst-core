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

package headroompolicy

import (
	"io/ioutil"
	"os"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/memoryheadroom"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory/headroom"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

var (
	qosLevel2PoolName = map[string]string{
		consts.PodAnnotationQoSLevelSharedCores:    qrmstate.PoolNameShare,
		consts.PodAnnotationQoSLevelReclaimedCores: qrmstate.PoolNameReclaim,
		consts.PodAnnotationQoSLevelSystemCores:    qrmstate.PoolNameReserve,
		consts.PodAnnotationQoSLevelDedicatedCores: qrmstate.PoolNameDedicated,
	}
)

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	return conf
}

func generateTestMetaServer(t *testing.T, podList []*v1.Pod,
	metricsFetcher metrictypes.MetricsFetcher) *metaserver.MetaServer {
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

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel string, annotations map[string]string,
	topologyAwareAssignments types.TopologyAwareAssignment, memoryRequest float64) *types.ContainerInfo {
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

func TestPolicyCanonical_calculateMemoryBuffer(t *testing.T) {
	t.Parallel()

	now := time.Now()

	type fields struct {
		podList                      []*v1.Pod
		containers                   []*types.ContainerInfo
		memoryHeadroomConfiguration  *memoryheadroom.MemoryHeadroomConfiguration
		policyCanonicalConfiguration *headroom.MemoryPolicyCanonicalConfiguration
		essentials                   types.ResourceEssentials
		setFakeMetric                func(store *metric.FakeMetricsFetcher)
	}
	tests := []struct {
		name    string
		fields  fields
		want    resource.Quantity
		wantErr bool
	}{
		{
			name: "normal disable buffer",
			fields: fields{
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod2",
							Namespace: "default",
							UID:       "pod2",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
							},
						},
					},
				},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelSharedCores, nil,
						nil, 1),
					makeContainerInfo("pod2", "default",
						"pod2", "container2",
						consts.PodAnnotationQoSLevelSystemCores, nil,
						nil, 1),
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						Enable: false,
					},
				},
				policyCanonicalConfiguration: &headroom.MemoryPolicyCanonicalConfiguration{
					MemoryUtilBasedConfiguration: &headroom.MemoryUtilBasedConfiguration{},
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})

					store.SetContainerMetric("pod2", "container2", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
					store.SetContainerMetric("pod2", "container1", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
				},
			},
			want: *resource.NewQuantity((96<<30)-(20<<30)*1.1-(10<<30), resource.BinarySI),
		},
		{
			name: "normal enable buffer",
			fields: fields{
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container1",
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod2",
							Namespace: "default",
							UID:       "pod2",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container2",
								},
							},
						},
					},
				},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelSharedCores, nil,
						nil, 1),
					makeContainerInfo("pod2", "default",
						"pod2", "container2",
						consts.PodAnnotationQoSLevelSystemCores, nil,
						nil, 1),
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						Enable:              true,
						FreeBasedRatio:      0.6,
						StaticBasedCapacity: 20 << 30,
					},
				},
				policyCanonicalConfiguration: &headroom.MemoryPolicyCanonicalConfiguration{
					MemoryUtilBasedConfiguration: &headroom.MemoryUtilBasedConfiguration{},
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemTotalSystem, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemFreeSystem, utilmetric.MetricData{Value: 60 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemUsedSystem, utilmetric.MetricData{Value: 40 << 30, Time: &now})

					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})

					store.SetContainerMetric("pod2", "container2", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
					store.SetContainerMetric("pod2", "container2", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
				},
			},
			want: *resource.NewQuantity((96<<30)-((20<<30)*1.1+(10<<30))+((60<<30)*0.6-(10<<30)-((20<<30)*1.1+(10<<30)-(20<<30)+(10<<30))+(20<<30)), resource.BinarySI),
		},
		{
			name: "enable buffer but memory free is not enough",
			fields: fields{
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container1",
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod2",
							Namespace: "default",
							UID:       "pod2",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container2",
								},
							},
						},
					},
				},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelSharedCores, nil,
						nil, 1),
					makeContainerInfo("pod2", "default",
						"pod2", "container2",
						consts.PodAnnotationQoSLevelSystemCores, nil,
						nil, 1),
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						Enable:              true,
						FreeBasedRatio:      0.6,
						StaticBasedCapacity: 20 << 30,
					},
				},
				policyCanonicalConfiguration: &headroom.MemoryPolicyCanonicalConfiguration{
					MemoryUtilBasedConfiguration: &headroom.MemoryUtilBasedConfiguration{},
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemTotalSystem, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemFreeSystem, utilmetric.MetricData{Value: 30 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemUsedSystem, utilmetric.MetricData{Value: 40 << 30, Time: &now})

					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})

					store.SetContainerMetric("pod2", "container2", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
					store.SetContainerMetric("pod2", "container2", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
				},
			},
			want: *resource.NewQuantity((96<<30)-((20<<30)*1.1+(10<<30))+(20<<30), resource.BinarySI),
		},
		{
			name: "enable buffer and cache oversold",
			fields: fields{
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod1",
							Namespace: "default",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container1",
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod2",
							Namespace: "default",
							UID:       "pod2",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container2",
								},
							},
						},
					},
				},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelSharedCores, nil,
						nil, 1),
					makeContainerInfo("pod2", "default",
						"pod2", "container2",
						consts.PodAnnotationQoSLevelSystemCores, nil,
						nil, 1),
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						Enable:              true,
						CacheBasedRatio:     0.6,
						FreeBasedRatio:      0.6,
						StaticBasedCapacity: 20 << 30,
					},
				},
				policyCanonicalConfiguration: &headroom.MemoryPolicyCanonicalConfiguration{
					MemoryUtilBasedConfiguration: &headroom.MemoryUtilBasedConfiguration{
						CPUMemRatioLowerBound: 1. / 6.,
						CPUMemRatioUpperBound: 1. / 3.5,
					},
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemTotalSystem, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemFreeSystem, utilmetric.MetricData{Value: 20 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemUsedSystem, utilmetric.MetricData{Value: 60 << 30, Time: &now})

					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 15 << 30, Time: &now})
					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 15 << 30, Time: &now})

					store.SetContainerMetric("pod2", "container2", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
					store.SetContainerMetric("pod2", "container2", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30, Time: &now})
				},
			},
			want: *resource.NewQuantity((96<<30)-((30<<30)*1.1+(10<<30))+((40<<30)-(20<<30))*0.6+(20<<30), resource.BinarySI),
		},
		{
			name: "dedicated numabinding pods reclaimed disabled",
			fields: fields{
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
					},
				},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelDedicatedCores,
						map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable},
						types.TopologyAwareAssignment{0: machine.NewCPUSet(0, 1, 2, 3, 4)}, 1),
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						Enable:              false,
						CacheBasedRatio:     0.6,
						FreeBasedRatio:      0.6,
						StaticBasedCapacity: 20 << 30,
					},
				},
				policyCanonicalConfiguration: &headroom.MemoryPolicyCanonicalConfiguration{
					MemoryUtilBasedConfiguration: &headroom.MemoryUtilBasedConfiguration{
						CPUMemRatioLowerBound: 1. / 6.,
						CPUMemRatioUpperBound: 1. / 3.5,
					},
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       false,
					ResourceUpperBound:  500 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemTotalSystem, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemFreeSystem, utilmetric.MetricData{Value: 20 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemUsedSystem, utilmetric.MetricData{Value: 60 << 30, Time: &now})

					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemRssContainer, utilmetric.MetricData{Value: 15 << 30, Time: &now})
					store.SetContainerMetric("pod1", "container1", pkgconsts.MetricMemCacheContainer, utilmetric.MetricData{Value: 15 << 30, Time: &now})
				},
			},
			want: *resource.NewQuantity((500-250-4)<<30, resource.BinarySI),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ckDir, err := ioutil.TempDir("", "checkpoint-TestPolicyCanonical_calculateMemoryBuffer")
			require.NoError(t, err)
			defer os.RemoveAll(ckDir)

			sfDir, err := ioutil.TempDir("", "statefile")
			require.NoError(t, err)
			defer os.RemoveAll(sfDir)

			conf := generateTestConfiguration(t, ckDir, sfDir)
			conf.MemoryPolicyCanonicalConfiguration = tt.fields.policyCanonicalConfiguration
			conf.GetDynamicConfiguration().MemoryHeadroomConfiguration = tt.fields.memoryHeadroomConfiguration

			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
			require.NoError(t, err)

			for _, c := range tt.fields.containers {
				err := metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
				assert.NoError(t, err)
			}

			metaServer := generateTestMetaServer(t, tt.fields.podList, metricsFetcher)

			p := NewPolicyCanonical(conf, nil, metaCache, metaServer, metrics.DummyMetrics{})

			store := metricsFetcher.(*metric.FakeMetricsFetcher)
			tt.fields.setFakeMetric(store)

			p.SetEssentials(tt.fields.essentials)

			err = p.Update()
			require.NoError(t, err)
			got, err := p.GetHeadroom()
			if (err != nil) != tt.wantErr {
				t.Errorf("calculateUtilBasedBuffer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("calculateUtilBasedBuffer() got = %v, want %v", got, tt.want)
			}
		})
	}
}
