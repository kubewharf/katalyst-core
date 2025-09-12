package rootfs

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
	"github.com/pkg/errors"
	"github.com/prometheus/procfs"
	"golang.org/x/sys/unix"
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

// syscallImpl is a variable that holds the actual syscall implementation
var syscallImpl = unix.Syscall6

// qCMD Function to construct the qCMD value for quotactl
func qCMD(cmd, typ uint32) uint32 {
	return (cmd << SUBCMDSHIFT) | (typ & SUBCMDMASK)
}

// GetTotalUsedBytesOfPVProjects Function to get total allocated quota (in bytes) for a device path
func GetTotalUsedBytesOfPVProjects(devicePath string) (int64, error) {
	bytePtrFromString, err := syscall.BytePtrFromString(devicePath)
	if err != nil {
		return 0, err
	}

	// Q_GETNEXTQUOTA makes quotactl return the quota info and the project ID ≥ the given number .
	var totalUsedBytes int64
	var projectID uint32 = SystemDirectorsProjectID + 1 // Start iterating from ID 1, which excludes the root project of system directories
	for {
		var dq nextdqblk
		_, _, errno := syscallImpl(
			unix.SYS_QUOTACTL,
			uintptr(qCMD(Q_GETNEXTQUOTA, PRJQUOTA)),
			uintptr(unsafe.Pointer(bytePtrFromString)),
			uintptr(projectID),
			uintptr(unsafe.Pointer(&dq)),
			0, 0)
		if errors.Is(errno, syscall.ESRCH) || errors.Is(errno, syscall.ENOENT) {
			// No more quota entries, break the loop
			break
		}
		if errno != 0 {
			return 0, fmt.Errorf("get project quota information failed (ID %d): %w", projectID, errno)
		}

		// Calculate used bytes for this project
		totalUsedBytes += int64(dq.dqbCurSpace)
		// Next loop will look for the next project id from 'dq.dqdID + 1'
		projectID = dq.dqdID + 1
	}

	return totalUsedBytes, nil
}

var GetMounts = manager.GetMounts

// GetDeviceForPath returns the device path for the given path
func GetDeviceForPath(path string) (string, error) {
	mounts, err := GetMounts()
	if err != nil {
		return "", fmt.Errorf("failed to get mounts: %w", err)
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
		return "", fmt.Errorf("no matching mount for path %s", path)
	}
	return bestMatch.Source, nil
}
