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

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestNewNetworkPluginCheckpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		expectEqual bool
		corruptFile bool
	}{
		{
			name:        "successful migration with pre-stop",
			expectEqual: true,
			corruptFile: false,
		},
		{
			name:        "corrupted checkpoint",
			expectEqual: false,
			corruptFile: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			inMemoryTmpDir := t.TempDir()

			stateDir := filepath.Join(tmpDir, "state")
			err := os.Mkdir(stateDir, 0o775)
			assert.NoError(t, err)
			defer os.RemoveAll(stateDir)

			qrmConfig := &qrm.QRMPluginsConfiguration{
				CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
					ReservedCPUCores: 4,
					CPUDynamicPolicyConfig: qrm.CPUDynamicPolicyConfig{
						EnableReserveCPUReversely: true,
					},
				},
			}
			machineInfo := &info.MachineInfo{}
			nics := make([]machine.InterfaceInfo, 0)
			reservedBandwidth := make(map[string]uint32)

			policyName := "test-policy"
			checkpointName := "test-checkpoint"

			// create old checkpoint manager to save the checkpoint
			oldCheckpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
			assert.NoError(t, err)

			oldCheckpoint := NewNetworkPluginCheckpoint()
			if tt.corruptFile {
				// create a corrupted old checkpoint
				corruptedFile := filepath.Join(stateDir, fmt.Sprintf("%s", checkpointName))
				err = ioutil.WriteFile(corruptedFile, []byte("corrupted data"), 0o644)
				assert.NoError(t, err)
			} else {
				oldCheckpoint.PolicyName = policyName
				podEntries := PodEntries{
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
							NumaNodes: machine.NewCPUSet(0),
						},
					},
				}
				oldCheckpoint.PodEntries = podEntries
				generatedNetworkState, err := GenerateMachineStateFromPodEntries(qrmConfig, nics, podEntries, reservedBandwidth)
				assert.NoError(t, err)
				oldCheckpoint.MachineState = generatedNetworkState
				err = oldCheckpointManager.CreateCheckpoint(checkpointName, oldCheckpoint)
				assert.NoError(t, err)
			}

			stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
				StateFileDirectory:         stateDir,
				InMemoryStateFileDirectory: inMemoryTmpDir,
				EnableInMemoryState:        true,
			}

			state, err := NewCheckpointState(qrmConfig, stateDirectoryConfig, checkpointName, policyName, machineInfo, nics, reservedBandwidth,
				false, metrics.DummyMetrics{})

			if tt.corruptFile {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			sc, ok := state.(*stateCheckpoint)
			assert.True(t, ok)

			newCheckpoint := NewNetworkPluginCheckpoint()

			// check if new checkpoint is created and verify equality
			err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, newCheckpoint)
			assert.NoError(t, err)

			// verify old checkpoint file existence
			checkpoint := NewNetworkPluginCheckpoint()
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
