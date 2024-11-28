//go:build amd64 && linux
// +build amd64,linux

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

package dynamicpolicy

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/eventbus"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// MPOL_MF_MOVE means move pages owned by this process to conform to policy
const MPOL_MF_MOVE = (1 << 1)

const (
	// GetNumaForPagesMaxEachTime get numa for 16384 pages(64MB) at most at a time
	GetNumaForPagesMaxEachTime = 16384

	// MovePagesMaxEachTime means move 1280 pages(5MB) at most at a time
	MovePagesMaxEachTime = 1280

	// MovePagesMinEachTime means move 64 pages(256 KB) at least at a time
	MovePagesMinEachTime = 64

	// MovePagesAcceptableTimeCost is acceptable time cost of each move pages is 20ms
	MovePagesAcceptableTimeCost = 20

	SystemNodeDir = "/sys/devices/system/node/"
	ProcDir       = "/proc"
)

type vmaInfo struct {
	start       uint64
	end         uint64
	virtualSize int64
	rss         int64
	pageSize    int64
}

type smapsInfo struct {
	totalRss int64
	vmas     []vmaInfo
}

// MigratePagesForContainer uses SYS_MIGRATE_PAGES syscall to migrate container process memory from
// sourceNUMAs to destNUMAs, and it may block process when migration. It is deprecated will be
// removed in a future release.
func MigratePagesForContainer(ctx context.Context, podUID, containerId string,
	numasCount int, sourceNUMAs, destNUMAs machine.CPUSet,
) error {
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

	startTime := time.Now()
	logs := make([]eventbus.SyscallLog, 0)
	var errList []error
containerLoop:
	for _, containerPidStr := range containerPids {
		containerPid, err := strconv.Atoi(containerPidStr)
		if err != nil {
			errList = append(errList, fmt.Errorf("pod: %s, container: %s, pid: %s invalid ",
				podUID, containerId, containerPidStr))
		}

		start := time.Now()
		_, _, errNo := unix.Syscall6(unix.SYS_MIGRATE_PAGES,
			uintptr(containerPid),
			uintptr(numasCount+1),
			uintptr(reflect.ValueOf(sourceMask).UnsafePointer()),
			uintptr(reflect.ValueOf(destMask).UnsafePointer()), 0, 0)
		if errNo != 0 {
			errList = append(errList, fmt.Errorf("pod: %s, container: %s, pid: %d, migrates pages from %s to %s failed with error: %v",
				podUID, containerId, containerPid, sourceNUMAs.String(), destNUMAs.String(), errNo.Error()))
		}
		logs = append(logs, eventbus.SyscallLog{
			Time: start,
			KeyValue: map[string]string{
				"pid":        containerPidStr,
				"sourceNuma": sourceNUMAs.String(),
				"destNuma":   destNUMAs.String(),
				"cost":       fmt.Sprintf("%dms", time.Since(start).Milliseconds()),
				"errNo":      fmt.Sprintf("%v", errNo),
			},
		})
		select {
		case <-ctx.Done():
			break containerLoop
		default:
		}
	}

	_ = eventbus.GetDefaultEventBus().Publish(consts.TopicNameSyscall, eventbus.SyscallEvent{
		BaseEventImpl: eventbus.BaseEventImpl{
			Time: startTime,
		},
		Cost:        time.Now().Sub(startTime),
		Syscall:     "SYS_MIGRATE_PAGES",
		PodUID:      podUID,
		ContainerID: containerId,
		Logs:        logs,
	})

	err = utilerrors.NewAggregate(errList)
	_ = asyncworker.EmitAsyncedMetrics(ctx, metrics.ConvertMapToTags(map[string]string{
		"podUID":      podUID,
		"containerID": containerId,
		"succeeded":   fmt.Sprintf("%v", err == nil),
	})...)

	return err
}

