//go:build amd64 && linux
// +build amd64,linux

package dynamicpolicy

import (
	"context"
	"fmt"
	"reflect"
	"strconv"

	"golang.org/x/sys/unix"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func MigratePagesForContainer(ctx context.Context, podUID, containerId string,
	numasCount int, sourceNUMAs, destNUMAs machine.CPUSet) error {
	memoryAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysMemory, podUID, containerId)

	if err != nil {
		return fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	containerPids, err := cgroupmgr.GetPidsWithAbsolutePath(memoryAbsCGPath)

	if err != nil {
		return fmt.Errorf("GetPidsWithAbsolutePath: %s failed with error: %v", memoryAbsCGPath, err)
	}

	sourceMask, err := bitmask.NewBitMask(sourceNUMAs.ToSliceInt()...)
	if err != nil {
		return fmt.Errorf("convert sourceNUMAs: %s to mask failed with error: %v", sourceNUMAs.String(), err)
	}

	destMask, err := bitmask.NewBitMask(destNUMAs.ToSliceInt()...)
	if err != nil {
		return fmt.Errorf("convert destNUMAs: %s to mask failed with error: %v", destNUMAs.String(), err)
	}

	var errList []error
containerLoop:
	for _, containerPidStr := range containerPids {
		containerPid, err := strconv.Atoi(containerPidStr)

		if err != nil {
			errList = append(errList, fmt.Errorf("pod: %s, container: %s, pid: %s invalid ",
				podUID, containerId, containerPidStr))
		}

		_, _, errNo := unix.Syscall6(unix.SYS_MIGRATE_PAGES,
			uintptr(containerPid),
			uintptr(numasCount+1),
			uintptr(reflect.ValueOf(sourceMask).UnsafePointer()),
			uintptr(reflect.ValueOf(destMask).UnsafePointer()), 0, 0)

		if errNo != 0 {
			errList = append(errList, fmt.Errorf("pod: %s, container: %s, pid: %d, migrates pages from %s to %s failed with error: %v",
				podUID, containerId, containerPid, sourceNUMAs.String(), destNUMAs.String(), errNo.Error()))
		}

		select {
		case <-ctx.Done():
			break containerLoop
		default:
		}
	}

	return utilerrors.NewAggregate(errList)
}
