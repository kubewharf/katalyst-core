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

package manager

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/opencontainers/runc/libcontainer/cgroups"
	"golang.org/x/sys/unix"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func IsCgroupPath(path string) bool {
	var fstat syscall.Statfs_t
	err := syscall.Statfs(path, &fstat)
	if err != nil {
		general.ErrorS(err, "failed to Statfs", "path", path)
		return false
	}
	return fstat.Type == unix.CGROUP2_SUPER_MAGIC || fstat.Type == unix.CGROUP_SUPER_MAGIC
}

func GetCgroupPids(cgroupPath string) ([]int, error) {
	var absCgroupPath string
	if strings.HasPrefix(cgroupPath, CgroupFSMountPoint) {
		absCgroupPath = cgroupPath
	} else {
		if cgroups.IsCgroup2UnifiedMode() {
			absCgroupPath = filepath.Join(CgroupFSMountPoint, cgroupPath)
		} else {
			absCgroupPath = filepath.Join(CgroupFSMountPoint, "cpuset", cgroupPath)
		}
	}

	pids, err := cgroups.GetAllPids(absCgroupPath)
	if err != nil {
		return nil, fmt.Errorf("failed to GetAllPids(%s), err %v", absCgroupPath, err)
	}

	return pids, nil
}
