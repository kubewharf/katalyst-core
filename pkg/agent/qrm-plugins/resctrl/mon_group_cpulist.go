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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resctrlutil "github.com/kubewharf/katalyst-core/pkg/util/resctrl"
)

// MonGroupCPUList describes CPU ownership, not task affinity.
type MonGroupCPUList struct {
	Name string
	CPUs machine.CPUSet
}

// MonGroupCPUListManager never creates or deletes monitoring groups.
type MonGroupCPUListManager interface {
	ListMonGroupCPULists(context.Context, string) ([]MonGroupCPUList, error)
	ApplyMonGroupCPULists(context.Context, string, map[string]machine.CPUSet) error
}

// Only an incomplete parent restore retains this snapshot across calls.
type monCPUListSnapshot struct {
	dir, file         *os.File
	dirInfo, fileInfo os.FileInfo
	parentMask        []byte
	groups            []*monCPUListFile
	buffer            []byte
}

type monCPUListFile struct {
	name              string
	dir, file         *os.File
	dirInfo, fileInfo os.FileInfo
	CPUs              machine.CPUSet
	mask              []byte
}

type parentMonRestore struct {
	snapshot *monCPUListSnapshot
	targets  map[string]machine.CPUSet
}

func (s *monCPUListSnapshot) close() {
	for _, g := range s.groups {
		g.file.Close()
		g.dir.Close()
	}
	s.file.Close()
	s.dir.Close()
}

// Held descriptors pin the incarnation, so its identity needs only one fstat.
func sameCPUListIdentity(held os.FileInfo, path string) error {
	current, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(held, current) {
		return fmt.Errorf("CPUList identity changed at %q", path)
	}
	return nil
}

func sameHeldCPUListFile(left, right *os.File) (bool, error) {
	a, err := left.Stat()
	if err != nil {
		return false, err
	}
	b, err := right.Stat()
	if err != nil {
		return false, err
	}
	return os.SameFile(a, b), nil
}

func readCPUListBytes(file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	content, err := io.ReadAll(file)
	return bytes.TrimSpace(content), err
}

func (m *cpuListManager) snapshotMonCPULists(ctx context.Context, closID string) (*monCPUListSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(m.root, closID)
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(path, cpus), os.O_RDWR, 0)
	if err != nil {
		dir.Close()
		return nil, err
	}
	s := &monCPUListSnapshot{dir: dir, file: file}
	success := false
	defer func() {
		if !success {
			s.close()
		}
	}()
	s.dirInfo, err = dir.Stat()
	if err != nil {
		return nil, err
	}
	s.fileInfo, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if err := sameCPUListIdentity(s.dirInfo, path); err != nil {
		return nil, err
	}
	if err := sameCPUListIdentity(s.fileInfo, filepath.Join(path, cpus)); err != nil {
		return nil, err
	}
	s.parentMask, err = readCPUListBytes(file)
	if err != nil {
		return nil, err
	}
	maxMask := len(s.parentMask)
	entries, err := os.ReadDir(filepath.Join(path, resctrlutil.MonGroupsDir))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		groupPath := filepath.Join(path, resctrlutil.MonGroupsDir, entry.Name())
		groupDir, err := os.Open(groupPath)
		if err != nil {
			return nil, err
		}
		opener := m.openMonFile
		if opener == nil {
			opener = os.OpenFile
		}
		groupFile, err := opener(filepath.Join(groupPath, cpus), os.O_RDWR, 0)
		if err != nil {
			groupDir.Close()
			return nil, err
		}
		g := &monCPUListFile{name: entry.Name(), dir: groupDir, file: groupFile}
		s.groups = append(s.groups, g)
		g.dirInfo, err = groupDir.Stat()
		if err != nil {
			return nil, err
		}
		g.fileInfo, err = groupFile.Stat()
		if err != nil {
			return nil, err
		}
		if err := sameCPUListIdentity(g.dirInfo, groupPath); err != nil {
			return nil, err
		}
		if err := sameCPUListIdentity(g.fileInfo, filepath.Join(groupPath, cpus)); err != nil {
			return nil, err
		}
		g.mask, err = readCPUListBytes(groupFile)
		if err != nil {
			return nil, err
		}
		g.CPUs, err = parseCPUListMask(g.mask)
		if err != nil {
			return nil, err
		}
		if len(g.mask) > maxMask {
			maxMask = len(g.mask)
		}
	}
	// One trailing newline and one extra byte detect growth, not just an equal prefix.
	s.buffer = make([]byte, maxMask+2)
	if err := m.checkMonCPUListSnapshot(ctx, closID, s); err != nil {
		return nil, err
	}
	success = true
	return s, nil
}

