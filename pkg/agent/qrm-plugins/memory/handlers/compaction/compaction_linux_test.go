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
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var compactionTestMu sync.Mutex

func serializeCompactionTest(t *testing.T) {
	t.Helper()
	compactionTestMu.Lock()
	t.Cleanup(compactionTestMu.Unlock)
}

// writeFakeProc creates <root>/<pid>/comm and <root>/<pid>/status with the given content.
func writeFakeProc(t *testing.T, root string, pid int, comm, state string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	assert.NoError(t, os.MkdirAll(dir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "status"),
		[]byte("Name:\t"+comm+"\nState:\t"+state+"\n"), 0o644))
}

// withFakeProcFS points ProcFSRoot at a temp dir and resets the discovery cache, restoring both
// afterwards.
func withFakeProcFS(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldRoot := ProcFSRoot
	ProcFSRoot = root

	numaKcompactdPidMu.Lock()
	oldPids := numaKcompactdPid
	numaKcompactdPid = make(map[int]int)
	numaKcompactdPidMu.Unlock()

	t.Cleanup(func() {
		ProcFSRoot = oldRoot
		numaKcompactdPidMu.Lock()
		numaKcompactdPid = oldPids
		numaKcompactdPidMu.Unlock()
	})
	return root
}

func TestCalculateTHPOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		basePageSize int
		thpPageSize  uint64
		wantOrder    int
		wantErr      bool
	}{
		{
			name:         "4 KiB base pages and 2 MiB THP",
			basePageSize: 4 * 1024,
			thpPageSize:  2 * 1024 * 1024,
			wantOrder:    9,
		},
		{
			name:         "64 KiB base pages and 512 MiB THP",
			basePageSize: 64 * 1024,
			thpPageSize:  512 * 1024 * 1024,
			wantOrder:    13,
		},
		{
			name:         "non-power-of-two page count",
			basePageSize: 4 * 1024,
			thpPageSize:  12 * 1024,
			wantErr:      true,
		},
		{
			name:         "invalid base page size",
			basePageSize: 0,
			thpPageSize:  2 * 1024 * 1024,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			order, err := calculateTHPOrder(tt.basePageSize, tt.thpPageSize)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.wantOrder, order)
		})
	}
}

func TestReadTHPOrder(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	oldPath := THPSizeFilePath
	THPSizeFilePath = filepath.Join(t.TempDir(), "hpage_pmd_size")
	t.Cleanup(func() {
		THPSizeFilePath = oldPath
		ResetTHPOrderForTest()
	})
	ResetTHPOrderForTest()

	thpPageSize := uint64(os.Getpagesize()) << 9
	assert.NoError(t, os.WriteFile(THPSizeFilePath,
		[]byte(strconv.FormatUint(thpPageSize, 10)+"\n"), 0o644))
	order, err := ReadTHPOrder()
	assert.NoError(t, err)
	assert.Equal(t, 9, order)

	// A successful result is cached even if the backing file subsequently changes.
	thpPageSize = uint64(os.Getpagesize()) << 10
	assert.NoError(t, os.WriteFile(THPSizeFilePath,
		[]byte(strconv.FormatUint(thpPageSize, 10)+"\n"), 0o644))
	order, err = ReadTHPOrder()
	assert.NoError(t, err)
	assert.Equal(t, 9, order)

	// Failed reads are not cached, so the next call can recover without an explicit reset.
	ResetTHPOrderForTest()
	assert.NoError(t, os.WriteFile(THPSizeFilePath, []byte("invalid\n"), 0o644))
	_, err = ReadTHPOrder()
	assert.Error(t, err)
	assert.NoError(t, os.WriteFile(THPSizeFilePath,
		[]byte(strconv.FormatUint(thpPageSize, 10)+"\n"), 0o644))
	order, err = ReadTHPOrder()
	assert.NoError(t, err)
	assert.Equal(t, 10, order)
}

