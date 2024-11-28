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

package reporter

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	nodeapis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	types2 "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func generateTestMetaCache(t *testing.T, conf *config.Configuration, metricsReader types.MetricsReader, containers ...types2.ContainerInfo) *metacache.MetaCacheImp {
	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsReader)
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	for _, container := range containers {
		c := container
		metaCache.AddContainer(container.PodUID, container.ContainerName, &c)
	}

	return metaCache
}

func TestRunNodeMetricReporter(t *testing.T) {
	t.Parallel()

	regDir, ckDir, statDir, err := tmpDirs()
	require.NoError(t, err)
	defer func() {
		os.RemoveAll(regDir)
		os.RemoveAll(ckDir)
		os.RemoveAll(statDir)
	}()

	conf := generateTestConfiguration(t, regDir, ckDir, statDir)
	clientSet := generateTestGenericClientSet(nil, nil)
	metaServer := generateTestMetaServer(clientSet, conf)
	metaReader := generateTestMetaCache(t, conf, metaServer.MetricsFetcher)

	reporter, err := NewNodeMetricsReporter(metrics.DummyMetrics{}, metaServer, metaReader, conf)

	require.NoError(t, err)
	require.NotNil(t, reporter)

	ctx, cancel := context.WithCancel(context.Background())
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		reporter.Run(ctx)
	}()
	time.Sleep(1 * time.Second)
	cancel()
	wg.Wait()
}

func mustParse(str string) *resource.Quantity {
	q := resource.MustParse(str)
	return &q
}

