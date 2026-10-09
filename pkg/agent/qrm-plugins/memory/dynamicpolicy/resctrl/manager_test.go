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
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
)

type fakeClosInvalidator struct {
	closIDs []string
}

func (f *fakeClosInvalidator) InvalidateClos(closID string) {
	f.closIDs = append(f.closIDs, closID)
}

type fakeClosLifecycle struct {
	mu            sync.Mutex
	closIDs       []string
	afterUpdate   func()
	lifecycleRuns int
}

func (f *fakeClosLifecycle) InvalidateClos(closID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closIDs = append(f.closIDs, closID)
}

func (f *fakeClosLifecycle) RunClosLifecycle(closID string, update func() (bool, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lifecycleRuns++
	changed, err := update()
	if err != nil {
		return err
	}
	if f.afterUpdate != nil {
		f.afterUpdate()
	}
	if changed {
		f.closIDs = append(f.closIDs, closID)
	}
	return nil
}

func TestManagerInvalidatesClosOnCreateAndRemove(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	invalidator := &fakeClosInvalidator{}
	manager := NewManager(&qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	}, invalidator).(*managerImpl)
	manager.root = tmpDir
	manager.enabled.Store(true)

	require.NoError(t, manager.Create("", "shared", false))
	require.Equal(t, []string{"shared"}, invalidator.closIDs)

	require.NoError(t, manager.removeClosLocked("shared", filepath.Join(tmpDir, "shared")))
	require.Equal(t, []string{"shared", "shared"}, invalidator.closIDs)
}

func TestManagerRunsCreateAndRemoveInsideClosLifecycle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lifecycle := &fakeClosLifecycle{}
	manager := NewManager(&qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	}, lifecycle).(*managerImpl)
	manager.root = root
	manager.enabled.Store(true)

	lifecycle.afterUpdate = func() {
		require.DirExists(t, filepath.Join(root, "shared"))
	}
	require.NoError(t, manager.Create("", "shared", false))

	lifecycle.afterUpdate = func() {
		require.NoDirExists(t, filepath.Join(root, "shared"))
	}
	manager.Lock()
	err := manager.removeClosLocked("shared", filepath.Join(root, "shared"))
	manager.Unlock()
	require.NoError(t, err)
	require.Equal(t, 2, lifecycle.lifecycleRuns)
	require.Equal(t, []string{"shared", "shared"}, lifecycle.closIDs)
}

func TestManagerLifecycleErrorDoesNotBumpEpochOrInvalidate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lifecycle := &fakeClosLifecycle{}
	manager := NewManager(&qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	}, lifecycle).(*managerImpl)
	manager.root = root
	manager.enabled.Store(true)
	removeErr := errors.New("remove failed")
	manager.removeAll = func(string) error { return removeErr }

	manager.Lock()
	err := manager.removeClosLocked("shared", filepath.Join(root, "shared"))
	manager.Unlock()
	require.ErrorIs(t, err, removeErr)
	require.Empty(t, lifecycle.closIDs)
}

func TestManagerLifecycleDoesNotReenterInvalidatorLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lifecycle := &fakeClosLifecycle{}
	manager := NewManager(&qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	}, lifecycle).(*managerImpl)
	manager.root = root
	manager.enabled.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- manager.Create("", "shared", false)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Create deadlocked by re-entering the lifecycle invalidator")
	}
}

func TestManagerExistingClosDoesNotBumpEpochOrInvalidate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "shared"), 0o755))
	lifecycle := &fakeClosLifecycle{}
	manager := NewManager(&qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	}, lifecycle).(*managerImpl)
	manager.root = root
	manager.enabled.Store(true)

	require.NoError(t, manager.Create("", "shared", false))
	require.Empty(t, lifecycle.closIDs)
}

func TestManagerCreateAcceptsExistingClosThatWinsCreateRace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager := NewManager(&qrmresctrl.ResctrlConfig{}).(*managerImpl)
	manager.root = root
	manager.enabled.Store(true)
	manager.mkdir = func(path string, mode os.FileMode) error {
		require.NoError(t, os.Mkdir(path, mode))
		return os.ErrExist
	}

	require.NoError(t, manager.Create("", "shared", false))

	require.DirExists(t, filepath.Join(root, "shared"))
}

