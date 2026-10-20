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

// Package compaction holds the node-level memory-compaction primitives shared by the memory
// handlers (numacompact and fragmem): reading per-NUMA fragmentation state, triggering compaction
// via sysfs, discovering the per-NUMA kcompactd kernel threads, and skipping a node whose kcompactd
// is currently busy.
package compaction

import (
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// hostMemNodePath is the sysfs prefix for per-NUMA node knobs; "<node>/compact" triggers
	// node-level memory compaction.
	hostMemNodePath = "/sys/devices/system/node/node"

	// commandKcompactd is the comm prefix of the per-NUMA kcompactd kernel threads
	// ("kcompactd<numaID>", e.g. kcompactd0).
	commandKcompactd = "kcompactd"
)

var (
	// ProcFSRoot is the procfs mount point. It is a variable so tests can point it at a fake tree.
	ProcFSRoot = "/proc"

	// UnusableIndexFilePath exposes the kernel's per-NUMA, per-zone external fragmentation data.
	// It is a variable so tests can point it at a fake file.
	UnusableIndexFilePath = "/sys/kernel/debug/extfrag/unusable_index"

	// THPSizeFilePath exposes the transparent hugepage size in bytes. It is a variable so tests can
	// point it at a fake file.
	THPSizeFilePath = "/sys/kernel/mm/transparent_hugepage/hpage_pmd_size"

	// cachedTHPOrder is populated after the first successful THP-order calculation. Base page size
	// and THP page size are immutable for the lifetime of a running kernel.
	cachedTHPOrder   *int
	cachedTHPOrderMu sync.Mutex

	// numaKcompactdPid maps a NUMA node id to the pid of its kcompactd kernel thread, whose comm is
	// "kcompactd<numaID>". It is discovered once (see ensureNumaKcompactdPids) so that the per-cycle
	// state check does not have to walk /proc every time.
	numaKcompactdPid   = make(map[int]int)
	numaKcompactdPidMu sync.RWMutex

	// CompactMemoryNodeFn triggers node-level memory compaction. It is a variable so that tests can
	// observe which NUMA nodes get compacted without touching sysfs.
	CompactMemoryNodeFn = setHostMemCompact

	// numaCompacting tracks which NUMA nodes currently have a compaction in progress, so that a
	// concurrent call (e.g. from another plugin sharing this helper) targeting the same node is
	// skipped rather than duplicating the work. Entries are added before the compaction starts and
	// removed once it returns.
	numaCompacting   = make(map[int]struct{})
	numaCompactingMu sync.Mutex
)

// tryAcquireNumaCompacting attempts to mark numaID as being compacted. It returns true if the
// caller acquired it (no compaction was in progress for that node), and false if another compaction
// is already running for numaID and the caller should skip.
func tryAcquireNumaCompacting(numaID int) bool {
	numaCompactingMu.Lock()
	defer numaCompactingMu.Unlock()
	if _, ok := numaCompacting[numaID]; ok {
		return false
	}
	numaCompacting[numaID] = struct{}{}
	return true
}

// releaseNumaCompacting clears the in-progress mark for numaID.
func releaseNumaCompacting(numaID int) {
	numaCompactingMu.Lock()
	defer numaCompactingMu.Unlock()
	delete(numaCompacting, numaID)
}

// setHostMemCompact triggers node-level memory compaction for the given NUMA node via sysfs.
func setHostMemCompact(node int) {
	targetFile := hostMemNodePath + strconv.Itoa(node) + "/compact"
	_ = os.WriteFile(targetFile, []byte(fmt.Sprintf("%d", 1)), 0o644)
}

func calculateTHPOrder(basePageSize int, thpSize uint64) (int, error) {
	if basePageSize <= 0 {
		return 0, fmt.Errorf("invalid base page size %d", basePageSize)
	}

	baseSize := uint64(basePageSize)
	if thpSize < baseSize || thpSize%baseSize != 0 {
		return 0, fmt.Errorf("THP page size %d is not a multiple of base page size %d", thpSize, basePageSize)
	}
	pageCount := thpSize / baseSize
	if pageCount&(pageCount-1) != 0 {
		return 0, fmt.Errorf("THP page count %d is not a power of two", pageCount)
	}
	return bits.TrailingZeros64(pageCount), nil
}