func TestNodeMetricUpdate(t *testing.T) {
	t.Parallel()

	regDir, ckDir, statDir, err := tmpDirs()
	require.NoError(t, err)
	defer func() {
		os.RemoveAll(regDir)
		os.RemoveAll(ckDir)
		os.RemoveAll(statDir)
	}()

	type args struct {
		metricSetter func(f *metric.FakeMetricsFetcher, p *nodeMetricsReporterPlugin)
		pods         []*corev1.Pod
		containers   []types2.ContainerInfo
	}

	tests := []struct {
		name                 string
		args                 args
		wantErr              bool
		wantNodeMetricStatus *nodeapis.NodeMetricStatus
	}{
		{
			name: "no metrics: update failed",
			args: args{
				metricSetter: func(f *metric.FakeMetricsFetcher, p *nodeMetricsReporterPlugin) {
				},
			},
			wantErr: true,
		},
		{
			name: "update with dedicated_cores, shared_cores, reclaimed_cores pods",
			args: args{
				metricSetter: func(f *metric.FakeMetricsFetcher, p *nodeMetricsReporterPlugin) {
					f.SetNodeMetric(consts.MetricMemUsedSystem, utilmetric.MetricData{Value: 200 << 30})
					f.SetNodeMetric(consts.MetricMemPageCacheSystem, utilmetric.MetricData{Value: 40 << 30})
					f.SetNodeMetric(consts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.5})
					for numaId := 0; numaId < 2; numaId++ {
						f.SetNumaMetric(numaId, consts.MetricMemUsedNuma, utilmetric.MetricData{Value: 50 << 30})
						f.SetNumaMetric(numaId, consts.MetricMemFilepageNuma, utilmetric.MetricData{Value: 10 << 30})
						f.SetNumaMetric(numaId, consts.MetricCPUUsageNuma, utilmetric.MetricData{Value: 2})
					}

					f.SetContainerMetric("uid1", "container1", consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 1})
					f.SetContainerMetric("uid1", "container1", consts.MetricMemUsageContainer, utilmetric.MetricData{Value: 20 << 30})
					f.SetContainerMetric("uid1", "container1", consts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30})
					for numaId := 0; numaId < 2; numaId++ {
						f.SetContainerNumaMetric("uid1", "container1", numaId, consts.MetricsCPUUsageNUMAContainer, utilmetric.MetricData{Value: 1})
						f.SetContainerNumaMetric("uid1", "container1", numaId, consts.MetricsMemAnonPerNumaContainer, utilmetric.MetricData{Value: 1 << 30})
					}

					f.SetContainerMetric("uid2", "container2", consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 1})
					f.SetContainerMetric("uid2", "container2", consts.MetricMemUsageContainer, utilmetric.MetricData{Value: 20 << 30})
					f.SetContainerMetric("uid2", "container2", consts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30})

					f.SetContainerMetric("uid3", "container3", consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 1})
					f.SetContainerMetric("uid3", "container3", consts.MetricMemUsageContainer, utilmetric.MetricData{Value: 20 << 30})
					f.SetContainerMetric("uid3", "container3", consts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30})

					f.SetContainerMetric("uid4", "container4", consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 1})
					f.SetContainerMetric("uid4", "container4", consts.MetricMemUsageContainer, utilmetric.MetricData{Value: 20 << 30})
					f.SetContainerMetric("uid4", "container4", consts.MetricMemCacheContainer, utilmetric.MetricData{Value: 10 << 30})
				},
				pods: []*corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:        "pod1",
							Namespace:   "default",
							UID:         "uid1",
							Annotations: map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "container1",
									Resources: corev1.ResourceRequirements{
										Limits: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceCPU:    resource.MustParse("4"),
											corev1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceCPU:    resource.MustParse("2"),
											corev1.ResourceMemory: resource.MustParse("20Gi"),
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
							Annotations: map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "container2",
									Resources: corev1.ResourceRequirements{
										Limits: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceCPU:    resource.MustParse("4"),
											corev1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceCPU:    resource.MustParse("2"),
											corev1.ResourceMemory: resource.MustParse("20Gi"),
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
							UID:         "uid3",
							Annotations: map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "container3",
									Resources: corev1.ResourceRequirements{
										Limits: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceCPU:    resource.MustParse("4"),
											corev1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceCPU:    resource.MustParse("2"),
											corev1.ResourceMemory: resource.MustParse("20Gi"),
										},
									},
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:        "pod4",
							Namespace:   "default",
							UID:         "uid4",
							Annotations: map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "container4",
									Resources: corev1.ResourceRequirements{
										Limits: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceCPU:    resource.MustParse("4"),
											corev1.ResourceMemory: resource.MustParse("40Gi"),
										},
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceCPU:    resource.MustParse("2"),
											corev1.ResourceMemory: resource.MustParse("20Gi"),
										},
									},
								},
							},
						},
					},
				},
				containers: []types2.ContainerInfo{
					{
						PodUID:         "uid1",
						PodNamespace:   "default",
						PodName:        "pod1",
						ContainerName:  "container1",
						ContainerType:  pluginapi.ContainerType_MAIN,
						ContainerIndex: 0,
					},
					{
						PodUID:         "uid2",
						PodNamespace:   "default",
						PodName:        "pod2",
						ContainerName:  "container2",
						ContainerType:  pluginapi.ContainerType_MAIN,
						ContainerIndex: 0,
					},
					{
						PodUID:         "uid3",
						PodNamespace:   "default",
						PodName:        "pod3",
						ContainerName:  "container3",
						ContainerType:  pluginapi.ContainerType_MAIN,
						ContainerIndex: 0,
					},
					{
						PodUID:                   "uid4",
						PodNamespace:             "default",
						PodName:                  "pod4",
						ContainerName:            "container4",
						ContainerType:            pluginapi.ContainerType_MAIN,
						ContainerIndex:           0,
						TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0), 1: machine.NewCPUSet(2)},
						RampUp:                   true,
					},
				},
			},
			wantErr: false,
			wantNodeMetricStatus: &nodeapis.NodeMetricStatus{
				UpdateTime: metav1.Time{},
				NodeMetric: &nodeapis.NodeMetricInfo{
					ResourceUsage: nodeapis.ResourceUsage{
						NUMAUsage: []nodeapis.NUMAMetricInfo{
							{
								NUMAId: 0,
								Usage: &nodeapis.ResourceMetric{
									Memory: mustParse("40Gi"),
									CPU:    mustParse("2"),
								},
							},
							{
								NUMAId: 1,
								Usage: &nodeapis.ResourceMetric{
									Memory: mustParse("40Gi"),
									CPU:    mustParse("2"),
								},
							},
						},
						GenericUsage: &nodeapis.ResourceMetric{
							CPU:    mustParse("8"),
							Memory: mustParse("200Gi"),
						},
					},
				},
				GroupMetric: []nodeapis.GroupMetricInfo{
					{
						QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
						ResourceUsage: nodeapis.ResourceUsage{
							NUMAUsage: nil,
							GenericUsage: &nodeapis.ResourceMetric{
								CPU:    mustParse("1"),
								Memory: mustParse("10Gi"),
							},
						},
						PodList: []string{"default/pod2"},
					},
					{
						QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
						ResourceUsage: nodeapis.ResourceUsage{
							NUMAUsage: []nodeapis.NUMAMetricInfo{
								{
									NUMAId: 0,
									Usage: &nodeapis.ResourceMetric{
										CPU:    mustParse("2"),
										Memory: mustParse("11Gi"),
									},
								},
								{
									NUMAId: 1,
									Usage: &nodeapis.ResourceMetric{
										CPU:    mustParse("2"),
										Memory: mustParse("11Gi"),
									},
								},
							},
							GenericUsage: &nodeapis.ResourceMetric{
								CPU:    mustParse("3"),
								Memory: mustParse("30Gi"),
							},
						},
						PodList: []string{"default/pod1", "default/pod4"},
					},
					{
						QoSLevel: apiconsts.PodAnnotationQoSLevelReclaimedCores,
						ResourceUsage: nodeapis.ResourceUsage{
							NUMAUsage: nil,
							GenericUsage: &nodeapis.ResourceMetric{
								CPU:    mustParse("1"),
								Memory: mustParse("10Gi"),
							},
						},
						PodList: []string{"default/pod3"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConfiguration(t, regDir, ckDir, statDir)
			clientSet := generateTestGenericClientSet(nil, nil)
			metaServer := generateTestMetaServer(clientSet, conf, tt.args.pods...)
			metaReader := generateTestMetaCache(t, conf, metaServer.MetricsFetcher, tt.args.containers...)

			reporter, err := NewNodeMetricsReporter(metrics.DummyMetrics{}, metaServer, metaReader, conf)

			require.NoError(t, err)
			require.NotNil(t, reporter)

			fakeMetricsFetcher := metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
			require.NotNil(t, fakeMetricsFetcher)
			reporterImpl, ok := reporter.(*nodeMetricsReporterImpl)
			require.True(t, ok)
			pluginWrapper, ok := reporterImpl.GenericPlugin.(*skeleton.PluginRegistrationWrapper)
			require.True(t, ok)
			plugin, ok := pluginWrapper.GenericPlugin.(*nodeMetricsReporterPlugin)
			require.True(t, ok)
			tt.args.metricSetter(fakeMetricsFetcher, plugin)

			ctx, cancel := context.WithCancel(context.Background())
			wg := sync.WaitGroup{}
			wg.Add(1)

			go func() {
				defer wg.Done()
				reporter.Run(ctx)
			}()

			time.Sleep(2 * time.Second)

			resp, err := plugin.GetReportContent(context.TODO(), nil)
			if err != nil {
				require.True(t, tt.wantErr)
			} else {
				require.False(t, tt.wantErr)
				require.Equal(t, &util.CNRGroupVersionKind, resp.Content[0].GroupVersionKind)
				require.Equal(t, v1alpha1.FieldType_Status, resp.Content[0].Field[0].FieldType)
				require.Equal(t, util.CNRFieldNameNodeMetricStatus, resp.Content[0].Field[0].FieldName)

				nodeMetricStatus := nodeapis.NodeMetricStatus{}
				err = json.Unmarshal(resp.Content[0].Field[0].Value, &nodeMetricStatus)
				require.NoError(t, err)
				tt.wantNodeMetricStatus.UpdateTime = *nodeMetricStatus.UpdateTime.DeepCopy()
				// to sort numa usage slice  to avoid flaky equality check
				for _, groupMetric := range nodeMetricStatus.GroupMetric {
					slice := groupMetric.ResourceUsage.NUMAUsage
					sort.Slice(slice, func(i, j int) bool {
						return slice[i].NUMAId < slice[j].NUMAId
					})
				}
				require.Equal(t, *tt.wantNodeMetricStatus, nodeMetricStatus)
			}

			cancel()
			wg.Wait()
		})
	}
}
