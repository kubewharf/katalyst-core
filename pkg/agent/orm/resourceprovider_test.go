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

package orm

import (
	"context"
	"io/ioutil"
	"os"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/kubelet/pkg/apis/podresources/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/metamanager"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestGetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	ckDir, err := ioutil.TempDir("", "checkpoint-Test")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	conf := generateTestConfiguration(ckDir)
	metaServer, err := generateTestMetaServer(conf, []*v1.Pod{})
	assert.NoError(t, err)
	metamanager := metamanager.NewManager(metrics.DummyMetrics{}, nil, metaServer)

	checkpointManager, err := checkpointmanager.NewCheckpointManager("/tmp/GetTopologyAwareResources")
	assert.NoError(t, err)

	for _, tc := range []struct {
		name         string
		pod          *v1.Pod
		container    *v1.Container
		expectedResp []*v12.TopologyAwareResource
	}{
		{
			name: "test1",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod1",
					Namespace: "namespace1",
					UID:       "uid1",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "container1",
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU:    resource.MustParse("4"),
									v1.ResourceMemory: resource.MustParse("8Gi"),
								},
							},
						},
					},
				},
			},
			container: &v1.Container{
				Name: "container1",
				Resources: v1.ResourceRequirements{
					Requests: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			},
			expectedResp: []*v12.TopologyAwareResource{
				{
					ResourceName:               "cpu",
					IsScalarResource:           true,
					AggregatedQuantity:         4,
					OriginalAggregatedQuantity: 4,
					TopologyAwareQuantityList: []*v12.TopologyAwareQuantity{
						{
							ResourceValue: 4,
							Node:          0,
							TopologyLevel: v12.TopologyLevel_NUMA,
						},
					},
					OriginalTopologyAwareQuantityList: []*v12.TopologyAwareQuantity{
						{
							ResourceValue: 4,
							Node:          0,
							TopologyLevel: v12.TopologyLevel_NUMA,
						},
					},
				},
				{
					ResourceName:               "memory",
					IsScalarResource:           true,
					AggregatedQuantity:         8589934592,
					OriginalAggregatedQuantity: 8589934592,
					TopologyAwareQuantityList: []*v12.TopologyAwareQuantity{
						{
							ResourceValue: 8589934592,
							Node:          0,
							TopologyLevel: v12.TopologyLevel_NUMA,
						},
					},
					OriginalTopologyAwareQuantityList: []*v12.TopologyAwareQuantity{
						{
							ResourceValue: 8589934592,
							Node:          0,
							TopologyLevel: v12.TopologyLevel_NUMA,
						},
					},
				},
			},
		},
		{
			name:         "nil pod",
			pod:          nil,
			container:    nil,
			expectedResp: []*v12.TopologyAwareResource{},
		},
		{
			name: "skip pod",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod",
					Namespace: "namespace",
					UID:       "uid",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
						},
					},
					Annotations: map[string]string{
						"katalyst.kubewharf.io/qos_level": "shared_cores",
					},
				},
			},
			container: &v1.Container{
				Name: "container",
			},
			expectedResp: nil,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := &ManagerImpl{
				reconcilePeriod:   time.Minute,
				endpoints:         map[string]endpoint.EndpointInfo{},
				socketdir:         "/tmp/GetTopologyAwareResources",
				socketname:        "tmp.sock",
				checkpointManager: checkpointManager,
				qosConfig:         generic.NewQoSConfiguration(),
				metaManager:       metamanager,
				emitter:           metrics.DummyMetrics{},
			}

			m.registerEndpoint("cpu", &pluginapi.ResourcePluginOptions{
				PreStartRequired:      true,
				WithTopologyAlignment: true,
				NeedReconcile:         true,
			}, &MockEndpoint{
				getTopologyAwareResourcesFunc: func(c context.Context, request *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
					return &pluginapi.GetTopologyAwareResourcesResponse{
						PodUid:       string(tc.pod.UID),
						PodName:      tc.pod.Name,
						PodNamespace: tc.pod.Namespace,
						ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
							ContainerName:      tc.container.Name,
							AllocatedResources: containerToCPUAllocatedResources(tc.container),
						},
					}, nil
				},
			})

			m.registerEndpoint("memory", &pluginapi.ResourcePluginOptions{
				PreStartRequired:      true,
				WithTopologyAlignment: true,
				NeedReconcile:         true,
			}, &MockEndpoint{
				getTopologyAwareResourcesFunc: func(c context.Context, request *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
					return &pluginapi.GetTopologyAwareResourcesResponse{
						PodUid:       string(tc.pod.UID),
						PodName:      tc.pod.Name,
						PodNamespace: tc.pod.Namespace,
						ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
							ContainerName:      tc.container.Name,
							AllocatedResources: containerToMemoryAllocatedResources(tc.container),
						},
					}, nil
				},
			})

			resp := m.GetTopologyAwareResources(tc.pod, tc.container)
			assert.True(t, equalTopologyAwareResourceList(resp, tc.expectedResp))
		})
	}
}

func TestGetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	ckDir, err := ioutil.TempDir("", "checkpoint-Test")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	conf := generateTestConfiguration(ckDir)
	metaServer, err := generateTestMetaServer(conf, []*v1.Pod{})
	assert.NoError(t, err)
	metamanager := metamanager.NewManager(metrics.DummyMetrics{}, nil, metaServer)

	checkpointManager, err := checkpointmanager.NewCheckpointManager("/tmp/GetTopologyAwareAllocatableResources")
	assert.NoError(t, err)

	m := &ManagerImpl{
		reconcilePeriod:   time.Minute,
		endpoints:         map[string]endpoint.EndpointInfo{},
		socketdir:         "/tmp/GetTopologyAwareAllocatableResources",
		socketname:        "tmp.sock",
		checkpointManager: checkpointManager,
		qosConfig:         generic.NewQoSConfiguration(),
		metaManager:       metamanager,
		emitter:           metrics.DummyMetrics{},
	}

	for _, tc := range []struct {
		name         string
		cpu          float64
		memory       float64
		expectedResp []*v12.AllocatableTopologyAwareResource
	}{
		{
			name:   "test1",
			cpu:    4,
			memory: 8589934592,
			expectedResp: []*v12.AllocatableTopologyAwareResource{
				{
					ResourceName:                  "cpu",
					IsScalarResource:              true,
					AggregatedAllocatableQuantity: 4,
					AggregatedCapacityQuantity:    4,
					TopologyAwareAllocatableQuantityList: []*v12.TopologyAwareQuantity{
						{
							ResourceValue: 4,
							Node:          0,
							TopologyLevel: v12.TopologyLevel_NUMA,
						},
					},
					TopologyAwareCapacityQuantityList: []*v12.TopologyAwareQuantity{
						{
							ResourceValue: 4,
							Node:          0,
							TopologyLevel: v12.TopologyLevel_NUMA,
						},
					},
				},
				{
					ResourceName:                  "memory",
					IsScalarResource:              true,
					AggregatedAllocatableQuantity: 8589934592,
					AggregatedCapacityQuantity:    8589934592,
					TopologyAwareAllocatableQuantityList: []*v12.TopologyAwareQuantity{
						{
							ResourceValue: 8589934592,
							Node:          0,
							TopologyLevel: v12.TopologyLevel_NUMA,
						},
					},
					TopologyAwareCapacityQuantityList: []*v12.TopologyAwareQuantity{
						{
							ResourceValue: 8589934592,
							Node:          0,
							TopologyLevel: v12.TopologyLevel_NUMA,
						},
					},
				},
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m.registerEndpoint("cpu", &pluginapi.ResourcePluginOptions{
				PreStartRequired:      true,
				WithTopologyAlignment: true,
				NeedReconcile:         true,
			}, &MockEndpoint{
				getTopologyAwareAllocatableResourcesFunc: func(c context.Context, request *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
					return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
						AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
							"cpu": {
								IsScalarResource:              true,
								AggregatedCapacityQuantity:    tc.cpu,
								AggregatedAllocatableQuantity: tc.cpu,
								TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
									{
										ResourceValue: tc.cpu,
										Node:          0,
										TopologyLevel: pluginapi.TopologyLevel_NUMA,
									},
								},
								TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
									{
										ResourceValue: tc.cpu,
										Node:          0,
										TopologyLevel: pluginapi.TopologyLevel_NUMA,
									},
								},
							},
						},
					}, nil
				},
			})

			m.registerEndpoint("memory", &pluginapi.ResourcePluginOptions{
				PreStartRequired:      true,
				WithTopologyAlignment: true,
				NeedReconcile:         true,
			}, &MockEndpoint{
				getTopologyAwareAllocatableResourcesFunc: func(c context.Context, request *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
					return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
						AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
							"memory": {
								IsScalarResource:              true,
								AggregatedCapacityQuantity:    tc.memory,
								AggregatedAllocatableQuantity: tc.memory,
								TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
									{
										ResourceValue: tc.memory,
										Node:          0,
										TopologyLevel: pluginapi.TopologyLevel_NUMA,
									},
								},
								TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
									{
										ResourceValue: tc.memory,
										Node:          0,
										TopologyLevel: pluginapi.TopologyLevel_NUMA,
									},
								},
							},
						},
					}, nil
				},
			})

			resp := m.GetTopologyAwareAllocatableResources()
			assert.True(t, equalAllocatableTopologyAwareResourceList(resp, tc.expectedResp))
		})
	}
}

