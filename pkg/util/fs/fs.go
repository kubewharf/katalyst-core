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

// Package fs provides small filesystem helpers that can be replaced by fakes in tests.
package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"strings"
)

// FS provides the filesystem operations required by util helpers.
type FS interface {
	// ReadFile returns the raw bytes at path.
	ReadFile(path string) ([]byte, error)
	// WriteFile writes b to path with mode.
	WriteFile(path string, b []byte, mode os.FileMode) error
	// Exists reports whether path exists.
	Exists(path string) bool
	// ReadDir returns the direct children of dir.
	ReadDir(dir string) ([]iofs.DirEntry, error)
}

// OSFS implements FS using the local operating system filesystem.
type OSFS struct{}

// NewOSFS returns an FS backed by the local operating system filesystem.
func NewOSFS() FS {
	return OSFS{}
}

// ReadFile reads file content from path.
func (OSFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile writes b to path with mode.
func (OSFS) WriteFile(path string, b []byte, mode os.FileMode) error {
	return os.WriteFile(path, b, mode)
}

// Exists reports whether path exists.
func (OSFS) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ReadDir returns the direct children of dir.
func (OSFS) ReadDir(dir string) ([]iofs.DirEntry, error) {
	return os.ReadDir(dir)
}

// WriteStringIfChanged writes s to path only if the current content differs.
func WriteStringIfChanged(f FS, path string, s string, mode os.FileMode) (bool, error) {
	if cur, err := f.ReadFile(path); err == nil {
		if strings.TrimRight(string(cur), "\n") == strings.TrimRight(s, "\n") {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := f.WriteFile(path, []byte(s), mode); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
