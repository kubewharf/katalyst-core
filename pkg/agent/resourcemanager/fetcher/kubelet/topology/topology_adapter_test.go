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

package topology

import (
	"context"
	"net"
	"os"
	"path"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	podresv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	"k8s.io/kubernetes/pkg/kubelet/apis/config"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	testutil "k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state/testing"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/kubeletconfig"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/kubelet/podresources"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type fakePodResourcesServer struct {
	podResources         *podresv1.ListPodResourcesResponse
	allocatableResources *podresv1.AllocatableResourcesResponse
	podresv1.UnimplementedPodResourcesListerServer
}

func (m *fakePodResourcesServer) List(_ context.Context, _ *podresv1.ListPodResourcesRequest) (*podresv1.ListPodResourcesResponse, error) {
	return m.podResources, nil
}

func (m *fakePodResourcesServer) GetAllocatableResources(_ context.Context, _ *podresv1.AllocatableResourcesRequest) (*podresv1.AllocatableResourcesResponse, error) {
	return m.allocatableResources, nil
}

func newFakePodResourcesServer(podResources *podresv1.ListPodResourcesResponse, allocatableResources *podresv1.AllocatableResourcesResponse) *grpc.Server {
	server := grpc.NewServer()
	podresv1.RegisterPodResourcesListerServer(server, &fakePodResourcesServer{
		podResources:         podResources,
		allocatableResources: allocatableResources,
	})
	return server
}

type fakePodResourcesListerClient struct {
	*podresv1.ListPodResourcesResponse
	*podresv1.AllocatableResourcesResponse
}

func (f *fakePodResourcesListerClient) List(_ context.Context, _ *podresv1.ListPodResourcesRequest, _ ...grpc.CallOption) (*podresv1.ListPodResourcesResponse, error) {
	return f.ListPodResourcesResponse, nil
}

func (f *fakePodResourcesListerClient) GetAllocatableResources(_ context.Context, _ *podresv1.AllocatableResourcesRequest, _ ...grpc.CallOption) (*podresv1.AllocatableResourcesResponse, error) {
	return f.AllocatableResourcesResponse, nil
}

func generateTestPod(namespace, name, uid string, qosLevel string, isBindNumaQoS bool,
	resourceRequirements map[string]v1.ResourceRequirements,
) *v1.Pod {
	p := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: qosLevel,
			},
		},
	}

	if isBindNumaQoS {
		p.Annotations = map[string]string{
			consts.PodAnnotationQoSLevelKey:          qosLevel,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
		}
	}

	for containerName, resourceRequirements := range resourceRequirements {
		p.Spec.Containers = append(p.Spec.Containers, v1.Container{
			Name:      containerName,
			Resources: resourceRequirements,
		})
	}
	return p
}

func generateTestDedicatedCoresPod(namespace, name, uid string, isBindNumaQoS bool) *v1.Pod {
	return generateTestPod(namespace, name, uid, consts.PodAnnotationQoSLevelDedicatedCores, isBindNumaQoS, nil)
}

func generateTestSharedCoresPod(namespace, name, uid string, isBindNumaQoS bool, resourceRequirements map[string]v1.ResourceRequirements) *v1.Pod {
	return generateTestPod(namespace, name, uid, consts.PodAnnotationQoSLevelSharedCores, isBindNumaQoS, resourceRequirements)
}

func generateFloat64ResourceValue(value string) float64 {
	resourceValue := resource.MustParse(value)
	return float64(resourceValue.Value())
}

func tmpSocketDir() (socketDir string, err error) {
	socketDir, err = os.MkdirTemp("", "pod_resources")
	if err != nil {
		return
	}
	err = os.MkdirAll(socketDir, 0o755)
	if err != nil {
		return "", err
	}
	return
}

func generateTestMetaServer(podList ...*v1.Pod) *metaserver.MetaServer {
	m := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher:          &pod.PodFetcherStub{PodList: podList},
			KatalystMachineInfo: &machine.KatalystMachineInfo{},
		},
	}

	m.ExtraTopologyInfo, _ = machine.GenerateDummyExtraTopology(2)
	return m
}

func withServiceProfilingManager(server *metaserver.MetaServer, manager spd.ServiceProfilingManager) *metaserver.MetaServer {
	if server == nil {
		server = &metaserver.MetaServer{}
	}

	if manager != nil {
		server.ServiceProfilingManager = manager
	}
	return server
}

func withExtraTopologyInfo(server *metaserver.MetaServer, topologyInfo *machine.ExtraTopologyInfo) *metaserver.MetaServer {
	if server == nil {
		server = &metaserver.MetaServer{}
	}
	if topologyInfo != nil {
		server.ExtraTopologyInfo = topologyInfo
	}
	return server
}

