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

package rdt

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSchemataCoordinatorPreservesUnmanagedLines(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;1=ff;\nMB:0=100;1=100;\n")
	coordinator := newSchemataCoordinator(root)

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{1: 0x7f, 0: 0x3f}))
	require.Equal(t, "L3:0=3f;1=7f;\nMB:0=100;1=100;\n", readSchemata(t, root, "clos"))

	require.NoError(t, coordinator.ApplyMB("clos", map[int]int{1: 80, 0: 60}))
	require.Equal(t, "L3:0=3f;1=7f;\nMB:0=60;1=80;\n", readSchemata(t, root, "clos"))
}

func TestSchemataCoordinatorReplacesIndentedResourceLines(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "    L3:0=7ff;1=7ff\n    MB:0=100;1=100\n")
	coordinator := newSchemataCoordinator(root)

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{1: 0x7f, 0: 0x3f}))
	require.Equal(t, "L3:0=3f;1=7f;\n    MB:0=100;1=100\n", readSchemata(t, root, "clos"))

	require.NoError(t, coordinator.ApplyMB("clos", map[int]int{1: 80, 0: 60}))
	require.Equal(t, "L3:0=3f;1=7f;\nMB:0=60;1=80;\n", readSchemata(t, root, "clos"))
}

func TestSchemataCoordinatorPreservesHighBitCATMask(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: uint64(1) << 63}))
	require.Equal(t, "L3:0=8000000000000000;\nMB:0=100;\n", readSchemata(t, root, "clos"))
}

func TestSchemataCoordinatorWritesZeroCATMask(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ffff;1=ffff;\nMB:0=100;1=100;\n")
	coordinator := newSchemataCoordinator(root)

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0, 1: 0}))
	require.Equal(t, "L3:0=0;1=0;\nMB:0=100;1=100;\n", readSchemata(t, root, "clos"))
}

func TestSchemataCoordinatorSerializesReadModifyWritePerClos(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)

	firstWriteStarted := make(chan struct{})
	unblockFirstWrite := make(chan struct{})
	var writeCalls int
	var mu sync.Mutex
	coordinator.writeFile = func(name string, data []byte, perm os.FileMode) error {
		mu.Lock()
		writeCalls++
		call := writeCalls
		mu.Unlock()
		if call == 1 {
			close(firstWriteStarted)
			<-unblockFirstWrite
		}
		return os.WriteFile(name, data, perm)
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f})
	}()
	<-firstWriteStarted

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- coordinator.ApplyMB("clos", map[int]int{0: 60})
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second update was not serialized, returned %v before first write completed", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(unblockFirstWrite)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	require.Equal(t, "L3:0=3f;\nMB:0=60;\n", readSchemata(t, root, "clos"))
}

func TestSchemataCoordinatorSkipsCachedL3Target(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)
	var writeCalls int
	coordinator.writeFile = func(name string, data []byte, perm os.FileMode) error {
		writeCalls++
		return os.WriteFile(name, data, perm)
	}

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 1, writeCalls)
}

func TestSchemataCoordinatorInvalidateClosReappliesSameTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)
	var writeCalls int
	coordinator.writeFile = func(name string, data []byte, perm os.FileMode) error {
		writeCalls++
		return os.WriteFile(name, data, perm)
	}

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 1, writeCalls)

	require.NoError(t, os.RemoveAll(filepath.Join(root, "clos")))
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator.InvalidateClos("clos")

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 2, writeCalls, "a recreated CLOS must not reuse its previous cached target")
	require.Equal(t, "L3:0=3f;\nMB:0=100;\n", readSchemata(t, root, "clos"))
}

func TestSchemataCoordinatorInvalidateClosSerializesWithApply(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	lock := newLockObserver()
	coordinator := newSchemataCoordinatorWithClosLock(root, func() closLocker {
		return lock
	})

	applyWriteStarted := make(chan struct{})
	releaseApplyWrite := make(chan struct{})
	var releaseApplyWriteOnce sync.Once
	releaseApply := func() {
		releaseApplyWriteOnce.Do(func() {
			close(releaseApplyWrite)
		})
	}
	t.Cleanup(releaseApply)
	var writeCalls int
	var writeCallsMu sync.Mutex
	coordinator.writeFile = func(name string, data []byte, perm os.FileMode) error {
		writeCallsMu.Lock()
		writeCalls++
		call := writeCalls
		writeCallsMu.Unlock()
		if call == 1 {
			close(applyWriteStarted)
			<-releaseApplyWrite
		}
		return os.WriteFile(name, data, perm)
	}

	applyDone := make(chan error, 1)
	go func() {
		applyDone <- coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f})
	}()
	<-applyWriteStarted

	recreated := make(chan error, 1)
	invalidateDone := make(chan struct{})
	go func() {
		if err := os.RemoveAll(filepath.Join(root, "clos")); err != nil {
			recreated <- err
			return
		}
		path := filepath.Join(root, "clos", schemataFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			recreated <- err
			return
		}
		if err := os.WriteFile(path, []byte("L3:0=ff;\nMB:0=100;\n"), 0o644); err != nil {
			recreated <- err
			return
		}
		recreated <- nil
		coordinator.InvalidateClos("clos")
		close(invalidateDone)
	}()
	require.NoError(t, <-recreated)

	select {
	case <-lock.waiters:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("invalidation did not contend for the CLOS lock while apply was blocked")
	}
	releaseApply()
	require.NoError(t, <-applyDone)
	<-invalidateDone

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	writeCallsMu.Lock()
	require.Equal(t, 2, writeCalls, "a CLOS recreated during apply must not reuse a stale cached target")
	writeCallsMu.Unlock()
}

