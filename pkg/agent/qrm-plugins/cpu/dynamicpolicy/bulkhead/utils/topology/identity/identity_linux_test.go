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

package identity

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStatCgroupIdentity(t *testing.T) {
	t.Parallel()
	path := t.TempDir()

	got, err := StatCgroupIdentity(path)
	if err != nil {
		t.Fatalf("StatCgroupIdentity: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat temporary cgroup: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat payload type = %T, want *syscall.Stat_t", info.Sys())
	}
	want := CgroupIdentity{Device: stat.Dev, Inode: stat.Ino}
	if got != want {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
}

func TestReadStableEntry(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	path := filepath.Join(parent, "pod")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir cgroup: %v", err)
	}

	_, err := ReadStableEntry(path, func() error {
		if err := os.Rename(path, path+".old"); err != nil {
			return err
		}
		return os.Mkdir(path, 0o755)
	})
	if !errors.Is(err, ErrCgroupIdentityChanged) {
		t.Fatalf("ReadStableEntry error = %v, want %v", err, ErrCgroupIdentityChanged)
	}
}
