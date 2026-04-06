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

package machine

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDeviceTopologyRegistry_TopologyChangeNotifiers(t *testing.T) {
	t.Parallel()

	registry := NewDeviceTopologyRegistry()
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProviderStub())

	callCount := 0
	registry.RegisterTopologyChangeNotifier(func() {
		callCount++
	})

	gpuTopology1 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
		},
	}

	// First set should trigger the notifier
	err := registry.SetDeviceTopology("gpu", gpuTopology1)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Setting identical topology should not trigger the notifier
	gpuTopology1Clone := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
		},
	}
	err = registry.SetDeviceTopology("gpu", gpuTopology1Clone)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Setting different topology should trigger the notifier
	gpuTopology2 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
			"gpu-1": {NumaNodes: []int{1}},
		},
	}
	err = registry.SetDeviceTopology("gpu", gpuTopology2)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestDeviceTopologyRegistry_GetDeviceNUMAAffinity(t *testing.T) {
	t.Parallel()

	npuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"npu-0": {NumaNodes: []int{0}},
			"npu-1": {NumaNodes: []int{1}},
			"npu-2": {NumaNodes: []int{0, 1}},
		},
	}

	gpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
			"gpu-1": {NumaNodes: []int{1}},
			"gpu-2": {NumaNodes: []int{2}},
		},
	}

	xpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"xpu-0": {NumaNodes: []int{0}},
			"xpu-1": {NumaNodes: []int{1}},
			"xpu-2": {NumaNodes: nil},
		},
	}

	dpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"dpu-0": {NumaNodes: []int{1}},
			"dpu-1": {NumaNodes: []int{0}},
			"dpu-2": {NumaNodes: []int{}},
		},
	}

	// Register device topology providers
	registry := NewDeviceTopologyRegistry()
	registry.RegisterDeviceTopologyProvider("npu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("xpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("dpu", NewDeviceTopologyProviderStub())
	err := registry.SetDeviceTopology("npu", npuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("gpu", gpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("xpu", xpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("dpu", dpuTopology)
	assert.NoError(t, err)

	tests := []struct {
		name        string
		deviceA     string
		deviceB     string
		expected    map[string][]string
		expectedErr bool
	}{
		{
			name:    "npu to gpu affinity",
			deviceA: "npu",
			deviceB: "gpu",
			expected: map[string][]string{
				"npu-0": {"gpu-0"},
				"npu-1": {"gpu-1"},
				"npu-2": {},
			},
		},
		{
			name:        "non-existent device A",
			deviceA:     "invalid device",
			deviceB:     "gpu",
			expectedErr: true,
		},
		{
			name:        "non-existent device B",
			deviceA:     "npu",
			deviceB:     "invalid device",
			expectedErr: true,
		},
		{
			name:    "devices with empty numa nodes are not considered to have affinity with each other",
			deviceA: "xpu",
			deviceB: "dpu",
			expected: map[string][]string{
				"xpu-0": {"dpu-1"},
				"xpu-1": {"dpu-0"},
				"xpu-2": {},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := registry.GetDeviceNUMAAffinity(tt.deviceA, tt.deviceB)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				evaluateDeviceNUMAAffinity(t, actual, tt.expected)
			}
		})
	}
}

func TestDeviceTopology_GroupDeviceAffinity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		deviceTopology         *DeviceTopology
		expectedDeviceAffinity map[int][]DeviceIDs
		expectedNil            bool
	}{
		{
			name:        "no affinity groups when PriorityDimensions is empty",
			expectedNil: true,
			deviceTopology: &DeviceTopology{
				PriorityDimensions: nil,
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{Name: "pcie", Value: "0"}: {"npu-1"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{Name: "pcie", Value: "0"}: {"npu-0"},
						},
					},
				},
			},
		},
		{
			name: "test simple affinity of 2 devices to 1 group with only affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie"},
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{Name: "pcie", Value: "0"}: {"npu-1"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{Name: "pcie", Value: "0"}: {"npu-0"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{Name: "pcie", Value: "1"}: {"npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{Name: "pcie", Value: "1"}: {"npu-2"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[int][]DeviceIDs{
				0: {
					{"npu-0", "npu-1"}, {"npu-2", "npu-3"},
				},
			},
		},
		{
			name: "test simple affinity of 4 devices to 1 group with only affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-2", "npu-3"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1", "npu-2"},
						},
					},
					"npu-4": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-5", "npu-6", "npu-7"},
						},
					},
					"npu-5": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-4", "npu-6", "npu-7"},
						},
					},
					"npu-6": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-4", "npu-5", "npu-7"},
						},
					},
					"npu-7": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-4", "npu-5", "npu-6"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[int][]DeviceIDs{
				0: {
					{"npu-0", "npu-1", "npu-2", "npu-3"}, {"npu-4", "npu-5", "npu-6", "npu-7"},
				},
			},
		},
		{
			name: "device topology includes self for one affinity level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-2", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-2", "npu-3"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[int][]DeviceIDs{
				0: {
					{"npu-0", "npu-1"}, {"npu-2", "npu-3"},
				},
			},
		},
		{
			name: "test simple affinity of 2 devices to 1 group with 2 affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "pcie",
								Value: "0",
							}: {"npu-1"},
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "pcie",
								Value: "0",
							}: {"npu-0"},
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-2", "npu-3"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "pcie",
								Value: "1",
							}: {"npu-3"},
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "pcie",
								Value: "1",
							}: {"npu-2"},
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1", "npu-2"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[int][]DeviceIDs{
				0: {
					{"npu-0", "npu-1"}, {"npu-2", "npu-3"},
				},
				1: {
					{"npu-0", "npu-1", "npu-2", "npu-3"},
				},
			},
		},
		{
			name: "device topology includes self for 2 affinity levels",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "pcie",
								Value: "0",
							}: {"npu-0", "npu-1"},
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "pcie",
								Value: "0",
							}: {"npu-0", "npu-1"},
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "pcie",
								Value: "1",
							}: {"npu-2", "npu-3"},
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "pcie",
								Value: "1",
							}: {"npu-2", "npu-3"},
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-1", "npu-2", "npu-3"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[int][]DeviceIDs{
				0: {
					{"npu-0", "npu-1"}, {"npu-2", "npu-3"},
				},
				1: {
					{"npu-0", "npu-1", "npu-2", "npu-3"},
				},
			},
		},
		{
			name: "unsorted device topology has no effect on result",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-2", "npu-1", "npu-3"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-3", "npu-0", "npu-2"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-1", "npu-0", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "0",
							}: {"npu-0", "npu-2", "npu-1"},
						},
					},
					"npu-4": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-6", "npu-5", "npu-7"},
						},
					},
					"npu-5": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-7", "npu-4", "npu-6"},
						},
					},
					"npu-6": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-5", "npu-4", "npu-7"},
						},
					},
					"npu-7": {
						DeviceAffinity: map[Dimension]DeviceIDs{
							{
								Name:  "numa",
								Value: "1",
							}: {"npu-6", "npu-4", "npu-5"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[int][]DeviceIDs{
				0: {
					{"npu-0", "npu-1", "npu-2", "npu-3"}, {"npu-4", "npu-5", "npu-6", "npu-7"},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deviceAffinity := tt.deviceTopology.GroupDeviceAffinity()
			if tt.expectedNil {
				assert.Nil(t, deviceAffinity)
				return
			}
			evaluateDeviceAffinity(t, deviceAffinity, tt.expectedDeviceAffinity)
		})
	}
}

func evaluateDeviceNUMAAffinity(t *testing.T, expectedDeviceNUMAAffinity, actualDeviceNUMAAffinity map[string][]string) {
	if len(actualDeviceNUMAAffinity) != len(expectedDeviceNUMAAffinity) {
		t.Errorf("deviceNUMAAffinity lengths don't match, expected %d, got %d", len(expectedDeviceNUMAAffinity), len(actualDeviceNUMAAffinity))
		return
	}

	for device, expected := range expectedDeviceNUMAAffinity {
		actual, ok := actualDeviceNUMAAffinity[device]
		if !ok {
			t.Errorf("expected device numa affinity for device %v, but it is not found", device)
			return
		}

		assert.ElementsMatch(t, expected, actual, "device numa affinity are not equal")
	}
}

func evaluateDeviceAffinity(t *testing.T, expectedDeviceAffinity, actualDeviceAffinity map[int][]DeviceIDs) {
	if len(actualDeviceAffinity) != len(expectedDeviceAffinity) {
		t.Errorf("expected %d affinities, got %d", len(expectedDeviceAffinity), len(actualDeviceAffinity))
		return
	}

	for priority, expected := range expectedDeviceAffinity {
		actual, ok := actualDeviceAffinity[priority]
		if !ok {
			t.Errorf("expected affinities for priority %v, but it is not found", priority)
			return
		}

		if !equalDeviceIDsGroupsIgnoreOrder(t, expected, actual) {
			return
		}
	}
}

func equalDeviceIDsGroupsIgnoreOrder(t *testing.T, expected, actual []DeviceIDs) bool {
	if len(expected) != len(actual) {
		t.Errorf("expected %d devices, got %d", len(expected), len(actual))
		return false
	}

	// Convert each DeviceIDs slice into a normalized, comparable form
	normalize := func(groups []DeviceIDs) []string {
		res := make([]string, len(groups))
		for i, group := range groups {
			sorted := append([]string{}, group...)
			sort.Strings(sorted)
			res[i] = strings.Join(sorted, ",")
		}
		sort.Strings(res)
		return res
	}

	normalizedExp := normalize(expected)
	normalizedAct := normalize(actual)

	for i := range normalizedExp {
		if normalizedExp[i] != normalizedAct[i] {
			t.Errorf("expected %s, got %s", normalizedAct[i], normalizedExp[i])
			return false
		}
	}

	return true
}

func TestDeviceTopologyRegistry_runAffinityProviders(t *testing.T) {
	t.Parallel()

	stopCh := make(chan struct{})

	// Set up the device topology registry and register the affinity provider stub
	registry := NewDeviceTopologyRegistry()
	affinityProviderWithValidChannel := newAffinityProviderStub(false)
	registry.RegisterDeviceTopologyProvider("test", NewDeviceTopologyProviderStub())
	registry.RegisterTopologyAffinityProvider("test", affinityProviderWithValidChannel)
	registry.lastDeviceTopologies["test"] = &DeviceTopology{}

	affinityProviderWithNilChannel := newAffinityProviderStub(true)
	registry.RegisterDeviceTopologyProvider("test-nil-chan", NewDeviceTopologyProviderStub())
	registry.RegisterTopologyAffinityProvider("test-nil-chan", affinityProviderWithNilChannel)
	registry.lastDeviceTopologies["test-nil-chan"] = &DeviceTopology{}

	go registry.runAffinityProviders(stopCh)

	time.Sleep(50 * time.Millisecond) // small delay to ensure watcher is ready

	providerStub, ok := affinityProviderWithValidChannel.(*deviceAffinityProviderStub)
	assert.True(t, ok)

	// Trigger change
	providerStub.TriggerChange()

	time.Sleep(100 * time.Millisecond)

	assert.True(t, providerStub.WasSetCalled())

	providerStubWithNilChannel, ok := affinityProviderWithNilChannel.(*deviceAffinityProviderStub)
	assert.True(t, ok)

	providerStubWithNilChannel.TriggerChange()

	time.Sleep(100 * time.Millisecond)

	// nil channel should not have SetDeviceAffinity called
	assert.False(t, providerStubWithNilChannel.WasSetCalled())

	close(stopCh)
}

func TestDeviceTopologyRegistry_GetDeviceTopologies(t *testing.T) {
	t.Parallel()

	registry := NewDeviceTopologyRegistry()
	gpu1Provider := NewDeviceTopologyProviderStub()
	gpu2Provider := NewDeviceTopologyProviderStub()
	registry.RegisterDeviceTopologyProvider("gpu-1", gpu1Provider)
	registry.RegisterDeviceTopologyProvider("gpu-2", gpu2Provider)

	topo1 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"d1": {Health: "Unhealthy", NumaNodes: []int{0}},
			"d2": {Health: "Healthy", NumaNodes: []int{1}},
		},
		PriorityDimensions: []string{"NUMA"},
		UpdateTime:         100,
	}
	topo2 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"d1": {Health: "Healthy", NumaNodes: []int{0}},
			"d3": {Health: "Healthy", NumaNodes: []int{2}},
		},
		UpdateTime: 200,
	}

	_ = registry.SetDeviceTopology("gpu-1", topo1)
	_ = registry.SetDeviceTopology("gpu-2", topo2)

	tests := []struct {
		name        string
		deviceNames []string
		expectedLen int
		expectErr   bool
		checkHealth map[string]string
	}{
		{
			name:        "get topologies from two existing devices",
			deviceNames: []string{"gpu-1", "gpu-2"},
			expectedLen: 2, // both topo1 and topo2
		},
		{
			name:        "one device missing, pick existing one",
			deviceNames: []string{"gpu-1", "non-existent"},
			expectedLen: 1, // only topo1
		},
		{
			name:        "all devices missing",
			deviceNames: []string{"invalid-1", "invalid-2"},
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topologies, err := registry.GetDeviceTopologies(tt.deviceNames)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, topologies, tt.expectedLen)
			}
		})
	}
}

