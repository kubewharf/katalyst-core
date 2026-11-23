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

package state

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGenerateDefaultResourceState_MultiDevices(t *testing.T) {
	t.Parallel()

	registry := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("gpu-1", machine.NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("gpu-2", machine.NewDeviceTopologyProviderStub())

	topo1 := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"d1": {Health: "Healthy"},
		},
		UpdateTime: 100,
	}
	topo2 := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"d2": {Health: "Healthy"},
		},
		UpdateTime: 200,
	}

	_ = registry.SetDeviceTopology("gpu-1", topo1)
	_ = registry.SetDeviceTopology("gpu-2", topo2)

	generator := NewGenericDefaultResourceStateGenerator([]string{"gpu-1", "gpu-2"}, registry, 1, true)
	state, err := generator.GenerateDefaultResourceState()

	assert.NoError(t, err)
	assert.Len(t, state, 1)
	assert.Contains(t, state, "d2")
}

func TestGenerateDefaultResourceState_PartialMissing(t *testing.T) {
	t.Parallel()

	registry := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("gpu-1", machine.NewDeviceTopologyProviderStub())

	topo1 := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"d1": {Health: "Healthy"},
		},
		UpdateTime: 100,
	}

	_ = registry.SetDeviceTopology("gpu-1", topo1)

	// One device missing, pick the existing one (d1)
	generator := NewGenericDefaultResourceStateGenerator([]string{"gpu-1", "non-existent"}, registry, 1, true)
	state, err := generator.GenerateDefaultResourceState()

	assert.NoError(t, err)
	assert.Len(t, state, 1)
	assert.Contains(t, state, "d1")
}

func TestGenerateResourceStateFromPodEntries_RetainsMissingDevices(t *testing.T) {
	t.Parallel()
	for _, currentCount := range []int{0, 4} {
		currentCount := currentCount
		t.Run(fmt.Sprintf("current_%d", currentCount), func(t *testing.T) {
			t.Parallel()
			topologyRegistry := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})
			topologyRegistry.RegisterDeviceTopologyProvider("device", machine.NewDeviceTopologyProviderStub())
			topology := &machine.DeviceTopology{Devices: make(map[string]machine.DeviceInfo)}
			previous := make(AllocationMap)
			for i := 0; i < 8; i++ {
				id := fmt.Sprintf("device-%d", i)
				previous[id] = &AllocationState{Allocatable: 10, PodEntries: PodEntries{
					"deleted": {"c": {TopologyAwareAllocations: map[string]Allocation{id: {Quantity: 5}}}},
				}}
				if i < currentCount {
					topology.Devices[id] = machine.DeviceInfo{}
				}
			}
			assert.NoError(t, topologyRegistry.SetDeviceTopology("device", topology))
			generator := NewGenericDefaultResourceStateGenerator([]string{"device"}, topologyRegistry, 20, true)
			entries := PodEntries{
				"live": {"c": {
					AllocationMeta: commonstate.AllocationMeta{PodUid: "live", ContainerName: "c"},
					DeviceName:     "nvidia.com/be-gpu",
					TopologyAwareAllocations: map[string]Allocation{
						"device-7": {Quantity: 2, NUMANodes: []int{1}},
						"device-6": {Quantity: 0},
					},
				}},
				"ignored": {"": {TopologyAwareAllocations: map[string]Allocation{"device-5": {Quantity: 1}}}, "nil": nil},
			}
			previousSnapshot, entriesSnapshot := previous.String(), entries.String()
			generated, err := GenerateResourceStateFromPodEntries(entries, generator, previous)
			if !assert.NoError(t, err) {
				return
			}
			assert.Len(t, generated, currentCount+2)
			assert.NotContains(t, generated, "device-5")
			assert.Equal(t, float64(10), generated["device-7"].Allocatable)
			owner := generated["device-7"].PodEntries["live"]["c"]
			assert.Equal(t, "nvidia.com/be-gpu", owner.DeviceName)
			assert.Equal(t, Allocation{Quantity: 2, NUMANodes: []int{1}}, owner.AllocatedAllocation)
			assert.Len(t, owner.TopologyAwareAllocations, 1)
			assert.NotContains(t, generated["device-7"].PodEntries, "deleted")
			assert.Equal(t, previousSnapshot, previous.String())
			assert.Equal(t, entriesSnapshot, entries.String())

			// After the last owner disappears, missing entries are naturally excluded.
			cleaned, err := GenerateResourceStateFromPodEntries(PodEntries{}, generator, generated)
			assert.NoError(t, err)
			assert.Len(t, cleaned, currentCount)

			// Reappearance uses fresh capacity while preserving the live allocation.
			topology.Devices["device-7"] = machine.DeviceInfo{}
			assert.NoError(t, topologyRegistry.SetDeviceTopology("device", topology))
			reappeared, err := GenerateResourceStateFromPodEntries(entries, generator, generated)
			assert.NoError(t, err)
			assert.Equal(t, float64(20), reappeared["device-7"].Allocatable)
			assert.Equal(t, float64(2), reappeared["device-7"].GetQuantityAllocated())
		})
	}
}

