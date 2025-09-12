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
	"os"
	"path/filepath"
	"testing"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGetReadonlyState(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	state, err := GetReadonlyState()
	if state == nil {
		as.NotNil(err)
	}
}

func TestGetWriteOnlyState(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	state, err := GetReadWriteState()
	if state == nil {
		as.NotNil(err)
	}
}

func TestTryMigrateStateSuccessful(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	inMemoryTmpDir := t.TempDir()

	stateDir := filepath.Join(tmpDir, "state")
	err := os.MkdirAll(stateDir, 0o775)
	assert.NoError(t, err)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.NoError(t, err)

	machineInfo := &info.MachineInfo{}
	reservedMemory := make(map[v1.ResourceName]map[int]uint64)
	defaultCache, err := NewMemoryPluginState(cpuTopology, machineInfo, reservedMemory)
	assert.NoError(t, err)

	policyName := "test-policy"
	checkpointName := "test-checkpoint"

	// create old checkpoint manager to save checkpoint
	oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
	assert.NoError(t, err)

	oldCheckpoint := NewMemoryPluginCheckpoint()
	oldCheckpoint.PolicyName = policyName

	podResourceEntries := PodResourceEntries{
		v1.ResourceMemory: PodEntries{
			"podUID": ContainerEntries{
				"testName": &AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "podUID",
						PodNamespace:   "testName",
						PodName:        "testName",
						ContainerName:  "testName",
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						QoSLevel:       consts.PodAnnotationQoSLevelDedicatedCores,
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
					},
					AggregatedQuantity:   9663676416,
					NumaAllocationResult: machine.NewCPUSet(0),
					TopologyAwareAllocations: map[int]uint64{
						0: 9663676416,
					},
				},
			},
		},
	}
	oldCheckpoint.PodResourceEntries = podResourceEntries

	err = oldCheckpointManager.CreateCheckpoint(checkpointName, oldCheckpoint)
	assert.NoError(t, err)

	// create a new checkpoint with a new checkpoint manager
	sc := &stateCheckpoint{
		policyName:          policyName,
		checkpointName:      checkpointName,
		cache:               defaultCache,
		skipStateCorruption: false,
		emitter:             metrics.DummyMetrics{},
	}

	// current checkpoint is pointing to the in memory directory
	sc.checkpointManager, err = checkpointmanager.NewCheckpointManager(inMemoryTmpDir)
	assert.NoError(t, err)

	newCheckpoint := NewMemoryPluginCheckpoint()
	err = sc.tryMigrateState(machineInfo, reservedMemory, inMemoryTmpDir, stateDir, true, newCheckpoint)
	assert.NoError(t, err)

	// check if new checkpoint is created and verify equality
	err = sc.checkpointManager.GetCheckpoint(checkpointName, newCheckpoint)
	assert.NoError(t, err)
	assert.Equal(t, newCheckpoint, oldCheckpoint)
}

func TestTryMigrateStateNoPreStop(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	inMemoryTmpDir := t.TempDir()

	stateDir := filepath.Join(tmpDir, "state")
	err := os.MkdirAll(stateDir, 0o775)
	assert.NoError(t, err)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.NoError(t, err)

	machineInfo := &info.MachineInfo{}
	reservedMemory := make(map[v1.ResourceName]map[int]uint64)
	defaultCache, err := NewMemoryPluginState(cpuTopology, machineInfo, reservedMemory)
	assert.NoError(t, err)

	policyName := "test-policy"
	checkpointName := "test-checkpoint"

	// create old checkpoint manager to save checkpoint
	oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
	assert.NoError(t, err)

	oldCheckpoint := NewMemoryPluginCheckpoint()
	oldCheckpoint.PolicyName = policyName
	podResourceEntries := PodResourceEntries{
		v1.ResourceMemory: PodEntries{
			"podUID": ContainerEntries{
				"testName": &AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "podUID",
						PodNamespace:   "testName",
						PodName:        "testName",
						ContainerName:  "testName",
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						QoSLevel:       consts.PodAnnotationQoSLevelDedicatedCores,
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
					},
					AggregatedQuantity:   9663676416,
					NumaAllocationResult: machine.NewCPUSet(0),
					TopologyAwareAllocations: map[int]uint64{
						0: 9663676416,
					},
				},
			},
		},
	}
	oldCheckpoint.PodResourceEntries = podResourceEntries

	err = oldCheckpointManager.CreateCheckpoint(checkpointName, oldCheckpoint)
	assert.NoError(t, err)

	// create a new checkpoint with a new checkpoint manager
	sc := &stateCheckpoint{
		policyName:          policyName,
		checkpointName:      checkpointName,
		cache:               defaultCache,
		skipStateCorruption: false,
		emitter:             metrics.DummyMetrics{},
	}

	// current checkpoint is pointing to the in memory directory
	sc.checkpointManager, err = checkpointmanager.NewCheckpointManager(inMemoryTmpDir)
	assert.NoError(t, err)

	newCheckpoint := NewMemoryPluginCheckpoint()
	// Migration stops prematurely when there is no pre-stop script
	err = sc.tryMigrateState(machineInfo, reservedMemory, inMemoryTmpDir, stateDir, false, newCheckpoint)
	assert.NoError(t, err)

	// check if new checkpoint is created and the two checkpoints should not be equal
	err = sc.checkpointManager.GetCheckpoint(checkpointName, newCheckpoint)
	assert.NoError(t, err)
	assert.NotEqual(t, newCheckpoint, oldCheckpoint)
}
