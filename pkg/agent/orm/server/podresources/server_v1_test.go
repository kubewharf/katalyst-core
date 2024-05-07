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

package podresources

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"

	podresourcestest "github.com/kubewharf/katalyst-core/pkg/agent/orm/server/podresources/testing"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestList(t *testing.T) {
	t.Parallel()

	var (
		numaID        = int64(1)
		podName       = "pod-name"
		podNamespace  = "pod-namespace"
		podUID        = types.UID("pod-uid")
		containerName = "container-name"
	)

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	devs := []*podresourcesapi.PodResources{
		{
			Name:      podName,
			Namespace: podNamespace,
			Containers: []*podresourcesapi.ContainerResources{
				{
					Name: containerName,
					Devices: []*podresourcesapi.ContainerDevices{
						{
							ResourceName: "resource",
							DeviceIds:    []string{"dev0", "dev1"},
							Topology:     &podresourcesapi.TopologyInfo{Nodes: []*podresourcesapi.NUMANode{{ID: numaID}}},
						},
					},
				},
			},
		},
	}

	topologyAwareResources := []*podresourcesapi.TopologyAwareResource{
		{
			ResourceName:               "cpu",
			IsScalarResource:           true,
			AggregatedQuantity:         4,
			OriginalAggregatedQuantity: 4,
			TopologyAwareQuantityList: []*podresourcesapi.TopologyAwareQuantity{
				{
					ResourceValue: 4,
					Node:          0,
					TopologyLevel: podresourcesapi.TopologyLevel_NUMA,
				},
			},
			OriginalTopologyAwareQuantityList: []*podresourcesapi.TopologyAwareQuantity{},
		},
	}

	for _, tc := range []struct {
		name             string
		pods             []*v1.Pod
		devices          []*podresourcesapi.PodResources
		resources        []*podresourcesapi.TopologyAwareResource
		expectedResponse *podresourcesapi.ListPodResourcesResponse
	}{
		{
			name:             "no pods",
			pods:             []*v1.Pod{},
			devices:          []*podresourcesapi.PodResources{},
			resources:        []*podresourcesapi.TopologyAwareResource{},
			expectedResponse: &podresourcesapi.ListPodResourcesResponse{},
		},
		{
			name: "pod without resources",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: podNamespace,
						Name:      podName,
						UID:       podUID,
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: containerName,
							},
						},
					},
				},
			},
			devices:   []*podresourcesapi.PodResources{},
			resources: []*podresourcesapi.TopologyAwareResource{},
			expectedResponse: &podresourcesapi.ListPodResourcesResponse{
				PodResources: []*podresourcesapi.PodResources{
					{
						Name:      podName,
						Namespace: podNamespace,
						Containers: []*podresourcesapi.ContainerResources{
							{
								Name:      containerName,
								Devices:   []*podresourcesapi.ContainerDevices{},
								Resources: []*podresourcesapi.TopologyAwareResource{},
							},
						},
					},
				},
			},
		},
		{
			name: "pod with resources",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: podNamespace,
						Name:      podName,
						UID:       podUID,
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: containerName,
							},
						},
					},
				},
			},
			devices:   devs,
			resources: topologyAwareResources,
			expectedResponse: &podresourcesapi.ListPodResourcesResponse{
				PodResources: []*podresourcesapi.PodResources{
					{
						Name:      podName,
						Namespace: podNamespace,
						Containers: []*podresourcesapi.ContainerResources{
							{
								Name: containerName,
								Devices: []*podresourcesapi.ContainerDevices{
									{
										ResourceName: "resource",
										DeviceIds:    []string{"dev0", "dev1"},
										Topology:     &podresourcesapi.TopologyInfo{Nodes: []*podresourcesapi.NUMANode{{ID: numaID}}},
									},
								},
								Resources: topologyAwareResources,
							},
						},
					},
				},
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockDevicesProvider := podresourcestest.NewMockDevicesProvider(mockCtrl)
			mockResourcesProvider := podresourcestest.NewMockResourcesProvider(mockCtrl)
			mockPodsProvider := podresourcestest.NewMockPodsProvider(mockCtrl)

			mockPodsProvider.EXPECT().GetPods().Return(tc.pods).AnyTimes().AnyTimes()
			mockDevicesProvider.EXPECT().GetDevices().Return(tc.devices).AnyTimes()
			mockResourcesProvider.EXPECT().UpdateAllocatedResources().Return().AnyTimes()
			mockResourcesProvider.EXPECT().GetTopologyAwareResources(&v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: podNamespace,
					Name:      podName,
					UID:       podUID,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: containerName,
						},
					},
				},
			}, &v1.Container{
				Name: containerName,
			}).Return(tc.resources).AnyTimes()

			server := NewV1PodResourcesServer(mockPodsProvider, mockResourcesProvider, mockDevicesProvider, metrics.DummyMetrics{})
			resp, err := server.List(context.TODO(), &podresourcesapi.ListPodResourcesRequest{})
			assert.NoError(t, err)

			assert.True(t, equalListResponse(resp, tc.expectedResponse))
		})
	}
}