// MovePagesForContainer uses SYS_MOVE_PAGES syscall to migrate container process memory from
// sourceNUMAs to destNUMAs, which has more fine-grained locks than migrate_page.
func MovePagesForContainer(ctx context.Context, podUID, containerId string,
	sourceNUMAs, destNUMAs machine.CPUSet,
) error {
	sourceNUMAs = sourceNUMAs.Difference(destNUMAs)
	if len(sourceNUMAs.ToSliceInt()) == 0 {
		return nil
	}

	memoryAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysMemory, podUID, containerId)
	if err != nil {
		return fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	containerPids, err := cgroupmgr.GetPidsWithAbsolutePath(memoryAbsCGPath)
	if err != nil {
		return fmt.Errorf("GetPidsWithAbsolutePath: %s failed with error: %v", memoryAbsCGPath, err)
	}

	startTime := time.Now()
	logs := make([]eventbus.SyscallLog, 0)
	var errList []error
containerLoop:
	for _, containerPidStr := range containerPids {
		select {
		case <-ctx.Done():
			break containerLoop
		default:
		}

		pid, err := strconv.Atoi(containerPidStr)
		if err != nil {
			errList = append(errList, fmt.Errorf("pod: %s, container: %s, pid: %s invalid ",
				podUID, containerId, containerPidStr))
			continue
		}

		start := time.Now()
		if err = MovePagesForProcess(ctx, ProcDir, pid, sourceNUMAs.ToSliceInt(), destNUMAs.ToSliceInt()); err != nil {
			errList = append(errList, fmt.Errorf("Move pages for pod: %s, container: %s, pid: %d failed: %v ",
				podUID, containerId, pid, err))
			continue
		}

		timeCost := time.Since(start).Milliseconds()
		general.Infof("MovePagesForProcess, cgroup: %s, pid: %d, source numas: %+v, dest numas: %+v, timecost: %dms",
			memoryAbsCGPath, pid, sourceNUMAs, destNUMAs, timeCost)
		logs = append(logs, eventbus.SyscallLog{
			Time: start,
			KeyValue: map[string]string{
				"pid":        containerPidStr,
				"sourceNuma": sourceNUMAs.String(),
				"destNuma":   destNUMAs.String(),
				"cost":       fmt.Sprintf("%dms", timeCost),
				"err":        fmt.Sprintf("%v", err),
			},
		})
	}

	_ = eventbus.GetDefaultEventBus().Publish(consts.TopicNameSyscall, eventbus.SyscallEvent{
		BaseEventImpl: eventbus.BaseEventImpl{
			Time: startTime,
		},
		Cost:        time.Now().Sub(startTime),
		Syscall:     "SYS_MOVE_PAGES",
		PodUID:      podUID,
		ContainerID: containerId,
		Logs:        logs,
	})

	err = utilerrors.NewAggregate(errList)
	_ = asyncworker.EmitAsyncedMetrics(ctx, metrics.ConvertMapToTags(map[string]string{
		"podUID":      podUID,
		"containerID": containerId,
		"succeeded":   fmt.Sprintf("%v", err == nil),
	})...)

	return err
}

func MovePagesForProcess(ctx context.Context, procDir string, pid int, srcNumas []int, dstNumas []int) error {
	pidSmapsInfo, err := getProcessPageStats(procDir, pid)
	if err != nil {
		return err
	}

	srcNumasBitSet, err := bitmask.NewBitMask(srcNumas...)
	if err != nil {
		return fmt.Errorf("failed to NewBitMask allowd numas %+v", srcNumas)
	}

	pagesMargin := GetNumaForPagesMaxEachTime
	var pagesAdrr []uint64
	var phyPagesAddr []uint64
	var maxGetProcessPagesNuma int64

	getPhyPagesOnSourceNumas := func() {
		t0 := time.Now()
		pagesNuma, err := getProcessPagesNuma(int32(pid), uint64(len(pagesAdrr)), pagesAdrr)
		if err != nil {
			return
		}

		if time.Since(t0).Milliseconds() > maxGetProcessPagesNuma {
			maxGetProcessPagesNuma = time.Since(t0).Milliseconds()
		}

		for i, n := range pagesNuma {
			if srcNumasBitSet.IsSet(int(n)) {
				pageAddr := pagesAdrr[i]
				phyPagesAddr = append(phyPagesAddr, pageAddr)
			}
		}
	}

	for _, vma := range pidSmapsInfo.vmas {
		for addr := vma.start; addr < vma.end; addr += uint64(vma.pageSize) {
			pagesAdrr = append(pagesAdrr, addr)
			pagesMargin--
			if pagesMargin == 0 {
				getPhyPagesOnSourceNumas()
				pagesMargin = GetNumaForPagesMaxEachTime
				pagesAdrr = pagesAdrr[:0]
			}
		}
	}

	general.Infof("getProcessPagesNuma pid: %d, timeCost max: %d ms\n", pid, maxGetProcessPagesNuma)

	// handle left pagesAddr whose length less than GetNumaForPagesMaxEachTime
	if len(pagesAdrr) > 0 {
		getPhyPagesOnSourceNumas()
	}

	if len(phyPagesAddr) == 0 {
		return nil
	}

	// needless get getNumasFreeMemRatio after each move_pages,
	// only call getNumasFreeMemRatio here is enough.
	dstNumasFreeMemRatio, err := getNumasFreeMemRatio(SystemNodeDir, dstNumas)
	if err != nil {
		return err
	}

	if len(dstNumasFreeMemRatio) == 0 {
		return fmt.Errorf("pid: %d dstNumasFreeMemRatio is zero", pid)
	}

	ratioTotal := 0
	for _, r := range dstNumasFreeMemRatio {
		ratioTotal += r
	}

	phyPagesAddrNext := 0
	var phyPagesToNuma []uint64
	totalPages := 0
	numaCount := 0

	var errList []error
numaLoop:
	for numaID, ratio := range dstNumasFreeMemRatio {
		select {
		case <-ctx.Done():
			break numaLoop
		default:
		}

		pagesCount := len(phyPagesAddr) * ratio / ratioTotal
		totalPages += pagesCount
		if totalPages > len(phyPagesAddr) {
			return fmt.Errorf("impossible, totalPages:%d greater than phyPagesAddr length: %d", totalPages, len(phyPagesAddr))
		}

		numaCount++
		start := phyPagesAddrNext
		if numaCount == len(dstNumasFreeMemRatio) { // last dest numa
			phyPagesToNuma = phyPagesAddr[start:]
		} else {
			end := phyPagesAddrNext + pagesCount
			phyPagesAddrNext = end
			phyPagesToNuma = phyPagesAddr[start:end]
		}

		if err := moveProcessPagesToOneNuma(ctx, int32(pid), phyPagesToNuma, numaID); err != nil {
			errList = append(errList, err)
			continue
		}
	}

	return utilerrors.NewAggregate(errList)
}