func Test_getZoneAllocationsByPodResources(t *testing.T) {
	t.Parallel()

	type args struct {
		podList               []*v1.Pod
		numaSocketZoneNodeMap map[util.ZoneNode]util.ZoneNode
		podResourcesList      []*podresv1.PodResources
		manager               spd.ServiceProfilingManager
		extraTopologyInfo     *machine.ExtraTopologyInfo
	}
	tests := []struct {
		name    string
		args    args
		want    map[util.ZoneNode]util.ZoneAllocations
		wantErr bool
	}{
		{
			name: "test-1",
			args: args{
				podList: []*v1.Pod{
					generateTestPod("default", "pod-1", "pod-1-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
					generateTestPod("default", "pod-2", "pod-2-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
					generateTestPod("default", "pod-3", "pod-3-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
				podResourcesList: []*podresv1.PodResources{
					{
						Namespace: "default",
						Name:      "pod-1",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 12,
												Node:          0,
											},
											{
												ResourceValue: 15,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("12G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("15G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
					{
						Namespace: "default",
						Name:      "pod-2",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
									{
										ResourceName: "disk",
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
											{
												ResourceValue: 24,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
					{
						Namespace: "default",
						Name:      "pod-3",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
									{
										ResourceName: "disk",
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
											{
												ResourceValue: 24,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
				},
				manager: spd.NewDummyServiceProfilingManager(
					map[types.UID]spd.DummyPodServiceProfile{},
				),
			},
			want: map[util.ZoneNode]util.ZoneAllocations{
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "0",
					},
				}: {
					{
						Consumer: "default/pod-1/pod-1-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("12"),
							"memory": resource.MustParse("12G"),
						},
					},
					{
						Consumer: "default/pod-2/pod-2-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
					{
						Consumer: "default/pod-3/pod-3-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "1",
					},
				}: {
					{
						Consumer: "default/pod-1/pod-1-uid",
						Requests: &v1.ResourceList{
							"cpu":    resource.MustParse("15"),
							"memory": resource.MustParse("15G"),
						},
					},
					{
						Consumer: "default/pod-2/pod-2-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
					{
						Consumer: "default/pod-3/pod-3-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
				},
			},
		},
		{
			name: "test for shared core with numa binding",
			args: args{
				podList: []*v1.Pod{
					generateTestSharedCoresPod("default", "pod-1", "pod-1-uid", true,
						map[string]v1.ResourceRequirements{
							"container-1": {
								Requests: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("10"),
									v1.ResourceMemory: resource.MustParse("10G"),
									"gpu":             resource.MustParse("2"),
								},
							},
						}),
					generateTestPod("default", "pod-2", "pod-2-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
					generateTestSharedCoresPod("default", "pod-3", "pod-3-uid", false,
						map[string]v1.ResourceRequirements{
							"container-1": {
								Requests: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("10"),
									v1.ResourceMemory: resource.MustParse("10G"),
									"gpu":             resource.MustParse("2"),
								},
							},
						}),
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
				podResourcesList: []*podresv1.PodResources{
					{
						Namespace: "default",
						Name:      "pod-1",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("10G"),
												Node:          0,
											},
										},
									},
								},
							},
						},
					},
					{
						Namespace: "default",
						Name:      "pod-2",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
									{
										ResourceName: "disk",
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
											{
												ResourceValue: 24,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
					{
						Namespace: "default",
						Name:      "pod-3",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
									{
										ResourceName: "disk",
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
											{
												ResourceValue: 24,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
				},
				manager: spd.NewDummyServiceProfilingManager(
					map[types.UID]spd.DummyPodServiceProfile{},
				),
			},
			want: map[util.ZoneNode]util.ZoneAllocations{
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "0",
					},
				}: {
					{
						Consumer: "default/pod-1/pod-1-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("10"),
							"memory": resource.MustParse("10G"),
						},
					},
					{
						Consumer: "default/pod-2/pod-2-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
					{
						Consumer: "default/pod-3/pod-3-uid",
						Requests: &v1.ResourceList{
							"gpu": resource.MustParse("1"),
						},
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "1",
					},
				}: {
					{
						Consumer: "default/pod-1/pod-1-uid",
						Requests: &v1.ResourceList{
							"gpu": resource.MustParse("1"),
						},
					},
					{
						Consumer: "default/pod-2/pod-2-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
					{
						Consumer: "default/pod-3/pod-3-uid",
						Requests: &v1.ResourceList{
							"gpu": resource.MustParse("1"),
						},
					},
				},
			},
		},
		{
			name: "test for numa memory bandwidth",
			args: args{
				podList: []*v1.Pod{
					generateTestPod("default", "pod-1", "pod-1-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("10"),
							},
						},
					}),
					generateTestPod("default", "pod-2", "pod-2-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("10"),
							},
						},
					}),
					generateTestPod("default", "pod-3", "pod-3-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("10"),
							},
						},
					}),
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
				podResourcesList: []*podresv1.PodResources{
					{
						Namespace: "default",
						Name:      "pod-1",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 12,
												Node:          0,
											},
											{
												ResourceValue: 15,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("12G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("15G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
					{
						Namespace: "default",
						Name:      "pod-2",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
									{
										ResourceName: "disk",
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
											{
												ResourceValue: 24,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
					{
						Namespace: "default",
						Name:      "pod-3",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
									{
										ResourceName: "disk",
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
											{
												ResourceValue: 24,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
				},
				manager: spd.NewDummyServiceProfilingManager(
					map[types.UID]spd.DummyPodServiceProfile{
						"pod-1-uid": {
							AggregatedMetric: []resource.Quantity{
								*resource.NewQuantity(10, resource.BinarySI),
							},
						},
						"pod-2-uid": {
							AggregatedMetric: []resource.Quantity{
								*resource.NewQuantity(10, resource.BinarySI),
							},
						},
						"pod-3-uid": {
							AggregatedMetric: []resource.Quantity{
								*resource.NewQuantity(10, resource.BinarySI),
							},
						},
					},
				),
				extraTopologyInfo: &machine.ExtraTopologyInfo{
					SiblingNumaInfo: &machine.SiblingNumaInfo{
						SiblingNumaAvgMBWAllocatableMap: map[int]int64{
							0: 8,
							1: 8,
						},
						SiblingNumaAvgMBWCapacityMap: map[int]int64{
							0: 10,
							1: 10,
						},
					},
				},
			},
			want: map[util.ZoneNode]util.ZoneAllocations{
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "0",
					},
				}: {
					{
						Consumer: "default/pod-1/pod-1-uid",
						Requests: &v1.ResourceList{
							"gpu":                          resource.MustParse("1"),
							"cpu":                          resource.MustParse("12"),
							"memory":                       resource.MustParse("12G"),
							consts.ResourceMemoryBandwidth: resource.MustParse("50"),
						},
					},
					{
						Consumer: "default/pod-2/pod-2-uid",
						Requests: &v1.ResourceList{
							"gpu":                          resource.MustParse("1"),
							"cpu":                          resource.MustParse("24"),
							"memory":                       resource.MustParse("32G"),
							consts.ResourceMemoryBandwidth: resource.MustParse("50"),
						},
					},
					{
						Consumer: "default/pod-3/pod-3-uid",
						Requests: &v1.ResourceList{
							"gpu":                          resource.MustParse("1"),
							"cpu":                          resource.MustParse("24"),
							"memory":                       resource.MustParse("32G"),
							consts.ResourceMemoryBandwidth: resource.MustParse("50"),
						},
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "1",
					},
				}: {
					{
						Consumer: "default/pod-1/pod-1-uid",
						Requests: &v1.ResourceList{
							"cpu":                          resource.MustParse("15"),
							"memory":                       resource.MustParse("15G"),
							consts.ResourceMemoryBandwidth: resource.MustParse("50"),
						},
					},
					{
						Consumer: "default/pod-2/pod-2-uid",
						Requests: &v1.ResourceList{
							"gpu":                          resource.MustParse("1"),
							"cpu":                          resource.MustParse("24"),
							"memory":                       resource.MustParse("32G"),
							consts.ResourceMemoryBandwidth: resource.MustParse("50"),
						},
					},
					{
						Consumer: "default/pod-3/pod-3-uid",
						Requests: &v1.ResourceList{
							"gpu":                          resource.MustParse("1"),
							"cpu":                          resource.MustParse("24"),
							"memory":                       resource.MustParse("32G"),
							consts.ResourceMemoryBandwidth: resource.MustParse("50"),
						},
					},
				},
			},
		},
		{
			name: "test for numa memory bandwidth without capacity",
			args: args{
				podList: []*v1.Pod{
					generateTestPod("default", "pod-1", "pod-1-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("10"),
							},
						},
					}),
					generateTestPod("default", "pod-2", "pod-2-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("10"),
							},
						},
					}),
					generateTestPod("default", "pod-3", "pod-3-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("10"),
							},
						},
					}),
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
				podResourcesList: []*podresv1.PodResources{
					{
						Namespace: "default",
						Name:      "pod-1",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 12,
												Node:          0,
											},
											{
												ResourceValue: 15,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("12G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("15G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
					{
						Namespace: "default",
						Name:      "pod-2",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
									{
										ResourceName: "disk",
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
											{
												ResourceValue: 24,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
					{
						Namespace: "default",
						Name:      "pod-3",
						Containers: []*podresv1.ContainerResources{
							{
								Name: "container-1",
								Devices: []*podresv1.ContainerDevices{
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 0},
											},
										},
									},
									{
										ResourceName: "gpu",
										Topology: &podresv1.TopologyInfo{
											Nodes: []*podresv1.NUMANode{
												{ID: 1},
											},
										},
									},
									{
										ResourceName: "disk",
									},
								},
								Resources: []*podresv1.TopologyAwareResource{
									{
										ResourceName: "cpu",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: 24,
												Node:          0,
											},
											{
												ResourceValue: 24,
												Node:          1,
											},
										},
									},
									{
										ResourceName: "memory",
										OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          0,
											},
											{
												ResourceValue: generateFloat64ResourceValue("32G"),
												Node:          1,
											},
										},
									},
								},
							},
						},
					},
				},
				manager: spd.NewDummyServiceProfilingManager(
					map[types.UID]spd.DummyPodServiceProfile{
						"pod-1-uid": {
							AggregatedMetric: []resource.Quantity{
								*resource.NewQuantity(10, resource.BinarySI),
							},
						},
						"pod-2-uid": {
							AggregatedMetric: []resource.Quantity{
								*resource.NewQuantity(10, resource.BinarySI),
							},
						},
						"pod-3-uid": {
							AggregatedMetric: []resource.Quantity{
								*resource.NewQuantity(10, resource.BinarySI),
							},
						},
					},
				),
			},
			want: map[util.ZoneNode]util.ZoneAllocations{
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "0",
					},
				}: {
					{
						Consumer: "default/pod-1/pod-1-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("12"),
							"memory": resource.MustParse("12G"),
						},
					},
					{
						Consumer: "default/pod-2/pod-2-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
					{
						Consumer: "default/pod-3/pod-3-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "1",
					},
				}: {
					{
						Consumer: "default/pod-1/pod-1-uid",
						Requests: &v1.ResourceList{
							"cpu":    resource.MustParse("15"),
							"memory": resource.MustParse("15G"),
						},
					},
					{
						Consumer: "default/pod-2/pod-2-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
					{
						Consumer: "default/pod-3/pod-3-uid",
						Requests: &v1.ResourceList{
							"gpu":    resource.MustParse("1"),
							"cpu":    resource.MustParse("24"),
							"memory": resource.MustParse("32G"),
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			qosConf := generic.NewQoSConfiguration()
			p := &topologyAdapterImpl{
				numaSocketZoneNodeMap: tt.args.numaSocketZoneNodeMap,
				qosConf:               qosConf,
				podResourcesFilter:    GenericPodResourcesFilter(qosConf),
				metaServer: withExtraTopologyInfo(withServiceProfilingManager(generateTestMetaServer(tt.args.podList...),
					tt.args.manager), tt.args.extraTopologyInfo),
			}
			got, err := p.getZoneAllocations(tt.args.podList, tt.args.podResourcesList)
			if (err != nil) != tt.wantErr {
				t.Errorf("getZoneAllocations() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("getZoneAllocations() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getZoneResourcesByAllocatableResources(t *testing.T) {
	t.Parallel()

	type args struct {
		allocatableResources  *podresv1.AllocatableResourcesResponse
		numaSocketZoneNodeMap map[util.ZoneNode]util.ZoneNode
		metaServer            *metaserver.MetaServer
		cacheGroupCPUsMap     map[int]sets.Int
	}
	tests := []struct {
		name              string
		args              args
		wantZoneResources map[util.ZoneNode]nodev1alpha1.Resources
		wantErr           bool
	}{
		{
			name: "test-1",
			args: args{
				cacheGroupCPUsMap: map[int]sets.Int{
					0: sets.NewInt(0, 2, 4, 6),
					1: sets.NewInt(1, 3, 5, 7),
				},
				allocatableResources: &podresv1.AllocatableResourcesResponse{
					Devices: []*podresv1.ContainerDevices{
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"0",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"1",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
					},
					Resources: []*podresv1.AllocatableTopologyAwareResource{
						{
							ResourceName: "cpu",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
						},
						{
							ResourceName: "memory",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
						},
						{
							ResourceName: "nic",
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          0,
									Type:          "NIC",
									Name:          "eth0",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          1,
									Type:          "NIC",
									Name:          "eth1",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
							},
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          0,
									Type:          "NIC",
									Name:          "eth0",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          1,
									Type:          "NIC",
									Name:          "eth1",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
							},
						},
					},
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
				metaServer: generateTestMetaServer(),
			},
			wantZoneResources: map[util.ZoneNode]nodev1alpha1.Resources{
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "0",
					},
				}: {
					Capacity: &v1.ResourceList{
						"gpu":    resource.MustParse("2"),
						"cpu":    resource.MustParse("24"),
						"memory": resource.MustParse("32G"),
					},
					Allocatable: &v1.ResourceList{
						"gpu":    resource.MustParse("2"),
						"cpu":    resource.MustParse("24"),
						"memory": resource.MustParse("32G"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "1",
					},
				}: {
					Capacity: &v1.ResourceList{
						"cpu":    resource.MustParse("24"),
						"memory": resource.MustParse("32G"),
					},
					Allocatable: &v1.ResourceList{
						"cpu":    resource.MustParse("24"),
						"memory": resource.MustParse("32G"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNIC,
						Name: "eth0",
					},
				}: {
					Capacity: &v1.ResourceList{
						"nic": resource.MustParse("10G"),
					},
					Allocatable: &v1.ResourceList{
						"nic": resource.MustParse("10G"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNIC,
						Name: "eth1",
					},
				}: {
					Capacity: &v1.ResourceList{
						"nic": resource.MustParse("10G"),
					},
					Allocatable: &v1.ResourceList{
						"nic": resource.MustParse("10G"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeCacheGroup,
						Name: "0",
					},
				}: {
					Capacity: &v1.ResourceList{
						"cpu": resource.MustParse("4"),
					},
					Allocatable: &v1.ResourceList{
						"cpu": resource.MustParse("4"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeCacheGroup,
						Name: "1",
					},
				}: {
					Capacity: &v1.ResourceList{
						"cpu": resource.MustParse("4"),
					},
					Allocatable: &v1.ResourceList{
						"cpu": resource.MustParse("4"),
					},
				},
			},
		},
		{
			name: "test-2",
			args: args{
				allocatableResources: &podresv1.AllocatableResourcesResponse{
					Devices: []*podresv1.ContainerDevices{
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"0",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"1",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
					},
					Resources: []*podresv1.AllocatableTopologyAwareResource{
						{
							ResourceName: "cpu",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 22,
									Node:          0,
								},
								{
									ResourceValue: 22,
									Node:          1,
								},
							},
						},
						{
							ResourceName: "memory",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("30G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("30G"),
									Node:          1,
								},
							},
						},
					},
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
				metaServer: generateTestMetaServer(),
			},
			wantZoneResources: map[util.ZoneNode]nodev1alpha1.Resources{
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "0",
					},
				}: {
					Capacity: &v1.ResourceList{
						"gpu":    resource.MustParse("2"),
						"cpu":    resource.MustParse("24"),
						"memory": resource.MustParse("32G"),
					},
					Allocatable: &v1.ResourceList{
						"gpu":    resource.MustParse("2"),
						"cpu":    resource.MustParse("22"),
						"memory": resource.MustParse("30G"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "1",
					},
				}: {
					Capacity: &v1.ResourceList{
						"cpu":    resource.MustParse("24"),
						"memory": resource.MustParse("32G"),
					},
					Allocatable: &v1.ResourceList{
						"cpu":    resource.MustParse("22"),
						"memory": resource.MustParse("30G"),
					},
				},
			},
		},
		{
			name: "test for numa memory bandwidth",
			args: args{
				allocatableResources: &podresv1.AllocatableResourcesResponse{
					Devices: []*podresv1.ContainerDevices{
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"0",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"1",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
					},
					Resources: []*podresv1.AllocatableTopologyAwareResource{
						{
							ResourceName: "cpu",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
								{
									ResourceValue: 24,
									Node:          2,
								},
								{
									ResourceValue: 24,
									Node:          3,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 22,
									Node:          0,
								},
								{
									ResourceValue: 22,
									Node:          1,
								},
								{
									ResourceValue: 22,
									Node:          2,
								},
								{
									ResourceValue: 22,
									Node:          3,
								},
							},
						},
						{
							ResourceName: "memory",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          2,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          3,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("30G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("30G"),
									Node:          1,
								},
								{
									ResourceValue: generateFloat64ResourceValue("30G"),
									Node:          2,
								},
								{
									ResourceValue: generateFloat64ResourceValue("30G"),
									Node:          3,
								},
							},
						},
					},
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(2): util.GenerateSocketZoneNode(1),
					util.GenerateNumaZoneNode(3): util.GenerateSocketZoneNode(1),
				},
				metaServer: func() *metaserver.MetaServer {
					m := generateTestMetaServer()
					m.ExtraTopologyInfo, _ = machine.GenerateDummyExtraTopology(4)
					m.SiblingNumaMap = map[int]sets.Int{
						0: sets.NewInt(1),
						1: sets.NewInt(0),
						2: sets.NewInt(3),
						3: sets.NewInt(2),
					}
					m.SiblingNumaAvgMBWAllocatableMap = map[int]int64{
						0: 8,
						1: 8,
						2: 8,
						3: 8,
					}
					m.SiblingNumaAvgMBWCapacityMap = map[int]int64{
						0: 10,
						1: 10,
						2: 10,
						3: 10,
					}
					return m
				}(),
			},
			wantZoneResources: map[util.ZoneNode]nodev1alpha1.Resources{
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "0",
					},
				}: {
					Capacity: &v1.ResourceList{
						"gpu":                          resource.MustParse("2"),
						"cpu":                          resource.MustParse("24"),
						"memory":                       resource.MustParse("32G"),
						consts.ResourceMemoryBandwidth: resource.MustParse("10"),
					},
					Allocatable: &v1.ResourceList{
						"gpu":                          resource.MustParse("2"),
						"cpu":                          resource.MustParse("22"),
						"memory":                       resource.MustParse("30G"),
						consts.ResourceMemoryBandwidth: resource.MustParse("8"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "1",
					},
				}: {
					Capacity: &v1.ResourceList{
						"cpu":                          resource.MustParse("24"),
						"memory":                       resource.MustParse("32G"),
						consts.ResourceMemoryBandwidth: resource.MustParse("10"),
					},
					Allocatable: &v1.ResourceList{
						"cpu":                          resource.MustParse("22"),
						"memory":                       resource.MustParse("30G"),
						consts.ResourceMemoryBandwidth: resource.MustParse("8"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "2",
					},
				}: {
					Capacity: &v1.ResourceList{
						"cpu":                          resource.MustParse("24"),
						"memory":                       resource.MustParse("32G"),
						consts.ResourceMemoryBandwidth: resource.MustParse("10"),
					},
					Allocatable: &v1.ResourceList{
						"cpu":                          resource.MustParse("22"),
						"memory":                       resource.MustParse("30G"),
						consts.ResourceMemoryBandwidth: resource.MustParse("8"),
					},
				},
				{
					Meta: util.ZoneMeta{
						Type: nodev1alpha1.TopologyTypeNuma,
						Name: "3",
					},
				}: {
					Capacity: &v1.ResourceList{
						"cpu":                          resource.MustParse("24"),
						"memory":                       resource.MustParse("32G"),
						consts.ResourceMemoryBandwidth: resource.MustParse("10"),
					},
					Allocatable: &v1.ResourceList{
						"cpu":                          resource.MustParse("22"),
						"memory":                       resource.MustParse("30G"),
						consts.ResourceMemoryBandwidth: resource.MustParse("8"),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &topologyAdapterImpl{
				metaServer:            tt.args.metaServer,
				numaSocketZoneNodeMap: tt.args.numaSocketZoneNodeMap,
				cacheGroupCPUsMap:     tt.args.cacheGroupCPUsMap,
			}
			zoneResourcesMap, err := p.getZoneResources(tt.args.allocatableResources)
			if (err != nil) != tt.wantErr {
				t.Errorf("getZoneResources() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(zoneResourcesMap, tt.wantZoneResources) {
				t.Errorf("getZoneResources() got zoneResources = %+v, wantZoneResources = %+v",
					zoneResourcesMap, tt.wantZoneResources)
			}
		})
	}
}

func Test_podResourcesServerTopologyAdapterImpl_GetTopologyZones_ReportRDMATopology(t *testing.T) {
	t.Parallel()

	type fields struct {
		podList               []*v1.Pod
		listPodResources      *podresv1.ListPodResourcesResponse
		allocatableResources  *podresv1.AllocatableResourcesResponse
		numaSocketZoneNodeMap map[util.ZoneNode]util.ZoneNode
	}
	tests := []struct {
		name    string
		fields  fields
		want    []*nodev1alpha1.TopologyZone
		wantErr bool
	}{
		{
			name: "test normal",
			fields: fields{
				podList: []*v1.Pod{
					generateTestPod("default", "pod-1", "pod-1-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
					generateTestPod("default", "pod-2", "pod-2-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
					generateTestPod("default", "pod-3", "pod-3-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
				},
				listPodResources: &podresv1.ListPodResourcesResponse{
					PodResources: []*podresv1.PodResources{
						{
							Namespace: "default",
							Name:      "pod-2",
							Containers: []*podresv1.ContainerResources{
								{
									Name: "container-1",
									Devices: []*podresv1.ContainerDevices{
										{
											ResourceName: "resource.katalyst.kubewharf.io/rdma",
											DeviceIds: []string{
												"eth0",
											},
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{
														ID: 0,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
				allocatableResources: &podresv1.AllocatableResourcesResponse{
					Devices: []*podresv1.ContainerDevices{
						{
							ResourceName: "resource.katalyst.kubewharf.io/rdma",
							DeviceIds: []string{
								"eth0",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
						{
							ResourceName: "resource.katalyst.kubewharf.io/rdma",
							DeviceIds: []string{
								"eth1",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 1},
								},
							},
						},
					},
					Resources: []*podresv1.AllocatableTopologyAwareResource{
						{
							ResourceName: "cpu",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
						},
						{
							ResourceName: "memory",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
						},
					},
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
			},
			want: []*nodev1alpha1.TopologyZone{
				{
					Type: nodev1alpha1.TopologyTypeSocket,
					Name: "0",
					Children: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "0",
							Resources: nodev1alpha1.Resources{
								Capacity: &v1.ResourceList{
									"cpu":                                 resource.MustParse("24"),
									"memory":                              resource.MustParse("32G"),
									"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
								},
								Allocatable: &v1.ResourceList{
									"cpu":                                 resource.MustParse("24"),
									"memory":                              resource.MustParse("32G"),
									"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
								},
							},
							Allocations: []*nodev1alpha1.Allocation{
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
									},
								},
							},
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeNIC,
									Name: "eth0",
									Resources: nodev1alpha1.Resources{
										Capacity: &v1.ResourceList{
											"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
										},
										Allocatable: &v1.ResourceList{
											"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
										},
									},
									Allocations: []*nodev1alpha1.Allocation{
										{
											Consumer: "default/pod-2/pod-2-uid",
											Requests: &v1.ResourceList{
												"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Type: nodev1alpha1.TopologyTypeSocket,
					Name: "1",
					Children: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "1",
							Resources: nodev1alpha1.Resources{
								Capacity: &v1.ResourceList{
									"cpu":                                 resource.MustParse("24"),
									"memory":                              resource.MustParse("32G"),
									"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
								},
								Allocatable: &v1.ResourceList{
									"cpu":                                 resource.MustParse("24"),
									"memory":                              resource.MustParse("32G"),
									"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
								},
							},
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeNIC,
									Name: "eth1",
									Resources: nodev1alpha1.Resources{
										Capacity: &v1.ResourceList{
											"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
										},
										Allocatable: &v1.ResourceList{
											"resource.katalyst.kubewharf.io/rdma": resource.MustParse("1"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := topologyAdapterImpl{
				client: &fakePodResourcesListerClient{
					ListPodResourcesResponse:     tt.fields.listPodResources,
					AllocatableResourcesResponse: tt.fields.allocatableResources,
				},
				metaServer:            generateTestMetaServer(tt.fields.podList...),
				qosConf:               generic.NewQoSConfiguration(),
				numaSocketZoneNodeMap: tt.fields.numaSocketZoneNodeMap,
				resourceNameToZoneTypeMap: map[string]string{
					"resource.katalyst.kubewharf.io/rdma": "NIC",
				},
			}
			got, err := p.GetTopologyZones(context.TODO())
			if (err != nil) != tt.wantErr {
				t.Errorf("GetTopologyZones() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Equal(t, true, apiequality.Semantic.DeepEqual(tt.want, got))
		})
	}
}

func Test_podResourcesServerTopologyAdapterImpl_GetTopologyZones(t *testing.T) {
	t.Parallel()

	type fields struct {
		podList                   []*v1.Pod
		listPodResources          *podresv1.ListPodResourcesResponse
		allocatableResources      *podresv1.AllocatableResourcesResponse
		numaSocketZoneNodeMap     map[util.ZoneNode]util.ZoneNode
		numaCacheGroupZoneNodeMap map[util.ZoneNode][]util.ZoneNode
		numaDistanceMap           map[int][]machine.NumaDistanceInfo
		cacheGroupCPUsMap         map[int]sets.Int
	}
	tests := []struct {
		name    string
		fields  fields
		want    []*nodev1alpha1.TopologyZone
		wantErr bool
	}{
		{
			name: "test normal",
			fields: fields{
				podList: []*v1.Pod{
					generateTestPod("default", "pod-1", "pod-1-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
					generateTestPod("default", "pod-2", "pod-2-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
					generateTestPod("default", "pod-3", "pod-3-uid", consts.PodAnnotationQoSLevelDedicatedCores, true, map[string]v1.ResourceRequirements{
						"container-1": {},
					}),
				},
				listPodResources: &podresv1.ListPodResourcesResponse{
					PodResources: []*podresv1.PodResources{
						{
							Namespace: "default",
							Name:      "pod-1",
							Containers: []*podresv1.ContainerResources{
								{
									Name: "container-1",
									Devices: []*podresv1.ContainerDevices{
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 0},
												},
											},
										},
									},
									Resources: []*podresv1.TopologyAwareResource{
										{
											ResourceName: "cpu",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: 12,
													Node:          0,
												},
												{
													ResourceValue: 15,
													Node:          1,
												},
											},
										},
										{
											ResourceName: "memory",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: generateFloat64ResourceValue("12G"),
													Node:          0,
												},
												{
													ResourceValue: generateFloat64ResourceValue("15G"),
													Node:          1,
												},
											},
										},
									},
								},
							},
						},
						{
							Namespace: "default",
							Name:      "pod-2",
							Containers: []*podresv1.ContainerResources{
								{
									Name: "container-1",
									Devices: []*podresv1.ContainerDevices{
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 0},
												},
											},
										},
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 1},
												},
											},
										},
										{
											ResourceName: "disk",
										},
									},
									Resources: []*podresv1.TopologyAwareResource{
										{
											ResourceName: "cpu",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: 24,
													Node:          0,
												},
												{
													ResourceValue: 24,
													Node:          1,
												},
											},
										},
										{
											ResourceName: "memory",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: generateFloat64ResourceValue("32G"),
													Node:          0,
												},
												{
													ResourceValue: generateFloat64ResourceValue("32G"),
													Node:          1,
												},
											},
										},
										{
											ResourceName: "nic",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: generateFloat64ResourceValue("10G"),
													Node:          0,
													Type:          "NIC",
													Name:          "eth0",
													TopologyLevel: podresv1.TopologyLevel_NUMA,
												},
											},
										},
									},
								},
							},
						},
						{
							Namespace: "default",
							Name:      "pod-3",
							Containers: []*podresv1.ContainerResources{
								{
									Name: "container-1",
									Devices: []*podresv1.ContainerDevices{
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 0},
												},
											},
										},
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 1},
												},
											},
										},
										{
											ResourceName: "disk",
										},
									},
									Resources: []*podresv1.TopologyAwareResource{
										{
											ResourceName: "cpu",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: 24,
													Node:          0,
												},
												{
													ResourceValue: 24,
													Node:          1,
												},
											},
										},
										{
											ResourceName: "memory",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: generateFloat64ResourceValue("32G"),
													Node:          0,
												},
												{
													ResourceValue: generateFloat64ResourceValue("32G"),
													Node:          1,
												},
											},
										},
									},
								},
							},
						},
					},
				},
				allocatableResources: &podresv1.AllocatableResourcesResponse{
					Devices: []*podresv1.ContainerDevices{
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"0",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"1",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
					},
					Resources: []*podresv1.AllocatableTopologyAwareResource{
						{
							ResourceName: "cpu",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
						},
						{
							ResourceName: "memory",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
						},
						{
							ResourceName: "nic",
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          0,
									Type:          "NIC",
									Name:          "eth0",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          1,
									Type:          "NIC",
									Name:          "eth1",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
							},
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          0,
									Type:          "NIC",
									Name:          "eth0",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          1,
									Type:          "NIC",
									Name:          "eth1",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
							},
						},
					},
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
				numaCacheGroupZoneNodeMap: map[util.ZoneNode][]util.ZoneNode{
					util.GenerateNumaZoneNode(0): {
						util.GenerateCacheGroupZoneNode(0),
						util.GenerateCacheGroupZoneNode(2),
					},
					util.GenerateNumaZoneNode(1): {
						util.GenerateCacheGroupZoneNode(1),
						util.GenerateCacheGroupZoneNode(3),
					},
				},
				numaDistanceMap: map[int][]machine.NumaDistanceInfo{
					0: {
						{
							Distance: 10,
							NumaID:   0,
						},
						{
							Distance: 20,
							NumaID:   1,
						},
					},
					1: {
						{
							Distance: 20,
							NumaID:   0,
						},
						{
							Distance: 10,
							NumaID:   1,
						},
					},
				},
				cacheGroupCPUsMap: map[int]sets.Int{
					0: sets.NewInt(0, 4, 8, 12, 16, 20, 24, 28),
					1: sets.NewInt(1, 5, 9, 13, 17, 21, 25, 29),
					2: sets.NewInt(2, 6, 10, 14, 18, 22, 26, 30),
					3: sets.NewInt(3, 7, 11, 15, 19, 23, 27, 31),
				},
			},
			want: []*nodev1alpha1.TopologyZone{
				{
					Type: nodev1alpha1.TopologyTypeSocket,
					Name: "0",
					Children: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "0",
							Resources: nodev1alpha1.Resources{
								Capacity: &v1.ResourceList{
									"gpu":    resource.MustParse("2"),
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
								Allocatable: &v1.ResourceList{
									"gpu":    resource.MustParse("2"),
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
							},
							Allocations: []*nodev1alpha1.Allocation{
								{
									Consumer: "default/pod-1/pod-1-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("12"),
										"memory": resource.MustParse("12G"),
									},
								},
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								{
									Consumer: "default/pod-3/pod-3-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
							},
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeCacheGroup,
									Name: "0",
									Resources: nodev1alpha1.Resources{
										Capacity: &v1.ResourceList{
											"cpu": resource.MustParse("8"),
										},
										Allocatable: &v1.ResourceList{
											"cpu": resource.MustParse("8"),
										},
									},
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "cpu_lists",
											Value: "0,4,8,12,16,20,24,28",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeCacheGroup,
									Name: "2",
									Resources: nodev1alpha1.Resources{
										Capacity: &v1.ResourceList{
											"cpu": resource.MustParse("8"),
										},
										Allocatable: &v1.ResourceList{
											"cpu": resource.MustParse("8"),
										},
									},
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "cpu_lists",
											Value: "2,6,10,14,18,22,26,30",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeNIC,
									Name: "eth0",
									Resources: nodev1alpha1.Resources{
										Capacity: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
										Allocatable: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
									},
									Allocations: []*nodev1alpha1.Allocation{
										{
											Consumer: "default/pod-2/pod-2-uid",
											Requests: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Type: nodev1alpha1.TopologyTypeSocket,
					Name: "1",
					Children: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeNuma,
							Name: "1",
							Resources: nodev1alpha1.Resources{
								Capacity: &v1.ResourceList{
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
								Allocatable: &v1.ResourceList{
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
							},
							Allocations: []*nodev1alpha1.Allocation{
								{
									Consumer: "default/pod-1/pod-1-uid",
									Requests: &v1.ResourceList{
										"cpu":    resource.MustParse("15"),
										"memory": resource.MustParse("15G"),
									},
								},
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								{
									Consumer: "default/pod-3/pod-3-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
							},
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeCacheGroup,
									Name: "1",
									Resources: nodev1alpha1.Resources{
										Capacity: &v1.ResourceList{
											"cpu": resource.MustParse("8"),
										},
										Allocatable: &v1.ResourceList{
											"cpu": resource.MustParse("8"),
										},
									},
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "cpu_lists",
											Value: "1,5,9,13,17,21,25,29",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeCacheGroup,
									Name: "3",
									Resources: nodev1alpha1.Resources{
										Capacity: &v1.ResourceList{
											"cpu": resource.MustParse("8"),
										},
										Allocatable: &v1.ResourceList{
											"cpu": resource.MustParse("8"),
										},
									},
									Attributes: []nodev1alpha1.Attribute{
										{
											Name:  "cpu_lists",
											Value: "3,7,11,15,19,23,27,31",
										},
									},
								},
								{
									Type: nodev1alpha1.TopologyTypeNIC,
									Name: "eth1",
									Resources: nodev1alpha1.Resources{
										Capacity: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
										Allocatable: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test validation failed",
			fields: fields{
				podList: []*v1.Pod{
					generateTestDedicatedCoresPod("default", "pod-1", "pod-1-uid", true),
					generateTestDedicatedCoresPod("default", "pod-2", "pod-2-uid", true),
					generateTestDedicatedCoresPod("default", "pod-3", "pod-3-uid", true),
				},
				listPodResources: &podresv1.ListPodResourcesResponse{
					PodResources: []*podresv1.PodResources{
						{
							Namespace: "default",
							Name:      "pod-1",
							Containers: []*podresv1.ContainerResources{
								{
									Name: "container-1",
									Devices: []*podresv1.ContainerDevices{
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 0},
												},
											},
										},
									},
									Resources: []*podresv1.TopologyAwareResource{
										{
											ResourceName: "cpu",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: 12,
													Node:          0,
												},
												{
													ResourceValue: 15,
													Node:          1,
												},
											},
										},
										{
											ResourceName: "memory",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: generateFloat64ResourceValue("12G"),
													Node:          0,
												},
												{
													ResourceValue: generateFloat64ResourceValue("15G"),
													Node:          1,
												},
											},
										},
									},
								},
							},
						},
						{
							Namespace: "default",
							Name:      "pod-2",
							Containers: []*podresv1.ContainerResources{
								{
									Name: "container-1",
									Devices: []*podresv1.ContainerDevices{
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 0},
												},
											},
										},
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 1},
												},
											},
										},
										{
											ResourceName: "disk",
										},
									},
									Resources: []*podresv1.TopologyAwareResource{
										{
											ResourceName: "cpu",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: 24,
													Node:          0,
												},
												{
													ResourceValue: 24,
													Node:          1,
												},
											},
										},
										{
											ResourceName: "memory",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: generateFloat64ResourceValue("32G"),
													Node:          0,
												},
												{
													ResourceValue: generateFloat64ResourceValue("32G"),
													Node:          1,
												},
											},
										},
									},
								},
							},
						},
						{
							Namespace: "default",
							Name:      "pod-3",
							Containers: []*podresv1.ContainerResources{
								{
									Name: "container-1",
									Devices: []*podresv1.ContainerDevices{
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 0},
												},
											},
										},
										{
											ResourceName: "gpu",
											Topology: &podresv1.TopologyInfo{
												Nodes: []*podresv1.NUMANode{
													{ID: 1},
												},
											},
										},
										{
											ResourceName: "disk",
										},
									},
									Resources: []*podresv1.TopologyAwareResource{
										{
											ResourceName: "cpu",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: 24,
													Node:          0,
												},
												{
													ResourceValue: 24,
													Node:          1,
												},
											},
										},
										{
											ResourceName: "memory",
											OriginalTopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
												{
													ResourceValue: generateFloat64ResourceValue("32G"),
													Node:          0,
												},
												{
													ResourceValue: generateFloat64ResourceValue("32G"),
													Node:          1,
												},
											},
										},
									},
								},
							},
						},
					},
				},
				allocatableResources: &podresv1.AllocatableResourcesResponse{
					Devices: []*podresv1.ContainerDevices{
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"0",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"1",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
					},
					Resources: []*podresv1.AllocatableTopologyAwareResource{},
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
			},
			wantErr: true,
		},
		{
			name: "pod resources empty",
			fields: fields{
				podList: []*v1.Pod{
					generateTestDedicatedCoresPod("default", "pod-1", "pod-1-uid", true),
					generateTestDedicatedCoresPod("default", "pod-2", "pod-2-uid", true),
					generateTestDedicatedCoresPod("default", "pod-3", "pod-3-uid", true),
				},
				listPodResources: &podresv1.ListPodResourcesResponse{
					PodResources: []*podresv1.PodResources{},
				},
				allocatableResources: &podresv1.AllocatableResourcesResponse{
					Devices: []*podresv1.ContainerDevices{
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"0",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
						{
							ResourceName: "gpu",
							DeviceIds: []string{
								"1",
							},
							Topology: &podresv1.TopologyInfo{
								Nodes: []*podresv1.NUMANode{
									{ID: 0},
								},
							},
						},
					},
					Resources: []*podresv1.AllocatableTopologyAwareResource{
						{
							ResourceName: "cpu",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: 24,
									Node:          0,
								},
								{
									ResourceValue: 24,
									Node:          1,
								},
							},
						},
						{
							ResourceName: "memory",
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          0,
								},
								{
									ResourceValue: generateFloat64ResourceValue("32G"),
									Node:          1,
								},
							},
						},
						{
							ResourceName: "nic",
							TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          0,
									Type:          "NIC",
									Name:          "eth0",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          1,
									Type:          "NIC",
									Name:          "eth1",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
							},
							TopologyAwareCapacityQuantityList: []*podresv1.TopologyAwareQuantity{
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          0,
									Type:          "NIC",
									Name:          "eth0",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
								{
									ResourceValue: generateFloat64ResourceValue("10G"),
									Node:          1,
									Type:          "NIC",
									Name:          "eth1",
									TopologyLevel: podresv1.TopologyLevel_NUMA,
								},
							},
						},
					},
				},
				numaSocketZoneNodeMap: map[util.ZoneNode]util.ZoneNode{
					util.GenerateNumaZoneNode(0): util.GenerateSocketZoneNode(0),
					util.GenerateNumaZoneNode(1): util.GenerateSocketZoneNode(1),
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &topologyAdapterImpl{
				client: &fakePodResourcesListerClient{
					ListPodResourcesResponse:     tt.fields.listPodResources,
					AllocatableResourcesResponse: tt.fields.allocatableResources,
				},
				metaServer:                generateTestMetaServer(tt.fields.podList...),
				qosConf:                   generic.NewQoSConfiguration(),
				numaSocketZoneNodeMap:     tt.fields.numaSocketZoneNodeMap,
				numaCacheGroupZoneNodeMap: tt.fields.numaCacheGroupZoneNodeMap,
				numaDistanceMap:           tt.fields.numaDistanceMap,
				cacheGroupCPUsMap:         tt.fields.cacheGroupCPUsMap,
			}
			got, err := p.GetTopologyZones(context.TODO())
			if (err != nil) != tt.wantErr {
				t.Errorf("GetTopologyZones() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Equal(t, true, apiequality.Semantic.DeepEqual(tt.want, got))
		})
	}
}

