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
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/stub"

	"github.com/containerd/nri/pkg/api"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/executor"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/metamanager"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/topology"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
)

func TestProcess(t *testing.T) {
	t.Parallel()

	res1 := TestResource{
		resourceName:     "domain1.com/resource1",
		resourceQuantity: *resource.NewQuantity(int64(2), resource.DecimalSI),
	}
	res2 := TestResource{
		resourceName:     "domain2.com/resource2",
		resourceQuantity: *resource.NewQuantity(int64(2), resource.DecimalSI),
	}

	testResources := []TestResource{
		res1,
		res2,
	}

	pods := []*v1.Pod{
		makePod("testPod", v1.ResourceList{
			"domain1.com/resource1": *resource.NewQuantity(2, resource.DecimalSI),
			"domain2.com/resource2": *resource.NewQuantity(2, resource.DecimalSI),
		}),
		makePod("skipPod", v1.ResourceList{
			"domain1.com/resource1": *resource.NewQuantity(2, resource.DecimalSI),
			"domain2.com/resource2": *resource.NewQuantity(2, resource.DecimalSI),
		}),
	}
	pods[1].OwnerReferences = []metav1.OwnerReference{
		{
			Kind: "DaemonSet",
		},
	}

	ckDir, err := ioutil.TempDir("", "checkpoint-Test")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	conf := generateTestConfiguration(ckDir)
	metaServer, err := generateTestMetaServer(conf, pods)
	assert.NoError(t, err)
	metamanager := metamanager.NewManager(metrics.DummyMetrics{}, nil, metaServer)

	checkpointManager, err := checkpointmanager.NewCheckpointManager("/tmp/process")
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := &ManagerImpl{
		ctx:               ctx,
		mode:              workModeBypass,
		endpoints:         map[string]endpoint.EndpointInfo{},
		socketdir:         "/tmp/process",
		metaManager:       metamanager,
		resourceNamesMap:  map[string]string{},
		podResources:      newPodResourcesChk(),
		resourceExecutor:  executor.NewExecutor(&cgroupmgr.FakeCgroupManager{}),
		checkpointManager: checkpointManager,
		podAddChan:        make(chan string, 1),
		podDeleteChan:     make(chan string, 1),
		qosConfig:         generic.NewQoSConfiguration(),
	}
	defer func() { _ = os.Remove("/tmp/process/kubelet_qrm_checkpoint") }()
	topologyManager, _ := topology.NewManager([]cadvisorapi.Node{
		{
			Id: 0,
		},
	}, "restricted", nil)
	topologyManager.AddHintProvider(m)
	m.topologyManager = topologyManager
	err = registerEndpointByRes(m, testResources)
	assert.NoError(t, err)

	go m.process()

	for _, pod := range pods {
		m.onPodAdd(string(pod.UID))
	}

	time.Sleep(1 * time.Second)
	containerResources := m.podResources.podResources(string(pods[0].UID))
	assert.NotNil(t, containerResources)
	assert.Equal(t, len(containerResources), 1)

	for _, containerResource := range containerResources {
		assert.Equal(t, len(containerResource), 2)
		allocationInfo, ok := containerResource["domain1.com/resource1"]
		assert.True(t, ok)
		assert.Equal(t, allocationInfo.OciPropertyName, "CpusetCpus")

		allocationInfo, ok = containerResource["domain2.com/resource2"]
		assert.True(t, ok)
		assert.Equal(t, allocationInfo.OciPropertyName, "CpusetMems")
	}

	containerResources = m.podResources.podResources(string(pods[1].UID))
	assert.Nil(t, containerResources)

	// remove pod
	for _, pod := range pods {
		m.onPodDelete(string(pod.UID))
	}
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, len(m.podResources.allAllocatedResourceNames()), 0)
	assert.Equal(t, len(m.podResources.pods()), 0)
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	pods := []*v1.Pod{
		makePod("testPod1", v1.ResourceList{
			"cpu":    *resource.NewQuantity(2, resource.DecimalSI),
			"memory": *resource.NewQuantity(2, resource.DecimalSI),
		}),
		makePod("testPod2", v1.ResourceList{
			"cpu":    *resource.NewQuantity(2, resource.DecimalSI),
			"memory": *resource.NewQuantity(2, resource.DecimalSI),
		}),
	}

	ckDir, err := ioutil.TempDir("", "checkpoint-Test")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	conf := generateTestConfiguration(ckDir)
	metaServer, err := generateTestMetaServer(conf, pods)
	assert.NoError(t, err)
	metamanager := metamanager.NewManager(metrics.DummyMetrics{}, nil, metaServer)

	checkpointManager, err := checkpointmanager.NewCheckpointManager("/tmp/reconcile")
	assert.NoError(t, err)

	m := &ManagerImpl{
		mode:        workModeBypass,
		endpoints:   map[string]endpoint.EndpointInfo{},
		socketdir:   "/tmp/reconcile",
		metaManager: metamanager,
		resourceNamesMap: map[string]string{
			"domain1.com/resource1": "domain1.com/resource1",
		},
		podResources:      newPodResourcesChk(),
		resourceExecutor:  executor.NewExecutor(&cgroupmgr.FakeCgroupManager{}),
		checkpointManager: checkpointManager,
		podAddChan:        make(chan string, 1),
		podDeleteChan:     make(chan string, 1),
		qosConfig:         generic.NewQoSConfiguration(),
	}
	defer func() { _ = os.Remove("/tmp/reconcile/kubelet_qrm_checkpoint") }()
	topologyManager, _ := topology.NewManager([]cadvisorapi.Node{
		{
			Id: 0,
		},
	}, "none", nil)
	topologyManager.AddHintProvider(m)
	m.topologyManager = topologyManager
	err = registerEndpointByPods(m, pods)
	assert.NoError(t, err)

	m.reconcile()

	assert.Equal(t, len(m.podResources.pods()), 2)
	for _, pod := range pods {
		containerResources := m.podResources.podResources(string(pod.UID))
		assert.NotNil(t, containerResources)

		for _, resourceAllocation := range containerResources {
			assert.Equal(t, len(resourceAllocation), 2)
		}
	}
}