func (s *monCPUListSnapshot) maskMatches(file *os.File, want []byte) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	n, err := io.ReadFull(file, s.buffer)
	if err == nil {
		return fmt.Errorf("CPU mask exceeds snapshot width at %q", file.Name())
	}
	if err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(s.buffer[:n]), want) {
		return fmt.Errorf("CPU ownership changed at %q", file.Name())
	}
	return nil
}

// Retain full identity, membership and ownership checks before every write.
// Raw-mask comparison avoids allocating and parsing CPU sets on each verification.
// Multiple kernfs writes remain non-atomic against external processes.
func (m *cpuListManager) checkMonCPUListSnapshot(ctx context.Context, closID string, s *monCPUListSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(m.root, closID)
	if err := sameCPUListIdentity(s.dirInfo, path); err != nil {
		return err
	}
	if err := sameCPUListIdentity(s.fileInfo, filepath.Join(path, cpus)); err != nil {
		return err
	}
	if err := s.maskMatches(s.file, s.parentMask); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(path, resctrlutil.MonGroupsDir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	index := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if index >= len(s.groups) || entry.Name() != s.groups[index].name {
			return fmt.Errorf("monitor group set changed for CLOS %q", closID)
		}
		index++
	}
	if index != len(s.groups) {
		return fmt.Errorf("monitor group set changed for CLOS %q", closID)
	}
	for _, g := range s.groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		groupPath := filepath.Join(path, resctrlutil.MonGroupsDir, g.name)
		if err := sameCPUListIdentity(g.dirInfo, groupPath); err != nil {
			return err
		}
		if err := sameCPUListIdentity(g.fileInfo, filepath.Join(groupPath, cpus)); err != nil {
			return err
		}
		if err := s.maskMatches(g.file, g.mask); err != nil {
			return err
		}
	}
	return nil
}

func (m *cpuListManager) ListMonGroupCPULists(ctx context.Context, closID string) ([]MonGroupCPUList, error) {
	if err := validateCLOSID(closID); err != nil {
		return nil, err
	}
	var groups []MonGroupCPUList
	update := func() (bool, error) {
		if _, pending := m.restores.Load(closID); pending {
			return false, fmt.Errorf("parent CLOS %q has an incomplete monitor restore", closID)
		}
		if _, err := os.Stat(filepath.Join(m.root, closID)); os.IsNotExist(err) {
			return false, nil
		}
		s, err := m.snapshotMonCPULists(ctx, closID)
		if err != nil {
			return false, err
		}
		defer s.close()
		groups = make([]MonGroupCPUList, 0, len(s.groups))
		for _, g := range s.groups {
			groups = append(groups, MonGroupCPUList{Name: g.name, CPUs: g.CPUs})
		}
		return false, nil
	}
	var err error
	if m.updater != nil {
		err = m.updater.RunClosResourceUpdate(closID, update)
	} else {
		_, err = update()
	}
	return groups, err
}

func (m *cpuListManager) ApplyMonGroupCPULists(ctx context.Context, closID string, targets map[string]machine.CPUSet) error {
	if err := validateCLOSID(closID); err != nil {
		return err
	}
	for name := range targets {
		if err := validateCLOSID(name); err != nil {
			return err
		}
	}
	update := func() (bool, error) {
		if err := m.finishParentRestore(ctx, closID); err != nil {
			return false, err
		}
		return false, m.applyMonGroupCPULists(ctx, closID, targets)
	}
	if m.updater != nil {
		return m.updater.RunClosResourceUpdate(closID, update)
	}
	_, err := update()
	return err
}

