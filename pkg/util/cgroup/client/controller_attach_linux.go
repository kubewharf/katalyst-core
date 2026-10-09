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
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func (c coreCgroupClient) ControllerMount(ctx context.Context, subsys string) (cgcommon.ControllerMount, error) {
	if err := ctx.Err(); err != nil {
		return cgcommon.ControllerMount{}, err
	}
	resolver := c.resolveControllerMount
	if resolver == nil {
		resolver = cgcommon.ResolveControllerMount
	}
	return resolver(subsys)
}

func (c coreCgroupClient) EnsureControllerDir(ctx context.Context, subsys, rel string) error {
	components, err := safeRelComponents(rel)
	if err != nil {
		return fmt.Errorf("EnsureControllerDir: %w", err)
	}
	rootFD, err := c.openControllerRoot(ctx, subsys)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(rootFD) }()

	dirFD, err := openControllerTarget(rootFD, components, true, subsys == cgcommon.CgroupSubsysCPUSet, rel)
	if err != nil {
		return fmt.Errorf("ensure controller cgroup %q @ %q: %w", rel, subsys, err)
	}
	defer func() { _ = unix.Close(dirFD) }()
	return ctx.Err()
}

func (c coreCgroupClient) ReadControllerFile(ctx context.Context, subsys, rel, file string) ([]byte, error) {
	if file != "cgroup.procs" && file != "tasks" {
		return nil, fmt.Errorf("ReadControllerFile: unsupported cgroup file %q", file)
	}
	dirFD, err := c.openControllerTarget(ctx, subsys, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fd, err := unix.Openat(dirFD, file, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s from pinned controller target %q: %w", file, rel, err)
	}
	f := os.NewFile(uintptr(fd), file)
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s from pinned controller target %q: %w", file, rel, err)
	}
	return raw, nil
}

func (c coreCgroupClient) AttachPIDToController(ctx context.Context, subsys, rel string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("AttachPIDToController: invalid pid %d", pid)
	}
	return c.attachIDToController(ctx, subsys, rel, "cgroup.procs", "pid", pid)
}

func (c coreCgroupClient) AttachTIDToController(ctx context.Context, subsys, rel string, tid int) error {
	if tid <= 0 {
		return fmt.Errorf("AttachTIDToController: invalid tid %d", tid)
	}
	return c.attachIDToController(ctx, subsys, rel, "tasks", "tid", tid)
}

func (c coreCgroupClient) attachIDToController(
	ctx context.Context,
	subsys, rel, file, idKind string,
	id int,
) error {
	dirFD, err := c.openControllerTarget(ctx, subsys, rel)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	if err := ctx.Err(); err != nil {
		return err
	}

	memberFD, err := unix.Openat(dirFD, file, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open %s from pinned controller target %q: %w", file, rel, err)
	}
	defer func() { _ = unix.Close(memberFD) }()

	payload := []byte(strconv.Itoa(id))
	for {
		n, writeErr := unix.Write(memberFD, payload)
		if writeErr == unix.EINTR {
			continue
		}
		if writeErr != nil {
			return fmt.Errorf("write %s %d to %s in pinned controller cgroup %q: %w", idKind, id, file, rel, writeErr)
		}
		if n != len(payload) {
			return fmt.Errorf("write %s %d to %s in pinned controller cgroup %q: short write %d/%d", idKind, id, file, rel, n, len(payload))
		}
		return nil
	}
}

func (c coreCgroupClient) openControllerTarget(ctx context.Context, subsys, rel string) (int, error) {
	components, err := safeRelComponents(rel)
	if err != nil {
		return -1, err
	}
	rootFD, err := c.openControllerRoot(ctx, subsys)
	if err != nil {
		return -1, err
	}
	targetFD, err := openControllerTarget(rootFD, components, false, false, rel)
	_ = unix.Close(rootFD)
	if err != nil {
		return -1, fmt.Errorf("open pinned controller cgroup %q @ %q: %w", rel, subsys, err)
	}
	return targetFD, nil
}

func (c coreCgroupClient) openControllerRoot(ctx context.Context, subsys string) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	mount, err := c.ControllerMount(ctx, subsys)
	if err != nil {
		return -1, fmt.Errorf("resolve controller mount %q: %w", subsys, err)
	}
	fd, err := unix.Open(mount.Root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open pinned controller root %q: %w", mount.Root, err)
	}
	return fd, nil
}

func openControllerTarget(rootFD int, components []string, create, initializeCPUSetMems bool, rel string) (int, error) {
	dirFD := rootFD
	for _, component := range components {
		if create {
			if err := unix.Mkdirat(dirFD, component, 0o755); err != nil && err != unix.EEXIST {
				if dirFD != rootFD {
					_ = unix.Close(dirFD)
				}
				return -1, err
			}
		}
		nextFD, err := unix.Openat(dirFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			if dirFD != rootFD {
				_ = unix.Close(dirFD)
			}
			return -1, err
		}
		if initializeCPUSetMems {
			if err := initializeCPUSetMemsAtFD(dirFD, nextFD, rel); err != nil {
				_ = unix.Close(nextFD)
				if dirFD != rootFD {
					_ = unix.Close(dirFD)
				}
				return -1, err
			}
		}
		if dirFD != rootFD {
			_ = unix.Close(dirFD)
		}
		dirFD = nextFD
	}
	if dirFD == rootFD {
		dupFD, err := unix.Dup(rootFD)
		if err != nil {
			return -1, err
		}
		return dupFD, nil
	}
	return dirFD, nil
}

func initializeCPUSetMemsAtFD(parentFD, childFD int, rel string) error {
	current, err := readFileAt(childFD, "cpuset.mems")
	if err != nil {
		if err == unix.ENOENT {
			return nil
		}
		return fmt.Errorf("read cpuset.mems @ %s: %w", rel, err)
	}
	if strings.TrimSpace(string(current)) != "" {
		return nil
	}
	parent, err := readFileAt(parentFD, "cpuset.mems")
	if err != nil {
		return fmt.Errorf("read parent cpuset.mems @ %s: %w", rel, err)
	}
	value := strings.TrimSpace(string(parent))
	if value == "" {
		return nil
	}
	fd, err := unix.Openat(childFD, "cpuset.mems", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open cpuset.mems @ %s: %w", rel, err)
	}
	defer func() { _ = unix.Close(fd) }()
	if _, err := unix.Write(fd, []byte(value)); err != nil {
		return fmt.Errorf("initialize cpuset.mems @ %s: %w", rel, err)
	}
	return nil
}

func readFileAt(dirFD int, file string) ([]byte, error) {
	fd, err := unix.Openat(dirFD, file, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), file)
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