func TestIsSkippedContainer(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name      string
		Pod       *v1.Pod
		Container *v1.Container
		Expected  bool
	}{
		{
			Name: "mainContainer",
			Pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "testContainer",
						},
					},
				},
			},
			Container: &v1.Container{
				Name: "testContainer",
			},
			Expected: false,
		},
		{
			Name: "initContainer",
			Pod: &v1.Pod{
				Spec: v1.PodSpec{
					InitContainers: []v1.Container{
						{
							Name: "testContainer",
						},
					},
				},
			},
			Container: &v1.Container{
				Name: "testContainer",
			},
			Expected: true,
		},
		{
			Name:      "fail",
			Pod:       nil,
			Container: nil,
			Expected:  false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			res := isSkippedContainer(tc.Pod, tc.Container)
			assert.Equal(t, res, tc.Expected)
		})
	}
}

func TestGetMappedResourceName(t *testing.T) {
	t.Parallel()

	m := &ManagerImpl{
		resourceNamesMap: map[string]string{
			"test/cpu":    "cpu",
			"test/memory": "memory",
		},
	}

	testCases := []struct {
		Name           string
		resourceName   string
		requests       v1.ResourceList
		expectErr      bool
		expectResource string
	}{
		{
			Name:         "cpu",
			resourceName: "test/cpu",
			requests: map[v1.ResourceName]resource.Quantity{
				"cpu": *resource.NewQuantity(1, resource.DecimalSI),
			},
			expectErr:      false,
			expectResource: "cpu",
		},
		{
			Name:         "not found",
			resourceName: "cpu",
			requests: map[v1.ResourceName]resource.Quantity{
				"cpu": *resource.NewQuantity(1, resource.DecimalSI),
			},
			expectErr:      false,
			expectResource: "cpu",
		},
		{
			Name:         "repetition",
			resourceName: "test/cpu",
			requests: map[v1.ResourceName]resource.Quantity{
				"cpu":      *resource.NewQuantity(1, resource.DecimalSI),
				"test/cpu": *resource.NewQuantity(1, resource.DecimalSI),
			},
			expectErr:      true,
			expectResource: "cpu",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			r, err := m.getMappedResourceName(tc.resourceName, tc.requests)
			if tc.expectErr {
				assert.NotNil(t, err)
			} else {
				assert.Nil(t, err)
				assert.Equal(t, r, tc.expectResource)
			}
		})
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	pods := []*v1.Pod{
		makePod("testPod1", v1.ResourceList{
			"cpu":    *resource.NewQuantity(2, resource.DecimalSI),
			"memory": *resource.NewQuantity(2, resource.DecimalSI),
		}),
		makePod("testPod2", v1.ResourceList{
			"cpu":    *resource.NewQuantity(2, resource.DecimalSI),
			"memory": *resource.NewQuantity(2, resource.DecimalSI),
		}),
	}

	ckDir, err := ioutil.TempDir("", "checkpoint-Test")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	conf := generateTestConfiguration(ckDir)
	metaServer, err := generateTestMetaServer(conf, pods)
	assert.NoError(t, err)

	checkpointManager, err := checkpointmanager.NewCheckpointManager("/tmp/run")
	assert.NoError(t, err)

	m := &ManagerImpl{
		mode:            workModeBypass,
		reconcilePeriod: 2 * time.Second,
		endpoints:       map[string]endpoint.EndpointInfo{},
		socketdir:       "/tmp/run",
		socketname:      "tmp.sock",
		resourceNamesMap: map[string]string{
			"domain1.com/resource1": "domain1.com/resource1",
		},
		podResources:      newPodResourcesChk(),
		resourceExecutor:  executor.NewExecutor(&cgroupmgr.FakeCgroupManager{}),
		checkpointManager: checkpointManager,
		podAddChan:        make(chan string, 1),
		podDeleteChan:     make(chan string, 1),
		qosConfig:         generic.NewQoSConfiguration(),
		podResourceSocket: "unix:/tmp/run/podresource.sock",
	}
	defer func() { _ = os.RemoveAll("/tmp/run") }()
	metaManager := metamanager.NewManager(metrics.DummyMetrics{}, m.podResources.pods, metaServer)
	m.metaManager = metaManager

	topologyManager, _ := topology.NewManager([]cadvisorapi.Node{
		{
			Id: 0,
		},
	}, "none", nil)
	topologyManager.AddHintProvider(m)
	m.topologyManager = topologyManager

	err = registerEndpointByPods(m, pods)
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Run(ctx)
	time.Sleep(5 * time.Second)

	assert.Equal(t, len(m.podResources.pods()), 2)
	assert.Equal(t, len(m.podResources.allAllocatedResourceNames()), 2)
}

