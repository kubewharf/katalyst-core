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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/external/rdt"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	cpus         = "cpus"
	monGroupsDir = "mon_groups"
)

// CPUListClos describes an existing resctrl CLOS directory. Epoch changes
// whenever the directory's filesystem identity changes.
type CPUListClos struct {
	ID    string
	Epoch uint64
}

// CPUListManager is the narrow resctrl query/write boundary used by CPU-side
// RDT resource plugins. ApplyCPUList may create a missing CLOS only when it
// needs to write a non-empty target; CLOS deletion stays owned by memory
// resctrl.Manager.
type CPUListManager interface {
	ListManagedClos(context.Context) ([]CPUListClos, error)
	ApplyCPUList(context.Context, string, string) error
}

type closResourceUpdater interface {
	RunClosResourceUpdate(closID string, update func() (bool, error)) error
}

type cpuListManager struct {
	root string

	mu      sync.Mutex
	epochs  map[string]uint64
	handles map[string]*os.File
	updater closResourceUpdater
	// writeFile is injectable so tests can force a failed cpus write and
	// observe rollback of a freshly created CLOS directory. It defaults to
	// os.WriteFile.
	writeFile func(name string, data []byte, perm os.FileMode) error
}

func NewCPUListManager() CPUListManager {
	return newCPUListManagerWithUpdater(consts.DefaultResctrlRootDir, rdt.NewDefaultManager())
}

func newCPUListManagerWithUpdater(root string, updater closResourceUpdater) *cpuListManager {
	return &cpuListManager{
		root:      root,
		epochs:    make(map[string]uint64),
		handles:   make(map[string]*os.File),
		updater:   updater,
		writeFile: os.WriteFile,
	}
}

// ListManagedClos enumerates the CLOS directories currently present at the
// resctrl root and reports each with an Epoch that advances whenever the
// directory at that path is a different incarnation than the one the manager
// previously observed.
//
// Identity fencing is done with held open directory file descriptors, not with
// stat timestamps: while the manager keeps a CLOS directory open, the kernel
// cannot recycle that inode, so a same-path delete+recreate is guaranteed to
// land on a different inode. Comparing the still-open old handle's f.Stat() to
// the current path stat via os.SameFile therefore detects the recreation
// reliably even on filesystems that reuse inode numbers (resctrl kernfs, Linux
// tmpfs). Detected recreations bump the epoch, swap the held handle, and close
// the old one; CLOS that disappeared have their handles closed.
func (m *cpuListManager) ListManagedClos(_ context.Context) ([]CPUListClos, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries, err := os.ReadDir(m.root)
	if os.IsNotExist(err) {
		m.closeAllHandlesLocked()
		m.epochs = make(map[string]uint64)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read resctrl root %q: %w", m.root, err)
	}

	seen := make(map[string]struct{}, len(entries))
	clos := make([]CPUListClos, 0, len(entries))
	for _, entry := range entries {
		if !isClosDir(entry) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat CLOS %q: %w", entry.Name(), err)
		}
		id := entry.Name()
		seen[id] = struct{}{}

		reincarnated := true
		if old, ok := m.handles[id]; ok {
			if oldStat, err := old.Stat(); err == nil && os.SameFile(oldStat, info) {
				reincarnated = false
			}
		}
		if reincarnated {
			m.epochs[id]++
			m.replaceHandleLocked(id)
		}
		clos = append(clos, CPUListClos{ID: id, Epoch: m.epochs[id]})
	}
	// Close handles for CLOS that disappeared since the last poll, and drop
	// their epoch generations so a deleted CLOS cannot leak an fd/memory nor be
	// resurrected by a later reuse of the same name carrying a stale generation.
	for id, h := range m.handles {
		if _, ok := seen[id]; !ok {
			h.Close()
			delete(m.handles, id)
			delete(m.epochs, id)
		}
	}
	return clos, nil
}

func (m *cpuListManager) ApplyCPUList(_ context.Context, closID, target string) error {
	if err := validateCLOSID(closID); err != nil {
		return err
	}
	createMissingClos := strings.TrimSpace(target) != ""
	mask, err := m.formatCPUListMask(target)
	if err != nil {
		return fmt.Errorf("format CLOS %q cpu_list %q: %w", closID, target, err)
	}
	update := func() (bool, error) {
		created, err := m.ensureClos(closID, createMissingClos)
		if err != nil {
			return false, err
		}
		if err := m.writeFile(filepath.Join(m.root, closID, cpus), []byte(mask), 0o644); err != nil {
			if os.IsNotExist(err) && !createMissingClos {
				return created, nil
			}
			if created {
				// Roll back the directory we just materialized so a failed
				// initial cpus write does not leave an empty, half-initialized
				// CLOS behind for the memory manager to reconcile later.
				_ = os.RemoveAll(filepath.Join(m.root, closID))
			}
			return created, err
		}
		if created {
			// The manager just materialized a new CLOS directory incarnation.
			// Own the epoch transition and record the directory identity now
			// (after cpus exists) so the next poll matches on both inode and
			// mtime without a spurious double-bump. This is in addition to the
			// polling-time sameCLOSIdentity check, which also catches recreations
			// the manager did not perform itself.
			m.noteClosCreated(closID)
		}
		return created, nil
	}
	var writeErr error
	if m.updater != nil {
		writeErr = m.updater.RunClosResourceUpdate(closID, update)
	} else {
		_, writeErr = update()
	}
	if writeErr != nil {
		return fmt.Errorf("write CLOS %q cpu_list: %w", closID, writeErr)
	}
	return nil
}