func TestGetAllocatableResources(t *testing.T) {
	t.Parallel()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	allDevs := []*podresourcesapi.ContainerDevices{
		{
			ResourceName: "resource",
			DeviceIds:    []string{"dev0"},
			Topology: &podresourcesapi.TopologyInfo{
				Nodes: []*podresourcesapi.NUMANode{
					{
						ID: 0,
					},
				},
			},
		},
		{
			ResourceName: "resource",
			DeviceIds:    []string{"dev1"},
			Topology: &podresourcesapi.TopologyInfo{
				Nodes: []*podresourcesapi.NUMANode{
					{
						ID: 1,
					},
				},
			},
		},
		{
			ResourceName: "resource-nt",
			DeviceIds:    []string{"devA"},
		},
		{
			ResourceName: "resource-mm",
			DeviceIds:    []string{"devM0"},
			Topology: &podresourcesapi.TopologyInfo{
				Nodes: []*podresourcesapi.NUMANode{
					{
						ID: 0,
					},
				},
			},
		},
		{
			ResourceName: "resource-mm",
			DeviceIds:    []string{"devMM"},
			Topology: &podresourcesapi.TopologyInfo{
				Nodes: []*podresourcesapi.NUMANode{
					{
						ID: 0,
					},
					{
						ID: 1,
					},
				},
			},
		},
	}

	allResources := []*podresourcesapi.AllocatableTopologyAwareResource{
		{
			ResourceName:                  "cpu",
			IsScalarResource:              true,
			AggregatedAllocatableQuantity: 8,
			AggregatedCapacityQuantity:    8,
			TopologyAwareAllocatableQuantityList: []*podresourcesapi.TopologyAwareQuantity{
				{
					ResourceValue: 8,
					Node:          0,
					TopologyLevel: podresourcesapi.TopologyLevel_NUMA,
				},
			},
			TopologyAwareCapacityQuantityList: []*podresourcesapi.TopologyAwareQuantity{
				{
					ResourceValue: 8,
					Node:          0,
					TopologyLevel: podresourcesapi.TopologyLevel_NUMA,
				},
			},
		},
		{
			ResourceName:                  "memory",
			IsScalarResource:              true,
			AggregatedAllocatableQuantity: 33395208192,
			AggregatedCapacityQuantity:    33395208192,
			TopologyAwareAllocatableQuantityList: []*podresourcesapi.TopologyAwareQuantity{
				{
					ResourceValue: 33395208192,
					Node:          0,
					TopologyLevel: podresourcesapi.TopologyLevel_NUMA,
				},
			},
			TopologyAwareCapacityQuantityList: []*podresourcesapi.TopologyAwareQuantity{
				{
					ResourceValue: 33395208192,
					Node:          0,
					TopologyLevel: podresourcesapi.TopologyLevel_NUMA,
				},
			},
		},
	}

	for _, tc := range []struct {
		name                                 string
		allDevices                           []*podresourcesapi.ContainerDevices
		allResources                         []*podresourcesapi.AllocatableTopologyAwareResource
		expectedAllocatableResourcesResponse *podresourcesapi.AllocatableResourcesResponse
	}{
		{
			name:                                 "no data",
			allDevices:                           []*podresourcesapi.ContainerDevices{},
			allResources:                         []*podresourcesapi.AllocatableTopologyAwareResource{},
			expectedAllocatableResourcesResponse: &podresourcesapi.AllocatableResourcesResponse{},
		},
		{
			name:         "no devices, all resources",
			allDevices:   []*podresourcesapi.ContainerDevices{},
			allResources: allResources,
			expectedAllocatableResourcesResponse: &podresourcesapi.AllocatableResourcesResponse{
				Resources: allResources,
			},
		},
		{
			name:         "no resources, all devices",
			allDevices:   allDevs,
			allResources: []*podresourcesapi.AllocatableTopologyAwareResource{},
			expectedAllocatableResourcesResponse: &podresourcesapi.AllocatableResourcesResponse{
				Devices: allDevs,
			},
		},
		{
			name:         "with devices and resources",
			allDevices:   allDevs,
			allResources: allResources,
			expectedAllocatableResourcesResponse: &podresourcesapi.AllocatableResourcesResponse{
				Devices:   allDevs,
				Resources: allResources,
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockPodsProvider := podresourcestest.NewMockPodsProvider(mockCtrl)
			mockDevicesProvider := podresourcestest.NewMockDevicesProvider(mockCtrl)
			mockResourcesProvider := podresourcestest.NewMockResourcesProvider(mockCtrl)

			mockDevicesProvider.EXPECT().GetAllocatableDevices().Return(tc.allDevices).AnyTimes()
			mockResourcesProvider.EXPECT().GetTopologyAwareAllocatableResources().Return(tc.allResources).AnyTimes()

			server := NewV1PodResourcesServer(mockPodsProvider, mockResourcesProvider, mockDevicesProvider, metrics.DummyMetrics{})

			resp, err := server.GetAllocatableResources(context.TODO(), &podresourcesapi.AllocatableResourcesRequest{})
			assert.NoError(t, err)

			assert.True(t, equalAllocatableResourcesResponse(tc.expectedAllocatableResourcesResponse, resp))
		})
	}
}