type TestResource struct {
	resourceName     string
	resourceQuantity resource.Quantity
}

func generateTestMetaServer(conf *config.Configuration, podList []*v1.Pod) (*metaserver.MetaServer, error) {
	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	if err != nil {
		return nil, err
	}

	ms, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	if err != nil {
		return ms, err
	}
	ms.PodFetcher = &pod.PodFetcherStub{
		PodList: podList,
	}
	return ms, nil
}

func generateTestConfiguration(checkpointDir string) *config.Configuration {
	conf, _ := options.NewOptions().Config()

	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	return conf
}

func makePod(name string, rl v1.ResourceList) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:  uuid.NewUUID(),
			Name: name,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: name,
					Resources: v1.ResourceRequirements{
						Requests: rl.DeepCopy(),
						Limits:   rl.DeepCopy(),
					},
				},
			},
		},
	}
}

func registerEndpointByRes(manager *ManagerImpl, testRes []TestResource) error {
	if manager == nil {
		return fmt.Errorf("registerEndpointByRes got nil manager")
	}

	for i, res := range testRes {
		var OciPropertyName string
		if res.resourceName == "domain1.com/resource1" {
			OciPropertyName = "CpusetCpus"
		} else if res.resourceName == "domain2.com/resource2" {
			OciPropertyName = "CpusetMems"
		}

		curResourceName := res.resourceName

		if res.resourceName == "domain1.com/resource1" || res.resourceName == "domain2.com/resource2" {
			manager.registerEndpoint(curResourceName, &pluginapi.ResourcePluginOptions{
				PreStartRequired:      true,
				WithTopologyAlignment: true,
				NeedReconcile:         true,
			}, &MockEndpoint{
				allocateFunc: func(req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
					if req == nil {
						return nil, fmt.Errorf("allocateFunc got nil request")
					}

					resp := new(pluginapi.ResourceAllocationResponse)
					resp.AllocationResult = new(pluginapi.ResourceAllocation)
					resp.AllocationResult.ResourceAllocation = make(map[string]*pluginapi.ResourceAllocationInfo)
					resp.AllocationResult.ResourceAllocation[curResourceName] = new(pluginapi.ResourceAllocationInfo)
					resp.AllocationResult.ResourceAllocation[curResourceName].Envs = make(map[string]string)
					resp.AllocationResult.ResourceAllocation[curResourceName].Envs[fmt.Sprintf("key%d", i)] = fmt.Sprintf("val%d", i)
					resp.AllocationResult.ResourceAllocation[curResourceName].Annotations = make(map[string]string)
					resp.AllocationResult.ResourceAllocation[curResourceName].Annotations[fmt.Sprintf("key%d", i)] = fmt.Sprintf("val%d", i)
					resp.AllocationResult.ResourceAllocation[curResourceName].IsScalarResource = true
					resp.AllocationResult.ResourceAllocation[curResourceName].IsNodeResource = true
					resp.AllocationResult.ResourceAllocation[curResourceName].AllocatedQuantity = req.ResourceRequests[curResourceName]
					resp.AllocationResult.ResourceAllocation[curResourceName].AllocationResult = "0-1"
					resp.AllocationResult.ResourceAllocation[curResourceName].OciPropertyName = OciPropertyName
					return resp, nil
				},
				topologyHints: []*pluginapi.TopologyHint{
					{
						Nodes:     []uint64{0},
						Preferred: true,
					},
				},
			})
		} else if res.resourceName == "domain3.com/resource3" {
			manager.registerEndpoint(curResourceName, &pluginapi.ResourcePluginOptions{
				PreStartRequired:      true,
				WithTopologyAlignment: true,
				NeedReconcile:         true,
			}, &MockEndpoint{
				allocateFunc: func(req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
					return nil, fmt.Errorf("mock error")
				},
				topologyHints: []*pluginapi.TopologyHint{
					{
						Nodes:     []uint64{0},
						Preferred: true,
					},
				},
			})
		}
	}

	return nil
}