func TestReadNumaUnusableIndex(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	oldPath := UnusableIndexFilePath
	UnusableIndexFilePath = filepath.Join(t.TempDir(), "unusable_index")
	t.Cleanup(func() {
		UnusableIndexFilePath = oldPath
	})

	content := "Node 0, zone DMA    0.000 0.000 0.000 0.000 0.000 0.000 0.000 0.000 0.000 0.100 0.200\n" +
		"Node 0, zone Normal 0.000 0.000 0.000 0.000 0.000 0.000 0.000 0.000 0.000 0.867 0.900\n"
	assert.NoError(t, os.WriteFile(UnusableIndexFilePath, []byte(content), 0o644))

	unusableIndex, err := ReadNumaUnusableIndex(0, 9)
	assert.NoError(t, err)
	assert.InDelta(t, 86.7, unusableIndex, 0.001)

	_, err = ReadNumaUnusableIndex(1, 9)
	assert.Error(t, err)
}

func TestReadNumaFreeMemorySizeAtOrAboveOrder(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	root := withFakeProcFS(t)
	pageSize := os.Getpagesize()
	content := "Node 0, zone DMA    0 0 0 0 0 0 0 0 0 8 4\n" +
		"Node 0, zone Normal 0 0 0 0 0 0 0 0 0 2 1\n"
	assert.NoError(t, os.WriteFile(filepath.Join(root, "buddyinfo"), []byte(content), 0o644))

	freeSize, err := ReadNumaFreeMemorySizeAtOrAboveOrder(0, 9)
	assert.NoError(t, err)
	expectedSize := uint64(pageSize) * (uint64(2)*(uint64(1)<<9) + uint64(1)*(uint64(1)<<10))
	assert.Equal(t, expectedSize, freeSize)

	_, err = ReadNumaFreeMemorySizeAtOrAboveOrder(1, 9)
	assert.Error(t, err)
}

func TestDiscoverNumaKcompactdPids(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	root := withFakeProcFS(t)

	writeFakeProc(t, root, 100, "kcompactd0", "S (sleeping)")
	writeFakeProc(t, root, 200, "kcompactd1", "D (disk sleep)")
	writeFakeProc(t, root, 300, "kswapd0", "S (sleeping)") // not kcompactd
	writeFakeProc(t, root, 400, "kcompactdX", "S")         // bad suffix, ignored

	pids := discoverNumaKcompactdPids()
	assert.Equal(t, map[int]int{0: 100, 1: 200}, pids)
}

func TestIsNumaKcompactdBusy(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	root := withFakeProcFS(t)

	writeFakeProc(t, root, 100, "kcompactd0", "S (sleeping)")
	writeFakeProc(t, root, 200, "kcompactd1", "D (disk sleep)")
	writeFakeProc(t, root, 300, "kcompactd2", "R (running)")

	ensureNumaKcompactdPids()

	assert.False(t, IsNumaKcompactdBusy(0)) // sleeping -> not busy
	assert.True(t, IsNumaKcompactdBusy(1))  // D state -> busy
	assert.True(t, IsNumaKcompactdBusy(2))  // R state -> busy
	assert.False(t, IsNumaKcompactdBusy(3)) // unknown pid -> conservatively false
}

func TestReadPidState(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	root := withFakeProcFS(t)

	writeFakeProc(t, root, 100, "kcompactd0", "D (disk sleep)")
	writeFakeProc(t, root, 200, "kcompactd1", "R (running)")

	state, ok := readPidState(100)
	assert.True(t, ok)
	assert.Equal(t, "D", state)

	state, ok = readPidState(200)
	assert.True(t, ok)
	assert.Equal(t, "R", state)

	_, ok = readPidState(999) // missing -> not ok
	assert.False(t, ok)
}