func equalListResponse(respA, respB *podresourcesapi.ListPodResourcesResponse) bool {
	if len(respA.PodResources) != len(respB.PodResources) {
		return false
	}
	for idx := 0; idx < len(respA.PodResources); idx++ {
		podResA := respA.PodResources[idx]
		podResB := respB.PodResources[idx]
		if podResA.Name != podResB.Name {
			return false
		}
		if podResA.Namespace != podResB.Namespace {
			return false
		}
		if len(podResA.Containers) != len(podResB.Containers) {
			return false
		}
		for jdx := 0; jdx < len(podResA.Containers); jdx++ {
			cntA := podResA.Containers[jdx]
			cntB := podResB.Containers[jdx]

			if cntA.Name != cntB.Name {
				return false
			}

			if !equalContainerDevices(cntA.Devices, cntB.Devices) {
				return false
			}

			if !equalContainerResources(cntA.Resources, cntB.Resources) {
				return false
			}
		}
	}
	return true
}

func equalContainerDevices(devA, devB []*podresourcesapi.ContainerDevices) bool {
	if len(devA) != len(devB) {
		return false
	}

	for idx := 0; idx < len(devA); idx++ {
		cntDevA := devA[idx]
		cntDevB := devB[idx]

		if cntDevA.ResourceName != cntDevB.ResourceName {
			return false
		}
		if !equalTopology(cntDevA.Topology, cntDevB.Topology) {
			return false
		}
		if !equalStrings(cntDevA.DeviceIds, cntDevB.DeviceIds) {
			return false
		}
	}

	return true
}

func equalContainerResources(resA, resB []*podresourcesapi.TopologyAwareResource) bool {
	if len(resA) != len(resB) {
		return false
	}

	for idx := 0; idx < len(resA); idx++ {
		cntDevA := resA[idx]
		cntDevB := resB[idx]

		if cntDevA.ResourceName != cntDevB.ResourceName ||
			cntDevA.IsNodeResource != cntDevB.IsNodeResource ||
			cntDevA.IsScalarResource != cntDevB.IsScalarResource ||
			cntDevA.AggregatedQuantity != cntDevB.AggregatedQuantity ||
			cntDevA.OriginalAggregatedQuantity != cntDevB.OriginalAggregatedQuantity {
			return false
		}

		if !equalTopologyAwareQuantityList(cntDevA.TopologyAwareQuantityList, cntDevB.TopologyAwareQuantityList) ||
			!equalTopologyAwareQuantityList(cntDevA.OriginalTopologyAwareQuantityList, cntDevB.OriginalTopologyAwareQuantityList) {
			return false
		}
	}

	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aCopy := append([]string{}, a...)
	sort.Strings(aCopy)
	bCopy := append([]string{}, b...)
	sort.Strings(bCopy)
	return reflect.DeepEqual(aCopy, bCopy)
}

func equalTopology(a, b *podresourcesapi.TopologyInfo) bool {
	if a == nil && b != nil {
		return false
	}
	if a != nil && b == nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func equalTopologyAwareQuantityList(a, b []*podresourcesapi.TopologyAwareQuantity) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		cntA := a[i]
		cntB := b[i]

		if cntA.ResourceValue != cntB.ResourceValue ||
			cntA.Node != cntB.Node ||
			cntA.Name != cntB.Name ||
			cntA.Type != cntB.Type {
			return false
		}
	}

	return true
}

func equalAllocatableResourcesResponse(respA, respB *podresourcesapi.AllocatableResourcesResponse) bool {
	if !equalContainerDevices(respA.Devices, respB.Devices) {
		return false
	}

	if !equalAllocatableTopologyAwareResource(respA.Resources, respB.Resources) {
		return false
	}
	return true
}

func equalAllocatableTopologyAwareResource(resA, resB []*podresourcesapi.AllocatableTopologyAwareResource) bool {
	if len(resA) != len(resB) {
		return false
	}

	for i := 0; i < len(resA); i++ {
		a := resA[i]
		b := resB[i]

		if a.ResourceName != b.ResourceName ||
			a.IsNodeResource != b.IsNodeResource ||
			a.IsScalarResource != b.IsScalarResource ||
			a.AggregatedAllocatableQuantity != b.AggregatedAllocatableQuantity ||
			a.AggregatedCapacityQuantity != b.AggregatedCapacityQuantity {
			return false
		}

		if !equalTopologyAwareQuantityList(a.TopologyAwareAllocatableQuantityList, b.TopologyAwareAllocatableQuantityList) ||
			!equalTopologyAwareQuantityList(a.TopologyAwareCapacityQuantityList, b.TopologyAwareCapacityQuantityList) {
			return false
		}
	}

	return true
}
