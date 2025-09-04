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

package kubelet

import (
	"context"
	"io/ioutil"
	"net"
	"os"
	"path"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	podresv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	apisconfig "k8s.io/kubernetes/pkg/kubelet/apis/config"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	testutil "k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state/testing"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/kubelet/topology"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/kubeletconfig"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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

func generateTestConfiguration(t *testing.T, dir string) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	assert.NoError(t, err)
	assert.NotNil(t, testConfiguration)

	testConfiguration.PodResourcesServerEndpoints = []string{
		path.Join(dir, "podresources.sock"),
	}
	testConfiguration.KubeletResourcePluginPaths = []string{
		path.Join(dir, "resource-plugins/"),
	}
	return testConfiguration
}

func generateTestMetaServer(podList ...*v1.Pod) *metaserver.MetaServer {
	fakeKubeletConfig := native.KubeletConfiguration{
		TopologyManagerPolicy: apisconfig.SingleNumaNodeTopologyManagerPolicy,
		TopologyManagerScope:  apisconfig.ContainerTopologyManagerScope,
	}

	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: podList},
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &info.MachineInfo{
					Topology: []info.Node{
						{
							Id: 0,
							Cores: []info.Core{
								{SocketID: 0, Id: 0, Threads: []int{0, 4}},
								{SocketID: 0, Id: 1, Threads: []int{1, 5}},
								{SocketID: 0, Id: 2, Threads: []int{2, 2}}, // Wrong case - should fail here
								{SocketID: 0, Id: 3, Threads: []int{3, 7}},
							},
						},
						{
							Id: 1,
							Cores: []info.Core{
								{SocketID: 1, Id: 4, Threads: []int{8, 12}},
								{SocketID: 1, Id: 5, Threads: []int{9, 13}},
								{SocketID: 1, Id: 6, Threads: []int{10, 14}}, // Wrong case - should fail here
								{SocketID: 1, Id: 7, Threads: []int{11, 15}},
							},
						},
					},
				},
				ExtraTopologyInfo: &machine.ExtraTopologyInfo{
					NumaDistanceMap: map[int][]machine.NumaDistanceInfo{
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
				},
			},
			KubeletConfigFetcher: kubeletconfig.NewFakeKubeletConfigFetcher(fakeKubeletConfig),
		},
	}
}

func tmpSocketDir() (socketDir string, err error) {
	socketDir, err = ioutil.TempDir("", "pod_resources")
	if err != nil {
		return
	}
	err = os.MkdirAll(socketDir, 0o755)
	if err != nil {
		return "", err
	}
	return
}

