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
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func TestControllerOperationsRejectUnsafeRelativePath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newCoreCgroupClientWithMount(root)
	for _, rel := range []string{"/target", ".", "..", "target/../escape", "target//child"} {
		if err := client.EnsureControllerDir(context.Background(), cgcommon.CgroupSubsysCPU, rel); err == nil {
			t.Errorf("EnsureControllerDir(%q) succeeded, want unsafe path error", rel)
		}
		if _, err := client.ReadControllerFile(context.Background(), cgcommon.CgroupSubsysCPU, rel, "cgroup.procs"); err == nil {
			t.Errorf("ReadControllerFile(%q) succeeded, want unsafe path error", rel)
		}
		if err := client.AttachPIDToController(context.Background(), cgcommon.CgroupSubsysCPU, rel, 123); err == nil {
			t.Errorf("AttachPIDToController(%q) succeeded, want unsafe path error", rel)
		}
		if err := client.AttachTIDToController(context.Background(), cgcommon.CgroupSubsysCPU, rel, 123); err == nil {
			t.Errorf("AttachTIDToController(%q) succeeded, want unsafe path error", rel)
		}
	}
}

func TestControllerOperationsCreateAndAttachCPUPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newCoreCgroupClientWithMount(root)
	ctx := context.Background()

	if err := client.EnsureControllerDir(ctx, cgcommon.CgroupSubsysCPU, "parent/system"); err != nil {
		t.Fatalf("EnsureControllerDir() error = %v", err)
	}
	target := filepath.Join(root, "parent", "system")
	if err := os.WriteFile(filepath.Join(target, "cgroup.procs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.AttachPIDToController(ctx, cgcommon.CgroupSubsysCPU, "parent/system", 123); err != nil {
		t.Fatalf("AttachPIDToController() error = %v", err)
	}
	got, err := client.ReadControllerFile(ctx, cgcommon.CgroupSubsysCPU, "parent/system", "cgroup.procs")
	if err != nil {
		t.Fatalf("ReadControllerFile() error = %v", err)
	}
	if string(got) != "123" {
		t.Fatalf("cgroup.procs = %q, want %q", got, "123")
	}
}

func TestAttachPIDToControllerRejectsSymlinkTraversal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	client := newCoreCgroupClientWithMount(root)
	if err := os.Mkdir(filepath.Join(outside, "system"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideProcs := filepath.Join(outside, "system", "cgroup.procs")
	if err := os.WriteFile(outsideProcs, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	err := client.AttachPIDToController(
		context.Background(), cgcommon.CgroupSubsysCPU, "escape/system", 123,
	)
	if err == nil {
		t.Fatal("AttachPIDToController() succeeded through a symlink")
	}
	got, readErr := os.ReadFile(outsideProcs)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(got) != 0 {
		t.Fatalf("outside cgroup.procs was modified: %q", got)
	}
}

func TestAttachPIDToControllerAcceptsRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newCoreCgroupClientWithMount(root)
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := client.AttachPIDToController(
		context.Background(), cgcommon.CgroupSubsysCPU, "", 123,
	); err != nil {
		t.Fatalf("AttachPIDToController() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "123" {
		t.Fatalf("root cgroup.procs = %q, want %q", got, "123")
	}
}

func TestAttachTIDToControllerWritesTasksThroughPinnedTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := newCoreCgroupClientWithMount(root)
	target := filepath.Join(root, "parent", "system")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	tasks := filepath.Join(target, "tasks")
	if err := os.WriteFile(tasks, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := client.AttachTIDToController(
		context.Background(), cgcommon.CgroupSubsysCPU, "parent/system", 456,
	); err != nil {
		t.Fatalf("AttachTIDToController() error = %v", err)
	}
	got, err := os.ReadFile(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "456" {
		t.Fatalf("tasks = %q, want %q", got, "456")
	}
}

func TestInitializeCPUSetMemsAtFDCopiesParentValue(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "system")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cpuset.mems"), []byte("0-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	childMems := filepath.Join(child, "cpuset.mems")
	if err := os.WriteFile(childMems, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(rootFD) }()
	childFD, err := unix.Open(child, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(childFD) }()

	if err := initializeCPUSetMemsAtFD(rootFD, childFD, "system"); err != nil {
		t.Fatalf("initializeCPUSetMemsAtFD() error = %v", err)
	}
	got, err := os.ReadFile(childMems)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "0-1" {
		t.Fatalf("cpuset.mems = %q, want %q", got, "0-1")
	}
}

func newCoreCgroupClientWithMount(root string) coreCgroupClient {
	return coreCgroupClient{
		resolveControllerMount: func(string) (cgcommon.ControllerMount, error) {
			return cgcommon.ControllerMount{Root: root}, nil
		},
	}
}
