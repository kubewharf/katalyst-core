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

package headroomassembler

import (
	"io/ioutil"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/cpuheadroom"
	metric_consts "github.com/kubewharf/katalyst-core/pkg/consts"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metaservercnr "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	metric_util "github.com/kubewharf/katalyst-core/pkg/util/metric"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	return conf
}

func generateTestMetaServer(t *testing.T, cnr *v1alpha1.CustomNodeResource, podList []*v1.Pod,
	metricsFetcher metrictypes.MetricsFetcher,
) *metaserver.MetaServer {
	// numa node0 cpu(s): 0-23,48-71
	// numa node1 cpu(s): 24-47,72-95
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &info.MachineInfo{
					NumCores: 96,
					Topology: []info.Node{
						{
							Id: 0,
						},
						{
							Id: 1,
						},
					},
				},
				CPUTopology: cpuTopology,
			},
			CNRFetcher:     &metaservercnr.CNRFetcherStub{CNR: cnr},
			PodFetcher:     &pod.PodFetcherStub{PodList: podList},
			MetricsFetcher: metricsFetcher,
		},
	}

	podProfiles := make(map[apitypes.UID]spd.DummyPodServiceProfile)
	podProfiles["uid5"] = spd.DummyPodServiceProfile{PerformanceLevel: spd.PerformanceLevelPoor}
	metaServer.SetServiceProfilingManager(spd.NewDummyServiceProfilingManager(podProfiles))
	return metaServer
}