func registerEndpointByPods(manager *ManagerImpl, pods []*v1.Pod) error {
	for _, resource := range []string{"cpu", "memory"} {
		resp := &pluginapi.GetResourcesAllocationResponse{
			PodResources: map[string]*pluginapi.ContainerResources{},
		}
		for _, pod := range pods {
			uid := string(pod.UID)
			if _, ok := resp.PodResources[uid]; !ok {
				resp.PodResources[uid] = &pluginapi.ContainerResources{
					ContainerResources: map[string]*pluginapi.ResourceAllocation{},
				}
			}
			for _, container := range pod.Spec.Containers {
				if _, ok := resp.PodResources[uid].ContainerResources[container.Name]; !ok {
					resp.PodResources[uid].ContainerResources[container.Name] = &pluginapi.ResourceAllocation{
						ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{},
					}
				}

				for resourceName, quantity := range container.Resources.Requests {
					if resourceName.String() == resource {
						resp.PodResources[uid].ContainerResources[container.Name].ResourceAllocation[string(resourceName)] = &pluginapi.ResourceAllocationInfo{
							IsNodeResource:    true,
							IsScalarResource:  false,
							AllocatedQuantity: float64(quantity.Value()),
							AllocationResult:  "0-1",
						}
					}
				}
			}
		}

		manager.registerEndpoint(resource, &pluginapi.ResourcePluginOptions{
			NeedReconcile: true,
		}, &MockEndpoint{
			resourceAlloc: func(ctx context.Context, request *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
				return resp, nil
			},
			allocateFunc: func(resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
				return &pluginapi.ResourceAllocationResponse{}, nil
			},
			topologyHints: []*pluginapi.TopologyHint{
				{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
		})
	}

	return nil
}

/* ------------------  mock endpoint for test  ----------------------- */
type MockEndpoint struct {
	allocateFunc                             func(resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error)
	resourceAlloc                            func(ctx context.Context, request *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error)
	getTopologyAwareResourcesFunc            func(c context.Context, request *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error)
	getTopologyAwareAllocatableResourcesFunc func(c context.Context, request *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error)
	stopTime                                 time.Time
	topologyHints                            []*pluginapi.TopologyHint
}

func (m *MockEndpoint) Stop() {
	m.stopTime = time.Now()
}
func (m *MockEndpoint) run(success chan<- bool) {}

func (m *MockEndpoint) Allocate(ctx context.Context, resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if m.IsStopped() {
		return nil, fmt.Errorf("endpoint %v has been stopped", m)
	}
	if m.allocateFunc != nil {
		return m.allocateFunc(resourceRequest)
	}
	return nil, nil
}

func (m *MockEndpoint) GetTopologyHints(c context.Context, resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	return &pluginapi.ResourceHintsResponse{
		PodUid:         resourceRequest.PodUid,
		PodNamespace:   resourceRequest.PodNamespace,
		PodName:        resourceRequest.PodName,
		ContainerName:  resourceRequest.ContainerName,
		ContainerIndex: resourceRequest.ContainerIndex,
		ContainerType:  resourceRequest.ContainerType,
		PodRole:        resourceRequest.PodRole,
		PodType:        resourceRequest.PodType,
		ResourceName:   resourceRequest.ResourceName,
		Labels:         resourceRequest.Labels,
		Annotations:    resourceRequest.Annotations,
		ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
			resourceRequest.ResourceName: {
				Hints: m.topologyHints,
			},
		},
	}, nil
}

func (m *MockEndpoint) IsStopped() bool {
	return !m.stopTime.IsZero()
}

var SGP int = 0

func (m *MockEndpoint) StopGracePeriodExpired() bool {
	if SGP == 0 {
		return false
	} else {
		return true
	}
}

func (m *MockEndpoint) RemovePod(ctx context.Context, removePodRequest *pluginapi.RemovePodRequest) (*pluginapi.RemovePodResponse, error) {
	return nil, nil
}

func (m *MockEndpoint) GetResourceAllocation(ctx context.Context, request *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
	if m.resourceAlloc != nil {
		return m.resourceAlloc(ctx, request)
	}
	return nil, nil
}

func (m *MockEndpoint) GetResourcePluginOptions(ctx context.Context, in *pluginapi.Empty, opts ...grpc.CallOption) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		NeedReconcile: true,
	}, nil
}

