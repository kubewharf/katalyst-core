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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
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
		reclaimParents              []string
		reclaimPercentages          []int
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   1.5,
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   0,
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
						consts.PodAnnotationQoSLevelReclaimedCores,
						map[string]string{
							cpuconsts.CPUStateAnnotationKeyNUMAHint: "0",
						},
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
						CacheBasedRatio:   0,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   2,
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
			want:    resource.MustParse("175Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("100Gi"),
				1: resource.MustParse("75Gi"),
			},
		},
		{
			name: "multi-path: weighted parent limits preserve total cap",
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   1.5,
					},
				},
				reclaimParents:     []string{"/kubereclaim/a", "/kubereclaim/b"},
				reclaimPercentages: []int{40, 60},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetCgroupMetric("/kubereclaim/a", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetCgroupMetric("/kubereclaim/b", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 100 << 30, Time: &now})
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
			name: "multi-path: missing parent metric keeps survivor weight",
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   1.5,
					},
				},
				reclaimParents:     []string{"/kubereclaim/a", "/kubereclaim/b"},
				reclaimPercentages: []int{40, 60},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetCgroupMetric("/kubereclaim/a", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 100 << 30, Time: &now})
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
			want:    resource.MustParse("60Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("30Gi"),
				1: resource.MustParse("30Gi"),
			},
		},
		{
			name: "multi-path: all parents missing returns error",
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   1.5,
					},
				},
				reclaimParents: []string{"/kubereclaim/a", "/kubereclaim/b"},
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
			wantErr: true,
		},
		{
			name: "multi-path numa-binding: weighted per-NUMA limits preserve total cap",
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
							Containers: []v1.Container{{Name: "container1"}},
						},
					},
				},
				containers: []*types.ContainerInfo{
					makeContainerInfo("pod1", "default",
						"pod1", "container1",
						consts.PodAnnotationQoSLevelReclaimedCores,
						map[string]string{cpuconsts.CPUStateAnnotationKeyNUMAHint: "0"},
						types.TopologyAwareAssignment{0: machine.NewCPUSet(0)}, 50<<30),
				},
				essentials: types.ResourceEssentials{
					EnableReclaim:       true,
					ResourceUpperBound:  500 << 30,
					ReservedForAllocate: 25 << 30,
				},
				memoryHeadroomConfiguration: &memoryheadroom.MemoryHeadroomConfiguration{
					MemoryUtilBasedConfiguration: &memoryheadroom.MemoryUtilBasedConfiguration{
						CacheBasedRatio:   0,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   2,
					},
				},
				reclaimParents:     []string{"/kubereclaim/a", "/kubereclaim/b"},
				reclaimPercentages: []int{40, 60},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetCgroupMetric("/kubereclaim/a", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetCgroupMetric("/kubereclaim/b", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetCgroupMetric("/kubereclaim/a-0", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 30 << 30, Time: &now})
					store.SetCgroupMetric("/kubereclaim/a-1", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 30 << 30, Time: &now})
					store.SetCgroupMetric("/kubereclaim/b-0", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 30 << 30, Time: &now})
					store.SetCgroupMetric("/kubereclaim/b-1", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 30 << 30, Time: &now})
					store.SetNodeMetric(pkgconsts.MetricMemScaleFactorSystem, utilmetric.MetricData{Value: 500, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: 250 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemFreeNuma, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetNumaMetric(0, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
					store.SetNumaMetric(1, pkgconsts.MetricMemInactiveFileNuma, utilmetric.MetricData{Value: 50 << 30, Time: &now})
				},
			},
			// NUMA 0 is actual-binding: per-NUMA limit = a-0(30Gi*40%)+b-0(30Gi*60%)
			// = 30Gi, *2 oversold = 60Gi. NUMA 1 is non-actual-binding: the
			// evenly-split parent baseline (50Gi) is now clamped down to its
			// materialized per-NUMA cgroup limit a-1(12Gi)+b-1(18Gi) = 30Gi,
			// *2 oversold = 60Gi (previously the clamp was absent -> 75Gi).
			wantErr: false,
			want:    resource.MustParse("120Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("60Gi"),
				1: resource.MustParse("60Gi"),
			},
		},
		{
			// Regression: for non-actual-binding NUMAs the parent reclaim cgroup
			// exposes only a single aggregate memory.max (100Gi), split evenly to
			// a 50Gi/NUMA baseline. Each NUMA also has a materialized per-NUMA
			// reclaim cgroup limit of 20Gi, which must clamp the baseline down.
			// Expected per-NUMA limit = min(50Gi, 20Gi) = 20Gi, then *1.5 oversold
			// = 30Gi. Without the clamp the baseline would stay at 50Gi -> 75Gi.
			name: "non-binding numa: per-NUMA cgroup limit clamps evenly-split baseline",
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   1.5,
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 20 << 30, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-1", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 20 << 30, Time: &now})
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
			want:    resource.MustParse("60Gi"),
			wantNUMA: map[int]resource.Quantity{
				0: resource.MustParse("30Gi"),
				1: resource.MustParse("30Gi"),
			},
		},
		{
			// Companion: when the per-NUMA cgroup limit is larger than the
			// evenly-split baseline (80Gi vs 50Gi), the clamp is a no-op and the
			// baseline is preserved -> 50Gi *1.5 = 75Gi. Guards against the clamp
			// over-shrinking when the actual per-NUMA limit is generous.
			name: "non-binding numa: larger per-NUMA cgroup limit is a no-op clamp",
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
						CacheBasedRatio:   0.5,
						RequestBasedRatio: 0.1,
						MaxOversoldRate:   1.5,
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 100 << 30, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 80 << 30, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-1", pkgconsts.MetricMemLimitCgroup, utilmetric.MetricData{Value: 80 << 30, Time: &now})
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
	}
	testMachineInfo := &machine.KatalystMachineInfo{
		CPUTopology: &machine.CPUTopology{
			CPUDetails: machine.CPUDetails{
				0: machine.CPUTopoInfo{NUMANodeID: 0},
				1: machine.CPUTopoInfo{NUMANodeID: 1},
			},
		},
	}
	_ = testMachineInfo
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ckDir := t.TempDir()
			sfDir := t.TempDir()

			conf := generateTestConfiguration(t, ckDir, sfDir)
			conf.GetDynamicConfiguration().MemoryHeadroomConfiguration = tt.fields.memoryHeadroomConfiguration

			// Build the reclaim path -> percentage map locally instead of going
			// through the process-global reclaim registry. Each subtest gets its
			// own map, so parallel subtests don't race on the shared registry.
			reclaimParents := tt.fields.reclaimParents
			if len(reclaimParents) == 0 {
				reclaimParents = []string{"/kubepods/besteffort"}
			}
			pathToPercent := map[string]float64{}
			for i, p := range reclaimParents {
				pct := 100 // default generic consumer percentage
				if i < len(tt.fields.reclaimPercentages) {
					pct = tt.fields.reclaimPercentages[i]
				}
				pathToPercent[p] = float64(pct)
			}

			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
			require.NoError(t, err)

			for _, c := range tt.fields.containers {
				err := metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
				assert.NoError(t, err)
			}

			metaServer := generateTestMetaServer(t, tt.fields.podList, metricsFetcher)

			p := NewPolicyNUMAAware(conf, nil, metaCache, metaServer, metrics.DummyMetrics{}).(*PolicyNUMAAware)
			// Pin reclaim paths locally: parent paths + per-NUMA children
			// "<parent>-<numaID>" for NUMA 0 and 1.
			numaBinding := map[int][]string{}
			for _, parent := range reclaimParents {
				for _, numaID := range []int{0, 1} {
					numaBinding[numaID] = append(numaBinding[numaID], fmt.Sprintf("%s-%d", parent, numaID))
				}
			}
			p.SetReclaimPathsForTest(reclaimParents, numaBinding)
			p.SetReclaimPercentageFnForTest(func(_ *dynamic.Configuration, path string) float64 {
				// Per-NUMA child paths ("<parent>-<numaID>") inherit the parent's
				// percentage, mirroring how the global reclaim registry resolves
				// the owner consumer via the reverse path index.
				if pct, ok := pathToPercent[path]; ok {
					return pct
				}
				for parent, pct := range pathToPercent {
					if strings.HasPrefix(path, parent+"-") {
						return pct
					}
				}
				return 0
			})

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
