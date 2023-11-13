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

package realtime

import (
	"io/ioutil"
	"math"
	"os"
	"testing"
	"time"

	consts2 "github.com/kubewharf/katalyst-api/pkg/consts"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubelet/config/v1beta1"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/kubeletconfig"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metric2 "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestUpdate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		pods             []*v1.Pod
		containerMetrics containerMetric
		node             *v1.Node
		expectRatio      map[string]float64
		expectError      bool
	}{
		{
			name: "low usage",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "testNode",
					Annotations: map[string]string{
						consts2.NodeAnnotationCPUOvercommitRatioKey:    "10",
						consts2.NodeAnnotationMemoryOvercommitRatioKey: "10",
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "uid-pod-1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container1",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(4, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.DecimalSI),
									},
								},
							},
						},
					},
				}, {
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "uid-pod-2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container2",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(4, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.DecimalSI),
									},
								},
							},
						},
					},
				},
			},
			containerMetrics: containerMetric{
				"uid-pod-1": {
					"container1": {
						consts.MetricCPUUsageContainer: 0.5,
						consts.MetricLoad1MinContainer: 0.1,
						consts.MetricLoad5MinContainer: 0.1,
						consts.MetricMemRssContainer:   1 * 1024 * 1024 * 1024,
					},
				},
				"uid-pod-2": {
					"container2": {
						consts.MetricCPUUsageContainer: 0.5,
						consts.MetricLoad1MinContainer: 0.1,
						consts.MetricLoad5MinContainer: 0.1,
						consts.MetricMemRssContainer:   1 * 1024 * 1024 * 1024,
					},
				},
			},
			expectError: false,
			expectRatio: map[string]float64{
				"cpu":    1.6,
				"memory": 1.17,
			},
		},
		{
			name: "high usage",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "testNode",
					Annotations: map[string]string{
						consts2.NodeAnnotationCPUOvercommitRatioKey:    "10",
						consts2.NodeAnnotationMemoryOvercommitRatioKey: "10",
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "uid-pod-1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container1",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(8, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(4, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
								},
							},
						},
					},
				}, {
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "uid-pod-2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container2",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(8, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(4, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
								},
							},
						},
					},
				},
			},
			containerMetrics: containerMetric{
				"uid-pod-1": {
					"container1": {
						consts.MetricCPUUsageContainer: 3,
						consts.MetricLoad1MinContainer: 1,
						consts.MetricLoad5MinContainer: 1,
						consts.MetricMemRssContainer:   7 * 1024 * 1024 * 1024,
					},
				},
				"uid-pod-2": {
					"container2": {
						consts.MetricCPUUsageContainer: 2,
						consts.MetricLoad1MinContainer: 1,
						consts.MetricLoad5MinContainer: 1,
						consts.MetricMemRssContainer:   6 * 1024 * 1024 * 1024,
					},
				},
			},
			expectError: false,
			expectRatio: map[string]float64{
				"cpu":    1.0,
				"memory": 1.0,
			},
		},
		{
			name: "no request",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "testNode",
					Annotations: map[string]string{
						consts2.NodeAnnotationCPUOvercommitRatioKey:    "10",
						consts2.NodeAnnotationMemoryOvercommitRatioKey: "10",
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "uid-pod-1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container1",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(8, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
								},
							},
						},
					},
				}, {
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "uid-pod-2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container2",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(8, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
								},
							},
						},
					},
				},
			},
			containerMetrics: containerMetric{
				"uid-pod-1": {
					"container1": {
						consts.MetricCPUUsageContainer: 3,
						consts.MetricLoad1MinContainer: 1,
						consts.MetricLoad5MinContainer: 1,
						consts.MetricMemRssContainer:   6 * 1024 * 1024 * 1024,
					},
				},
				"uid-pod-2": {
					"container2": {
						consts.MetricCPUUsageContainer: 2,
						consts.MetricLoad1MinContainer: 1,
						consts.MetricLoad5MinContainer: 1,
						consts.MetricMemRssContainer:   6 * 1024 * 1024 * 1024,
					},
				},
			},
			expectError: false,
			expectRatio: map[string]float64{
				"cpu":    1.0,
				"memory": 1.0,
			},
		},
		{
			name: "no metrics",
			node: &v1.Node{
				ObjectMeta: v12.ObjectMeta{
					Name: "testNode",
					Annotations: map[string]string{
						consts2.NodeAnnotationCPUOvercommitRatioKey:    "10",
						consts2.NodeAnnotationMemoryOvercommitRatioKey: "10",
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "uid-pod-1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container1",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(8, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.DecimalSI),
									},
								},
							},
						},
					},
				}, {
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "uid-pod-2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "container2",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(8, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.DecimalSI),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
										v1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.DecimalSI),
									},
								},
							},
						},
					},
				},
			},
			containerMetrics: containerMetric{},
			expectError:      false,
			expectRatio: map[string]float64{
				"cpu":    1.75,
				"memory": 1.25,
			},
		},
	}

	checkpointDir, err := ioutil.TempDir("", "checkpoint-update")
	require.NoError(t, err)
	defer os.RemoveAll(checkpointDir)

	statefileDir, err := ioutil.TempDir("", "statefile-update")
	require.NoError(t, err)
	defer os.RemoveAll(statefileDir)

	conf := generateTestConfiguration(t, checkpointDir, statefileDir, "testNode")

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ms := generateTestMetaServer(t, conf, tc.pods, tc.containerMetrics, tc.node)
			r := NewRealtimeOvercommitmentAdvisor(config.NewConfiguration(), ms, metrics.DummyMetrics{})
			err := r.update()
			if tc.expectError {
				assert.NotNil(t, err)
			} else {
				assert.NoError(t, err)

				ratio, _ := r.GetOvercommitRatio()
				for resourceName, value := range ratio {
					assert.Less(t, math.Abs(value-tc.expectRatio[string(resourceName)]), 0.01)
				}
			}
		})
	}
}

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string, nodeName string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.NodeName = nodeName
	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	return conf
}

func generateTestMetaServer(t *testing.T, conf *config.Configuration, podList []*v1.Pod, containerMetrics containerMetric, node *v1.Node) *metaserver.MetaServer {
	genericClient := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(node),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}

	meta, err := metaserver.NewMetaServer(genericClient, metrics.DummyMetrics{}, conf)
	assert.NoError(t, err)

	meta.PodFetcher = &pod.PodFetcherStub{
		PodList: podList,
	}

	now := time.Now()
	fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	for podUID, podData := range containerMetrics {
		for containerName, containerData := range podData {
			for metricName, value := range containerData {
				fakeMetricsFetcher.SetContainerMetric(podUID, containerName, metricName, metric2.MetricData{
					Value: value,
					Time:  &now,
				})
			}
		}
	}

	meta.MetricsFetcher = fakeMetricsFetcher

	meta.KubeletConfigFetcher = kubeletconfig.NewFakeKubeletConfigFetcher(v1beta1.KubeletConfiguration{})

	meta.MachineInfo = &info.MachineInfo{
		NumCores:       16,
		MemoryCapacity: 32 * 1024 * 1024 * 1024,
	}
	return meta
}

type containerMetric map[string]map[string]map[string]float64