func (m *MockEndpoint) GetTopologyAwareResources(c context.Context, request *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	if m.getTopologyAwareResourcesFunc != nil {
		return m.getTopologyAwareResourcesFunc(c, request)
	}
	return nil, nil
}

func (m *MockEndpoint) GetTopologyAwareAllocatableResources(c context.Context, request *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	if m.getTopologyAwareAllocatableResourcesFunc != nil {
		return m.getTopologyAwareAllocatableResourcesFunc(c, request)
	}
	return nil, nil
}

type fakeNRIStub struct{}

func (f *fakeNRIStub) Run(ctx context.Context) error {
	return nil
}

func (f *fakeNRIStub) Start(ctx context.Context) error {
	return nil
}

func (f *fakeNRIStub) Stop() {
	return
}

func (f *fakeNRIStub) Wait() {
	return
}

func (f *fakeNRIStub) UpdateContainers(_ []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	return nil, nil
}

func TestManagerImpl_Configure(t *testing.T) {
	t.Parallel()
	conf := "{\"events\":[\"RunPodSandbox\",\"CreateContainer\",\"UpdateContainer\",\"RemovePodSandbox\"]}"
	m := &ManagerImpl{
		nriStub: &fakeNRIStub{},
	}
	eventMask, err := m.Configure(context.TODO(), conf, "", "")
	res := stub.EventMask(141)
	assert.NoError(t, err)
	assert.Equal(t, eventMask, res)
}

func TestManagerImpl_Synchronize(t *testing.T) {
	t.Parallel()
	m := &ManagerImpl{
		nriStub: &fakeNRIStub{},
	}
	update, err := m.Synchronize(context.TODO(), []*api.PodSandbox{}, []*api.Container{})
	assert.NoError(t, err)
	assert.Nil(t, update)
}

