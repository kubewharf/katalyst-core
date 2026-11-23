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

package staticpolicy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// Tests deliberately use the real custom/resource plugins and state implementations.
func reconciliationInventory(name string, ids ...string) *pluginapi.UpdateAllocatableAssociatedDevicesRequest {
	request := &pluginapi.UpdateAllocatableAssociatedDevicesRequest{DeviceName: name}
	for _, id := range ids {
		request.Devices = append(request.Devices, &pluginapi.AssociatedDevice{
			ID: id, Health: pluginapi.Healthy,
			Topology: &pluginapi.TopologyInfo{Nodes: []*pluginapi.NUMANode{{ID: 0}}},
		})
	}
	return request
}

func TestRDMAInventoryReconciliationOptionalInitialization(t *testing.T) {
	t.Parallel()
	for _, existing := range []string{"none", "empty state", "pod entries"} {
		existing := existing
		t.Run(existing, func(t *testing.T) {
			t.Parallel()
			p := makeTestStaticPolicy(t)
			generator, ok := p.DefaultResourceStateGeneratorRegistry.GetGenerator(gpuconsts.RDMADeviceType)
			require.True(t, ok)
			require.False(t, generator.MustInitDefaultResourceState())
			_, err := p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.GPUDeviceNames[0], "gpu-0"))
			require.NoError(t, err)
			require.NotContains(t, p.GetState().GetMachineState(), v1.ResourceName(gpuconsts.RDMADeviceType))

			switch existing {
			case "empty state":
				p.GetState().SetResourceState(gpuconsts.RDMADeviceType, state.AllocationMap{}, false)
			case "pod entries":
				p.GetState().SetAllocationInfo(gpuconsts.RDMADeviceType, "owner", "c", &state.AllocationInfo{
					DeviceName:               p.Conf.RDMADeviceNames[0],
					TopologyAwareAllocations: map[string]state.Allocation{"rdma-0": {Quantity: 1, NUMANodes: []int{0}}},
				}, false)
			}
			_, err = p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.RDMADeviceNames[0], "rdma-0", "rdma-1"))
			require.NoError(t, err)
			rdma := p.GetState().GetMachineState()[gpuconsts.RDMADeviceType]
			if existing == "none" {
				require.Nil(t, rdma) // Inventory alone must not eagerly initialize RDMA.
				return
			}
			require.Len(t, rdma, 2)

			// A real subsequent RDMA callback removes free devices and retains owned ones.
			_, err = p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.RDMADeviceNames[0], "rdma-2"))
			require.NoError(t, err)
			rdma = p.GetState().GetMachineState()[gpuconsts.RDMADeviceType]
			require.Contains(t, rdma, "rdma-2")
			require.NotContains(t, rdma, "rdma-1")
			if existing == "pod entries" {
				require.Contains(t, rdma, "rdma-0")
				require.Equal(t, float64(1), rdma["rdma-0"].GetQuantityAllocated())
				_, err = p.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: "owner"})
				require.NoError(t, err)
				require.NotContains(t, p.GetState().GetMachineState()[gpuconsts.RDMADeviceType], "rdma-0")
			} else {
				require.NotContains(t, rdma, "rdma-0")
			}
			_, err = p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.RDMADeviceNames[0]))
			require.NoError(t, err)
			require.Empty(t, p.GetState().GetMachineState()[gpuconsts.RDMADeviceType])
		})
	}
}

func reconciliationAdmission(p *StaticPolicy, podUID string) *pluginapi.AssociatedDeviceRequest {
	name := p.Conf.GPUDeviceNames[0]
	return &pluginapi.AssociatedDeviceRequest{
		DeviceName: name,
		ResourceRequest: &pluginapi.ResourceRequest{
			PodUid: podUID, PodNamespace: "default", PodName: podUID,
			ContainerName: "c", ContainerType: pluginapi.ContainerType_MAIN,
			ResourceName:     string(consts.ResourceGPUMemory),
			ResourceRequests: map[string]float64{string(consts.ResourceGPUMemory): 1},
			Annotations:      map[string]string{consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores},
			Hint:             &pluginapi.TopologyHint{Nodes: []uint64{0}},
		},
		DeviceRequest: []*pluginapi.DeviceRequest{{
			DeviceName: name, DeviceRequest: 1, AvailableDevices: []string{"gpu-0"},
			Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
		}},
	}
}

