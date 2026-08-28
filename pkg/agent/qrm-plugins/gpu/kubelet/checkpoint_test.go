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

package kubelet

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	cpmerrors "k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
	"k8s.io/kubernetes/pkg/kubelet/cm/devicemanager/checkpoint"
)

type mockCheckpointManager struct {
	checkpoint checkpoint.DeviceManagerCheckpoint
	err        error
}

func (m *mockCheckpointManager) GetCheckpoint(_ string, cp checkpointmanager.Checkpoint) error {
	if m.err != nil {
		return m.err
	}
	if m.checkpoint != nil {
		data, err := m.checkpoint.MarshalCheckpoint()
		if err != nil {
			return err
		}
		return cp.UnmarshalCheckpoint(data)
	}
	return nil
}

func (m *mockCheckpointManager) CreateCheckpoint(_ string, _ checkpointmanager.Checkpoint) error {
	return nil
}

func (m *mockCheckpointManager) RemoveCheckpoint(_ string) error {
	return nil
}

func (m *mockCheckpointManager) ListCheckpoints() ([]string, error) {
	return nil, nil
}

func TestReadAllocations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		manager checkpointmanager.CheckpointManager
		want    []Allocation
		wantErr bool
	}{
		{
			name:    "nil manager",
			wantErr: true,
		},
		{
			name:    "checkpoint not found",
			manager: &mockCheckpointManager{err: cpmerrors.ErrCheckpointNotFound},
			want:    []Allocation{},
		},
		{
			name:    "checkpoint read error",
			manager: &mockCheckpointManager{err: fmt.Errorf("read failed")},
			wantErr: true,
		},
		{
			name: "filters resources and deduplicates devices",
			manager: &mockCheckpointManager{checkpoint: checkpoint.New([]checkpoint.PodDevicesEntry{
				{
					PodUID:        "pod-uid",
					ContainerName: "container",
					ResourceName:  "gpu.example.com/device",
					DeviceIDs:     checkpoint.DevicesPerNUMA{0: {"gpu-0", "gpu-0", "gpu-1"}},
				},
				{
					PodUID:        "pod-uid",
					ContainerName: "container",
					ResourceName:  "other.example.com/device",
					DeviceIDs:     checkpoint.DevicesPerNUMA{0: {"other-0"}},
				},
			}, nil)},
			want: []Allocation{{
				PodUID:        "pod-uid",
				ContainerName: "container",
				ResourceName:  "gpu.example.com/device",
				DeviceIDs:     []string{"gpu-0", "gpu-1"},
			}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ReadAllocations(tt.manager, sets.NewString("gpu.example.com/device"))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