func TestManagerImpl_RunPodSandbox(t *testing.T) {
	t.Parallel()

	pods := []*v1.Pod{
		makePod("testPod1", v1.ResourceList{
			"cpu":    *resource.NewQuantity(2, resource.DecimalSI),
			"memory": *resource.NewQuantity(2, resource.DecimalSI),
		}),
		makePod("testPod2", v1.ResourceList{
			"cpu":    *resource.NewQuantity(2, resource.DecimalSI),
			"memory": *resource.NewQuantity(2, resource.DecimalSI),
		}),
	}

	m := &ManagerImpl{
		nriStub:      &fakeNRIStub{},
		endpoints:    map[string]endpoint.EndpointInfo{},
		podResources: newPodResourcesChk(),
	}
	topologyManager, _ := topology.NewManager([]cadvisorapi.Node{
		{
			Id: 0,
		},
	}, "none", nil)
	topologyManager.AddHintProvider(m)
	m.topologyManager = topologyManager

	ckDir, err := ioutil.TempDir("", "checkpoint-Test")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	conf := generateTestConfiguration(ckDir)
	metaServer, err := generateTestMetaServer(conf, pods)
	assert.NoError(t, err)
	metaManager := metamanager.NewManager(metrics.DummyMetrics{}, m.podResources.pods, metaServer)
	m.metaManager = metaManager

	checkpointManager, err := checkpointmanager.NewCheckpointManager("/tmp/process")
	assert.NoError(t, err)

	m.checkpointManager = checkpointManager
	podUID1 := "testPodUID1"
	err = m.RunPodSandbox(context.TODO(), &api.PodSandbox{Uid: podUID1})
	assert.Error(t, err, fmt.Errorf("failed to find pod by uid testPod1"))
}

func TestManagerImpl_CreateContainer(t *testing.T) {
	t.Parallel()
	m := &ManagerImpl{
		podResources: newPodResourcesChk(),
	}

	// test CpuSetCpus
	resourceCpuSetCpusAllocationInfo := generateCpuSetCpusAllocationInfo()
	podUID1 := "testPodUID1"
	podName1 := "testPodName1"
	containerName1 := "testContainer1"
	containerID1 := "03edb7b6b6becaba276d2c8f5557927661774a69c0cb2230a1fe1f297ca4d4f6"
	m.podResources.insert(podUID1, containerName1, "cpu", resourceCpuSetCpusAllocationInfo)
	pod1 := &api.PodSandbox{
		Name: podName1,
		Uid:  podUID1,
	}
	container1 := &api.Container{
		Id:   containerID1,
		Name: containerName1,
	}
	containerAdjust1, _, err := m.CreateContainer(context.TODO(), pod1, container1)
	assert.NoError(t, err)
	res1 := &api.ContainerAdjustment{
		Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{
			Cpus: "5-6,10",
		}}},
	}
	assert.Equal(t, containerAdjust1, res1)

	// test CpuSetMems
	resourceCpuSetMemsAllocationInfo := generateCpuSetMemsAllocationInfo()
	podUID2 := "testPodUID2"
	podName2 := "testPodName2"
	containerName2 := "testContainer2"
	containerID2 := "03edb7b6b6becaba276d2c8f5557927661774a69c0cb2230a1fe1f297ca4d4f5"
	m.podResources.insert(podUID2, containerName2, "cpu", resourceCpuSetMemsAllocationInfo)
	pod2 := &api.PodSandbox{
		Name: podName2,
		Uid:  podUID2,
	}
	container2 := &api.Container{
		Id:   containerID2,
		Name: containerName2,
	}
	containerAdjust2, _, err := m.CreateContainer(context.TODO(), pod2, container2)
	assert.NoError(t, err)
	res2 := &api.ContainerAdjustment{
		Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{
			Mems: "7-8,11",
		}}},
	}
	assert.Equal(t, containerAdjust2, res2)

	// test pod not exist
	podNotExist := &api.PodSandbox{
		Name: "PodNotExist",
		Uid:  "PodUIDNotExist",
	}
	containerNotExist := &api.Container{
		Id:   "ContainerIDNotExist",
		Name: "ContainerNotExist",
	}
	containerAdjust, _, err := m.CreateContainer(context.TODO(), podNotExist, containerNotExist)
	assert.Nil(t, containerAdjust)
	assert.NoError(t, err)
}