func TestNewKubeletReporterPlugin(t *testing.T) {
	t.Parallel()

	dir, err := tmpSocketDir()
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	conf := generateTestConfiguration(t, dir)

	listener, err := net.Listen("unix", conf.PodResourcesServerEndpoints[0])
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	server := newFakePodResourcesServer(
		&podresv1.ListPodResourcesResponse{
			PodResources: []*podresv1.PodResources{
				{
					Name:      "pod-1",
					Namespace: "default",
					PodRole:   "pod-role-1",
					PodType:   "pod-type-1",
					Containers: []*podresv1.ContainerResources{
						{
							Name: "container-1",
							Devices: []*podresv1.ContainerDevices{
								{
									ResourceName: "resource-1",
									Topology: &podresv1.TopologyInfo{
										Nodes: []*podresv1.NUMANode{
											{
												ID: 1,
											},
										},
									},
								},
							},
							Memory: []*podresv1.ContainerMemory{
								{
									MemoryType: "test",
									Size_:      100,
									Topology: &podresv1.TopologyInfo{
										Nodes: []*podresv1.NUMANode{
											{
												ID: 1,
											},
										},
									},
								},
							},
							Resources: []*podresv1.TopologyAwareResource{
								{
									ResourceName:     "cpu",
									IsScalarResource: true,
									TopologyAwareQuantityList: []*podresv1.TopologyAwareQuantity{
										{
											ResourceValue: 10.0,
											Node:          1,
										},
									},
								},
							},
						},
					},
					Labels: map[string]string{
						"aa": "bb",
					},
					Annotations: map[string]string{
						"aa": "bb",
					},
				},
			},
		},
		&podresv1.AllocatableResourcesResponse{
			Devices: []*podresv1.ContainerDevices{
				{
					ResourceName: "resource-1",
					Topology: &podresv1.TopologyInfo{
						Nodes: []*podresv1.NUMANode{
							{
								ID: 1,
							},
						},
					},
				},
			},
			Memory: []*podresv1.ContainerMemory{
				{
					MemoryType: "test",
					Size_:      100,
					Topology: &podresv1.TopologyInfo{
						Nodes: []*podresv1.NUMANode{
							{
								ID: 1,
							},
						},
					},
				},
			},
			Resources: []*podresv1.AllocatableTopologyAwareResource{
				{
					ResourceName:     "cpu",
					IsScalarResource: true,
					TopologyAwareAllocatableQuantityList: []*podresv1.TopologyAwareQuantity{
						{
							ResourceValue: 10.0,
							Node:          1,
						},
					},
				},
			},
		},
	)

	go func() {
		err := server.Serve(listener)
		assert.NoError(t, err)
	}()

	meta := generateTestMetaServer(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
			UID:       "pod-1-uid",
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
			},
		},
	})

	callback := func(name string, resp *v1alpha1.GetReportContentResponse) {
		klog.Infof("Callback called with name: %s, resp: %#v", name, resp)
	}

	plugin, err := NewKubeletReporterPlugin(metrics.DummyMetrics{}, meta, conf, callback)
	assert.NoError(t, err)

	success := make(chan bool)
	go plugin.Run(success)

	<-success

	checkpointManager, err := checkpointmanager.NewCheckpointManager(conf.KubeletResourcePluginPaths[0])
	assert.NoError(t, err)

	err = checkpointManager.CreateCheckpoint(pkgconsts.KubeletQoSResourceManagerCheckpoint, &testutil.MockCheckpoint{})
	assert.NoError(t, err)

	time.Sleep(10 * time.Millisecond)
	plugin.Stop()

	time.Sleep(10 * time.Millisecond)
}

func TestGetTopologyPolicyReportContent(t *testing.T) {
	t.Parallel()

	dir, err := tmpSocketDir()
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	conf := generateTestConfiguration(t, dir)
	conf.EnableReportTopologyPolicy = true

	meta := generateTestMetaServer()

	callback := func(name string, resp *v1alpha1.GetReportContentResponse) {
		klog.Infof("Callback called with name: %s, resp: %#v", name, resp)
	}

	plugin, err := NewKubeletReporterPlugin(metrics.DummyMetrics{}, meta, conf, callback)
	assert.NoError(t, err)
	kubePlugin := plugin.(*kubeletPlugin)

	_, err = kubePlugin.getTopologyPolicyReportContent(context.TODO())
	assert.NoError(t, err)
}

func TestGetTopologyStatusContent(t *testing.T) {
	t.Parallel()

	dir, err := tmpSocketDir()
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	conf := generateTestConfiguration(t, dir)
	conf.EnableReportTopologyPolicy = true

	meta := generateTestMetaServer()

	callback := func(name string, resp *v1alpha1.GetReportContentResponse) {
		klog.Infof("Callback called with name: %s, resp: %#v", name, resp)
	}

	plugin, err := NewKubeletReporterPlugin(metrics.DummyMetrics{}, meta, conf, callback)
	assert.NoError(t, err)
	kubePlugin := plugin.(*kubeletPlugin)

	kubePlugin.topologyStatusAdapter = topology.DummyAdapter{}
	_, err = kubePlugin.getReportContent(context.TODO())
	assert.NoError(t, err)
}