func TestManagerImpl_Create(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	m := &managerImpl{
		root: tmpDir,
		config: &qrmresctrl.ResctrlConfig{
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

func TestManagerImpl_CreateIgnoresLegacyLifecycleFlag(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	m := &managerImpl{
		root: tmpDir,
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: false,
		},
	}
	m.enabled.Store(true)

	err := m.Create("pod1", "shared-01", true)
	assert.NoError(t, err)

	closPath := filepath.Join(tmpDir, "shared-01")
	assert.DirExists(t, closPath)
	assert.DirExists(t, filepath.Join(closPath, MonGroupsDir, PodDirPrefix+"pod1"))
}

func TestManagerCreateFailsWhileDisableRDTTransitionIsPending(t *testing.T) {
	t.Parallel()
	manager := &managerImpl{
		root:       t.TempDir(),
		disableRDT: true,
	}
	manager.enabled.Store(true)

	err := manager.Create("pod-a", "share-01", true)

	require.ErrorContains(t, err, "RDT is disabled")
	require.NoDirExists(t, filepath.Join(manager.root, "share-01"))
}

func TestManagerCreateReturnsUnavailableUntilResctrlIsReady(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		enabled bool
		root    string
	}{
		{name: "not enabled", root: t.TempDir()},
		{name: "root unavailable", enabled: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			manager := &managerImpl{root: tt.root}
			manager.enabled.Store(tt.enabled)

			err := manager.Create("pod-a", "share-01", false)

			require.ErrorIs(t, err, ErrRDTUnavailable)
		})
	}
}

func TestManagerImpl_ReconcileClosCleansInactivePodsAndStaleClos(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	m := &managerImpl{
		root: tmpDir,
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: true,
		},
	}
	m.enabled.Store(true)

	// Prepare directories
	activePod := "active"
	inactivePod := "inactive"

	// Create active
	err := m.Create(activePod, "shared-01", true)
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
	err = os.WriteFile(filepath.Join(tmpDir, "shared-empty", cpus), []byte(""), 0o644)
	assert.NoError(t, err)

	activeUIDs := sets.NewString(activePod)
	err = m.ReconcileClos(ClosReconcileState{ActivePodUIDs: activeUIDs})
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

func TestManagerImpl_ReconcileClosRemovesInactiveMonGroup(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	m := &managerImpl{
		root: tmpDir,
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: false,
		},
	}
	m.enabled.Store(true)

	inactivePath := filepath.Join(tmpDir, "shared-01", MonGroupsDir, PodDirPrefix+"inactive")
	err := os.MkdirAll(inactivePath, 0o755)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(inactivePath, tasks), []byte(""), 0o644)
	assert.NoError(t, err)

	activeUIDs := sets.NewString()
	err = m.ReconcileClos(ClosReconcileState{ActivePodUIDs: activeUIDs})
	assert.NoError(t, err)

	assert.NoDirExists(t, inactivePath)
}

func TestManagerImpl_ReconcileClosSkipsConfiguredClosIDs(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	m := &managerImpl{
		root: tmpDir,
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlGroupLifecycleManagement: true,
			SkipCleanupClosIDs:                    sets.NewString("shared-skip", "non-exist-skip", ""),
		},
	}
	m.enabled.Store(true)

	// Create a dir that will be skipped
	err := m.Create("skip_pod", "shared-skip", true)
	assert.NoError(t, err)
	skipPodPath := filepath.Join(tmpDir, "shared-skip", MonGroupsDir, PodDirPrefix+"skip_pod")
	err = os.MkdirAll(skipPodPath, 0o755)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(skipPodPath, tasks), []byte(""), 0o644)
	assert.NoError(t, err)

	// Create a dir that will NOT be skipped
	err = m.Create("noskip_pod", "shared-noskip", true)
	assert.NoError(t, err)
	noskipPodPath := filepath.Join(tmpDir, "shared-noskip", MonGroupsDir, PodDirPrefix+"noskip_pod")
	err = os.MkdirAll(noskipPodPath, 0o755)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(noskipPodPath, tasks), []byte(""), 0o644)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "shared-noskip", tasks), []byte(""), 0o644)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "shared-noskip", cpus), []byte(""), 0o644)
	assert.NoError(t, err)

	activeUIDs := sets.NewString()
	err = m.ReconcileClos(ClosReconcileState{ActivePodUIDs: activeUIDs})
	assert.NoError(t, err)

	// Verify skipped dir should STILL exist
	_, err = os.Stat(skipPodPath)
	assert.NoError(t, err, "skipped pod dir should still exist")

	// Verify not skipped dir should be gone
	_, err = os.Stat(noskipPodPath)
	assert.True(t, os.IsNotExist(err), "not skipped pod dir should be cleaned up")

	// Verify skipped closID dir should STILL exist
	_, err = os.Stat(filepath.Join(tmpDir, "shared-skip"))
	assert.NoError(t, err, "skipped closID dir should still exist")

	// Verify not skipped closID dir should be gone
	_, err = os.Stat(filepath.Join(tmpDir, "shared-noskip"))
	assert.True(t, os.IsNotExist(err), "not skipped closID dir should be cleaned up")
}

