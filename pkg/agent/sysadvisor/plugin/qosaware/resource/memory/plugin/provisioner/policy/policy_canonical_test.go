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

package policy

import (
	"io/ioutil"
	"os"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
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

func TestPolicyCanonical(t *testing.T) {
	t.Parallel()

	now := time.Now()

	type fields struct {
		podList       []*v1.Pod
		containers    []*types.ContainerInfo
		essentials    types.ResourceEssentials
		setFakeMetric func(store *metric.FakeMetricsFetcher)
	}

	tests := []struct {
		name    string
		fields  fields
		want    machine.MemoryDetails
		wantErr bool
	}{
		{
			name: "error: numa metrics: mem.free.numa missing",
			fields: fields{
				podList:    []*v1.Pod{},
				containers: []*types.ContainerInfo{},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
				},
			},
			wantErr: true,
		},
		{
			name: "error: numa metrics: mem.total.numa missing",
			fields: fields{
				podList:    []*v1.Pod{},
				containers: []*types.ContainerInfo{},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
				},
			},
			wantErr: true,
		},
		{
			name: "error: node metrics missing",
			fields: fields{
				podList:    []*v1.Pod{},
				containers: []*types.ContainerInfo{},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
				},
			},
			wantErr: true,
		},
		{
			name: "normal: no containers",
			fields: fields{
				podList:    []*v1.Pod{},
				containers: []*types.ContainerInfo{},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 5 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
				},
			},
			wantErr: false,
			want: machine.MemoryDetails{
				// mem.free.numa - reserved / availableNumaNum - (totalFree * 2 * mem.scale.factor / 10000) / availableNumaNum
				0: (100 << 30) - (5<<30)/2 - ((250<<30+250<<30)*2*500/10000)/2,
				1: (100 << 30) - (5<<30)/2 - ((250<<30+250<<30)*2*500/10000)/2,
			},
		},
		{
			name: "normal: reclaimed_cores containers only",
			fields: fields{
				podList: []*v1.Pod{},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelReclaimedCores, nil,
						nil, 20<<30),
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 5 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
				},
			},
			wantErr: false,
			want: machine.MemoryDetails{
				// mem.free.numa - reserved / availableNumaNum - (totalFree * 2 * mem.scale.factor / 10000) / availableNumaNum + memoryRequest / availableNumaNum
				0: (100 << 30) - (5<<30)/2 - ((250<<30+250<<30)*2*500/10000)/2 + (20<<30)/2,
				1: (100 << 30) - (5<<30)/2 - ((250<<30+250<<30)*2*500/10000)/2 + (20<<30)/2,
			},
		},
		{
			name: "normal: reclaimed_cores containers with numa-exclusive containers",
			fields: fields{
				podList: []*v1.Pod{},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelReclaimedCores, nil,
						nil, 20<<30),
					makeContainerInfo("pod2", "default",
						"pod2", "container2",
						consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
						},
						types.TopologyAwareAssignment{
							0: machine.NewCPUSet(0),
						}, 30<<30)},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 5 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
				},
			},
			wantErr: false,
			want: machine.MemoryDetails{
				0: 0,
				// mem.free.numa - reserved / availableNumaNum - (totalFree * 2 * mem.scale.factor / 10000) / availableNumaNum + memoryRequest / availableNumaNum
				1: (100 << 30) - (5 << 30) - ((250 << 30) * 2 * 500 / 10000) + (20 << 30),
			},
		},
		{
			name: "normal: all numa-exclusive containers",
			fields: fields{
				podList: []*v1.Pod{},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
						},
						types.TopologyAwareAssignment{
							0: machine.NewCPUSet(0),
						}, 10<<30),
					makeContainerInfo("pod2", "default",
						"pod2", "container2",
						consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
						},
						types.TopologyAwareAssignment{
							1: machine.NewCPUSet(1),
						}, 20<<30)},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 5 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
				},
			},
			wantErr: false,
			want: machine.MemoryDetails{
				0: 0,
				1: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ckDir, err := ioutil.TempDir("", "checkpoint-TestProvisionPolicyCanonical")
			require.NoError(t, err)
			defer os.RemoveAll(ckDir)

			sfDir, err := ioutil.TempDir("", "statefile")

			require.NoError(t, err)
			defer os.RemoveAll(sfDir)

			conf := generateTestConfiguration(t, ckDir, sfDir)

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
			if (err != nil) != tt.wantErr {
				t.Errorf("update() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			got := p.GetProvision()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetProvision() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if get, want := got, tt.want; !get.Equal(want) {
				t.Errorf("GetProvision() got = %v, want %v", get, want)
			}
		})
	}
}