func (m *cpuListManager) applyMonGroupCPULists(ctx context.Context, closID string, targets map[string]machine.CPUSet) error {
	if len(targets) == 0 {
		return ctx.Err()
	}
	if _, err := os.Stat(filepath.Join(m.root, closID)); os.IsNotExist(err) {
		return nil
	}
	s, err := m.snapshotMonCPULists(ctx, closID)
	if err != nil {
		return err
	}
	defer s.close()
	return m.assignMonCPULists(ctx, closID, s, targets)
}

// Normal allocation and parent restoration share a single assignment path.
func (m *cpuListManager) assignMonCPULists(ctx context.Context, closID string, s *monCPUListSnapshot, targets map[string]machine.CPUSet) error {
	parent, err := parseCPUListMask(s.parentMask)
	if err != nil {
		return err
	}
	wordCount := bytes.Count(s.parentMask, []byte(",")) + 1
	final := make(map[string]machine.CPUSet, len(s.groups))
	masks := make(map[string][]byte, len(targets))
	used := machine.NewCPUSet()
	for _, g := range s.groups {
		target := g.CPUs
		if set, ok := targets[g.name]; ok {
			target = set
		}
		if !target.IsSubsetOf(parent) {
			return fmt.Errorf("monitor group %q target exceeds parent CLOS %q", g.name, closID)
		}
		var overlap bool
		target.Each(func(cpu int) { overlap = overlap || used.Contains(cpu) })
		if overlap {
			return fmt.Errorf("monitor group %q has conflicting CPU ownership", g.name)
		}
		used.AddSet(target)
		final[g.name] = target
		if !target.Equals(g.CPUs) {
			mask, err := formatCPUSetMask(target, wordCount)
			if err != nil {
				return err
			}
			masks[g.name] = []byte(mask)
		}
	}
	for _, g := range s.groups {
		mask, changed := masks[g.name]
		if !changed {
			continue
		}
		if err := m.checkMonCPUListSnapshot(ctx, closID, s); err != nil {
			return err
		}
		if err := m.writeMonCPUList(g.file, string(mask)); err != nil {
			return fmt.Errorf("assign monitor group %q CPUs: %w", g.name, err)
		}
		for _, sibling := range s.groups {
			if sibling == g {
				continue
			}
			var overlap bool
			final[g.name].Each(func(cpu int) { overlap = overlap || sibling.CPUs.Contains(cpu) })
			if !overlap {
				continue
			}
			sibling.CPUs = sibling.CPUs.Difference(final[g.name])
			value, err := formatCPUSetMask(sibling.CPUs, wordCount)
			if err != nil {
				return err
			}
			sibling.mask = []byte(value)
		}
		g.CPUs, g.mask = final[g.name], mask
	}
	return m.checkMonCPUListSnapshot(ctx, closID, s)
}