func moveProcessPagesToOneNuma(ctx context.Context, pid int32, pagesAddr []uint64, dstNuma int) (err error) {
	leftPhyPages := pagesAddr[:]

	var movePagesLatencyMax int64
	var movePagesLenWhenLatencyMax int
	movePagesEachTime := MovePagesMinEachTime
	moveCount := 0
	var movingPagesAddr []uint64

	var errList []error
pagesLoop:
	for len(leftPhyPages) > 0 {
		select {
		case <-ctx.Done():
			break pagesLoop
		default:
		}

		if len(leftPhyPages) > movePagesEachTime {
			movingPagesAddr = leftPhyPages[:movePagesEachTime]
			leftPhyPages = leftPhyPages[movePagesEachTime:]
		} else {
			movingPagesAddr = leftPhyPages[:]
			leftPhyPages = leftPhyPages[:0]
		}

		nodes := make([]int32, len(movingPagesAddr))
		for i := range movingPagesAddr {
			nodes[i] = int32(dstNuma)
		}

		start := time.Now()
		_, err := moveProcessPages(pid, uint64(len(movingPagesAddr)), movingPagesAddr, nodes)
		if err != nil {
			errList = append(errList, fmt.Errorf("failed move pages for pid: %d, numa: %d err: %v", pid, dstNuma, err))
			continue
		}
		moveCount++
		timeCost := time.Since(start).Milliseconds()

		if timeCost == 0 {
			movePagesEachTime = MovePagesMaxEachTime
			continue
		}

		if timeCost > movePagesLatencyMax {
			movePagesLatencyMax = timeCost
			movePagesLenWhenLatencyMax = len(movingPagesAddr)
		}

		movePagesEachTime = movePagesEachTime * MovePagesAcceptableTimeCost / int(timeCost)
		if movePagesEachTime < MovePagesMinEachTime {
			movePagesEachTime = MovePagesMinEachTime
		} else if movePagesEachTime > MovePagesMaxEachTime {
			movePagesEachTime = MovePagesMaxEachTime
		}
	}

	if movePagesLatencyMax > 0 {
		general.Infof("moveProcessPagesToOneNuma pid: %d, dest numa: %d, moveCount: %d, timeCost max: %d ms, movePages len: %d\n", pid, dstNuma, moveCount, movePagesLatencyMax, movePagesLenWhenLatencyMax)
	}
	return utilerrors.NewAggregate(errList)
}

func getNumaNodeFreeMemMB(systemNodeDir string, nodeID int) (uint64, error) {
	nodeVmstatFile := filepath.Join(systemNodeDir, fmt.Sprintf("node%d/vmstat", nodeID))
	lines, err := general.ReadFileIntoLines(nodeVmstatFile)
	if err != nil {
		e := fmt.Errorf("failed to ReadFile %s, err %s", nodeVmstatFile, err)
		return 0, e
	}

	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		if len(fields) != 2 {
			return 0, fmt.Errorf("invalid line %s in vmstat file %s", line, nodeVmstatFile)
		}

		if fields[0] != "nr_free_pages" {
			continue
		}

		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid line %s in vmstat file %s, err %s", line, nodeVmstatFile, err)
		}

		return val * 4 / 1024, nil
	}

	return 0, fmt.Errorf("failed to find nr_free_pages in %s", nodeVmstatFile)
}