// TestSetHostMemCompact verifies the sysfs payload and propagates errors using a fake node tree.
func TestSetHostMemCompact(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	oldPath := hostMemNodePath
	hostMemNodePath = filepath.Join(t.TempDir(), "node")
	t.Cleanup(func() { hostMemNodePath = oldPath })

	nodePath := hostMemNodePath + "0"
	require.NoError(t, os.Mkdir(nodePath, 0o755))
	require.NoError(t, setHostMemCompact(0))
	content, err := os.ReadFile(filepath.Join(nodePath, "compact"))
	require.NoError(t, err)
	assert.Equal(t, "1", string(content))

	// Missing nodes and an unwritable target must return the underlying filesystem error.
	assert.ErrorIs(t, setHostMemCompact(1), os.ErrNotExist)
	require.NoError(t, os.Remove(filepath.Join(nodePath, "compact")))
	require.NoError(t, os.Mkdir(filepath.Join(nodePath, "compact"), 0o755))
	assert.Error(t, setHostMemCompact(0))
}

func TestTryCompactNUMANode(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	root := withFakeProcFS(t)

	// NUMA 0 kcompactd is sleeping (not busy); NUMA 1 kcompactd is in D state (busy).
	writeFakeProc(t, root, 100, "kcompactd0", "S (sleeping)")
	writeFakeProc(t, root, 200, "kcompactd1", "D (disk sleep)")
	ensureNumaKcompactdPids()

	var compacted []int
	oldFn := CompactMemoryNodeFn
	CompactMemoryNodeFn = func(numaID int) error {
		compacted = append(compacted, numaID)
		return nil
	}
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })

	// NUMA 0 not busy -> compacted, returns true.
	assert.True(t, TryCompactNUMANode(0))
	// NUMA 1 busy -> skipped, returns false.
	assert.False(t, TryCompactNUMANode(1))

	assert.Equal(t, []int{0}, compacted)
}

// TestTryCompactNUMANodeWriteFailure verifies failed writes return false and release the node
// so a subsequent attempt can succeed.
func TestTryCompactNUMANodeWriteFailure(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	root := withFakeProcFS(t)
	writeFakeProc(t, root, 100, "kcompactd0", "S (sleeping)")

	oldFn := CompactMemoryNodeFn
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })
	calls := 0
	CompactMemoryNodeFn = func(int) error {
		calls++
		if calls == 1 {
			return os.ErrPermission
		}
		return nil
	}

	assert.False(t, TryCompactNUMANode(0))
	assert.True(t, TryCompactNUMANode(0), "a failed write must release the per-NUMA guard")
	assert.Equal(t, 2, calls)
}

// TestTryCompactNUMANodeConcurrentSameNode verifies that while one call is compacting a NUMA
// node, a concurrent call targeting the same node is skipped (returns false) instead of running a
// second, overlapping compaction.
func TestTryCompactNUMANodeConcurrentSameNode(t *testing.T) {
	t.Parallel()
	serializeCompactionTest(t)

	root := withFakeProcFS(t)

	// NUMA 0 kcompactd is sleeping (not busy), so compaction is otherwise allowed.
	writeFakeProc(t, root, 100, "kcompactd0", "S (sleeping)")
	ensureNumaKcompactdPids()

	// Block the first compaction inside CompactMemoryNodeFn until the test lets it finish, so the
	// second concurrent call observes an in-progress compaction for the same node.
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	oldFn := CompactMemoryNodeFn
	CompactMemoryNodeFn = func(numaID int) error {
		calls++
		if calls == 1 {
			// Only the first (blocking) compaction signals and waits; later calls return at once.
			close(started)
			<-release
		}
		return nil
	}
	t.Cleanup(func() { CompactMemoryNodeFn = oldFn })

	var wg sync.WaitGroup
	var firstResult bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		firstResult = TryCompactNUMANode(0)
	}()

	// Wait until the first compaction is in progress, then issue a concurrent call for the same node.
	<-started
	secondResult := TryCompactNUMANode(0)

	// The concurrent call must be skipped without invoking CompactMemoryNodeFn again.
	assert.False(t, secondResult)

	// Let the first compaction finish and confirm it succeeded and ran exactly once.
	close(release)
	wg.Wait()
	assert.True(t, firstResult)
	assert.Equal(t, 1, calls)

	// After it returns, the node is no longer marked in progress, so a later call is allowed again.
	assert.True(t, TryCompactNUMANode(0))
}