func (m *cpuListManager) writeMonCPUList(file *os.File, mask string) error {
	if m.writeMonMask == nil {
		return writeCPUListDescriptor(file, mask)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	n, err := m.writeMonMask(file, mask)
	if err == nil && n != len(mask) {
		err = io.ErrShortWrite
	}
	return err
}

// Keep the original ownership and identities until the clipped restore succeeds.
func (m *cpuListManager) applyParentCPUList(ctx context.Context, closID, mask string) error {
	s, err := m.snapshotMonCPULists(ctx, closID)
	if err != nil {
		return err
	}
	retained := false
	defer func() {
		if !retained {
			s.close()
		}
	}()
	if bytes.Equal(s.parentMask, []byte(mask)) {
		return nil
	}
	parent, err := parseCPUListMask([]byte(mask))
	if err != nil {
		return err
	}
	restore := make(map[string]machine.CPUSet, len(s.groups))
	for _, g := range s.groups {
		restore[g.name] = g.CPUs.Intersection(parent)
	}
	if err := m.checkMonCPUListSnapshot(ctx, closID, s); err != nil {
		return err
	}
	if m.writeFile != nil {
		err = m.writeFile(filepath.Join(m.root, closID, cpus), []byte(mask), 0o644)
	} else {
		err = writeCPUListDescriptor(s.file, mask)
	}
	if err != nil {
		return err
	}
	s.parentMask = []byte(mask)
	zero, err := formatCPUSetMask(machine.NewCPUSet(), bytes.Count(s.parentMask, []byte(","))+1)
	if err != nil {
		return err
	}
	for _, g := range s.groups {
		g.CPUs, g.mask = machine.NewCPUSet(), []byte(zero)
	}
	if len(s.buffer) < len(mask)+2 {
		s.buffer = make([]byte, len(mask)+2)
	}
	m.restores.Store(closID, &parentMonRestore{snapshot: s, targets: restore})
	retained = true
	return m.finishParentRestore(ctx, closID)
}

func (m *cpuListManager) retireParentRestore(ctx context.Context, closID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	value, pending := m.restores.Load(closID)
	if !pending {
		return false, nil
	}
	r := value.(*parentMonRestore)
	held, err := r.snapshot.dir.Stat()
	if err != nil {
		return false, err
	}
	current, err := os.Stat(filepath.Join(m.root, closID))
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err == nil && os.SameFile(held, current) {
		return false, nil
	}
	m.restores.Delete(closID)
	r.snapshot.close()
	return true, nil
}

func (m *cpuListManager) finishParentRestore(ctx context.Context, closID string) error {
	parentRetired, err := m.retireParentRestore(ctx, closID)
	if err != nil {
		return err
	}
	if parentRetired {
		return fmt.Errorf("parent CLOS %q retired during incomplete restore", closID)
	}
	value, pending := m.restores.Load(closID)
	if !pending {
		return nil
	}
	r := value.(*parentMonRestore)
	// Refresh membership only after proving the same parent incarnation. Retired
	// children lose their restore targets; replacements retain current ownership.
	fresh, err := m.snapshotMonCPULists(ctx, closID)
	if err != nil {
		return err
	}
	accepted := false
	defer func() {
		if !accepted {
			fresh.close()
		}
	}()
	parentMatches, err := sameHeldCPUListFile(r.snapshot.file, fresh.file)
	if err != nil {
		return err
	}
	if !parentMatches {
		return fmt.Errorf("parent CLOS %q CPU mask identity changed during incomplete restore", closID)
	}
	if !bytes.Equal(r.snapshot.parentMask, fresh.parentMask) {
		return fmt.Errorf("parent CLOS %q changed during incomplete restore", closID)
	}
	byName := make(map[string]*monCPUListFile, len(fresh.groups))
	for _, g := range fresh.groups {
		byName[g.name] = g
	}
	var retired []string
	for _, old := range r.snapshot.groups {
		current := byName[old.name]
		if current == nil {
			retired = append(retired, old.name)
			continue
		}
		dirMatches, err := sameHeldCPUListFile(old.dir, current.dir)
		if err != nil {
			return err
		}
		fileMatches, err := sameHeldCPUListFile(old.file, current.file)
		if err != nil {
			return err
		}
		if !dirMatches || !fileMatches {
			retired = append(retired, old.name)
			continue
		}
		if !bytes.Equal(old.mask, current.mask) {
			return fmt.Errorf("monitor group %q changed during incomplete restore", old.name)
		}
	}
	for _, name := range retired {
		delete(r.targets, name)
	}
	r.snapshot.close()
	r.snapshot = fresh
	accepted = true
	if err := m.assignMonCPULists(ctx, closID, r.snapshot, r.targets); err != nil {
		return err
	}
	m.restores.Delete(closID)
	r.snapshot.close()
	return nil
}

func writeCPUListDescriptor(file *os.File, mask string) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	n, err := file.WriteString(mask)
	if err == nil && n != len(mask) {
		err = io.ErrShortWrite
	}
	return err
}

func parseCPUListMask(content []byte) (machine.CPUSet, error) {
	set := machine.NewCPUSet()
	words := strings.Split(strings.TrimSpace(string(content)), ",")
	for i, word := range words {
		value, err := strconv.ParseUint(word, 16, 32)
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("parse CPU mask: %w", err)
		}
		for bit := 0; bit < 32; bit++ {
			if value&(uint64(1)<<uint(bit)) != 0 {
				set.Add((len(words)-1-i)*32 + bit)
			}
		}
	}
	return set, nil
}
