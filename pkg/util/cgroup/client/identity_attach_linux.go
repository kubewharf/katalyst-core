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
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func (coreCgroupClient) AttachPIDWithIdentity(
	ctx context.Context,
	rel string,
	identity CgroupIdentity,
	pid int,
) error {
	root := cgcommon.GetCgroupRootPath(cgcommon.CgroupSubsysCPUSet)
	return attachPIDAtRootWithIdentity(ctx, root, rel, identity, pid)
}

func attachPIDAtRootWithIdentity(
	ctx context.Context,
	root string,
	rel string,
	expected CgroupIdentity,
	pid int,
) error {
	if pid <= 0 {
		return fmt.Errorf("AttachPIDWithIdentity: invalid pid %d", pid)
	}
	if expected == (CgroupIdentity{}) {
		return fmt.Errorf("AttachPIDWithIdentity: empty cgroup identity")
	}
	components, err := safeRelComponents(rel)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	dirFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open pinned cgroup root %q: %w", root, err)
	}
	defer func() { _ = unix.Close(dirFD) }()

	for _, component := range components {
		nextFD, openErr := unix.Openat(
			dirFD,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		if openErr != nil {
			return fmt.Errorf("open pinned cgroup component %q @ %q: %w", component, rel, openErr)
		}
		_ = unix.Close(dirFD)
		dirFD = nextFD
	}

	var stat unix.Stat_t
	if err := unix.Fstat(dirFD, &stat); err != nil {
		return fmt.Errorf("fstat pinned cgroup %q: %w", rel, err)
	}
	actual := CgroupIdentity{Device: stat.Dev, Inode: stat.Ino}
	if actual != expected {
		return fmt.Errorf("%w: rel=%q expected=%+v actual=%+v", ErrCgroupIdentityMismatch, rel, expected, actual)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	procsFD, err := unix.Openat(
		dirFD,
		"cgroup.procs",
		unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return fmt.Errorf("open cgroup.procs from pinned target %q: %w", rel, err)
	}
	defer func() { _ = unix.Close(procsFD) }()

	payload := []byte(strconv.Itoa(pid))
	for {
		n, writeErr := unix.Write(procsFD, payload)
		if writeErr == unix.EINTR {
			continue
		}
		if writeErr != nil {
			return fmt.Errorf("write pid %d to pinned cgroup %q: %w", pid, rel, writeErr)
		}
		if n != len(payload) {
			return fmt.Errorf("write pid %d to pinned cgroup %q: short write %d/%d", pid, rel, n, len(payload))
		}
		return nil
	}
}

func safeRelComponents(rel string) ([]string, error) {
	if rel == "" {
		return nil, nil
	}
	if strings.HasPrefix(rel, "/") || path.Clean(rel) != rel {
		return nil, fmt.Errorf("AttachPIDWithIdentity: unsafe relative cgroup path %q", rel)
	}
	components := strings.Split(rel, "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("AttachPIDWithIdentity: unsafe relative cgroup path %q", rel)
		}
	}
	return components, nil
}
