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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/memoryheadroom"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestPolicyNUMAAware(t *testing.T) {
	t.Parallel()

	now := time.Now()

	type fields struct {
		podList                     []*v1.Pod
		containers                  []*types.ContainerInfo
		memoryHeadroomConfiguration *memoryheadroom.MemoryHeadroomConfiguration
		essentials                  types.ResourceEssentials
		setFakeMetric               func(store *metric.FakeMetricsFetcher)
	}
	tests := []struct {
		name     string
		fields   fields
		want     resource.Quantity
		wantNUMA map[int]resource.Quantity
		wantErr  bool
	}{
		{
			name: "numa metrics missing",
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
				},
			},
			wantErr: true,
		},
		{
			name: "node metrics missing",
			fields: fields{
				podList:    []*v1.Pod{},
				containers: []*types.ContainerInfo{},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  100 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
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
					ResourceUpperBound:  400 << 30,
					ReservedForAllocate: 4 << 30,
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						CacheBasedRatio: 0.5,
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
				},
			},
			wantErr: false,
			want:    resource.MustParse("221Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("110.5Gi"),
				1: resource.MustParse("110.5Gi"),
			},
		},
		{
			name: "normal: reclaimed_cores containers only",
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
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelReclaimedCores, nil,
						types.TopologyAwareAssignment{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(1),
						}, 20<<30),
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						CacheBasedRatio: 0.5,
					},
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  400 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
				},
			},
			wantErr: false,
			want:    resource.MustParse("241Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("120.5Gi"),
				1: resource.MustParse("120.5Gi"),
			},
		},
		{
			name: "normal: reclaimed_cores containers with numa-exclusive containers",
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
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pod2",
							Namespace: "default",
							UID:       "pod2",
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container2",
								},
							},
						},
						Status: v1.PodStatus{
							Phase: v1.PodRunning,
							ContainerStatuses: []v1.ContainerStatus{
								{
									Name:        "container2",
									ContainerID: "container2",
								},
							},
						},
					},
				},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelReclaimedCores, nil,
						types.TopologyAwareAssignment{
							1: machine.NewCPUSet(1),
						}, 20<<30),
					makeContainerInfo("pod2", "default",
						"pod2", "container2",
						consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
						},
						types.TopologyAwareAssignment{
							0: machine.NewCPUSet(0),
						}, 30<<30),
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						CacheBasedRatio: 0.5,
					},
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  400 << 30,
					ReservedForAllocate: 4 << 30,
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
				},
			},
			wantErr: false,
			want:    resource.MustParse("130.5Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("0"),
				1: resource.MustParse("130.5Gi"),
			},
		},
		{
			name: "reviseNUMAHeadroomMemory with max oversold rate",
			fields: fields{
				podList:    []*v1.Pod{},
				containers: []*types.ContainerInfo{},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  400 << 30,
					ReservedForAllocate: 4 << 30,
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						CacheBasedRatio: 0.5,
						MaxOversoldRate: 1.5,
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
				},
			},
			wantErr: false,
			want:    resource.MustParse("150Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("75Gi"),
				1: resource.MustParse("75Gi"),
			},
		},
		{
			name: "reviseNUMAHeadroomMemory with zero oversold rate",
			fields: fields{
				podList:    []*v1.Pod{},
				containers: []*types.ContainerInfo{},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  400 << 30,
					ReservedForAllocate: 4 << 30,
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						CacheBasedRatio: 0.5,
						MaxOversoldRate: 0,
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
				},
			},
			wantErr: false,
			want:    resource.MustParse("221Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("110.5Gi"),
				1: resource.MustParse("110.5Gi"),
			},
		},
		{
			name: "reviseNUMAHeadroomMemory with max oversold rate and with actual numa binding reclaim pods",
			fields: fields{
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:       "pod1",
							Name:      "pod1",
							Namespace: "default",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationNUMABindResultKey: "0",
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
				},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelReclaimedCores, nil,
						types.TopologyAwareAssignment{
							0: machine.NewCPUSet(0),
						}, 50<<30),
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  500 << 30,
					ReservedForAllocate: 25 << 30,
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						CacheBasedRatio: 0,
						MaxOversoldRate: 2,
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-1", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
				},
			},
			wantErr: false,
			want:    resource.MustParse("180Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("100Gi"),
				1: resource.MustParse("80Gi"),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ckDir, err := ioutil.TempDir("", "checkpoint-TestPolicyNUMAAware")
			require.NoError(t, err)
			defer os.RemoveAll(ckDir)

			sfDir, err := ioutil.TempDir("", "statefile")
			require.NoError(t, err)
			defer os.RemoveAll(sfDir)

			conf := generateTestConfiguration(t, ckDir, sfDir)
			conf.GetDynamicConfiguration().MemoryHeadroomConfiguration = tt.fields.memoryHeadroomConfiguration

			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
			require.NoError(t, err)

			for _, c := range tt.fields.containers {
				err := metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
				assert.NoError(t, err)
			}

			metaServer := generateTestMetaServer(t, tt.fields.podList, metricsFetcher)

			p := NewPolicyNUMAAware(conf, nil, metaCache, metaServer, metrics.DummyMetrics{})

			store := metricsFetcher.(*metric.FakeMetricsFetcher)
			tt.fields.setFakeMetric(store)

			p.SetEssentials(tt.fields.essentials)

			err = p.Update()
			if (err != nil) != tt.wantErr {
				t.Errorf("update() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			got, gotNUMA, err := p.GetHeadroom()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetHeadroom() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Truef(t, got.Equal(tt.want), "GetHeadroom() = %v, want %v", got, tt.want)
			assert.Truef(t, apiequality.Semantic.DeepEqual(gotNUMA, tt.wantNUMA), "GetHeadroom() = %v, want %v", gotNUMA, tt.wantNUMA)
		})
	}
}
