//go:build linux || darwin
// +build linux darwin

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

package resctrl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

func TestManagerImpl_Create(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "resctrl_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	m := &managerImpl{
		root: tmpDir,
		config: &qrm.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: true,
		},
	}
	m.enabled.Store(true)

	type args struct {
		podUID         string
		closID         string
		createMonGroup bool
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "create closID only",
			args: args{
				podUID:         "pod1",
				closID:         "shared-01",
				createMonGroup: false,
			},
			wantErr: false,
		},
		{
			name: "create closID and monGroup",
			args: args{
				podUID:         "pod2",
				closID:         "shared-02",
				createMonGroup: true,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := m.Create(tt.args.podUID, tt.args.closID, tt.args.createMonGroup); (err != nil) != tt.wantErr {
				t.Errorf("managerImpl.Create() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Verify directories
			closPath := filepath.Join(tmpDir, tt.args.closID)
			_, err := os.Stat(closPath)
			assert.NoError(t, err)

			if tt.args.createMonGroup {
				monPath := filepath.Join(closPath, MonGroupsDir, PodDirPrefix+tt.args.podUID)
				_, err := os.Stat(monPath)
				assert.NoError(t, err)
			}
		})
	}
}

func TestManagerImpl_Create_Disabled(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "resctrl_test_disabled")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	m := &managerImpl{
		root: tmpDir,
		config: &qrm.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: false,
		},
	}
	m.enabled.Store(true)

	err = m.Create("pod1", "shared-01", true)
	assert.NoError(t, err)

	// Verify directories should NOT exist
	closPath := filepath.Join(tmpDir, "shared-01")
	_, err = os.Stat(closPath)
	assert.True(t, os.IsNotExist(err), "closID dir should not exist when disabled")
}

func TestManagerImpl_Cleanup(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "resctrl_cleanup_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	m := &managerImpl{
		root: tmpDir,
		config: &qrm.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: true,
		},
	}
	m.enabled.Store(true)

	// Prepare directories
	activePod := "active"
	inactivePod := "inactive"

	// Create active
	err = m.Create(activePod, "shared-01", true)
	assert.NoError(t, err)

	// Create inactive
	err = m.Create(inactivePod, "shared-01", true)
	assert.NoError(t, err)

	// Create tasks file for inactive to make it "empty" (size 0)
	inactivePath := filepath.Join(tmpDir, "shared-01", MonGroupsDir, PodDirPrefix+inactivePod)
	// We need to ensure parent dirs exist because we might have skipped creation if logic was wrong,
	// but here we enabled it.
	err = os.MkdirAll(inactivePath, 0o755)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(inactivePath, tasks), []byte(""), 0o644)
	assert.NoError(t, err)

	// Create tasks file for active (not empty) to simulate running task?
	// But Cleanup checks activePodUIDs list first.
	activePath := filepath.Join(tmpDir, "shared-01", MonGroupsDir, PodDirPrefix+activePod)
	err = os.MkdirAll(activePath, 0o755)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(activePath, tasks), []byte(""), 0o644)
	assert.NoError(t, err)

	// Create empty closID
	err = m.Create("dummy", "shared-empty", false) // only closID
	assert.NoError(t, err)
	// Create empty tasks file in shared-empty
	err = os.WriteFile(filepath.Join(tmpDir, "shared-empty", tasks), []byte(""), 0o644)
	assert.NoError(t, err)

	// Run Cleanup
	activeUIDs := sets.NewString(activePod)
	err = m.Cleanup(activeUIDs)
	assert.NoError(t, err)

	// Verify
	// Active should exist
	_, err = os.Stat(activePath)
	assert.NoError(t, err)

	// Inactive should be gone
	_, err = os.Stat(inactivePath)
	assert.True(t, os.IsNotExist(err))

	// shared-empty should be gone
	_, err = os.Stat(filepath.Join(tmpDir, "shared-empty"))
	assert.True(t, os.IsNotExist(err))

	// shared-01 should exist
	_, err = os.Stat(filepath.Join(tmpDir, "shared-01"))
	assert.NoError(t, err)
}

func TestManagerImpl_Cleanup_Disabled(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "resctrl_cleanup_disabled_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	m := &managerImpl{
		root: tmpDir,
		config: &qrm.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: false,
		},
	}
	m.enabled.Store(true)

	// Manually create a directory that would be cleaned up if enabled
	inactivePath := filepath.Join(tmpDir, "shared-01", MonGroupsDir, PodDirPrefix+"inactive")
	err = os.MkdirAll(inactivePath, 0o755)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(inactivePath, tasks), []byte(""), 0o644)
	assert.NoError(t, err)

	// Run Cleanup
	activeUIDs := sets.NewString()
	err = m.Cleanup(activeUIDs)
	assert.NoError(t, err)

	// Verify inactive should STILL exist because cleanup is disabled
	_, err = os.Stat(inactivePath)
	assert.NoError(t, err, "inactive pod dir should still exist when cleanup is disabled")
}

func TestManagerImpl_GetMonGroupsCount(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "resctrl_count_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	m := &managerImpl{
		root: tmpDir,
		config: &qrm.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: true,
		},
	}
	m.enabled.Store(true)

	// Create some groups
	m.Create("pod1", "shared-01", true)
	m.Create("pod2", "shared-01", true)
	m.Create("pod3", "shared-02", true)
	m.Create("pod4", "shared-02", false) // no mon group

	count, err := m.GetMonGroupsCount()
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count) // pod1, pod2, pod3
}