func TestManagerImpl_UpdateContainer(t *testing.T) {
	t.Parallel()
	m := &ManagerImpl{
		nriStub: &fakeNRIStub{},
	}
	update, err := m.UpdateContainer(context.TODO(), &api.PodSandbox{}, &api.Container{}, &api.LinuxResources{})
	assert.NoError(t, err)
	assert.Nil(t, update)
}

func TestManagerImpl_RemovePodSandbox(t *testing.T) {
	t.Parallel()
	m := &ManagerImpl{
		nriStub:      &fakeNRIStub{},
		endpoints:    map[string]endpoint.EndpointInfo{},
		podResources: newPodResourcesChk(),
	}
	topologyManager, _ := topology.NewManager([]cadvisorapi.Node{
		{
			Id: 0,
		},
	}, "none", nil)
	topologyManager.AddHintProvider(m)
	m.topologyManager = topologyManager

	checkpointManager, err := checkpointmanager.NewCheckpointManager("/tmp/process")
	assert.NoError(t, err)

	m.checkpointManager = checkpointManager
	podUID := "testPodUID"
	err = m.RemovePodSandbox(context.TODO(), &api.PodSandbox{Uid: podUID})
	assert.NoError(t, err)
}

func TestManagerImpl_onClose(t *testing.T) {
	t.Parallel()
	m := &ManagerImpl{
		nriStub: &fakeNRIStub{},
	}
	m.onClose()
}

func TestManagerImpl_updateContainerByNRI(t *testing.T) {
	t.Parallel()
	m := &ManagerImpl{
		podResources: newPodResourcesChk(),
		nriStub:      &fakeNRIStub{},
	}

	resourceCpuSetCpusAllocationInfo := generateCpuSetCpusAllocationInfo()
	podUID1 := "testPodUID1"
	containerName1 := "testContainer1"
	containerID1 := "03edb7b6b6becaba276d2c8f5557927661774a69c0cb2230a1fe1f297ca4d4f6"
	m.podResources.insert(podUID1, containerName1, "cpu", resourceCpuSetCpusAllocationInfo)
	m.updateContainerByNRI(podUID1, containerID1, containerName1)
}

func TestManagerImpl_getNRIContainerUpdate(t *testing.T) {
	t.Parallel()
	m := &ManagerImpl{
		podResources: newPodResourcesChk(),
	}

	resourceCpuSetCpusAllocationInfo := generateCpuSetCpusAllocationInfo()
	podUID1 := "testPodUID1"
	containerName1 := "testContainer1"
	containerID1 := "03edb7b6b6becaba276d2c8f5557927661774a69c0cb2230a1fe1f297ca4d4f6"
	m.podResources.insert(podUID1, containerName1, "cpu", resourceCpuSetCpusAllocationInfo)
	containerUpdate1 := m.getNRIContainerUpdate(podUID1, containerID1, containerName1)
	res1 := &api.ContainerUpdate{
		ContainerId: containerID1,
		Linux: &api.LinuxContainerUpdate{Resources: &api.LinuxResources{
			Cpu: &api.LinuxCPU{
				Cpus: "5-6,10",
			},
		}},
	}
	assert.Equal(t, containerUpdate1, res1)

	resourceCpuSetMemsAllocationInfo := generateCpuSetMemsAllocationInfo()
	podUID2 := "testPodUID2"
	containerName2 := "testContainer2"
	containerID2 := "03edb7b6b6becaba276d2c8f5557927661774a69c0cb2230a1fe1f297ca4d4f5"
	m.podResources.insert(podUID2, containerName2, "cpu", resourceCpuSetMemsAllocationInfo)
	containerUpdate2 := m.getNRIContainerUpdate(podUID2, containerID2, containerName2)
	res2 := &api.ContainerUpdate{
		ContainerId: containerID2,
		Linux: &api.LinuxContainerUpdate{Resources: &api.LinuxResources{
			Cpu: &api.LinuxCPU{
				Mems: "7-8,11",
			},
		}},
	}
	assert.Equal(t, containerUpdate2, res2)
}