func (m *cpuListManager) CPUListMatches(_ context.Context, closID, target string) (bool, error) {
	if err := validateCLOSID(closID); err != nil {
		return false, err
	}
	mask, err := m.formatCPUListMask(target)
	if err != nil {
		return false, fmt.Errorf("format CLOS %q cpu_list %q: %w", closID, target, err)
	}
	content, err := os.ReadFile(filepath.Join(m.root, closID, cpus))
	if err != nil {
		return false, fmt.Errorf("read CLOS %q cpu_list: %w", closID, err)
	}
	return strings.TrimSpace(string(content)) == mask, nil
}

func (m *cpuListManager) ensureClos(closID string, create bool) (bool, error) {
	if err := validateCLOSID(closID); err != nil {
		return false, err
	}
	closPath := filepath.Join(m.root, closID)
	if !create {
		return false, nil
	}
	if _, err := os.Stat(closPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat CLOS %q: %w", closID, err)
	}
	if err := os.Mkdir(closPath, 0o755); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("create CLOS %q: %w", closID, err)
	}
	return true, nil
}

// noteClosCreated records that the manager has just created a new CLOS
// directory incarnation. It advances the per-CLOS epoch and swaps the held
// directory handle to the freshly created path so the next ListManagedClos poll
// sees a matching handle (no double-bump) and keeps the old inode pinned only
// until the swap. Opening the new handle is best-effort: on failure the next
// poll reconciles by replacing whatever handle it finds.
func (m *cpuListManager) noteClosCreated(closID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epochs[closID]++
	m.replaceHandleLocked(closID)
}

// replaceHandleLocked closes any previously held handle for closID and opens a
// new one on the directory at the manager root. Callers must hold m.mu.
func (m *cpuListManager) replaceHandleLocked(closID string) {
	if old, ok := m.handles[closID]; ok {
		old.Close()
		delete(m.handles, closID)
	}
	f, err := os.Open(filepath.Join(m.root, closID))
	if err != nil {
		return
	}
	m.handles[closID] = f
}

// closeAllHandlesLocked closes every held CLOS directory handle and clears the
// map. Callers must hold m.mu.
func (m *cpuListManager) closeAllHandlesLocked() {
	for id, h := range m.handles {
		h.Close()
		delete(m.handles, id)
	}
}

func (m *cpuListManager) formatCPUListMask(target string) (string, error) {
	wordCount, err := m.resctrlCPUMaskWordCount()
	if err != nil {
		return "", err
	}
	words := make([]uint32, wordCount)
	if strings.TrimSpace(target) == "" {
		return formatCPUListMaskWords(words), nil
	}
	cpus, err := machine.Parse(target)
	if err != nil {
		return "", fmt.Errorf("parse cpu list: %w", err)
	}
	for _, cpu := range cpus.ToSliceInt() {
		if cpu < 0 {
			return "", fmt.Errorf("negative cpu id %d", cpu)
		}
		word := cpu / 32
		if word >= len(words) {
			return "", fmt.Errorf("cpu id %d exceeds resctrl cpus mask width %d", cpu, len(words)*32)
		}
		words[word] |= uint32(1) << uint(cpu%32)
	}
	return formatCPUListMaskWords(words), nil
}

func (m *cpuListManager) resctrlCPUMaskWordCount() (int, error) {
	content, err := os.ReadFile(filepath.Join(m.root, cpus))
	if err != nil {
		return 0, fmt.Errorf("read resctrl root cpus: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(content)), ",")
	count := 0
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, err := strconv.ParseUint(part, 16, 32); err != nil {
			return 0, fmt.Errorf("parse resctrl root cpus word %q: %w", part, err)
		}
		count++
	}
	if count == 0 {
		return 0, fmt.Errorf("resctrl root cpus is empty")
	}
	return count, nil
}

// reservedResctrlRootEntries are non-CLOS directories that always live at the
// resctrl root. It is package-level and only ever read, so ListManagedClos can
// reuse it across every enumerated entry instead of allocating a fresh set per
// entry on each periodic list.
var reservedResctrlRootEntries = sets.NewString("info", "mon_data", monGroupsDir)

func isClosDir(entry os.DirEntry) bool {
	return entry.IsDir() && !reservedResctrlRootEntries.Has(entry.Name())
}

func formatCPUListMaskWords(words []uint32) string {
	parts := make([]string, 0, len(words))
	for i := len(words) - 1; i >= 0; i-- {
		parts = append(parts, fmt.Sprintf("%08x", words[i]))
	}
	return strings.Join(parts, ",")
}