// ReadTHPOrder returns the buddy allocation order corresponding to the kernel's transparent
// hugepage size and the current machine's base page size.
func ReadTHPOrder() (int, error) {
	cachedTHPOrderMu.Lock()
	defer cachedTHPOrderMu.Unlock()
	if cachedTHPOrder != nil {
		return *cachedTHPOrder, nil
	}

	data, err := os.ReadFile(THPSizeFilePath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", THPSizeFilePath, err)
	}
	thpSize, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || thpSize == 0 {
		return 0, fmt.Errorf("invalid THP page size %q", strings.TrimSpace(string(data)))
	}
	basePageSize := os.Getpagesize()
	order, err := calculateTHPOrder(basePageSize, thpSize)
	if err != nil {
		return 0, err
	}
	general.Infof("determined THP order %d (base page size %d bytes, THP page size %d bytes)",
		order, basePageSize, thpSize)
	cachedTHPOrder = &order
	return order, nil
}

// ReadNumaUnusableIndex reads the unusable index for the requested NUMA node, Normal zone, and
// allocation order. The kernel exposes the index in [0, 1]; this function returns the percentage
// form in [0, 100].
func ReadNumaUnusableIndex(numaID, order int) (float64, error) {
	data, err := os.ReadFile(UnusableIndexFilePath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", UnusableIndexFilePath, err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "Node" || fields[2] != "zone" || fields[3] != "Normal" {
			continue
		}

		nodeID, err := strconv.Atoi(strings.TrimSuffix(fields[1], ","))
		if err != nil || nodeID != numaID {
			continue
		}

		if order < 0 {
			return 0, fmt.Errorf("invalid negative order %d", order)
		}
		unusableIndexField := 4 + order
		if unusableIndexField >= len(fields) {
			return 0, fmt.Errorf("order %d is missing for NUMA %d Normal zone", order, numaID)
		}

		unusableIndex, err := strconv.ParseFloat(fields[unusableIndexField], 64)
		if err != nil || math.IsNaN(unusableIndex) || math.IsInf(unusableIndex, 0) ||
			unusableIndex < 0 || unusableIndex > 1 {
			return 0, fmt.Errorf("invalid unusable index %q for NUMA %d order %d",
				fields[unusableIndexField], numaID, order)
		}
		return unusableIndex * 100, nil
	}

	return 0, fmt.Errorf("NUMA %d Normal zone not found in %s", numaID, UnusableIndexFilePath)
}

// ReadNumaFreeMemorySizeAtOrAboveOrder reads /proc/buddyinfo and returns, in bytes, the total free
// memory in the requested NUMA node's Normal zone whose buddy order is at least minOrder.
func ReadNumaFreeMemorySizeAtOrAboveOrder(numaID, minOrder int) (uint64, error) {
	if minOrder < 0 {
		return 0, fmt.Errorf("invalid negative order %d", minOrder)
	}

	buddyInfoPath := filepath.Join(ProcFSRoot, "buddyinfo")
	data, err := os.ReadFile(buddyInfoPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", buddyInfoPath, err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "Node" || fields[2] != "zone" || fields[3] != "Normal" {
			continue
		}

		nodeID, err := strconv.Atoi(strings.TrimSuffix(fields[1], ","))
		if err != nil || nodeID != numaID {
			continue
		}

		orderCounts := fields[4:]
		if minOrder >= len(orderCounts) {
			return 0, fmt.Errorf("order %d is missing for NUMA %d Normal zone", minOrder, numaID)
		}

		var freeSize uint64
		pageSize := uint64(os.Getpagesize())
		for order := minOrder; order < len(orderCounts); order++ {
			blockCount, err := strconv.ParseUint(orderCounts[order], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid free block count %q for NUMA %d order %d",
					orderCounts[order], numaID, order)
			}
			freeSize += blockCount * pageSize * (uint64(1) << order)
		}
		return freeSize, nil
	}

	return 0, fmt.Errorf("NUMA %d Normal zone not found in %s", numaID, buddyInfoPath)
}

