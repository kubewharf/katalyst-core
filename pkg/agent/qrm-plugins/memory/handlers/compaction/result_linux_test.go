//go:build linux
// +build linux

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

package compaction

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNUMACompactionNeverCreatesOrTruncatesKnob(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	root := t.TempDir()
	oldPath := hostMemNodePath
	hostMemNodePath = filepath.Join(root, "node")
	t.Cleanup(func() { hostMemNodePath = oldPath })
	path := filepath.Join(root, "node0", "compact")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.ErrorIs(t, setHostMemCompact(0), os.ErrNotExist)
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.WriteFile(path, []byte("suffix"), 0o600))
	require.NoError(t, setHostMemCompact(0))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "1uffix", string(data))
}

func TestDifferentNUMACompactionsRemainIndependent(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	require.True(t, tryAcquireNumaCompacting(0))
	defer releaseNumaCompacting(0)
	require.True(t, tryAcquireNumaCompacting(1))
	defer releaseNumaCompacting(1)
	require.False(t, tryAcquireNumaCompacting(GlobalNUMAID))
}

func TestGlobalCompactionReleasesGuardAfterPanic(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	withFakeProcFS(t)
	oldFn := CompactMemoryNodeFn
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })
	CompactMemoryNodeFn = func(int) error { panic("global write panic") }
	require.Panics(t, func() { CompactMemory() })
	CompactMemoryNodeFn = func(int) error { return os.ErrPermission }
	result := CompactMemory()
	require.Equal(t, CompactionWriteFailed, result.State)
	require.ErrorIs(t, result.Err, os.ErrPermission)
	require.NotNil(t, result.Cost)
	CompactMemoryNodeFn = func(int) error { return nil }
	require.Equal(t, CompactionWriteReturned, CompactNUMANode(0).State)
	require.Equal(t, CompactionWriteReturned, CompactMemory().State)
}

func TestGlobalCompactionMissingKnobIsNotCreated(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	root := withFakeProcFS(t)
	path := filepath.Join(root, "sys/vm/compact_memory")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	result := CompactMemory()
	require.Equal(t, CompactionWriteFailed, result.State)
	require.ErrorIs(t, result.Err, os.ErrNotExist)
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestGlobalCompactionWritesProcOnce(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	root := withFakeProcFS(t)
	path := filepath.Join(root, "sys/vm/compact_memory")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	result := CompactMemory()
	require.Equal(t, CompactionWriteReturned, result.State)
	require.NoError(t, result.Err)
	require.NotNil(t, result.Cost)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "1", string(data))
}

func TestGlobalCompactionExcludesNUMAWrites(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	withFakeProcFS(t)
	oldFn := CompactMemoryNodeFn
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	defer func() { close(release); <-done }()
	CompactMemoryNodeFn = func(id int) error {
		if id == -1 {
			close(entered)
			<-release
		}
		return nil
	}
	go func() { defer close(done); CompactMemory() }()
	<-entered
	require.Equal(t, CompactionNUMABusy, CompactNUMANode(0).State)
	require.Equal(t, CompactionNUMABusy, CompactMemory().State)
	select {
	case <-done:
		t.Fatal("global write unexpectedly returned")
	case <-time.After(time.Millisecond):
	}
}

func TestGlobalCompactionBlockedByNUMAWrite(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	withFakeProcFS(t)
	require.True(t, tryAcquireNumaCompacting(1))
	defer releaseNumaCompacting(1)
	result := CompactMemory()
	require.Equal(t, CompactionNUMABusy, result.State)
	require.Nil(t, result.Cost)
}

func TestGlobalCompactionChecksAllKcompactd(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	root := withFakeProcFS(t)
	writeFakeProc(t, root, 100, "kcompactd0", "S (sleeping)")
	writeFakeProc(t, root, 200, "kcompactd1", "D (disk sleep)")
	oldFn := CompactMemoryNodeFn
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })
	CompactMemoryNodeFn = func(int) error { t.Error("must not write when a kcompactd is busy"); return nil }
	result := CompactMemory()
	require.Equal(t, CompactionKcompactdBusy, result.State)
	require.Nil(t, result.Cost)
}

func TestCompactNUMANodeResult(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	root := withFakeProcFS(t)
	writeFakeProc(t, root, 100, "kcompactd0", "S (sleeping)")
	writeFakeProc(t, root, 200, "kcompactd1", "D (disk sleep)")
	oldFn := CompactMemoryNodeFn
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })
	calls := 0
	CompactMemoryNodeFn = func(int) error { calls++; return nil }

	result := CompactNUMANode(0)
	assert.Equal(t, CompactionWriteReturned, result.State)
	assert.NoError(t, result.Err)
	require.NotNil(t, result.Cost)
	assert.Equal(t, CompactionKcompactdBusy, CompactNUMANode(1).State)
	require.True(t, tryAcquireNumaCompacting(0))
	assert.Equal(t, CompactionNUMABusy, CompactNUMANode(0).State)
	releaseNumaCompacting(0)
	CompactMemoryNodeFn = func(int) error { return os.ErrPermission }
	result = CompactNUMANode(0)
	assert.Equal(t, CompactionWriteFailed, result.State)
	assert.ErrorIs(t, result.Err, os.ErrPermission)
	require.NotNil(t, result.Cost)
	CompactMemoryNodeFn = func(int) error { calls++; return nil }
	assert.True(t, TryCompactNUMANode(0), "legacy wrapper and failed-write guard remain compatible")
	assert.Equal(t, 2, calls)
}

func TestCompactNUMANodeReleasesGuardAfterPanic(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)
	withFakeProcFS(t)
	oldFn := CompactMemoryNodeFn
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })
	CompactMemoryNodeFn = func(int) error { panic("write panic") }
	assert.Panics(t, func() { CompactNUMANode(0) })
	CompactMemoryNodeFn = func(int) error { return nil }
	assert.Equal(t, CompactionWriteReturned, CompactNUMANode(0).State)
}