func TestHeadroomAssemblerCommon_GetHeadroom(t *testing.T) {
	t.Parallel()

	now := time.Now()

	type fields struct {
		regions                        map[string]region.QoSRegion
		entries                        types.RegionEntries
		cnr                            *v1alpha1.CustomNodeResource
		podList                        []*v1.Pod
		reclaimedResourceConfiguration *reclaimedresource.ReclaimedResourceConfiguration
		setFakeMetric                  func(store *metric.FakeMetricsFetcher)
		setMetaCache                   func(cache *metacache.MetaCacheImp)

		allowSharedCoresOverlapReclaimedCores bool
	}
	tests := []struct {
		name    string
		fields  fields
		want    resource.Quantity
		wantErr bool
	}{
		{
			name: "normal report",
			fields: fields{
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:        "pod1",
							Namespace:   "default",
							UID:         "uid1",
							Annotations: map[string]string{consts.PodAnnotationNUMABindResultKey: "0"},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container1",
									Resources: v1.ResourceRequirements{
										Limits: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("2"),
											v1.ResourceMemory: resource.MustParse("20Gi"),
										},
									},
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:        "pod2",
							Namespace:   "default",
							UID:         "uid1",
							Annotations: map[string]string{consts.PodAnnotationNUMABindResultKey: "1000"},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container1",
									Resources: v1.ResourceRequirements{
										Limits: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("2"),
											v1.ResourceMemory: resource.MustParse("20Gi"),
										},
									},
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:        "pod3",
							Namespace:   "default",
							UID:         "uid1",
							Annotations: map[string]string{consts.PodAnnotationNUMABindResultKey: "hehe"},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container1",
									Resources: v1.ResourceRequirements{
										Limits: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("2"),
											v1.ResourceMemory: resource.MustParse("20Gi"),
										},
									},
								},
							},
						},
					},
				},
				entries: map[string]*types.RegionInfo{
					"share": {
						RegionType:    configapi.QoSRegionTypeShare,
						OwnerPoolName: "share",
						BindingNumas:  machine.NewCPUSet(0, 1),
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
							},
						},
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: "Socket",
								Name: "0",
								Children: []*v1alpha1.TopologyZone{
									{
										Type: "Numa",
										Name: "0",
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
											},
										},
									},
								},
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         true,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0,
							MaxOversoldRate:                1.5,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					for i := 0; i < 10; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.3, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 3, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})

					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 3, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
						},
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewMilliQuantity(13000, resource.DecimalSI),
		},
		{
			name: "limited by quota",
			fields: fields{
				entries: map[string]*types.RegionInfo{
					"share": {
						RegionType:    configapi.QoSRegionTypeShare,
						OwnerPoolName: "share",
						BindingNumas:  machine.NewCPUSet(0, 1),
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
							},
						},
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: "Socket",
								Name: "0",
								Children: []*v1alpha1.TopologyZone{
									{
										Type: "Numa",
										Name: "0",
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
											},
										},
									},
								},
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         true,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0,
							MaxOversoldRate:                1.5,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					for i := 0; i < 10; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.3, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 3, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort-0", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})

					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 3, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: 5000, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
						},
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(8, resource.DecimalSI),
		},
		{
			name: "allow shared cores overlap reclaimed cores",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: true,
				entries: map[string]*types.RegionInfo{
					"share-0": {
						RegionName:    "share-0",
						OwnerPoolName: "share-0",
						BindingNumas:  machine.NewCPUSet(0, 1),
						RegionType:    configapi.QoSRegionTypeShare,
						Pods: map[string]sets.String{
							"pod1": sets.NewString("container1"),
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
							},
						},
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: "Socket",
								Name: "0",
								Children: []*v1alpha1.TopologyZone{
									{
										Type: "Numa",
										Name: "0",
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
											},
										},
									},
								},
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         true,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0,
							MaxOversoldRate:                1.5,
							NonReclaimUtilizationHigh:      0.7,
							NonReclaimUtilizationLow:       0.5,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					for i := 0; i < 10; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.8, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
					store.SetContainerMetric("pod1", "container1", metric_consts.MetricCPUUsageContainer, metric_util.MetricData{Value: 4})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
						},
					})
					require.NoError(t, err)
					err = cache.SetPoolInfo("share-0", &types.PoolInfo{
						PoolName: "share-0",
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 2, 4),
							1: machine.NewCPUSet(5, 6, 7, 8),
						},
						OriginalTopologyAwareAssignments: nil,
						RegionNames:                      sets.NewString("share-0"),
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(5, resource.DecimalSI),
		},
		{
			name: "disable util based",
			fields: fields{
				entries: map[string]*types.RegionInfo{
					"share": {
						RegionType:    configapi.QoSRegionTypeShare,
						OwnerPoolName: "share",
						BindingNumas:  machine.NewCPUSet(0, 1),
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         false,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0,
							MaxOversoldRate:                1.5,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					for i := 0; i < 10; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.3, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 3, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 100000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
						},
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(10, resource.DecimalSI),
		},
		{
			name: "gap by oversold ratio",
			fields: fields{
				entries: map[string]*types.RegionInfo{
					"share": {
						RegionType:    configapi.QoSRegionTypeShare,
						OwnerPoolName: "share",
						BindingNumas:  machine.NewCPUSet(0, 1),
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
							},
						},
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: "Socket",
								Name: "0",
								Children: []*v1alpha1.TopologyZone{
									{
										Type: "Numa",
										Name: "0",
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
											},
										},
									},
								},
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         true,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0,
							MaxOversoldRate:                1.2,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					for i := 0; i < 10; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 0, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
						},
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(12, resource.DecimalSI),
		},
		{
			name: "over maximum core utilization",
			fields: fields{
				entries: map[string]*types.RegionInfo{
					"share": {
						RegionType:    configapi.QoSRegionTypeShare,
						OwnerPoolName: "share",
						BindingNumas:  machine.NewCPUSet(0, 1),
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("15000"),
							},
						},
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: "Socket",
								Name: "0",
								Children: []*v1alpha1.TopologyZone{
									{
										Type: "Numa",
										Name: "0",
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												consts.ReclaimedResourceMilliCPU: resource.MustParse("15000"),
											},
										},
									},
								},
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         true,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0.8,
							MaxOversoldRate:                1.5,
							NonReclaimUtilizationHigh:      0.7,
							NonReclaimUtilizationLow:       0.5,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					for i := 0; i < 96; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.9, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 9, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
						},
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(14, resource.DecimalSI),
		},
		{
			name: "reclaim pool overload",
			fields: fields{
				entries: map[string]*types.RegionInfo{
					"share": {
						RegionType:    configapi.QoSRegionTypeShare,
						OwnerPoolName: "share",
						BindingNumas:  machine.NewCPUSet(0, 1),
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("15000"),
							},
						},
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: "Socket",
								Name: "0",
								Children: []*v1alpha1.TopologyZone{
									{
										Type: "Numa",
										Name: "0",
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												consts.ReclaimedResourceMilliCPU: resource.MustParse("15000"),
											},
										},
									},
								},
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         true,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0.8,
							MaxOversoldRate:                1.5,
							NonReclaimUtilizationHigh:      0,
							NonReclaimUtilizationLow:       0,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					for i := 0; i < 96; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.9, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 9, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
						},
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(0, resource.DecimalSI),
		},
		{
			name: "limited by capacity",
			fields: fields{
				entries: map[string]*types.RegionInfo{
					"share": {
						RegionType:    configapi.QoSRegionTypeShare,
						OwnerPoolName: "share",
						BindingNumas:  machine.NewCPUSet(0, 1),
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("86000"),
							},
						},
						TopologyZone: []*v1alpha1.TopologyZone{
							{
								Type: "Socket",
								Name: "0",
								Children: []*v1alpha1.TopologyZone{
									{
										Type: "Numa",
										Name: "0",
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												consts.ReclaimedResourceMilliCPU: resource.MustParse("43000"),
											},
										},
									},
									{
										Type: "Numa",
										Name: "1",
										Resources: v1alpha1.Resources{
											Allocatable: &v1.ResourceList{
												consts.ReclaimedResourceMilliCPU: resource.MustParse("43000"),
											},
										},
									},
								},
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         true,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0.8,
							MaxOversoldRate:                1.5,
							MaxHeadroomCapacityRate:        1.,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					now := time.Now()
					for i := 0; i < 96; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.3, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 28.8, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-43"),
							1: machine.MustParse("49-85"),
						},
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(96, resource.DecimalSI),
		},
		{
			name: "numa-exclusive region headroom",
			fields: fields{
				entries: map[string]*types.RegionInfo{
					"dedicated": {
						RegionType:    configapi.QoSRegionTypeDedicatedNumaExclusive,
						OwnerPoolName: "dedicated",
						BindingNumas:  machine.NewCPUSet(0),
						Headroom:      10,
						RegionStatus: types.RegionStatus{
							BoundType: types.BoundUpper,
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("86000"),
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         false,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0.8,
							MaxOversoldRate:                1.5,
							MaxHeadroomCapacityRate:        1.,
						},
					},
				},
				podList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:        "pod1",
							Namespace:   "default",
							UID:         "uid1",
							Annotations: map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container1",
									Resources: v1.ResourceRequirements{
										Limits: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("2"),
											v1.ResourceMemory: resource.MustParse("20Gi"),
										},
									},
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:        "pod2",
							Namespace:   "default",
							UID:         "uid2",
							Annotations: map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "container2",
									Resources: v1.ResourceRequirements{
										Limits: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("4"),
											v1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[v1.ResourceName]resource.Quantity{
											v1.ResourceCPU:    resource.MustParse("2"),
											v1.ResourceMemory: resource.MustParse("20Gi"),
										},
									},
								},
							},
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					now := time.Now()
					for i := 0; i < 96; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.3, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 28.8, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
							1: machine.MustParse("49-96"),
						},
					})
					require.NoError(t, err)
					err = cache.AddContainer("uid1", "container1", &types.ContainerInfo{
						PodName:      "pod1",
						PodNamespace: "default",
						PodUID:       "uid1",
						QoSLevel:     consts.PodAnnotationQoSLevelDedicatedCores,
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(68, resource.DecimalSI),
		},
		{
			name: "numa-exclusive region headroom util",
			fields: fields{
				entries: map[string]*types.RegionInfo{
					"dedicated": {
						RegionType:    configapi.QoSRegionTypeDedicatedNumaExclusive,
						OwnerPoolName: "dedicated",
						BindingNumas:  machine.NewCPUSet(0),
						Headroom:      10,
						RegionStatus: types.RegionStatus{
							BoundType: types.BoundUpper,
						},
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					Status: v1alpha1.CustomNodeResourceStatus{
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("86000"),
							},
						},
					},
				},
				reclaimedResourceConfiguration: &reclaimedresource.ReclaimedResourceConfiguration{
					EnableReclaim: true,
					CPUHeadroomConfiguration: &cpuheadroom.CPUHeadroomConfiguration{
						CPUUtilBasedConfiguration: &cpuheadroom.CPUUtilBasedConfiguration{
							Enable:                         true,
							TargetReclaimedCoreUtilization: 0.6,
							MaxReclaimedCoreUtilization:    0.8,
							MaxOversoldRate:                1.5,
							MaxHeadroomCapacityRate:        1.,
						},
					},
				},
				setFakeMetric: func(store *metric.FakeMetricsFetcher) {
					now := time.Now()
					for i := 0; i < 96; i++ {
						store.SetCPUMetric(i, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.3, Time: &now})
					}
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: 28.8, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: -1, Time: &now})
					store.SetCgroupMetric("/kubepods/besteffort", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
				},
				setMetaCache: func(cache *metacache.MetaCacheImp) {
					err := cache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
						PoolName: commonstate.PoolNameReclaim,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.MustParse("0-9"),
							1: machine.MustParse("49-96"),
						},
					})
					require.NoError(t, err)
				},
			},
			want: *resource.NewQuantity(70, resource.DecimalSI),
		},
	}

	patches := gomonkey.ApplyFunc(cgroupmgr.GetCPUWithRelativePath, func(relCgroupPath string) (*common.CPUStats, error) {
		return &common.CPUStats{
			CpuPeriod: 100000,
			CpuQuota:  -1,
		}, nil
	})
	defer t.Cleanup(func() {
		patches.Reset()
	})

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ckDir, err := ioutil.TempDir("", "checkpoint-TestHeadroomAssemblerCommon_GetHeadroom")
			require.NoError(t, err)
			defer os.RemoveAll(ckDir)

			sfDir, err := ioutil.TempDir("", "statefile")
			require.NoError(t, err)
			defer os.RemoveAll(sfDir)

			conf := generateTestConfiguration(t, ckDir, sfDir)
			conf.GetDynamicConfiguration().ReclaimedResourceConfiguration = tt.fields.reclaimedResourceConfiguration
			conf.GetDynamicConfiguration().AllowSharedCoresOverlapReclaimedCores = tt.fields.allowSharedCoresOverlapReclaimedCores
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
			require.NoError(t, err)

			err = metaCache.SetRegionEntries(tt.fields.entries)
			require.NoError(t, err)
			tt.fields.setMetaCache(metaCache)

			metaServer := generateTestMetaServer(t, tt.fields.cnr, tt.fields.podList, metricsFetcher)

			for name, regionInfo := range tt.fields.entries {
				r := region.NewQoSRegionBase(name, regionInfo.OwnerPoolName, regionInfo.RegionType, conf, nil, false, metaCache, metaServer, metrics.DummyMetrics{})
				r.SetBindingNumas(regionInfo.BindingNumas)
				r.SetEssentials(types.ResourceEssentials{
					EnableReclaim: tt.fields.reclaimedResourceConfiguration.EnableReclaim,
				})
				tt.fields.regions = map[string]region.QoSRegion{
					name: r,
				}
			}
			reservedForReclaim := map[int]int{0: 2, 1: 2}
			numaAvailable := map[int]int{0: 46, 1: 46}

			ha := NewHeadroomAssemblerCommon(conf, nil, &tt.fields.regions, &reservedForReclaim, &numaAvailable, nil, metaCache, metaServer, metrics.DummyMetrics{})

			store := metricsFetcher.(*metric.FakeMetricsFetcher)
			tt.fields.setFakeMetric(store)

			got, _, err := ha.GetHeadroom()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetHeadroom() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetHeadroom() got = %v, want %v", got, tt.want)
			}
		})
	}
}
