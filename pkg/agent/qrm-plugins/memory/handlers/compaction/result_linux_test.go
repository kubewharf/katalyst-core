//go:build linux

package compaction

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompactNUMANodeResult(t *testing.T) {
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
	serializeCompactionTest(t)
	withFakeProcFS(t)
	oldFn := CompactMemoryNodeFn
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })
	CompactMemoryNodeFn = func(int) error { panic("write panic") }
	assert.Panics(t, func() { CompactNUMANode(0) })
	CompactMemoryNodeFn = func(int) error { return nil }
	assert.Equal(t, CompactionWriteReturned, CompactNUMANode(0).State)
}