// ratio range [1, 10], so return value is an approximate value
func getNumasFreeMemRatio(systemNodeDir string, numas []int) (map[int]int, error) {
	var totalSize uint64
	numaFreeMem := make(map[int]uint64)
	for _, n := range numas {
		freeMemSize, err := getNumaNodeFreeMemMB(systemNodeDir, n)
		if err != nil {
			return nil, fmt.Errorf("failed to getNumaNodeFreeMem for node %d, err %s", n, err)
		}

		numaFreeMem[n] = freeMemSize
		totalSize += freeMemSize
	}

	// convert numa free memory ratio to a approximate value which is in the range [1, 10]
	var fraction uint64 = 1
	if totalSize > 10 {
		fraction = (totalSize + 10) / 10
	}

	numaFreeMemRatio := make(map[int]int)
	for numaID, freeMemSize := range numaFreeMem {
		val := freeMemSize / fraction
		if val == 0 {
			continue
		}
		numaFreeMemRatio[numaID] = int(val)
	}

	return numaFreeMemRatio, nil
}

func getProcessPageStats(procDir string, pid int) (*smapsInfo, error) {
	smapFile := filepath.Join(procDir, fmt.Sprintf("%d/smaps", pid))
	lines, err := general.ReadFileIntoLines(smapFile)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadLines(%s), err %v", smapFile, err)
	}

	info := &smapsInfo{}
	var vma *vmaInfo
	for _, line := range lines {
		if vma == nil {
			elems := strings.Fields(line)
			if len(elems) == 0 {
				continue
			}

			vmaRange := strings.Split(elems[0], "-")
			if len(vmaRange) != 2 {
				continue
			}

			vmaStart, err := strconv.ParseUint(vmaRange[0], 16, 64)
			if err != nil {
				continue
			}

			vmaEnd, err := strconv.ParseUint(vmaRange[1], 16, 64)
			if err != nil {
				continue
			}

			vma = &vmaInfo{
				start:       vmaStart,
				end:         vmaEnd,
				virtualSize: -1,
				rss:         -1,
				pageSize:    -1,
			}
			continue
		}

		if strings.HasPrefix(line, "Size:") {
			elems := strings.Fields(line)
			if len(elems) != 3 {
				continue
			}

			sizeKB, err := strconv.ParseInt(elems[1], 10, 64)
			if err != nil {
				continue
			}
			vma.virtualSize = sizeKB * 1024
			continue
		}

		if strings.HasPrefix(line, "KernelPageSize:") {
			elems := strings.Fields(line)
			if len(elems) != 3 {
				continue
			}

			pageSizeKB, err := strconv.ParseInt(elems[1], 10, 64)
			if err != nil {
				continue
			}
			vma.pageSize = pageSizeKB * 1024
			continue
		}

		if strings.HasPrefix(line, "Rss:") {
			elems := strings.Fields(line)
			if len(elems) != 3 {
				continue
			}

			rssKB, err := strconv.ParseInt(elems[1], 10, 64)
			if err != nil {
				continue
			}
			vma.rss = rssKB * 1024
			continue
		}

		if strings.HasPrefix(line, "VmFlags:") {
			if vma.virtualSize == -1 {
				continue
			}
			if vma.rss == -1 {
				continue
			}
			if vma.pageSize == -1 {
				continue
			}

			info.totalRss += vma.rss
			info.vmas = append(info.vmas, *vma)
			vma = nil
		}
	}

	return info, nil
}

func getProcessPagesNuma(pid int32, pagesCount uint64, pagesAddr []uint64) (pagesNuma []int32, err error) {
	return movePages(pid, pagesCount, pagesAddr, nil)
}

func moveProcessPages(pid int32, pagesCount uint64, pagesAddr []uint64, nodes []int32) (pagesNuma []int32, err error) {
	return movePages(pid, pagesCount, pagesAddr, nodes)
}

// nodes param can also be nil, in which case move_pages() does not move any pages but instead will return the node where each page currently resides, in the status array
func movePages(pid int32, pagesCount uint64, pagesAddr []uint64, nodes []int32) (pagesNuma []int32, err error) {
	status := make([]int32, pagesCount)
	for i := range status {
		status[i] = -111
	}

	var nodesBaseAddr *int32
	if len(nodes) > 0 {
		nodesBaseAddr = &nodes[0]
	}

	moveFlags := int32(MPOL_MF_MOVE)
	_, _, errNo := unix.Syscall6(unix.SYS_MOVE_PAGES,
		uintptr(pid),
		uintptr(pagesCount),
		uintptr(unsafe.Pointer(&pagesAddr[0])),
		uintptr(unsafe.Pointer(nodesBaseAddr)),
		uintptr(unsafe.Pointer(&status[0])),
		uintptr(moveFlags))

	if errNo != 0 {
		return nil, fmt.Errorf("failed to call SYS_MOVE_PAGES syscall, err %v", errNo.Error())
	}

	return status, nil
}
