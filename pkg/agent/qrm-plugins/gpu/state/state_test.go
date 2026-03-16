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
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestStateMemMachineStateSyncNotifiers(t *testing.T) {
	t.Parallel()

	s := &gpuPluginState{
		machineState: make(AllocationResourcesMap),
	}

	callCount := 0
	s.AddMachineStateSyncNotifier(func() {
		callCount++
	})

	// Test SetMachineState
	newMachineState := AllocationResourcesMap{
		"testResource": {
			"device1": &AllocationState{Allocatable: 1},
		},
	}
	s.SetMachineState(newMachineState, false)
	assert.Equal(t, 1, callCount)

	s.SetMachineState(newMachineState, false)
	assert.Equal(t, 2, callCount) // Unconditional trigger

	// Test SetResourceState
	s.SetResourceState("testResource2", AllocationMap{"device2": &AllocationState{Allocatable: 2}}, false)
	assert.Equal(t, 3, callCount)

	// Test ClearState
	s.ClearState()
	assert.Equal(t, 4, callCount)
}

func TestAllocationResourcesMap_GetRatioOfAccompanyResourceToTargetResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		accompanyResourceName string
		targetResourceName    string
		arm                   AllocationResourcesMap
		want                  float64
	}{
		{
			name:                  "normal case",
			accompanyResourceName: "accompanyResource",
			targetResourceName:    "targetResource",
			arm: AllocationResourcesMap{
				v1.ResourceName("accompanyResource"): {
					"accompany1": {},
					"accompany2": {},
					"accompany3": {},
					"accompany4": {},
				},
				v1.ResourceName("targetResource"): {
					"target1": {},
					"target2": {},
				},
			},
			want: 2.0,
		},
		{
			name:                  "got a ratio that is a fraction",
			accompanyResourceName: "accompanyResource",
			targetResourceName:    "targetResource",
			arm: AllocationResourcesMap{
				v1.ResourceName("accompanyResource"): {
					"accompany1": {},
					"accompany2": {},
				},
				v1.ResourceName("targetResource"): {
					"target1": {},
					"target2": {},
					"target3": {},
					"target4": {},
				},
			},
			want: 0.5,
		},
		{
			name:                  "no devices for target resource",
			accompanyResourceName: "accompanyResource",
			targetResourceName:    "targetResource",
			arm: AllocationResourcesMap{
				v1.ResourceName("accompanyResource"): {
					"accompany1": {},
					"accompany2": {},
				},
			},
			want: 0,
		},
		{
			name:                  "no devices for accompany resource and target resource",
			accompanyResourceName: "accompanyResource",
			targetResourceName:    "targetResource",
			arm:                   AllocationResourcesMap{},
			want:                  0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.arm.GetRatioOfAccompanyResourceToTargetResource(tt.accompanyResourceName, tt.targetResourceName)
			if got != tt.want {
				t.Errorf("GetRatioOfAccompanyResourceToTargetResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewGPUPluginCheckpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		corruptFile bool
		expectEqual bool
	}{
		{
			name:        "successful migration with pre-stop",
			corruptFile: false,
			expectEqual: true,
		},
		{
			name:        "corrupted checkpoint",
			corruptFile: true,
			expectEqual: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			inMemoryTmpDir := t.TempDir()

			stateDir := filepath.Join(tmpDir, "state")
			err := os.MkdirAll(stateDir, 0o775)
			assert.NoError(t, err)

			stateGenerator := NewDefaultResourceStateGeneratorStub()

			policyName := "test-policy"
			checkpointName := "test-checkpoint"

			// create old checkpoint manager to save the checkpoint
			oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
			assert.NoError(t, err)

			oldCheckpoint := NewGPUPluginCheckpoint()
			if tt.corruptFile {
				// create a corrupted old checkpoint
				corruptedFile := filepath.Join(stateDir, fmt.Sprintf("%s", checkpointName))
				err = ioutil.WriteFile(corruptedFile, []byte("corrupted data"), 0o644)
				assert.NoError(t, err)
			} else {
				oldCheckpoint.PolicyName = policyName
				am, err := stateGenerator.GenerateDefaultResourceState()
				assert.NoError(t, err)

				oldCheckpoint.MachineState = map[v1.ResourceName]AllocationMap{
					"gpu": am,
				}
				oldCheckpoint.PodResourceEntries = PodResourceEntries{
					"gpu": {
						"pod0": {
							"container0": {
								AllocatedAllocation: Allocation{
									Quantity:  10,
									NUMANodes: []int{0},
								},
							},
						},
					},
				}
				err = oldCheckpointManager.CreateCheckpoint(checkpointName, oldCheckpoint)
				assert.NoError(t, err)
			}

			stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
				StateFileDirectory:         stateDir,
				InMemoryStateFileDirectory: inMemoryTmpDir,
				EnableInMemoryState:        true,
			}

			registry := NewDefaultResourceStateGeneratorRegistry()
			registry.RegisterResourceStateGenerator("gpu", stateGenerator)

			state, err := NewCheckpointState(stateDirectoryConfig, nil, checkpointName, policyName, registry, false, metrics.DummyMetrics{})

			if tt.corruptFile {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			sc, ok := state.(*stateCheckpoint)
			assert.True(t, ok)

			newCheckpoint := NewGPUPluginCheckpoint()

			// check if new checkpoint is created and verify equality
			err = sc.checkpointManager.GetCheckpoint(checkpointName, newCheckpoint)
			assert.NoError(t, err)

			// verify old checkpoint file existence
			checkpoint := NewGPUPluginCheckpoint()
			err = oldCheckpointManager.GetCheckpoint(checkpointName, checkpoint)

			assert.Error(t, err)
			assert.Equal(t, err, errors.ErrCheckpointNotFound)

			if tt.expectEqual {
				assert.Equal(t, newCheckpoint, oldCheckpoint)
			} else {
				assert.NotEqual(t, newCheckpoint, oldCheckpoint)
			}
		})
	}
}

func TestAllocationResourcesMap_GetAllocatedDeviceIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		resourceName  v1.ResourceName
		podUID        string
		containerName string
		arm           AllocationResourcesMap
		wantIDs       []string
	}{
		{
			name:          "normal two devices",
			resourceName:  v1.ResourceName("nvidia.com/gpu"),
			podUID:        "pod-1",
			containerName: "ctr-1",
			arm: AllocationResourcesMap{
				v1.ResourceName("nvidia.com/gpu"): AllocationMap{
					"devA": &AllocationState{PodEntries: PodEntries{
						"pod-1": ContainerEntries{
							"ctr-1": &AllocationInfo{},
						},
					}},
					"devB": &AllocationState{PodEntries: PodEntries{
						"pod-1": ContainerEntries{
							"ctr-1": &AllocationInfo{},
						},
					}},
				},
			},
			wantIDs: []string{"devA", "devB"},
		},
		{
			name:          "resource missing",
			resourceName:  v1.ResourceName("nvidia.com/gpu"),
			podUID:        "pod-1",
			containerName: "ctr-1",
			arm: AllocationResourcesMap{
				v1.ResourceName("another.resource"): AllocationMap{
					"devX": &AllocationState{PodEntries: PodEntries{
						"pod-1": ContainerEntries{
							"ctr-1": &AllocationInfo{},
						},
					}},
				},
			},
			wantIDs: []string{},
		},
		{
			name:          "nil states and mismatches are ignored",
			resourceName:  v1.ResourceName("example.com/device"),
			podUID:        "pod-1",
			containerName: "ctr-1",
			arm: AllocationResourcesMap{
				v1.ResourceName("example.com/device"): AllocationMap{
					// nil AllocationState
					"nilState": nil,
					// nil PodEntries
					"nilEntries": &AllocationState{},
					// different podUID
					"otherPod": &AllocationState{PodEntries: PodEntries{
						"pod-2": ContainerEntries{
							"ctr-1": &AllocationInfo{},
						},
					}},
					// container present but nil AllocationInfo
					"nilAllocInfo": &AllocationState{PodEntries: PodEntries{
						"pod-1": ContainerEntries{
							"ctr-1": nil,
						},
					}},
					// valid match
					"match": &AllocationState{PodEntries: PodEntries{
						"pod-1": ContainerEntries{
							"ctr-1": &AllocationInfo{},
						},
					}},
				},
			},
			wantIDs: []string{"match"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.arm.GetAllocatedDeviceIDs(tt.resourceName, tt.podUID, tt.containerName)

			// Compare as sets since map iteration order is nondeterministic
			assert.Equal(t, len(tt.wantIDs), len(got))
			wantSet := map[string]struct{}{}
			for _, id := range tt.wantIDs {
				wantSet[id] = struct{}{}
			}
			for _, id := range got {
				if _, ok := wantSet[id]; !ok {
					t.Fatalf("unexpected id %q in result %v, want %v", id, got, tt.wantIDs)
				}
			}
		})
	}
}
