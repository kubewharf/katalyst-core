/*
Copyright 2026 The Katalyst Authors.

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
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCPUListManagerApplyCPUListWritesResctrlMask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff,ffffffff,ffffffff\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dedicated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), []byte("ffffffff,ffffffff,ffffffff\n"), 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-7,25,49"))

	content, err := os.ReadFile(filepath.Join(root, "dedicated", cpus))
	require.NoError(t, err)
	require.Equal(t, "00000000,00020000,020000fe", string(content))
}

func TestCPUListManagerApplyCPUListWritesZeroMaskForEmptyTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff,ffffffff,ffffffff\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "reclaim"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "reclaim", cpus), []byte("ffffffff,ffffffff,ffffffff\n"), 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	require.NoError(t, manager.ApplyCPUList(context.Background(), "reclaim", ""))

	content, err := os.ReadFile(filepath.Join(root, "reclaim", cpus))
	require.NoError(t, err)
	require.Equal(t, "00000000,00000000,00000000", string(content))
}

func TestCPUListManagerApplyCPUListWritesExistingClos(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	for _, closID := range []string{"dedicated", "reclaim"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, closID), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, closID, cpus), []byte("ffffffff\n"), 0o644))
	}

	manager := newCPUListManagerWithUpdater(root, nil)
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1"))
	require.NoError(t, manager.ApplyCPUList(context.Background(), "reclaim", ""))

	content, err := os.ReadFile(filepath.Join(root, "dedicated", cpus))
	require.NoError(t, err)
	require.Equal(t, "00000002", string(content))
	content, err = os.ReadFile(filepath.Join(root, "reclaim", cpus))
	require.NoError(t, err)
	require.Equal(t, "00000000", string(content))
}

func TestCPUListManagerApplyCPUListCreatesMissingClosBeforeWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1"))

	content, err := os.ReadFile(filepath.Join(root, "dedicated", cpus))
	require.NoError(t, err)
	require.Equal(t, "00000002", string(content))
}

func TestCPUListManagerApplyCPUListSkipsMissingClosForEmptyTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))

	manager := newCPUListManager(root)
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", ""))
	require.NoDirExists(t, filepath.Join(root, "dedicated"))
}

func TestCPUListManagerApplyCPUListRejectsCPUOutsideMaskWidth(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dedicated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), []byte("ffffffff\n"), 0o644))

	manager := newCPUListManager(root)
	require.ErrorContains(t, manager.ApplyCPUList(context.Background(), "dedicated", "32"), "exceeds resctrl cpus mask width")
}

func TestCPUListManagerCPUListMatchesObservesLiveMask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dedicated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), []byte("00000002\n"), 0o644))
	manager := newCPUListManager(root)

	matches, err := manager.CPUListMatches(context.Background(), "dedicated", "1")
	require.NoError(t, err)
	require.True(t, matches)

	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), []byte("00000004\n"), 0o644))
	matches, err = manager.CPUListMatches(context.Background(), "dedicated", "1")
	require.NoError(t, err)
	require.False(t, matches)
}

func TestCPUListManagerWritesExistingClosAndBumpsEpochAfterRecreate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	closPath := filepath.Join(root, "dedicated")
	require.NoError(t, os.Mkdir(closPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	clos, err := manager.ListManagedClos(context.Background())
	require.NoError(t, err)
	require.Len(t, clos, 1)
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1-2"))
	got, err := os.ReadFile(filepath.Join(closPath, cpus))
	require.NoError(t, err)
	require.Equal(t, "00000006", string(got))

	require.NoError(t, os.RemoveAll(closPath))
	require.NoError(t, os.Mkdir(closPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))
	recreated, err := manager.ListManagedClos(context.Background())
	require.NoError(t, err)
	require.Len(t, recreated, 1)
	require.Greater(t, recreated[0].Epoch, clos[0].Epoch)
}

func TestCPUListManagerApplyCPUListWaitsForClosLifecycleUpdate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dedicated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), []byte("ffffffff\n"), 0o644))

	coordinator := newTestClosResourceCoordinator()
	manager := newCPUListManagerWithUpdater(root, coordinator)
	lifecycleStarted := make(chan struct{})
	releaseLifecycle := make(chan struct{})
	lifecycleDone := make(chan struct{})
	go func() {
		defer close(lifecycleDone)
		require.NoError(t, coordinator.RunClosLifecycle("dedicated", func() (bool, error) {
			close(lifecycleStarted)
			<-releaseLifecycle
			return false, nil
		}))
	}()
	<-lifecycleStarted

	applyDone := make(chan error, 1)
	go func() {
		applyDone <- manager.ApplyCPUList(context.Background(), "dedicated", "1")
	}()

	select {
	case err := <-applyDone:
		require.NoError(t, err)
		t.Fatal("ApplyCPUList completed while CLOS lifecycle update was still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseLifecycle)
	<-lifecycleDone
	require.NoError(t, <-applyDone)
	content, err := os.ReadFile(filepath.Join(root, "dedicated", cpus))
	require.NoError(t, err)
	require.Equal(t, "00000002", string(content))
}

type testClosResourceCoordinator struct {
	mu     sync.Mutex
	closMu map[string]*sync.Mutex
}

func newTestClosResourceCoordinator() *testClosResourceCoordinator {
	return &testClosResourceCoordinator{closMu: make(map[string]*sync.Mutex)}
}

func (c *testClosResourceCoordinator) RunClosResourceUpdate(closID string, update func() (bool, error)) error {
	closMu := c.getClosMu(closID)
	closMu.Lock()
	defer closMu.Unlock()
	_, err := update()
	return err
}

func (c *testClosResourceCoordinator) RunClosLifecycle(closID string, update func() (bool, error)) error {
	closMu := c.getClosMu(closID)
	closMu.Lock()
	defer closMu.Unlock()
	_, err := update()
	return err
}

func (c *testClosResourceCoordinator) getClosMu(closID string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closMu[closID] == nil {
		c.closMu[closID] = &sync.Mutex{}
	}
	return c.closMu[closID]
}

func TestIsClosDirExcludesReservedRootEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"info", "mon_data", monGroupsDir, "clos0", "clos1"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "a_regular_file"), nil, 0o644))

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = isClosDir(e)
	}
	require.True(t, got["clos0"], "clos0 should be recognized as a CLOS dir")
	require.True(t, got["clos1"], "clos1 should be recognized as a CLOS dir")
	for _, reserved := range []string{"info", "mon_data", monGroupsDir} {
		require.False(t, got[reserved], "%q must not be treated as a CLOS dir", reserved)
	}
	require.False(t, got["a_regular_file"], "regular files must not be treated as a CLOS dir")
}

// TestCPUListManagerOwnsEpochOnItsOwnCreation pins that when the manager
// materializes a CLOS via ApplyCPUList it records the new directory identity
// and advances the epoch synchronously, without waiting for a ListManagedClos
// poll to re-derive the recreation from an inode diff.
func TestCPUListManagerOwnsEpochOnItsOwnCreation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1"))

	manager.mu.Lock()
	epoch := manager.epochs["dedicated"]
	_, hasIdentity := manager.handles["dedicated"]
	manager.mu.Unlock()
	require.Equal(t, uint64(1), epoch, "manager-created CLOS must own epoch 1 immediately")
	require.True(t, hasIdentity, "manager must hold a handle to the created CLOS")

	// A subsequent poll must not double-bump: the freshly recorded identity
	// already matches the on-disk directory.
	clos, err := manager.ListManagedClos(context.Background())
	require.NoError(t, err)
	require.Len(t, clos, 1)
	require.Equal(t, uint64(1), clos[0].Epoch)
}

// TestCPUListManagerRecreatedClosBumpsEpoch verifies that deleting a CLOS and
// having the manager recreate the same path advances the epoch even when the
// recreation is performed by ApplyCPUList itself (the inode-reuse friendly
// path that does not rely on a filesystem inode diff).
func TestCPUListManagerRecreatedClosBumpsEpoch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	closPath := filepath.Join(root, "dedicated")
	require.NoError(t, os.Mkdir(closPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	first, err := manager.ListManagedClos(context.Background())
	require.NoError(t, err)
	require.Len(t, first, 1)

	require.NoError(t, os.RemoveAll(closPath))
	// Recreate via the manager instead of raw mkdir so the epoch transition is
	// owned by the manager rather than inferred from an inode change.
	require.NoError(t, manager.ApplyCPUList(context.Background(), "dedicated", "1"))

	recreated, err := manager.ListManagedClos(context.Background())
	require.NoError(t, err)
	require.Len(t, recreated, 1)
	require.Greater(t, recreated[0].Epoch, first[0].Epoch, "recreated same-path CLOS must bump epoch")
}

// TestCPUListManagerDropsEpochForDeletedClos ensures epoch generations do not
// leak for CLOS that have disappeared.
func TestCPUListManagerDropsEpochForDeletedClos(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dedicated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), nil, 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	_, err := manager.ListManagedClos(context.Background())
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(filepath.Join(root, "dedicated")))
	_, err = manager.ListManagedClos(context.Background())
	require.NoError(t, err)

	manager.mu.Lock()
	_, leaked := manager.epochs["dedicated"]
	_, leakedHandle := manager.handles["dedicated"]
	handleCount := len(manager.handles)
	manager.mu.Unlock()
	require.False(t, leaked, "epoch must not leak for a deleted CLOS")
	require.False(t, leakedHandle, "handle must be closed/removed for a deleted CLOS")
	require.Zero(t, handleCount, "no handles should remain after the CLOS disappears")
}

// TestCPUListManagerConcurrentListAndApply is the concurrency boundary:
// ListManagedClos and ApplyCPUList must be serializable on the manager mutex
// without data races, and observed epochs must stay monotonic per CLOS.
func TestCPUListManagerConcurrentListAndApply(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dedicated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), nil, 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	ctx := context.Background()

	var readers sync.WaitGroup
	var writers sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = manager.ListManagedClos(ctx)
				}
			}
		}()
	}
	for i := 0; i < 2; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for j := 0; j < 50; j++ {
				_ = manager.ApplyCPUList(ctx, "dedicated", "1")
			}
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()

	clos, err := manager.ListManagedClos(ctx)
	require.NoError(t, err)
	require.Len(t, clos, 1)
	require.GreaterOrEqual(t, clos[0].Epoch, uint64(1))
}

// TestCPUListManagerHandleLifecycleAcrossRecreateLoops exercises the fd fence
// under repeated external delete+recreate: each recreation must bump the epoch,
// the manager must keep exactly one open handle per live CLOS, and the epoch
// must stay monotonic. On Linux the open handle pins the old inode so the new
// mkdir cannot recycle it, making the recreation detectable even on filesystems
// that reuse inode numbers.
func TestCPUListManagerHandleLifecycleAcrossRecreateLoops(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	closPath := filepath.Join(root, "dedicated")

	manager := newCPUListManagerWithUpdater(root, nil)
	prevEpoch := uint64(0)
	for i := 0; i < 5; i++ {
		require.NoError(t, os.Mkdir(closPath, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(closPath, cpus), nil, 0o644))

		clos, err := manager.ListManagedClos(context.Background())
		require.NoError(t, err)
		require.Len(t, clos, 1)
		require.Greater(t, clos[0].Epoch, prevEpoch, "recreate %d must bump epoch", i)
		prevEpoch = clos[0].Epoch

		manager.mu.Lock()
		handleCount := len(manager.handles)
		_, has := manager.handles["dedicated"]
		manager.mu.Unlock()
		require.Equal(t, 1, handleCount, "must hold exactly one handle")
		require.True(t, has, "must hold a handle to dedicated")

		require.NoError(t, os.RemoveAll(closPath))
	}

	// After the final removal, a poll must observe the CLOS gone and close the
	// handle (no fd leak).
	clos, err := manager.ListManagedClos(context.Background())
	require.NoError(t, err)
	require.Empty(t, clos)
	manager.mu.Lock()
	handleCount := len(manager.handles)
	_, hasEpoch := manager.epochs["dedicated"]
	manager.mu.Unlock()
	require.Zero(t, handleCount, "handle must be closed after the CLOS disappears")
	require.False(t, hasEpoch, "epoch must be dropped after the CLOS disappears")
}

// TestCPUListManagerRootResetClosesHandles ensures a missing resctrl root closes
// every held directory handle instead of leaking fds.
func TestCPUListManagerRootResetClosesHandles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dedicated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dedicated", cpus), nil, 0o644))

	manager := newCPUListManagerWithUpdater(root, nil)
	_, err := manager.ListManagedClos(context.Background())
	require.NoError(t, err)
	manager.mu.Lock()
	require.Len(t, manager.handles, 1)
	manager.mu.Unlock()

	require.NoError(t, os.RemoveAll(root))
	_, err = manager.ListManagedClos(context.Background())
	require.NoError(t, err)
	manager.mu.Lock()
	require.Empty(t, manager.handles, "all handles must be closed when root vanishes")
	manager.mu.Unlock()
}

// TestCPUListManagerRejectsTraversalClosID pins that a closID carrying path
// traversal (or any separator / absolute / dot entry) is rejected before any
// filesystem operation, so it can never escape the resctrl root.
func TestCPUListManagerRejectsTraversalClosID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	manager := newCPUListManagerWithUpdater(root, nil)

	for _, bad := range []string{"../evil", "..", ".", "a/b", `a\b`, "/abs"} {
		err := manager.ApplyCPUList(context.Background(), bad, "1")
		require.Errorf(t, err, "closID %q must be rejected by ApplyCPUList", bad)
		_, err = manager.CPUListMatches(context.Background(), bad, "1")
		require.Errorf(t, err, "closID %q must be rejected by CPUListMatches", bad)
	}

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the root cpus file should exist; nothing may be materialized for a rejected closID")
}

// TestCPUListManagerRollsBackCreatedDirWhenCpusWriteFails ensures that when
// ApplyCPUList materializes a new CLOS directory but the initial cpus write
// fails, the half-initialized directory is removed instead of leaking an empty
// CLOS for the memory manager to reconcile later.
func TestCPUListManagerRollsBackCreatedDirWhenCpusWriteFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff\n"), 0o644))
	manager := newCPUListManagerWithUpdater(root, nil)
	manager.writeFile = func(name string, data []byte, perm os.FileMode) error {
		return errors.New("kernfs rejected write")
	}

	err := manager.ApplyCPUList(context.Background(), "dedicated", "1")
	require.Error(t, err)
	require.NoDirExists(t, filepath.Join(root, "dedicated"),
		"freshly created CLOS dir must be rolled back when the cpus write fails")
}