func TestConcurrentTopologyUpdatesAndAdmission(t *testing.T) {
	t.Parallel()
	p := makeTestStaticPolicy(t)
	// The default test configuration has no GPU memory capacity. Configure both
	// the strategy limit and its state generator before exercising admission.
	p.Conf.GPUMemoryAllocatablePerGPU = resource.MustParse("1000")
	p.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(string(consts.ResourceGPUMemory),
		state.NewGenericDefaultResourceStateGenerator(p.Conf.GPUDeviceNames, p.DeviceTopologyRegistry, 1000, true))
	_, err := p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.GPUDeviceNames[0], "gpu-0", "gpu-lost"))
	require.NoError(t, err)
	for _, name := range []v1.ResourceName{gpuconsts.GPUDeviceType, consts.ResourceGPUMemory} {
		p.GetState().SetAllocationInfo(name, "existing-owner", "c", &state.AllocationInfo{
			AllocationMeta:           commonstate.AllocationMeta{PodUid: "existing-owner", ContainerName: "c"},
			TopologyAwareAllocations: map[string]state.Allocation{"gpu-lost": {Quantity: 1, NUMANodes: []int{0}}},
		}, false)
	}
	// Initialize the device maps for that existing owner before concurrent work.
	_, err = p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.GPUDeviceNames[0], "gpu-0"))
	require.NoError(t, err)

	const rounds = 20
	start := make(chan struct{})
	failures := make(chan error, rounds*6)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < rounds; i++ {
			ids := []string{"gpu-0"}
			if i%2 == 0 {
				ids = append(ids, "gpu-free")
			}
			_, err := p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.GPUDeviceNames[0], ids...))
			if err != nil {
				failures <- fmt.Errorf("inventory: %w", err)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < rounds; i++ {
			uid := fmt.Sprintf("new-pod-%d", i)
			// This exercises real preallocation, device strategy, resource replay and persistence.
			response, err := p.AllocateAssociatedDevice(context.Background(), reconciliationAdmission(p, uid))
			if err != nil {
				failures <- fmt.Errorf("admission: %w", err)
				continue
			}
			if response == nil || response.AllocationResult == nil || len(response.AllocationResult.AllocatedDevices) != 1 || response.AllocationResult.AllocatedDevices[0] != "gpu-0" {
				failures <- fmt.Errorf("unexpected admission response: %v", response)
			}
			for _, name := range []v1.ResourceName{gpuconsts.GPUDeviceType, consts.ResourceGPUMemory} {
				allocation := p.GetState().GetAllocationInfo(name, uid, "c")
				if allocation == nil || allocation.TopologyAwareAllocations["gpu-0"].Quantity != 1 {
					failures <- fmt.Errorf("missing allocation for %s in resource %s", uid, name)
				}
			}
			_, err = p.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: uid})
			if err != nil {
				failures <- fmt.Errorf("cleanup: %w", err)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < rounds; i++ {
			_, err := p.GetAssociatedDeviceTopologyHints(context.Background(), reconciliationAdmission(p, fmt.Sprintf("hint-pod-%d", i)))
			if err != nil {
				failures <- fmt.Errorf("hints: %w", err)
			}
		}
	}()
	close(start)
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent update/admission/cleanup/hints did not finish")
	}
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	_, err = p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.GPUDeviceNames[0], "gpu-0"))
	require.NoError(t, err)
	for _, name := range []v1.ResourceName{gpuconsts.GPUDeviceType, consts.ResourceGPUMemory} {
		devices := p.GetState().GetMachineState()[name]
		require.Len(t, devices, 2)
		require.Equal(t, float64(1), devices["gpu-lost"].GetQuantityAllocated())
		require.Zero(t, devices["gpu-0"].GetQuantityAllocated())
	}
}

// Failure injection is fixture-local; it never patches global functions.
type reconciliationFailureGenerator struct {
	state.DefaultResourceStateGenerator
	err error
}

func (g reconciliationFailureGenerator) GenerateDefaultResourceState() (state.AllocationMap, error) {
	return nil, g.err
}

type reconciliationStoreTracker struct {
	state.State
	stores int
}

func (s *reconciliationStoreTracker) StoreState() error { s.stores++; return s.State.StoreState() }

