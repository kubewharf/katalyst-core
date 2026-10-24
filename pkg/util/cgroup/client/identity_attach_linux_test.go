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

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAttachPIDAtRootWithIdentityWritesPinnedTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "parent", "system")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	procs := filepath.Join(target, "cgroup.procs")
	if err := os.WriteFile(procs, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := testDirectoryIdentity(t, target)

	if err := attachPIDAtRootWithIdentity(context.Background(), root, "parent/system", identity, 123); err != nil {
		t.Fatalf("attachPIDAtRootWithIdentity() error = %v", err)
	}
	got, err := os.ReadFile(procs)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "123" {
		t.Fatalf("cgroup.procs = %q, want %q", got, "123")
	}
}

func TestAttachPIDAtRootWithIdentityRejectsSameNameRecreation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "system")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	staleIdentity := testDirectoryIdentity(t, target)
	if err := os.Rename(target, filepath.Join(root, "old-system")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	procs := filepath.Join(target, "cgroup.procs")
	if err := os.WriteFile(procs, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	err := attachPIDAtRootWithIdentity(context.Background(), root, "system", staleIdentity, 123)
	if !errors.Is(err, ErrCgroupIdentityMismatch) {
		t.Fatalf("error = %v, want ErrCgroupIdentityMismatch", err)
	}
	got, readErr := os.ReadFile(procs)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(got) != 0 {
		t.Fatalf("recreated target cgroup.procs was modified: %q", got)
	}
}

func TestAttachPIDAtRootWithIdentityRejectsSymlinkTraversal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "system")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "cgroup.procs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	err := attachPIDAtRootWithIdentity(
		context.Background(),
		root,
		"escape/system",
		testDirectoryIdentity(t, target),
		123,
	)
	if err == nil {
		t.Fatalf("symlink traversal must fail")
	}
	got, readErr := os.ReadFile(filepath.Join(target, "cgroup.procs"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(got) != 0 {
		t.Fatalf("outside cgroup.procs was modified: %q", got)
	}
}

func testDirectoryIdentity(t *testing.T, path string) CgroupIdentity {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat type = %T, want *syscall.Stat_t", info.Sys())
	}
	return CgroupIdentity{Device: stat.Dev, Inode: stat.Ino}
}