// discoverNumaKcompactdPids walks ProcFSRoot and records, per NUMA node, the pid of its kcompactd
// kernel thread (comm == "kcompactd<numaID>").
func discoverNumaKcompactdPids() map[int]int {
	result := make(map[int]int)

	dirEnts, err := os.ReadDir(ProcFSRoot)
	if err != nil {
		general.Errorf("failed to read %s: %v", ProcFSRoot, err)
		return result
	}

	for _, de := range dirEnts {
		pid, err := strconv.Atoi(de.Name())
		if err != nil || pid <= 0 {
			continue
		}

		b, err := os.ReadFile(filepath.Join(ProcFSRoot, de.Name(), "comm"))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(b))

		// kcompactd kernel threads are named "kcompactd<node>" (e.g. kcompactd0, kcompactd1).
		if !strings.HasPrefix(comm, commandKcompactd) {
			continue
		}
		numaID, err := strconv.Atoi(strings.TrimPrefix(comm, commandKcompactd))
		if err != nil || numaID < 0 {
			continue
		}
		result[numaID] = pid
	}

	return result
}

// ensureNumaKcompactdPids discovers the per-NUMA kcompactd pids once and caches them. Until the
// discovery finds at least one kcompactd thread it retries on every call, so a transient failure
// (or a not-yet-ready procfs) does not permanently leave the cache empty.
func ensureNumaKcompactdPids() {
	numaKcompactdPidMu.RLock()
	populated := len(numaKcompactdPid) > 0
	numaKcompactdPidMu.RUnlock()
	if populated {
		return
	}

	pids := discoverNumaKcompactdPids()
	if len(pids) == 0 {
		return
	}

	numaKcompactdPidMu.Lock()
	numaKcompactdPid = pids
	numaKcompactdPidMu.Unlock()
	general.Infof("discovered kcompactd pids per NUMA: %v", pids)
}

// readPidState returns the single-letter process state (e.g. "R", "S", "D") read from the State
// field of /proc/<pid>/status, and whether it was read successfully.
func readPidState(pid int) (string, bool) {
	b, err := os.ReadFile(filepath.Join(ProcFSRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return "", false
	}

	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "State:") {
			continue
		}
		// e.g. "State:\tD (disk sleep)" -> fields[1] == "D".
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[1], true
		}
	}
	return "", false
}

// IsNumaKcompactdBusy reports whether the kcompactd thread bound to numaID is currently busy doing
// compaction, i.e. it is in the R (running) or D (uninterruptible sleep) state. When the pid is
// unknown or its state cannot be read, it conservatively returns false so compaction is allowed.
func IsNumaKcompactdBusy(numaID int) bool {
	numaKcompactdPidMu.RLock()
	pid, ok := numaKcompactdPid[numaID]
	numaKcompactdPidMu.RUnlock()
	if !ok {
		return false
	}

	state, ok := readPidState(pid)
	if !ok {
		return false
	}
	return state == "R" || state == "D"
}

// TryCompactNUMANode triggers node-level memory compaction for numaID unless that node's own
// kcompactd thread is currently busy (R/D state), in which case the node is skipped. It returns
// true if compaction was performed and false if the node was skipped (kcompactd busy, or another
// caller is already compacting this node). Emitting a metric is left to the caller so each plugin
// can use its own metric name and tags. The per-NUMA kcompactd pids are discovered lazily on first
// use (and retried until found), so callers do not have to prime them before the scan loop.
func TryCompactNUMANode(numaID int) bool {
	// Guard against concurrent compaction of the same node (e.g. two plugins sharing this helper):
	// if another caller is already compacting numaID, skip rather than duplicate the work.
	if !tryAcquireNumaCompacting(numaID) {
		general.Infof("skip NUMA %d: a compaction is already in progress", numaID)
		return false
	}
	defer releaseNumaCompacting(numaID)

	// Make sure the per-NUMA kcompactd pids are known before consulting their state. This is a
	// no-op once the pids have been discovered.
	ensureNumaKcompactdPids()

	if IsNumaKcompactdBusy(numaID) {
		general.Infof("skip NUMA %d: kcompactd is busy (R/D state)", numaID)
		return false
	}

	CompactMemoryNodeFn(numaID)
	return true
}

// ResetKcompactdPidsForTest clears the discovered kcompactd pid cache so tests can start from a
// known-empty state. It is intended for use by tests only.
func ResetKcompactdPidsForTest() {
	numaKcompactdPidMu.Lock()
	defer numaKcompactdPidMu.Unlock()
	numaKcompactdPid = make(map[int]int)
}

// ResetTHPOrderForTest clears the cached THP order. It is intended for use by tests only.
func ResetTHPOrderForTest() {
	cachedTHPOrderMu.Lock()
	defer cachedTHPOrderMu.Unlock()
	cachedTHPOrder = nil
}