func TestGenerateResourceStateFromPodEntries_SkipsMissingCapacity(t *testing.T) {
	t.Parallel()
	registry := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("device", machine.NewDeviceTopologyProviderStub())
	assert.NoError(t, registry.SetDeviceTopology("device", &machine.DeviceTopology{Devices: map[string]machine.DeviceInfo{"current": {}}}))
	generator := NewGenericDefaultResourceStateGenerator([]string{"device"}, registry, 1, true)
	entries := PodEntries{"owner": {"c": {TopologyAwareAllocations: map[string]Allocation{
		"missing":  {Quantity: 1},
		"current":  {Quantity: 2},
		"retained": {Quantity: 3},
	}}}}
	before := entries.String()
	previous := AllocationMap{"retained": {Allocatable: 10}}
	result, err := GenerateResourceStateFromPodEntries(entries, generator, previous)
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, result, 2)
	assert.NotContains(t, result, "missing")
	assert.Equal(t, float64(2), result["current"].GetQuantityAllocated())
	assert.Equal(t, float64(3), result["retained"].GetQuantityAllocated())
	assert.Equal(t, float64(10), result["retained"].Allocatable)
	assert.Equal(t, before, entries.String())

	result, err = GenerateResourceStateFromPodEntries(entries, generator, nil)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Contains(t, result, "current")
	assert.Equal(t, before, entries.String())
}

func TestGenerateMachineStateFromPodEntries_PerResourceRetention(t *testing.T) {
	t.Parallel()
	topologyRegistry := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	topologyRegistry.RegisterDeviceTopologyProvider("device", machine.NewDeviceTopologyProviderStub())
	assert.NoError(t, topologyRegistry.SetDeviceTopology("device", &machine.DeviceTopology{Devices: map[string]machine.DeviceInfo{"current": {}}}))
	registry := NewDefaultResourceStateGeneratorRegistry()
	for _, name := range []string{"gpu_device", "milligpu", "rdma_device"} {
		registry.RegisterResourceStateGenerator(name, NewGenericDefaultResourceStateGenerator([]string{"device"}, topologyRegistry, 10, true))
	}
	previous := AllocationResourcesMap{
		"gpu_device":  {"gone": {Allocatable: 1}},
		"milligpu":    {"gone": {Allocatable: 1000}},
		"rdma_device": {"lost-rdma": {Allocatable: 1}},
	}
	entries := PodResourceEntries{
		"milligpu":    {"owner": {"c": {TopologyAwareAllocations: map[string]Allocation{"gone": {Quantity: 50}}}}},
		"rdma_device": {"rdma-owner": {"c": {TopologyAwareAllocations: map[string]Allocation{"lost-rdma": {Quantity: 1}}}}},
	}
	generated, err := GenerateMachineStateFromPodEntries(entries, registry, previous)
	if !assert.NoError(t, err) {
		return
	}
	assert.Contains(t, generated[v1.ResourceName("milligpu")], "gone")
	assert.Contains(t, generated[v1.ResourceName("rdma_device")], "lost-rdma")
	assert.NotContains(t, generated[v1.ResourceName("gpu_device")], "gone")

	cache, err := NewGPUPluginState(nil, registry)
	assert.NoError(t, err)
	checkpoint := NewGPUPluginCheckpoint()
	checkpoint.PolicyName, checkpoint.MachineState, checkpoint.PodResourceEntries = "static", previous, entries
	restored := &stateCheckpoint{cache: cache, policyName: "static", defaultResourceStateGenerators: registry}
	_, err = restored.RestoreState(checkpoint)
	assert.NoError(t, err)
	assert.Equal(t, generated, restored.GetMachineState())

	delete(entries["milligpu"], "owner")
	cleaned, err := GenerateMachineStateFromPodEntries(entries, registry, generated)
	assert.NoError(t, err)
	assert.NotContains(t, cleaned[v1.ResourceName("milligpu")], "gone")
	assert.Contains(t, cleaned[v1.ResourceName("rdma_device")], "lost-rdma")
}
