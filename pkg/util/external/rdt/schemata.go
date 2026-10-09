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

package rdt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	schemataFile = "schemata"
	l3Resource   = "L3"
	mbResource   = "MB"
)

type closLocker interface {
	Lock()
	Unlock()
}

// SchemataCoordinator serializes updates to a CLOS schemata file and caches
// successfully applied resource targets. A schemata file contains multiple
// independently managed resources, so each update is read-modify-write.
type SchemataCoordinator struct {
	root string

	readFile  func(string) ([]byte, error)
	writeFile func(string, []byte, os.FileMode) error

	mu          sync.Mutex
	closMu      map[string]closLocker
	newClosLock func() closLocker
	targets     map[string]map[string]string
}

func newSchemataCoordinator(root string) *SchemataCoordinator {
	return newSchemataCoordinatorWithClosLock(root, func() closLocker {
		return &sync.Mutex{}
	})
}

func newSchemataCoordinatorWithClosLock(root string, newClosLock func() closLocker) *SchemataCoordinator {
	return &SchemataCoordinator{
		root:        root,
		readFile:    os.ReadFile,
		writeFile:   os.WriteFile,
		closMu:      make(map[string]closLocker),
		newClosLock: newClosLock,
		targets:     make(map[string]map[string]string),
	}
}

// ApplyL3 applies only the L3 schemata line.
func (s *SchemataCoordinator) ApplyL3(clos string, ways map[int]uint64) error {
	return s.apply(clos, l3Resource, formatSchemataValues(ways, 16))
}

// ApplyMB applies only the MB schemata line.
func (s *SchemataCoordinator) ApplyMB(clos string, mba map[int]int) error {
	return s.apply(clos, mbResource, formatMBValues(mba))
}

func (s *SchemataCoordinator) apply(clos, resource, target string) error {
	closMu := s.getClosMu(clos)
	closMu.Lock()
	defer closMu.Unlock()

	if s.isCached(clos, resource, target) {
		return nil
	}

	path := filepath.Join(s.root, clos, schemataFile)
	current, err := s.readFile(path)
	if err != nil {
		return fmt.Errorf("read schemata for CLOS %q: %w", clos, err)
	}

	updated := replaceSchemataLine(string(current), resource, target)
	if err := s.writeFile(path, []byte(updated), 0o644); err != nil {
		// A failed write may have changed the file partially. Invalidate every
		// target for this CLOS so a later reconciliation always retries RMW.
		s.invalidateClos(clos)
		return fmt.Errorf("write schemata for CLOS %q: %w", clos, err)
	}

	s.cache(clos, resource, target)
	return nil
}

func (s *SchemataCoordinator) getClosMu(clos string) closLocker {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closMu[clos] == nil {
		s.closMu[clos] = s.newClosLock()
	}
	return s.closMu[clos]
}

func (s *SchemataCoordinator) isCached(clos, resource, target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targets[clos][resource] == target
}

func (s *SchemataCoordinator) cache(clos, resource, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.targets[clos] == nil {
		s.targets[clos] = make(map[string]string)
	}
	s.targets[clos][resource] = target
}

func (s *SchemataCoordinator) invalidateClos(clos string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.targets, clos)
}

// InvalidateClos clears cached targets for a CLOS that has been created or
// removed outside this coordinator.
func (s *SchemataCoordinator) InvalidateClos(clos string) {
	closMu := s.getClosMu(clos)
	closMu.Lock()
	defer closMu.Unlock()

	s.invalidateClos(clos)
}

// RunClosResourceUpdate serializes any non-schemata CLOS resource update with
// schemata read-modify-write operations and lifecycle updates for the same
// CLOS. If update reports changed=true, cached schemata targets are invalidated
// before the per-CLOS lock is released, even when update returns an error.
func (s *SchemataCoordinator) RunClosResourceUpdate(clos string, update func() (bool, error)) error {
	closMu := s.getClosMu(clos)
	closMu.Lock()
	defer closMu.Unlock()

	changed, err := update()
	if changed {
		s.invalidateClos(clos)
	}
	if err != nil {
		return err
	}
	return nil
}

// RunClosLifecycle serializes a CLOS filesystem lifecycle update with schemata
// read-modify-write operations. If update reports changed=true, cached
// schemata targets are invalidated before the per-CLOS lock is released, even
// when update returns an error.
func (s *SchemataCoordinator) RunClosLifecycle(clos string, update func() (bool, error)) error {
	closMu := s.getClosMu(clos)
	closMu.Lock()
	defer closMu.Unlock()

	changed, err := update()
	if changed {
		s.invalidateClos(clos)
	}
	if err != nil {
		return err
	}
	return nil
}

func formatSchemataValues(values map[int]uint64, base int) string {
	keys := make([]int, 0, len(values))
	for id := range values {
		keys = append(keys, id)
	}
	sort.Ints(keys)

	var builder strings.Builder
	for _, id := range keys {
		if base == 16 {
			fmt.Fprintf(&builder, "%d=%x;", id, values[id])
		} else {
			fmt.Fprintf(&builder, "%d=%d;", id, values[id])
		}
	}
	return builder.String()
}

func formatMBValues(values map[int]int) string {
	keys := make([]int, 0, len(values))
	for id := range values {
		keys = append(keys, id)
	}
	sort.Ints(keys)

	var builder strings.Builder
	for _, id := range keys {
		fmt.Fprintf(&builder, "%d=%d;", id, values[id])
	}
	return builder.String()
}

func replaceSchemataLine(schemata, resource, target string) string {
	lines := strings.Split(strings.TrimSuffix(schemata, "\n"), "\n")
	prefix := resource + ":"
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[i] = prefix + target
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, prefix+target)
	}
	return strings.Join(lines, "\n") + "\n"
}
