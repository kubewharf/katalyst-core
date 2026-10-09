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
	"strings"
	"sync"
)

type CLOSLifecycleService interface {
	EnsurePendingCLOS(ctx context.Context, closID string) (ResolvedCLOS, error)
	DeleteCLOS(ctx context.Context, closID string) error
	Recover(ctx context.Context) error
}

type closLifecycleService struct {
	root   string
	mu     *sync.Mutex
	mkdir  func(string, os.FileMode) error
	remove func(string) error
}

var lifecycleLocks sync.Map

func NewCLOSLifecycleService(root string) CLOSLifecycleService {
	lock, _ := lifecycleLocks.LoadOrStore(root, &sync.Mutex{})
	return newCLOSLifecycleService(root, lock.(*sync.Mutex), os.Mkdir, os.RemoveAll)
}

func NewCLOSLifecycleServiceWithOperations(root string,
	mkdir func(string, os.FileMode) error, remove func(string) error,
) CLOSLifecycleService {
	lock, _ := lifecycleLocks.LoadOrStore(root, &sync.Mutex{})
	return newCLOSLifecycleService(root, lock.(*sync.Mutex), mkdir, remove)
}

func newCLOSLifecycleService(root string, mu *sync.Mutex,
	mkdir func(string, os.FileMode) error, remove func(string) error,
) CLOSLifecycleService {
	if mkdir == nil {
		mkdir = os.Mkdir
	}
	if remove == nil {
		remove = os.RemoveAll
	}
	return &closLifecycleService{root: root, mu: mu, mkdir: mkdir, remove: remove}
}

func (s *closLifecycleService) EnsurePendingCLOS(_ context.Context, closID string) (ResolvedCLOS, error) {
	if err := validateCLOSID(closID); err != nil {
		return ResolvedCLOS{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.root, closID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := s.mkdir(path, 0o755); err != nil && !os.IsExist(err) {
			return ResolvedCLOS{}, fmt.Errorf("create CLOS %q: %w", closID, err)
		}
	} else if err != nil {
		return ResolvedCLOS{}, fmt.Errorf("inspect CLOS %q: %w", closID, err)
	}

	identity, err := DirectoryIdentityForPath(path)
	if err != nil {
		return ResolvedCLOS{}, err
	}
	return ResolvedCLOS{
		ID:         closID,
		Identity:   identity,
		Generation: 1,
		Phase:      ActivationActive,
	}, nil
}

func (s *closLifecycleService) DeleteCLOS(_ context.Context, closID string) error {
	if err := validateCLOSID(closID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.root, closID)
	empty, err := closDirectoryEmpty(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("refuse to delete non-empty CLOS %q", closID)
	}
	return s.remove(path)
}

func (s *closLifecycleService) Recover(_ context.Context) error {
	return nil
}

func validateCLOSID(closID string) error {
	if closID == "" || closID == "." || closID == ".." ||
		filepath.IsAbs(closID) || strings.ContainsAny(closID, `/\`) {
		return fmt.Errorf("invalid clos id %q", closID)
	}
	return nil
}

func closDirectoryEmpty(path string) (bool, error) {
	for _, name := range []string{"tasks", "cpus"} {
		content, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			return false, err
		}
		if len(strings.TrimSpace(string(content))) != 0 {
			return false, nil
		}
	}
	entries, err := os.ReadDir(filepath.Join(path, "mon_groups"))
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