func TestManagerImpl_ReconcileClosReadsTasksAndCPUsContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		contentFile    string
		createMonGroup bool
	}{
		{
			name:           "tasks content protects mon group",
			contentFile:    tasks,
			createMonGroup: true,
		},
		{
			name:        "cpus content protects clos",
			contentFile: cpus,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			closPath := filepath.Join(root, "shared-01")
			require.NoError(t, os.MkdirAll(filepath.Join(closPath, MonGroupsDir), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(closPath, tasks), nil, 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))

			protectedPath := closPath
			if tt.createMonGroup {
				protectedPath = filepath.Join(closPath, MonGroupsDir, PodDirPrefix+"inactive")
				require.NoError(t, os.MkdirAll(protectedPath, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(protectedPath, tasks), nil, 0o644))
			}

			m := newEnabledManager(root, &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: true,
			})
			m.readFile = func(path string) ([]byte, error) {
				if path == filepath.Join(protectedPath, tt.contentFile) {
					return []byte(" \n42\n"), nil
				}
				return os.ReadFile(path)
			}

			require.NoError(t, m.ReconcileClos(ClosReconcileState{ActivePodUIDs: sets.NewString()}))
			assert.DirExists(t, protectedPath)
		})
	}
}

func TestManagerImpl_ReconcileClosReadFailureProtectsDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	closPath := filepath.Join(root, "shared-01")
	monGroupPath := filepath.Join(closPath, MonGroupsDir, PodDirPrefix+"inactive")
	require.NoError(t, os.MkdirAll(monGroupPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(monGroupPath, tasks), nil, 0o644))

	m := newEnabledManager(root, &qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	})
	m.readFile = func(path string) ([]byte, error) {
		if path == filepath.Join(monGroupPath, tasks) {
			return nil, errors.New("read failed")
		}
		return os.ReadFile(path)
	}

	require.NoError(t, m.ReconcileClos(ClosReconcileState{ActivePodUIDs: sets.NewString()}))
	assert.DirExists(t, monGroupPath)
}

func TestManagerImpl_ReconcileClosKeepsDefaultClosOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, closID := range []string{"reclaim", "share"} {
		closPath := filepath.Join(root, closID)
		require.NoError(t, os.MkdirAll(filepath.Join(closPath, MonGroupsDir), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(closPath, tasks), nil, 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))
	}

	m := newEnabledManager(root, &qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
		DefaultClosIDs:                        []string{"reclaim"},
	})

	require.NoError(t, m.ReconcileClos(ClosReconcileState{ActivePodUIDs: sets.NewString()}))
	assert.DirExists(t, filepath.Join(root, "reclaim"))
	assert.NoDirExists(t, filepath.Join(root, "share"))
}

func TestManagerImpl_ReconcileClosRemovesInactiveDefaultClosMonGroup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	closPath := filepath.Join(root, "reclaim")
	monGroupPath := filepath.Join(closPath, MonGroupsDir, PodDirPrefix+"inactive")
	require.NoError(t, os.MkdirAll(monGroupPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(closPath, tasks), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(monGroupPath, tasks), nil, 0o644))

	m := newEnabledManager(root, &qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
		DefaultClosIDs:                        []string{"reclaim"},
	})

	require.NoError(t, m.ReconcileClos(ClosReconcileState{ActivePodUIDs: sets.NewString()}))
	assert.DirExists(t, closPath)
	assert.NoDirExists(t, monGroupPath)
}

