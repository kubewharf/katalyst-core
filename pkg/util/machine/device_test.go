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

func TestDeviceTopologyRegistry_GetAffinityDevices(t *testing.T) {
	t.Parallel()

	npuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"npu-0": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "0"}}: {},
				},
			},
			"npu-1": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "1"}}: {},
					{PriorityLevel: 1, Dimension: Dimension{Name: "numa", Value: "0"}}:   {},
				},
			},
			"npu-2": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "0"}}: {},
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "1"}}: {},
				},
			},
		},
	}

	gpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "0"}}: {},
					{PriorityLevel: 1, Dimension: Dimension{Name: "numa", Value: "0"}}:   {},
				},
			},
			"gpu-1": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "1"}}: {},
					{PriorityLevel: 1, Dimension: Dimension{Name: "numa", Value: "0"}}:   {},
				},
			},
			"gpu-2": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "2"}}: {},
					{PriorityLevel: 1, Dimension: Dimension{Name: "numa", Value: "1"}}:   {},
				},
			},
		},
	}

	xpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"xpu-0": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "0"}}: {},
				},
			},
			"xpu-1": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "1"}}: {},
				},
			},
			"xpu-2": {
				DeviceAffinity: nil,
			},
		},
	}

	dpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"dpu-0": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "1"}}: {},
				},
			},
			"dpu-1": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "socket", Value: "0"}}: {},
				},
			},
			"dpu-2": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{},
			},
		},
	}

	// Topologies with disjoint affinity dimensions to ensure no cross-device affinity
	apuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"apu-0": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "pcie", Value: "0"}}: {},
				},
			},
			"apu-1": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "pcie", Value: "1"}}: {},
				},
			},
		},
	}

	bpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"bpu-0": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "fabric", Value: "0"}}: {},
				},
			},
			"bpu-1": {
				DeviceAffinity: map[AffinityPriority]DeviceIDs{
					{PriorityLevel: 0, Dimension: Dimension{Name: "fabric", Value: "1"}}: {},
				},
			},
		},
	}

	// Register device topology providers
	registry := NewDeviceTopologyRegistry()
	registry.RegisterDeviceTopologyProvider("npu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("xpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("dpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("apu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("bpu", NewDeviceTopologyProviderStub())
	err := registry.SetDeviceTopology("npu", npuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("gpu", gpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("xpu", xpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("dpu", dpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("apu", apuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("bpu", bpuTopology)
	assert.NoError(t, err)

	tests := []struct {
		name        string
		deviceA     string
		deviceB     string
		expected    map[string]map[int][]string
		expectedErr bool
	}{
		{
			name:    "npu to gpu affinity",
			deviceA: "npu",
			deviceB: "gpu",
			expected: map[string]map[int][]string{
				"npu-0": {0: {"gpu-0"}},
				"npu-1": {0: {"gpu-1"}, 1: {"gpu-0", "gpu-1"}},
				"npu-2": {0: {"gpu-0", "gpu-1"}},
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
			name:    "devices with empty affinity are not considered to have affinity with each other",
			deviceA: "xpu",
			deviceB: "dpu",
			expected: map[string]map[int][]string{
				"xpu-0": {0: {"dpu-1"}},
				"xpu-1": {0: {"dpu-0"}},
			},
		},
		{
			name:     "no matching affinity returns empty map",
			deviceA:  "apu",
			deviceB:  "bpu",
			expected: map[string]map[int][]string{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := registry.GetAffinityDevices(tt.deviceA, tt.deviceB)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				evaluateDeviceAffinityMap(t, tt.expected, actual)
			}
		})
	}
}

func TestDeviceTopology_GroupDeviceAffinity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		deviceTopology         *DeviceTopology
		expectedDeviceAffinity [][]DeviceIDs
		expectedNil            bool
	}{
		{
			name:        "no affinity groups when PriorityDimensions is empty",
			expectedNil: true,
			deviceTopology: &DeviceTopology{
				PriorityDimensions: nil,
				Devices: map[string]DeviceInfo{
					"npu-0": {
						Dimensions: map[string]string{"pcie": "0"},
					},
					"npu-1": {
						Dimensions: map[string]string{"pcie": "0"},
					},
				},
			},
		},
		{
			name: "test simple affinity of 2 devices to 1 group with only affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"pcie": "0"}},
					"npu-1": {Dimensions: map[string]string{"pcie": "0"}},
					"npu-2": {Dimensions: map[string]string{"pcie": "1"}},
					"npu-3": {Dimensions: map[string]string{"pcie": "1"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1"}, {"npu-2", "npu-3"}},
			},
		},
		{
			name: "test simple affinity of 4 devices to 1 group with only affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"numa": "0"}},
					"npu-3": {Dimensions: map[string]string{"numa": "0"}},
					"npu-4": {Dimensions: map[string]string{"numa": "1"}},
					"npu-5": {Dimensions: map[string]string{"numa": "1"}},
					"npu-6": {Dimensions: map[string]string{"numa": "1"}},
					"npu-7": {Dimensions: map[string]string{"numa": "1"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1", "npu-2", "npu-3"}, {"npu-4", "npu-5", "npu-6", "npu-7"}},
			},
		},
		{
			name: "device topology includes self for one affinity level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"numa": "1"}},
					"npu-3": {Dimensions: map[string]string{"numa": "1"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1"}, {"npu-2", "npu-3"}},
			},
		},
		{
			name: "test simple affinity of 2 devices to 1 group with 2 affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"pcie": "0", "numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"pcie": "0", "numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"pcie": "1", "numa": "0"}},
					"npu-3": {Dimensions: map[string]string{"pcie": "1", "numa": "0"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1"}, {"npu-2", "npu-3"}},
				{{"npu-0", "npu-1", "npu-2", "npu-3"}},
			},
		},
		{
			name: "device topology includes self for 2 affinity levels",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"pcie": "0", "numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"pcie": "0", "numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"pcie": "1", "numa": "0"}},
					"npu-3": {Dimensions: map[string]string{"pcie": "1", "numa": "0"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1"}, {"npu-2", "npu-3"}},
				{{"npu-0", "npu-1", "npu-2", "npu-3"}},
			},
		},
		{
			name: "unsorted device topology has no effect on result",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"numa": "0"}},
					"npu-3": {Dimensions: map[string]string{"numa": "0"}},
					"npu-4": {Dimensions: map[string]string{"numa": "1"}},
					"npu-5": {Dimensions: map[string]string{"numa": "1"}},
					"npu-6": {Dimensions: map[string]string{"numa": "1"}},
					"npu-7": {Dimensions: map[string]string{"numa": "1"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1", "npu-2", "npu-3"}, {"npu-4", "npu-5", "npu-6", "npu-7"}},
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

func evaluateDeviceAffinityMap(t *testing.T, expected map[string]map[int][]string, actual map[string]DeviceAffinity) {
	if len(actual) != len(expected) {
		t.Errorf("deviceAffinity lengths don't match, expected %d, got %d", len(expected), len(actual))
		return
	}

	for device, expectedAffinity := range expected {
		affinityByPriority, ok := actual[device]
		if !ok {
			t.Errorf("expected device affinity for device %v, but it is not found", device)
			return
		}

		// Flatten actual DeviceAffinity (grouped by AffinityPriority) into priority->deviceIDs map
		flattened := make(map[int][]string)
		for ap, ids := range affinityByPriority {
			p := ap.GetPriorityLevel()
			flattened[p] = append(flattened[p], ids...)
		}

		for priority, expectedDevices := range expectedAffinity {
			actualDevices, ok := flattened[priority]
			if !ok {
				t.Errorf("expected affinity for priority %d for device %s, but it is not found", priority, device)
				return
			}
			assert.ElementsMatch(t, expectedDevices, actualDevices, "device affinity devices are not equal for device %s priority %d", device, priority)
		}
	}
}

func evaluateDeviceAffinity(t *testing.T, expectedDeviceAffinity, actualDeviceAffinity [][]DeviceIDs) {
	if len(actualDeviceAffinity) != len(expectedDeviceAffinity) {
		t.Errorf("expected %d affinities, got %d", len(expectedDeviceAffinity), len(actualDeviceAffinity))
		return
	}

	for priority := range expectedDeviceAffinity {
		if !equalDeviceIDsGroupsIgnoreOrder(t, expectedDeviceAffinity[priority], actualDeviceAffinity[priority]) {
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
		Dimensions: DeviceDimensions{
			"numa":   "0",
			"":       "1",
			"socket": "",
			"pcie":   "2",
		},
	}

	dimensions := deviceInfo.GetDimensions()
	// GetDimensions currently returns the raw DeviceDimensions map without
	// additional filtering or ordering. Verify that behavior here.
	assert.Equal(t, deviceInfo.Dimensions, dimensions)
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

func TestDeviceAffinity_GetDeviceIDsByPriorityLevel(t *testing.T) {
	t.Parallel()

	ap := func(level int, name, value string) AffinityPriority {
		return AffinityPriority{PriorityLevel: level, Dimension: Dimension{Name: name, Value: value}}
	}

	tests := []struct {
		name     string
		affinity DeviceAffinity
		expected map[int][]string
	}{
		{
			name:     "empty map returns empty result",
			affinity: DeviceAffinity{},
			expected: map[int][]string{},
		},
		{
			name:     "nil map returns empty result",
			affinity: nil,
			expected: map[int][]string{},
		},
		{
			name: "single level simple",
			affinity: DeviceAffinity{
				ap(0, "socket", "0"): {"gpu-1", "gpu-2"},
			},
			expected: map[int][]string{
				0: {"gpu-1", "gpu-2"},
			},
		},
		{
			name: "same level merges and deduplicates",
			affinity: DeviceAffinity{
				ap(0, "socket", "0"): {"dev-a", "dev-b"},
				ap(0, "numa", "0"):   {"dev-b", "dev-c"},
			},
			expected: map[int][]string{
				0: {"dev-a", "dev-b", "dev-c"},
			},
		},
		{
			name: "multiple levels with overlaps",
			affinity: DeviceAffinity{
				ap(0, "socket", "1"): {"d0"},
				ap(1, "numa", "0"):   {"d1", "d2"},
				ap(1, "fabric", "x"): {"d2", "d3"},
			},
			expected: map[int][]string{
				0: {"d0"},
				1: {"d1", "d2", "d3"},
			},
		},
		{
			name: "input lists contain duplicates",
			affinity: DeviceAffinity{
				ap(2, "group", "g"):  {"x", "x", "y"},
				ap(2, "group", "g2"): {"y", "z", "z"},
			},
			expected: map[int][]string{
				2: {"x", "y", "z"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.affinity.GetDeviceIDsByPriorityLevel()

			// Compare keys
			assert.Equal(t, len(tt.expected), len(got))
			for lvl, expIDs := range tt.expected {
				actIDs, ok := got[lvl]
				if !ok {
					t.Fatalf("missing level %d in result", lvl)
				}
				assert.ElementsMatch(t, expIDs, []string(actIDs))
			}
		})
	}
}
