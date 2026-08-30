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

package dynamicpolicy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCheckpointWriteFailure(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	t.Run("Allocate", func(t *testing.T) {
		t.Parallel()

		checkpointDir := t.TempDir()
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, checkpointDir)
		require.NoError(t, err)

		req := newCPUCheckpointTestRequest()
		restore := blockCPUCheckpointWrites(t, filepath.Join(checkpointDir, cpuPluginStateFileName))

		resp, err := policy.Allocate(context.Background(), req)
		require.Error(t, err)
		require.Nil(t, resp)

		restore()
		_, err = policy.Allocate(context.Background(), req)
		require.NoError(t, err)

		restoredPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, checkpointDir)
		require.NoError(t, err)
		require.NotNil(t, restoredPolicy.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	})

	t.Run("RemovePod", func(t *testing.T) {
		t.Parallel()

		checkpointDir := t.TempDir()
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, checkpointDir)
		require.NoError(t, err)

		req := newCPUCheckpointTestRequest()
		_, err = policy.Allocate(context.Background(), req)
		require.NoError(t, err)

		restore := blockCPUCheckpointWrites(t, filepath.Join(checkpointDir, cpuPluginStateFileName))
		removeReq := &pluginapi.RemovePodRequest{PodUid: req.PodUid}
		resp, err := policy.RemovePod(context.Background(), removeReq)
		require.Error(t, err)
		require.Nil(t, resp)

		restore()
		_, err = policy.RemovePod(context.Background(), removeReq)
		require.NoError(t, err)

		restoredPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, checkpointDir)
		require.NoError(t, err)
		require.Nil(t, restoredPolicy.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	})
}

func newCPUCheckpointTestRequest() *pluginapi.ResourceRequest {
	return &pluginapi.ResourceRequest{
		PodUid:        "checkpoint-pod",
		PodNamespace:  "default",
		PodName:       "checkpoint-pod",
		ContainerName: "container",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}
}

func blockCPUCheckpointWrites(t *testing.T, checkpointPath string) func() {
	t.Helper()

	checkpoint, err := os.ReadFile(checkpointPath)
	require.NoError(t, err)
	info, err := os.Stat(checkpointPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(checkpointPath))
	require.NoError(t, os.Mkdir(checkpointPath, 0o755))

	restored := false
	restore := func() {
		if restored {
			return
		}
		require.NoError(t, os.RemoveAll(checkpointPath))
		require.NoError(t, os.WriteFile(checkpointPath, checkpoint, info.Mode().Perm()))
		restored = true
	}
	t.Cleanup(restore)
	return restore
}
