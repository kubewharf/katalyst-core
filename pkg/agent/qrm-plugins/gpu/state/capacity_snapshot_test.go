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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetResourceCapacityState(t *testing.T) {
	t.Parallel()
	for _, checkpoint := range []bool{false, true} {
		checkpoint := checkpoint
		name := "memory"
		if checkpoint {
			name = "checkpoint"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			memory := &gpuPluginState{machineState: AllocationResourcesMap{
				"gpu_device": {
					"gpu-0":    {Allocatable: 1, PodEntries: PodEntries{"owner": {"main": &AllocationInfo{}}}},
					"gpu-zero": {Allocatable: 0},
					"gpu-nil":  nil,
				},
				"other-resource": {"other-device": {Allocatable: 100}},
			}}
			var reader ReadonlyState = memory
			if checkpoint {
				reader = &stateCheckpoint{cache: memory}
			}

			snapshot := reader.GetResourceCapacityState("gpu_device")
			require.Len(t, snapshot, 2)
			require.Equal(t, float64(1), snapshot["gpu-0"].Allocatable)
			require.Zero(t, snapshot["gpu-zero"].Allocatable)
			require.Nil(t, snapshot["gpu-0"].PodEntries)
			require.Nil(t, reader.GetResourceCapacityState("missing-resource"))

			// Neither the returned device objects nor the map alias internal state.
			snapshot["gpu-0"].Allocatable = 99
			delete(snapshot, "gpu-zero")
			require.Equal(t, float64(1), memory.machineState["gpu_device"]["gpu-0"].Allocatable)
			require.Contains(t, memory.machineState["gpu_device"], "gpu-zero")
			require.Contains(t, memory.machineState["gpu_device"]["gpu-0"].PodEntries, "owner")
		})
	}
}

func TestGetResourceCapacityStateNilCheckpoint(t *testing.T) {
	t.Parallel()
	var checkpoint *stateCheckpoint
	require.Nil(t, checkpoint.GetResourceCapacityState("gpu_device"))
}