func TestSchemataCoordinatorResourceUpdateSerializesWithLifecycle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	coordinator := newSchemataCoordinator(root)
	lifecycleStarted := make(chan struct{})
	releaseLifecycle := make(chan struct{})
	lifecycleDone := make(chan error, 1)
	go func() {
		lifecycleDone <- coordinator.RunClosLifecycle("clos", func() (bool, error) {
			close(lifecycleStarted)
			<-releaseLifecycle
			return false, nil
		})
	}()
	<-lifecycleStarted

	resourceUpdateDone := make(chan error, 1)
	go func() {
		resourceUpdateDone <- coordinator.RunClosResourceUpdate("clos", func() (bool, error) {
			return false, os.WriteFile(filepath.Join(root, "updated"), []byte("ok"), 0o644)
		})
	}()
	select {
	case err := <-resourceUpdateDone:
		require.NoError(t, err)
		t.Fatal("resource update completed while CLOS lifecycle update was still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseLifecycle)
	require.NoError(t, <-lifecycleDone)
	require.NoError(t, <-resourceUpdateDone)
	content, err := os.ReadFile(filepath.Join(root, "updated"))
	require.NoError(t, err)
	require.Equal(t, "ok", string(content))
}

func TestSchemataCoordinatorResourceUpdateInvalidatesCacheWhenClosChanged(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)
	var writeCalls int
	coordinator.writeFile = func(name string, data []byte, perm os.FileMode) error {
		writeCalls++
		return os.WriteFile(name, data, perm)
	}

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 1, writeCalls)
	require.NoError(t, coordinator.RunClosResourceUpdate("clos", func() (bool, error) {
		return true, nil
	}))
	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 2, writeCalls)
}

func TestSchemataCoordinatorResourceUpdateInvalidatesCacheWhenClosChangedAndUpdateFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)
	var writeCalls int
	coordinator.writeFile = func(name string, data []byte, perm os.FileMode) error {
		writeCalls++
		return os.WriteFile(name, data, perm)
	}

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 1, writeCalls)
	require.Error(t, coordinator.RunClosResourceUpdate("clos", func() (bool, error) {
		return true, errors.New("resource update failed after CLOS changed")
	}))
	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 2, writeCalls)
}

type lockObserver struct {
	tokens  chan struct{}
	waiters chan struct{}
}

func newLockObserver() *lockObserver {
	lock := &lockObserver{
		tokens:  make(chan struct{}, 1),
		waiters: make(chan struct{}, 1),
	}
	lock.tokens <- struct{}{}
	return lock
}

func (l *lockObserver) Lock() {
	select {
	case <-l.tokens:
		return
	default:
	}
	l.waiters <- struct{}{}
	<-l.tokens
}

func (l *lockObserver) Unlock() {
	l.tokens <- struct{}{}
}

func TestSchemataCoordinatorWriteFailureDoesNotCacheTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)
	writeErr := errors.New("write failed")
	var writeCalls int
	coordinator.writeFile = func(name string, data []byte, perm os.FileMode) error {
		writeCalls++
		if writeCalls == 1 {
			return writeErr
		}
		return os.WriteFile(name, data, perm)
	}

	require.ErrorIs(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}), writeErr)
	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 2, writeCalls)
}

func TestSchemataCoordinatorWriteFailureInvalidatesPreviousTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)
	writeErr := errors.New("write failed after partial update")
	var writeCalls int
	coordinator.writeFile = func(name string, data []byte, perm os.FileMode) error {
		writeCalls++
		if writeCalls == 2 {
			return writeErr
		}
		return os.WriteFile(name, data, perm)
	}

	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.ErrorIs(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x7f}), writeErr)
	require.NoError(t, coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f}))
	require.Equal(t, 3, writeCalls, "a failed write must invalidate the previous cache entry")
}

func TestSchemataCoordinatorSerializesLifecycleWithStaleRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSchemata(t, root, "clos", "L3:0=ff;\nMB:0=100;\n")
	coordinator := newSchemataCoordinator(root)

	readDone := make(chan struct{})
	releaseRead := make(chan struct{})
	coordinator.readFile = func(name string) ([]byte, error) {
		content, err := os.ReadFile(name)
		close(readDone)
		<-releaseRead
		return content, err
	}

	applyDone := make(chan error, 1)
	go func() {
		applyDone <- coordinator.ApplyL3("clos", map[int]uint64{0: 0x3f})
	}()
	<-readDone

	lifecycleStarted := make(chan struct{})
	lifecycleDone := make(chan error, 1)
	go func() {
		lifecycleDone <- coordinator.RunClosLifecycle("clos", func() (bool, error) {
			close(lifecycleStarted)
			if err := os.RemoveAll(filepath.Join(root, "clos")); err != nil {
				return false, err
			}
			path := filepath.Join(root, "clos", schemataFile)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return false, err
			}
			err := os.WriteFile(path, []byte("L3:0=ff;\nMB:0=55;\n"), 0o644)
			return err == nil, err
		})
	}()

	select {
	case <-lifecycleStarted:
		t.Fatal("CLOS lifecycle update ran while ApplyL3 held a stale schemata snapshot")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseRead)
	require.NoError(t, <-applyDone)
	require.NoError(t, <-lifecycleDone)
	require.Equal(t, "L3:0=ff;\nMB:0=55;\n", readSchemata(t, root, "clos"),
		"the stale ApplyL3 snapshot must not overwrite the recreated CLOS")
}

func writeSchemata(t *testing.T, root, clos, content string) {
	t.Helper()
	path := filepath.Join(root, clos, "schemata")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func readSchemata(t *testing.T, root, clos string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, clos, "schemata"))
	require.NoError(t, err)
	return string(data)
}
