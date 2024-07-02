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

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/metamanager"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/topology"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

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