func TestInventoryReconciliationGeneratorFailure(t *testing.T) {
	t.Parallel()
	p := makeTestStaticPolicy(t)
	request := reconciliationInventory(p.Conf.GPUDeviceNames[0], "gpu-0", "gpu-lost")
	_, err := p.UpdateAllocatableAssociatedDevices(context.Background(), request)
	require.NoError(t, err)
	p.GetState().SetAllocationInfo(gpuconsts.GPUDeviceType, "owner", "c", &state.AllocationInfo{
		TopologyAwareAllocations: map[string]state.Allocation{"gpu-lost": {Quantity: 1}},
	}, false)
	beforeState, beforeEntries := p.GetState().GetMachineState(), p.GetState().GetPodResourceEntries()
	tracked := &reconciliationStoreTracker{State: p.GetState()}
	p.SetState(tracked)
	original, ok := p.DefaultResourceStateGeneratorRegistry.GetGenerator(string(consts.ResourceGPUMemory))
	require.True(t, ok)
	failure := errors.New("injected generator failure")
	p.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(string(consts.ResourceGPUMemory), reconciliationFailureGenerator{DefaultResourceStateGenerator: original, err: failure})
	request = reconciliationInventory(p.Conf.GPUDeviceNames[0], "gpu-0", "gpu-new")
	response, err := p.UpdateAllocatableAssociatedDevices(context.Background(), request)
	require.ErrorIs(t, err, failure)
	require.Nil(t, response)
	require.Equal(t, beforeState, tracked.GetMachineState())
	require.Equal(t, beforeEntries, tracked.GetPodResourceEntries())
	require.Zero(t, tracked.stores)
	topology, err := p.DeviceTopologyRegistry.GetDeviceTopology(request.DeviceName)
	require.NoError(t, err)
	require.NotContains(t, topology.Devices, "gpu-lost") // Registry publication is not rolled back.

	p.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(string(consts.ResourceGPUMemory), original)
	_, err = p.UpdateAllocatableAssociatedDevices(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, 1, tracked.stores)
	require.Contains(t, tracked.GetMachineState()[gpuconsts.GPUDeviceType], "gpu-lost")
}

func TestInventoryReconciliationCheckpointFailure(t *testing.T) {
	t.Parallel()
	p := makeTestStaticPolicy(t)
	_, err := p.UpdateAllocatableAssociatedDevices(context.Background(), reconciliationInventory(p.Conf.GPUDeviceNames[0], "gpu-0", "gpu-lost"))
	require.NoError(t, err)
	p.GetState().SetAllocationInfo(gpuconsts.GPUDeviceType, "owner", "c", &state.AllocationInfo{
		TopologyAwareAllocations: map[string]state.Allocation{"gpu-lost": {Quantity: 1}},
	}, false)
	// Use an isolated real checkpoint store, then make its directory unwriteable as a path.
	directory := filepath.Join(t.TempDir(), "checkpoint")
	persistent, err := state.NewCheckpointState(&statedirectory.StateDirectoryConfiguration{StateFileDirectory: directory}, p.Conf.QRMPluginsConfiguration,
		"test", "static", state.NewDefaultResourceStateGeneratorRegistry(), false, metrics.DummyMetrics{})
	require.NoError(t, err)
	persistent.SetMachineState(p.GetState().GetMachineState(), false)
	persistent.SetPodResourceEntries(p.GetState().GetPodResourceEntries(), false)
	require.NoError(t, persistent.StoreState())
	beforeDisk, err := os.ReadFile(filepath.Join(directory, "test"))
	require.NoError(t, err)
	backup := directory + "-backup"
	require.NoError(t, os.Rename(directory, backup))
	require.NoError(t, os.WriteFile(directory, []byte("not a directory"), 0o600))
	p.SetState(persistent)
	beforeEntries := persistent.GetPodResourceEntries()
	request := reconciliationInventory(p.Conf.GPUDeviceNames[0], "gpu-0", "gpu-new")
	response, err := p.UpdateAllocatableAssociatedDevices(context.Background(), request)
	require.ErrorContains(t, err, "persist GPU machine state")
	require.Nil(t, response)
	require.Contains(t, persistent.GetMachineState()[gpuconsts.GPUDeviceType], "gpu-new")
	require.Contains(t, persistent.GetMachineState()[gpuconsts.GPUDeviceType], "gpu-lost")
	require.Equal(t, beforeEntries, persistent.GetPodResourceEntries())
	unchangedDisk, err := os.ReadFile(filepath.Join(backup, "test"))
	require.NoError(t, err)
	require.Equal(t, beforeDisk, unchangedDisk)

	// Repair only the fixture's filesystem and repeat the same inventory.
	require.NoError(t, os.Rename(directory, directory+"-blocker"))
	require.NoError(t, os.Rename(backup, directory))
	_, err = p.UpdateAllocatableAssociatedDevices(context.Background(), request)
	require.NoError(t, err)
	afterDisk, err := os.ReadFile(filepath.Join(directory, "test"))
	require.NoError(t, err)
	require.NotEqual(t, beforeDisk, afterDisk)
	restored, err := state.NewCheckpointState(&statedirectory.StateDirectoryConfiguration{StateFileDirectory: directory}, p.Conf.QRMPluginsConfiguration,
		"test", "static", p.DefaultResourceStateGeneratorRegistry, false, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Equal(t, persistent.GetMachineState(), restored.GetMachineState())
	require.Equal(t, persistent.GetPodResourceEntries(), restored.GetPodResourceEntries())
}
