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

package rootfs

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"github.com/pkg/errors"
	"github.com/prometheus/procfs"
	"golang.org/x/sys/unix"

	"github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
)

// Define constants related to Linux kernel for ext4
const (
	// Define constants related to quotactl, from /usr/include/linux/quota.h
	Q_GETNEXTQUOTA = 0x800009 // Get next quota entry
	PRJQUOTA       = 2        // Project quota type

	// Define constants related to qCMD, from /usr/include/linux/quota.h
	SUBCMDSHIFT = 8
	SUBCMDMASK  = 0x00ff

	SystemDirectorsProjectID = 0
)

// nextdqblk structure for getting next quota entry
type nextdqblk struct {
	dqbBHardlimit uint64
	dqbBSoftlimit uint64
	dqbCurSpace   uint64
	dqbIHardlimit uint64
	dqbISoftlimit uint64
	dqbCurInodes  uint64
	dqbBTime      uint64
	dqbITime      uint64
	dqbValid      uint32
	dqdID         uint32 // the project ID
}

// qCMD Function to construct the qCMD value for quotactl
func qCMD(cmd, typ uint32) uint32 {
	return (cmd << SUBCMDSHIFT) | (typ & SUBCMDMASK)
}

// GetTotalUsedBytesOfPVProjects returns the total used bytes for all projects on the given device path.
func GetTotalUsedBytesOfPVProjects(devicePath string) (int64, error) {
	bytePtrFromString, err := syscall.BytePtrFromString(devicePath)
	if err != nil {
		return 0, err
	}
	// fetch executes a single QUOTACTL syscall to retrieve quota info for the given project ID.
	fetch := func(projectID uint32) (nextdqblk, error) {
		var dq nextdqblk
		_, _, errno := unix.Syscall6(
			unix.SYS_QUOTACTL,
			uintptr(qCMD(Q_GETNEXTQUOTA, PRJQUOTA)),
			uintptr(unsafe.Pointer(bytePtrFromString)),
			uintptr(projectID),
			uintptr(unsafe.Pointer(&dq)),
			0, 0,
		)

		if errors.Is(errno, syscall.ESRCH) || errors.Is(errno, syscall.ENOENT) {
			// No more quota entries
			return nextdqblk{}, errno
		}
		if errno != 0 {
			return nextdqblk{}, fmt.Errorf("get project quota information failed (ID %d): %w", projectID, errno)
		}

		return dq, nil
	}

	// Accumulate total quota usage across all projects.
	return sumBytesByFetch(fetch)
}

// sumBytesByFetch repeatedly calls fetch for consecutive project IDs and
// aggregates their quota usage until no more projects are found.
func sumBytesByFetch(fetch func(projectID uint32) (nextdqblk, error)) (int64, error) {
	var total int64
	projectID := uint32(SystemDirectorsProjectID + 1)

	for {
		dq, err := fetch(projectID)
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) {
			// No more projects, stop iteration
			break
		}
		if err != nil {
			return 0, err
		}

		total += int64(dq.dqbCurSpace)
		projectID = dq.dqdID + 1
	}

	return total, nil
}

var GetMounts = manager.GetMounts

// GetDeviceForPathAndCheckPrjquota returns the device path and whether prjquota is enabled
func GetDeviceForPathAndCheckPrjquota(path string) (string, bool, error) {
	mounts, err := GetMounts()
	if err != nil {
		return "", false, fmt.Errorf("failed to get mounts: %w", err)
	}

	var bestMatch *procfs.MountInfo
	bestPrefixLen := -1
	for _, m := range mounts {
		if strings.HasPrefix(path, m.MountPoint) && len(m.MountPoint) > bestPrefixLen {
			bestPrefixLen = len(m.MountPoint)
			bestMatch = m
		}
	}
	if bestMatch == nil {
		return "", false, fmt.Errorf("no matching mount for path %s", path)
	}

	_, prjquotaEnabled := bestMatch.Options["prjquota"]
	if !prjquotaEnabled {
		_, prjquotaEnabled = bestMatch.SuperOptions["prjquota"]
	}

	return bestMatch.Source, prjquotaEnabled, nil
}