func Test_podResourcesServerTopologyAdapterImpl_GetTopologyPolicy(t *testing.T) {
	t.Parallel()

	fakeKubeletConfig := native.KubeletConfiguration{
		TopologyManagerPolicy: config.SingleNumaNodeTopologyManagerPolicy,
		TopologyManagerScope:  config.ContainerTopologyManagerScope,
	}

	p := &topologyAdapterImpl{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KubeletConfigFetcher: kubeletconfig.NewFakeKubeletConfigFetcher(fakeKubeletConfig),
			},
		},
	}
	got, err := p.GetTopologyPolicy(context.TODO())
	assert.NoError(t, err)
	assert.Equal(t, nodev1alpha1.TopologyPolicySingleNUMANodeContainerLevel, got)
}

func Test_podResourcesServerTopologyAdapterImpl_Run(t *testing.T) {
	t.Parallel()

	dir, err := tmpSocketDir()
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	endpoints := []string{
		path.Join(dir, "podresources.sock"),
	}

	kubeletResourcePluginPath := []string{
		path.Join(dir, "resource-plugins/"),
	}

	listener, err := net.Listen("unix", endpoints[0])
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	server := newFakePodResourcesServer(
		&podresv1.ListPodResourcesResponse{},
		&podresv1.AllocatableResourcesResponse{},
	)

	go func() {
		err := server.Serve(listener)
		assert.NoError(t, err)
	}()

	testMetaServer := generateTestMetaServer()

	getNumaInfo := func() ([]info.Node, error) {
		return []info.Node{}, nil
	}

	ctx, cancel := context.WithCancel(context.TODO())
	notifier := make(chan struct{}, 1)
	p, _ := NewPodResourcesServerTopologyAdapter(testMetaServer, generic.NewQoSConfiguration(),
		endpoints, kubeletResourcePluginPath, pkgconsts.KubeletQoSResourceManagerCheckpoint, nil,
		nil, getNumaInfo, nil, podresources.GetV1Client, []string{"cpu", "memory"})
	err = p.Run(ctx, func() {})
	assert.NoError(t, err)

	checkpointManager, err := checkpointmanager.NewCheckpointManager(kubeletResourcePluginPath[0])
	assert.NoError(t, err)

	err = checkpointManager.CreateCheckpoint(pkgconsts.KubeletQoSResourceManagerCheckpoint, &testutil.MockCheckpoint{})
	assert.NoError(t, err)

	time.Sleep(10 * time.Millisecond)

	cancel()
	close(notifier)
	time.Sleep(10 * time.Millisecond)
}
