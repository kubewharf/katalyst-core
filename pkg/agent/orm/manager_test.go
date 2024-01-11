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
				}},
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
		t.Run(tc.Name, func(t *testing.T) {
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
		t.Run(tc.Name, func(t *testing.T) {
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
	}
	defer func() { _ = os.Remove("/tmp/run/kubelet_qrm_checkpoint") }()
	defer func() { _ = os.Remove("/tmp/run/tmp.sock") }()
	metaManager := metamanager.NewManager(metrics.DummyMetrics{}, m.podResources.pods, metaServer)
	m.metaManager = metaManager

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
		})
	}

	return nil
}

/* ------------------  mock endpoint for test  ----------------------- */
type MockEndpoint struct {
	allocateFunc  func(resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error)
	resourceAlloc func(ctx context.Context, request *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error)
	stopTime      time.Time
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