func containerToCPUAllocatedResources(container *v1.Container) map[string]*pluginapi.TopologyAwareResource {
	res := map[string]*pluginapi.TopologyAwareResource{}

	if container == nil {
		return res
	}
	res["cpu"] = &pluginapi.TopologyAwareResource{
		IsScalarResource:           true,
		AggregatedQuantity:         float64(container.Resources.Requests.Cpu().Value()),
		OriginalAggregatedQuantity: float64(container.Resources.Requests.Cpu().Value()),
		TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
			{
				ResourceValue: float64(container.Resources.Requests.Cpu().Value()),
				Node:          0,
				TopologyLevel: pluginapi.TopologyLevel_NUMA,
			},
		},
		OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
			{
				ResourceValue: float64(container.Resources.Requests.Cpu().Value()),
				Node:          0,
				TopologyLevel: pluginapi.TopologyLevel_NUMA,
			},
		},
	}

	return res
}

func containerToMemoryAllocatedResources(container *v1.Container) map[string]*pluginapi.TopologyAwareResource {
	res := map[string]*pluginapi.TopologyAwareResource{}

	res["memory"] = &pluginapi.TopologyAwareResource{
		IsScalarResource:           true,
		AggregatedQuantity:         float64(container.Resources.Requests.Memory().Value()),
		OriginalAggregatedQuantity: float64(container.Resources.Requests.Memory().Value()),
		TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
			{
				ResourceValue: float64(container.Resources.Requests.Memory().Value()),
				Node:          0,
				TopologyLevel: pluginapi.TopologyLevel_NUMA,
			},
		},
		OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
			{
				ResourceValue: float64(container.Resources.Requests.Memory().Value()),
				Node:          0,
				TopologyLevel: pluginapi.TopologyLevel_NUMA,
			},
		},
	}

	return res
}

func equalTopologyAwareResourceList(a, b []*v12.TopologyAwareResource) bool {
	if len(a) != len(b) {
		return false
	}

	mapa := topologyAwareResourceListToMap(a)
	mapb := topologyAwareResourceListToMap(b)

	for resourceName, resourcea := range mapa {
		resourceb, ok := mapb[resourceName]
		if !ok {
			return false
		}

		if resourcea.IsScalarResource != resourceb.IsScalarResource ||
			resourcea.IsNodeResource != resourceb.IsNodeResource ||
			resourcea.AggregatedQuantity != resourceb.AggregatedQuantity ||
			resourcea.OriginalAggregatedQuantity != resourceb.OriginalAggregatedQuantity {
			return false
		}

		if !reflect.DeepEqual(resourcea.TopologyAwareQuantityList, resourceb.TopologyAwareQuantityList) {
			return false
		}
		if !reflect.DeepEqual(resourcea.OriginalTopologyAwareQuantityList, resourceb.OriginalTopologyAwareQuantityList) {
			return false
		}
	}

	return true
}

func equalAllocatableTopologyAwareResourceList(a, b []*v12.AllocatableTopologyAwareResource) bool {
	if len(a) != len(b) {
		return false
	}

	mapa := allocatableTopologyAwareResourceListToMap(a)
	mapb := allocatableTopologyAwareResourceListToMap(b)

	for resourceName, resourcea := range mapa {
		resourceb, ok := mapb[resourceName]
		if !ok {
			return false
		}

		if resourcea.IsScalarResource != resourceb.IsScalarResource ||
			resourcea.IsNodeResource != resourceb.IsNodeResource ||
			resourcea.AggregatedAllocatableQuantity != resourceb.AggregatedAllocatableQuantity ||
			resourcea.AggregatedCapacityQuantity != resourceb.AggregatedCapacityQuantity {
			return false
		}

		if !reflect.DeepEqual(resourcea.TopologyAwareAllocatableQuantityList, resourceb.TopologyAwareAllocatableQuantityList) {
			return false
		}
		if !reflect.DeepEqual(resourcea.TopologyAwareCapacityQuantityList, resourceb.TopologyAwareCapacityQuantityList) {
			return false
		}
	}

	return true
}

func topologyAwareResourceListToMap(datas []*v12.TopologyAwareResource) map[string]*v12.TopologyAwareResource {
	res := map[string]*v12.TopologyAwareResource{}
	for _, data := range datas {
		res[data.ResourceName] = data
	}
	return res
}

func allocatableTopologyAwareResourceListToMap(datas []*v12.AllocatableTopologyAwareResource) map[string]*v12.AllocatableTopologyAwareResource {
	res := map[string]*v12.AllocatableTopologyAwareResource{}
	for _, data := range datas {
		res[data.ResourceName] = data
	}
	return res
}