func TestDeviceInfo_GetDimensions(t *testing.T) {
	t.Parallel()

	deviceInfo := DeviceInfo{
		DeviceAffinity: map[Dimension]DeviceIDs{
			{
				Name:  "numa",
				Value: "0",
			}: {"npu-1"},
			{
				Name:  "",
				Value: "1",
			}: {"npu-2"},
			{
				Name:  "socket",
				Value: "",
			}: {"npu-3"},
			{
				Name:  "pcie",
				Value: "2",
			}: {"npu-4"},
		},
	}

	dimensions := deviceInfo.GetDimensions()
	assert.Len(t, dimensions, 2)
	assert.Equal(t, "numa", dimensions[0].Name)
	assert.Equal(t, "0", dimensions[0].Value)
	assert.Equal(t, "pcie", dimensions[1].Name)
	assert.Equal(t, "2", dimensions[1].Value)
}

func TestDeviceTopologyRegistry_GetLatestDeviceTopology(t *testing.T) {
	t.Parallel()

	registry := NewDeviceTopologyRegistry()
	gpu1Provider := NewDeviceTopologyProviderStub()
	gpu2Provider := NewDeviceTopologyProviderStub()
	registry.RegisterDeviceTopologyProvider("gpu-1", gpu1Provider)
	registry.RegisterDeviceTopologyProvider("gpu-2", gpu2Provider)

	topo1 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"d1": {Health: "Unhealthy", NumaNodes: []int{0}},
			"d2": {Health: "Healthy", NumaNodes: []int{1}},
		},
		PriorityDimensions: []string{"NUMA"},
		UpdateTime:         100,
	}
	topo2 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"d1": {Health: "Healthy", NumaNodes: []int{0}},
			"d3": {Health: "Healthy", NumaNodes: []int{2}},
		},
		UpdateTime: 200,
	}

	_ = registry.SetDeviceTopology("gpu-1", topo1)
	_ = registry.SetDeviceTopology("gpu-2", topo2)

	tests := []struct {
		name        string
		deviceNames []string
		expectedLen int
		expectErr   bool
		checkHealth map[string]string
	}{
		{
			name:        "pick latest from two existing devices",
			deviceNames: []string{"gpu-1", "gpu-2"},
			expectedLen: 2, // Only topo2.Devices (d1, d3)
			checkHealth: map[string]string{"d1": "Healthy", "d3": "Healthy"},
		},
		{
			name:        "one device missing, pick existing one",
			deviceNames: []string{"gpu-1", "non-existent"},
			expectedLen: 2, // Only topo1.Devices (d1, d2)
			checkHealth: map[string]string{"d1": "Unhealthy", "d2": "Healthy"},
		},
		{
			name:        "all devices missing",
			deviceNames: []string{"invalid-1", "invalid-2"},
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			latest, err := registry.GetLatestDeviceTopology(tt.deviceNames)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, latest.Devices, tt.expectedLen)
				for id, health := range tt.checkHealth {
					assert.Equal(t, health, latest.Devices[id].Health)
				}
			}
		})
	}
}