func TestManagerImpl_GetMonGroupsCount(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	m := &managerImpl{
		root: tmpDir,
		config: &qrmresctrl.ResctrlConfig{
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

func TestManagerImpl_ReconcileClos(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *qrmresctrl.ResctrlConfig
		setup         func(t *testing.T, root string)
		state         ClosReconcileState
		wantExists    []string
		wantNotExists []string
		wantErr       bool
	}{
		{
			name: "create default reclaim clos",
			config: &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: true,
				DefaultClosIDs:                        []string{"reclaim"},
			},
			state:      ClosReconcileState{},
			wantExists: []string{"reclaim"},
		},
		{
			name: "recreate expected clos",
			config: &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: true,
			},
			state: ClosReconcileState{
				ExpectedClosIDs: sets.NewString("shared-01"),
			},
			wantExists: []string{"shared-01"},
		},
		{
			name: "remove unknown clos with empty tasks mon groups and cpus",
			config: &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: true,
			},
			setup: func(t *testing.T, root string) {
				closPath := filepath.Join(root, "stale")
				assert.NoError(t, os.MkdirAll(filepath.Join(closPath, MonGroupsDir), 0o755))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, tasks), nil, 0o644))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))
			},
			state:         ClosReconcileState{},
			wantNotExists: []string{"stale"},
		},
		{
			name: "skip cleanup clos remains protected",
			config: &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: true,
				SkipCleanupClosIDs:                    sets.NewString("protected"),
			},
			setup: func(t *testing.T, root string) {
				closPath := filepath.Join(root, "protected")
				assert.NoError(t, os.MkdirAll(filepath.Join(closPath, MonGroupsDir), 0o755))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, tasks), nil, 0o644))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))
			},
			state:      ClosReconcileState{},
			wantExists: []string{"protected"},
		},
		{
			name: "disable rdt force preserves skip clos",
			config: &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: true,
				DefaultClosIDs:                        []string{"reclaim"},
				SkipCleanupClosIDs:                    sets.NewString("reclaim"),
			},
			setup: func(t *testing.T, root string) {
				closPath := filepath.Join(root, "reclaim")
				assert.NoError(t, os.MkdirAll(filepath.Join(closPath, MonGroupsDir, PodDirPrefix+"pod"), 0o755))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, tasks), []byte("occupied"), 0o644))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), []byte("1"), 0o644))
			},
			state:      ClosReconcileState{DisableRDT: true},
			wantExists: []string{"reclaim"},
		},
		{
			name: "disable rdt removes non skip nonempty clos",
			config: &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: true,
				SkipCleanupClosIDs:                    sets.NewString("protected"),
			},
			setup: func(t *testing.T, root string) {
				closPath := filepath.Join(root, "stale")
				assert.NoError(t, os.MkdirAll(filepath.Join(closPath, MonGroupsDir, PodDirPrefix+"pod"), 0o755))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, tasks), []byte("occupied"), 0o644))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), []byte("1"), 0o644))
			},
			state:         ClosReconcileState{DisableRDT: true},
			wantNotExists: []string{"stale"},
		},
		{
			name: "disable rdt removes default-named clos",
			config: &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: false,
				DefaultClosIDs:                        []string{"reclaim"},
			},
			setup: func(t *testing.T, root string) {
				closPath := filepath.Join(root, "reclaim")
				assert.NoError(t, os.MkdirAll(filepath.Join(closPath, MonGroupsDir), 0o755))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, tasks), []byte("occupied"), 0o644))
				assert.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), []byte("1"), 0o644))
			},
			state:         ClosReconcileState{DisableRDT: true},
			wantNotExists: []string{"reclaim"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, root)
			}
			m := newEnabledManager(root, tt.config)

			err := m.ReconcileClos(tt.state)
			assert.Equal(t, tt.wantErr, err != nil)
			for _, closID := range tt.wantExists {
				assert.DirExists(t, filepath.Join(root, closID))
			}
			for _, closID := range tt.wantNotExists {
				assert.NoDirExists(t, filepath.Join(root, closID))
			}
		})
	}
}

func TestManagerImpl_ReconcileClos_DisableRDTDeleteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	assert.NoError(t, os.MkdirAll(filepath.Join(root, "reclaim"), 0o755))

	m := newEnabledManager(root, &qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	})
	m.removeAll = func(string) error {
		return errors.New("remove failed")
	}

	assert.Error(t, m.ReconcileClos(ClosReconcileState{DisableRDT: true}))
	assert.DirExists(t, filepath.Join(root, "reclaim"))
}

func TestManagerImpl_ReconcileClos_RecreatesExpectedClos(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := newEnabledManager(root, &qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	})
	state := ClosReconcileState{ExpectedClosIDs: sets.NewString("shared-01")}

	assert.NoError(t, m.ReconcileClos(state))
	assert.NoError(t, os.RemoveAll(filepath.Join(root, "shared-01")))
	assert.NoError(t, m.ReconcileClos(state))
	assert.DirExists(t, filepath.Join(root, "shared-01"))
}

func TestManagerImpl_ReconcileClosCleansPreviouslyCreatedCustomClos(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := newEnabledManager(root, &qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	})

	assert.NoError(t, m.ReconcileClos(ClosReconcileState{ExpectedClosIDs: sets.NewString("custom-pool")}))
	assert.DirExists(t, filepath.Join(root, "custom-pool"))
	assert.NoError(t, os.WriteFile(filepath.Join(root, "custom-pool", tasks), nil, 0o644))
	assert.NoError(t, os.WriteFile(filepath.Join(root, "custom-pool", cpus), nil, 0o644))

	assert.NoError(t, m.ReconcileClos(ClosReconcileState{ExpectedClosIDs: sets.NewString()}))
	assert.NoDirExists(t, filepath.Join(root, "custom-pool"))
}

func TestManagerImpl_CreateRejectsClosWhileRDTDisabled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := newEnabledManager(root, &qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	})

	assert.NoError(t, m.ReconcileClos(ClosReconcileState{DisableRDT: true}))
	assert.ErrorIs(t, m.Create("pod", "shared-01", true), ErrRDTDisabled)
	assert.NoDirExists(t, filepath.Join(root, "shared-01"))

	assert.NoError(t, m.ReconcileClos(ClosReconcileState{DisableRDT: false}))
	assert.NoError(t, m.Create("pod", "shared-01", true))
	assert.DirExists(t, filepath.Join(root, "shared-01"))
}

func TestManagerImpl_CreateAndReconcileClosInvalidateLifecycleTransitions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	invalidator := &fakeClosInvalidator{}
	m := NewManager(&qrmresctrl.ResctrlConfig{
		EnableResctrlGroupLifecycleManagement: true,
	}, invalidator).(*managerImpl)
	m.root = root
	m.enabled.Store(true)

	assert.NoError(t, m.Create("pod", "shared-01", false))
	assert.Equal(t, []string{"shared-01"}, invalidator.closIDs)

	assert.NoError(t, m.ReconcileClos(ClosReconcileState{DisableRDT: true}))
	assert.Equal(t, []string{"shared-01", "shared-01"}, invalidator.closIDs)
}

func TestManagerDisableRDTRemovesAllNonSkippedClos(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, closID := range []string{"dedicated", "external"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, closID), 0o755))
	}
	externalMonGroup := filepath.Join(root, "dedicated", MonGroupsDir, PodDirPrefix+"external-pod")
	require.NoError(t, os.MkdirAll(externalMonGroup, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(externalMonGroup, tasks), nil, 0o644))
	manager := NewManager(&qrmresctrl.ResctrlConfig{}).(*managerImpl)
	manager.root = root
	manager.enabled.Store(true)

	require.NoError(t, manager.ReconcileClos(ClosReconcileState{
		DisableRDT:    true,
		ActivePodUIDs: sets.NewString(),
	}))

	require.NoDirExists(t, filepath.Join(root, "dedicated"))
	require.NoDirExists(t, filepath.Join(root, "external"))
	require.NoDirExists(t, externalMonGroup)
}

func TestManagerImpl_SkipFilesystemAccessWhenResctrlUnavailable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	tests := []struct {
		name      string
		dirName   string
		enabled   bool
		emptyRoot bool
	}{
		{
			name:      "mountpoint disappeared",
			dirName:   "mountpoint-disappeared",
			enabled:   true,
			emptyRoot: true,
		},
		{
			name:    "resctrl disabled",
			dirName: "resctrl-disabled",
			enabled: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testRoot := filepath.Join(root, tt.dirName)
			require.NoError(t, os.MkdirAll(testRoot, 0o755))

			// The manager operates purely on the explicit, absolute root below;
			// no process working-directory mutation is required, so the test
			// stays parallel-safe.
			managerRoot := testRoot
			if tt.emptyRoot {
				managerRoot = ""
			}
			m := newEnabledManager(testRoot, &qrmresctrl.ResctrlConfig{
				EnableResctrlGroupLifecycleManagement: true,
			})
			m.Lock()
			m.root = managerRoot
			m.enabled.Store(tt.enabled)
			m.Unlock()

			staleClosPath := filepath.Join(testRoot, "stale")
			require.NoError(t, os.MkdirAll(staleClosPath, 0o755))

			assert.ErrorIs(t, m.Create("pod", "created", false), ErrRDTUnavailable)
			assert.NoError(t, m.ReconcileClos(ClosReconcileState{
				DisableRDT:      true,
				ExpectedClosIDs: sets.NewString("expected"),
				ActivePodUIDs:   sets.NewString(),
			}))

			assert.NoDirExists(t, filepath.Join(testRoot, "created"))
			assert.NoDirExists(t, filepath.Join(testRoot, "expected"))
			assert.DirExists(t, staleClosPath)
		})
	}
}

func newEnabledManager(root string, config *qrmresctrl.ResctrlConfig) *managerImpl {
	m := &managerImpl{
		root:   root,
		config: config,
	}
	m.enabled.Store(true)
	return m
}
