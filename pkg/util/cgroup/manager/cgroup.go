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
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/eventbus"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	MPOL_DEFAULT = iota
	MPOL_PREFERRED
	MPOL_BIND
	MPOL_INTERLEAVE
	MPOL_LOCAL
	MPOL_MAX
)

func ApplyMemoryWithRelativePath(relCgroupPath string, data *common.MemoryData) error {
	if data == nil {
		return fmt.Errorf("ApplyMemoryWithRelativePath with nil cgroup data")
	}

	absCgroupPath := common.GetAbsCgroupPath("memory", relCgroupPath)
	return GetManager().ApplyMemory(absCgroupPath, data)
}

func ApplyCPUWithRelativePath(relCgroupPath string, data *common.CPUData) error {
	if data == nil {
		return fmt.Errorf("ApplyCPUWithRelativePath with nil cgroup data")
	}

	absCgroupPath := common.GetAbsCgroupPath("cpu", relCgroupPath)
	return GetManager().ApplyCPU(absCgroupPath, data)
}

func ApplyCPUSetWithRelativePath(relCgroupPath string, data *common.CPUSetData) error {
	if data == nil {
		return fmt.Errorf("ApplyCPUSetForContainer with nil cgroup data")
	}

	absCgroupPath := common.GetAbsCgroupPath("cpuset", relCgroupPath)
	return GetManager().ApplyCPUSet(absCgroupPath, data)
}

func ApplyCPUSetWithAbsolutePath(absCgroupPath string, data *common.CPUSetData) error {
	if data == nil {
		return fmt.Errorf("ApplyCPUSetWithAbsolutePath with nil cgroup data")
	}

	return GetManager().ApplyCPUSet(absCgroupPath, data)
}

func ApplyCPUSetForContainer(podUID, containerId string, data *common.CPUSetData) error {
	if data == nil {
		return fmt.Errorf("ApplyCPUSetForContainer with nil cgroup data")
	}

	cpusetAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysCPUSet, podUID, containerId)
	if err != nil {
		return fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	return ApplyCPUSetWithAbsolutePath(cpusetAbsCGPath, data)
}

func ApplyNetClsWithRelativePath(relCgroupPath string, data *common.NetClsData) error {
	if data == nil {
		return fmt.Errorf("ApplyNetClsWithRelativePath with nil cgroup data")
	}

	absCgroupPath := common.GetAbsCgroupPath("net_cls", relCgroupPath)
	return GetManager().ApplyNetCls(absCgroupPath, data)
}

func ApplyNetClsWithAbsolutePath(absCgroupPath string, data *common.NetClsData) error {
	if data == nil {
		return fmt.Errorf("ApplyNetClsWithRelativePath with nil cgroup data")
	}

	return GetManager().ApplyNetCls(absCgroupPath, data)
}

// ApplyNetClsForContainer applies the net_cls config for a container.
func ApplyNetClsForContainer(podUID, containerId string, data *common.NetClsData) error {
	if data == nil {
		return fmt.Errorf("ApplyNetClass with nil cgroup data")
	}

	netClsAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysNetCls, podUID, containerId)
	if err != nil {
		return fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	return ApplyNetClsWithAbsolutePath(netClsAbsCGPath, data)
}

func ApplyIOCostQoSWithRelativePath(relCgroupPath string, devID string, data *common.IOCostQoSData) error {
	if data == nil {
		return fmt.Errorf("ApplyIOCostQoSWithRelativePath with nil cgroup data")
	}

	absCgroupPath := common.GetAbsCgroupPath(common.CgroupSubsysIO, relCgroupPath)
	return ApplyIOCostQoSWithAbsolutePath(absCgroupPath, devID, data)
}

func ApplyIOCostQoSWithAbsolutePath(absCgroupPath string, devID string, data *common.IOCostQoSData) error {
	if data == nil {
		return fmt.Errorf("ApplyIOCostQoSWithAbsolutePath with nil cgroup data")
	}

	return GetManager().ApplyIOCostQoS(absCgroupPath, devID, data)
}

func ApplyIOCostModelWithRelativePath(relCgroupPath string, devID string, data *common.IOCostModelData) error {
	if data == nil {
		return fmt.Errorf("ApplyIOCostModelWithRelativePath with nil cgroup data")
	}

	absCgroupPath := common.GetAbsCgroupPath(common.CgroupSubsysIO, relCgroupPath)
	return ApplyIOCostModelWithAbsolutePath(absCgroupPath, devID, data)
}

func ApplyIOCostModelWithAbsolutePath(absCgroupPath string, devID string, data *common.IOCostModelData) error {
	if data == nil {
		return fmt.Errorf("ApplyIOCostModelWithAbsolutePath with nil cgroup data")
	}

	return GetManager().ApplyIOCostModel(absCgroupPath, devID, data)
}

func ApplyIOWeightWithRelativePath(relCgroupPath string, devID string, weight uint64) error {
	absCgroupPath := common.GetAbsCgroupPath(common.CgroupSubsysIO, relCgroupPath)
	return ApplyIOWeightWithAbsolutePath(absCgroupPath, devID, weight)
}

func ApplyIOWeightWithAbsolutePath(absCgroupPath string, devID string, weight uint64) error {
	return GetManager().ApplyIOWeight(absCgroupPath, devID, weight)
}

func ApplyUnifiedDataWithAbsolutePath(absCgroupPath, cgroupFileName, data string) error {
	return GetManager().ApplyUnifiedData(absCgroupPath, cgroupFileName, data)
}

// ApplyUnifiedDataForContainer applies the data to cgroupFileName in subsys for a container.
func ApplyUnifiedDataForContainer(podUID, containerId, subsys, cgroupFileName, data string) error {
	absCgroupPath, err := common.GetContainerAbsCgroupPath(subsys, podUID, containerId)
	if err != nil {
		return fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	return ApplyUnifiedDataWithAbsolutePath(absCgroupPath, cgroupFileName, data)
}

func GetMemoryWithRelativePath(relCgroupPath string) (*common.MemoryStats, error) {
	absCgroupPath := common.GetAbsCgroupPath("memory", relCgroupPath)
	return GetManager().GetMemory(absCgroupPath)
}

func GetMemoryWithAbsolutePath(absCgroupPath string) (*common.MemoryStats, error) {
	return GetManager().GetMemory(absCgroupPath)
}

func GetMemoryPressureWithAbsolutePath(absCgroupPath string, t common.PressureType) (*common.MemoryPressure, error) {
	return GetManager().GetMemoryPressure(absCgroupPath, t)
}

func GetIOCostQoSWithRelativePath(relCgroupPath string) (map[string]*common.IOCostQoSData, error) {
	absCgroupPath := common.GetAbsCgroupPath(common.CgroupSubsysIO, relCgroupPath)
	return GetIOCostQoSWithAbsolutePath(absCgroupPath)
}

func GetIOCostQoSWithAbsolutePath(absCgroupPath string) (map[string]*common.IOCostQoSData, error) {
	return GetManager().GetIOCostQoS(absCgroupPath)
}

func GetIOCostModelWithRelativePath(relCgroupPath string) (map[string]*common.IOCostModelData, error) {
	absCgroupPath := common.GetAbsCgroupPath(common.CgroupSubsysIO, relCgroupPath)
	return GetIOCostModelWithAbsolutePath(absCgroupPath)
}

func GetIOCostModelWithAbsolutePath(absCgroupPath string) (map[string]*common.IOCostModelData, error) {
	return GetManager().GetIOCostModel(absCgroupPath)
}

func GetDeviceIOWeightWithRelativePath(relCgroupPath, devID string) (uint64, bool, error) {
	absCgroupPath := common.GetAbsCgroupPath(common.CgroupSubsysIO, relCgroupPath)
	return GetDeviceIOWeightWithAbsolutePath(absCgroupPath, devID)
}

func GetDeviceIOWeightWithAbsolutePath(absCgroupPath, devID string) (uint64, bool, error) {
	return GetManager().GetDeviceIOWeight(absCgroupPath, devID)
}

func GetIOStatWithRelativePath(relCgroupPath string) (map[string]map[string]string, error) {
	absCgroupPath := common.GetAbsCgroupPath(common.CgroupSubsysIO, relCgroupPath)
	return GetIOStatWithAbsolutePath(absCgroupPath)
}

func GetIOStatWithAbsolutePath(absCgroupPath string) (map[string]map[string]string, error) {
	return GetManager().GetIOStat(absCgroupPath)
}

func GetCPUWithRelativePath(relCgroupPath string) (*common.CPUStats, error) {
	absCgroupPath := common.GetAbsCgroupPath("cpu", relCgroupPath)
	return GetManager().GetCPU(absCgroupPath)
}

func GetCPUSetWithAbsolutePath(absCgroupPath string) (*common.CPUSetStats, error) {
	return GetManager().GetCPUSet(absCgroupPath)
}

func GetCPUSetWithRelativePath(relCgroupPath string) (*common.CPUSetStats, error) {
	absCgroupPath := common.GetAbsCgroupPath("cpuset", relCgroupPath)
	return GetManager().GetCPUSet(absCgroupPath)
}

func GetMetricsWithRelativePath(relCgroupPath string, subsystems map[string]struct{}) (*common.CgroupMetrics, error) {
	return GetManager().GetMetrics(relCgroupPath, subsystems)
}

func GetPidsWithRelativePath(relCgroupPath string) ([]string, error) {
	absCgroupPath := common.GetAbsCgroupPath(common.DefaultSelectedSubsys, relCgroupPath)
	return GetManager().GetPids(absCgroupPath)
}

func GetPidsWithAbsolutePath(absCgroupPath string) ([]string, error) {
	return GetManager().GetPids(absCgroupPath)
}

func GetTasksWithRelativePath(cgroupPath, subsys string) ([]string, error) {
	absCgroupPath := common.GetAbsCgroupPath(subsys, cgroupPath)
	return GetManager().GetTasks(absCgroupPath)
}

func GetTasksWithAbsolutePath(absCgroupPath string) ([]string, error) {
	return GetManager().GetTasks(absCgroupPath)
}

func GetCPUSetForContainer(podUID, containerId string) (*common.CPUSetStats, error) {
	cpusetAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysCPUSet, podUID, containerId)
	if err != nil {
		return nil, fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	return GetCPUSetWithAbsolutePath(cpusetAbsCGPath)
}

func DropCacheWithTimeoutForContainer(ctx context.Context, podUID, containerId string, timeoutSecs int, nbytes int64) error {
	memoryAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysMemory, podUID, containerId)
	if err != nil {
		return fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	err = DropCacheWithTimeoutAndAbsCGPath(timeoutSecs, memoryAbsCGPath, nbytes)
	_ = asyncworker.EmitAsyncedMetrics(ctx, metrics.ConvertMapToTags(map[string]string{
		"podUID":      podUID,
		"containerID": containerId,
		"succeeded":   fmt.Sprintf("%v", err == nil),
	})...)
	return err
}

func DropCacheWithTimeoutAndAbsCGPath(timeoutSecs int, absCgroupPath string, nbytes int64) error {
	var (
		startTime  = time.Now()
		data       int64
		cgroupFile string
	)

	var cmd string
	if common.CheckCgroup2UnifiedMode() {
		if nbytes == 0 {
			general.Infof("[DropCacheWithTimeoutAndAbsCGPath] skip drop cache on %s since nbytes is zero", absCgroupPath)
			return nil
		}
		// cgv2
		cgroupFile = "memory.reclaim"
		data = nbytes
	} else {
		// cgv1
		cgroupFile = "memory.force_empty"
		data = 0
	}
	cmd = fmt.Sprintf("timeout %d echo %d > %s", timeoutSecs, data, filepath.Join(absCgroupPath, cgroupFile))

	_, err := exec.Command("bash", "-c", cmd).Output()

	delta := time.Since(startTime).Seconds()
	general.Infof("[DropCacheWithTimeoutAndAbsCGPath] it takes %v to do \"%s\" on cgroup: %s", delta, cmd, absCgroupPath)
	_ = eventbus.GetDefaultEventBus().Publish(consts.TopicNameApplyCGroup, eventbus.RawCGroupEvent{
		BaseEventImpl: eventbus.BaseEventImpl{
			Time: startTime,
		},
		Cost:       time.Now().Sub(startTime),
		CGroupPath: absCgroupPath,
		CGroupFile: cgroupFile,
		Data:       strconv.Itoa(int(data)),
	})

	// if this command timeout, a none-nil error will be returned,
	// but we should return error iff error returns without timeout
	if err != nil && int(delta) < timeoutSecs {
		return err
	}

	return nil
}

func SetExtraCGMemLimitWithTimeoutAndRelCGPath(ctx context.Context, relCgroupPath string, timeoutSecs int, nbytes int64) error {
	memoryAbsCGPath := common.GetAbsCgroupPath(common.CgroupSubsysMemory, relCgroupPath)

	err := SetExtraCGMemLimitWithTimeoutAndAbsCGPath(timeoutSecs, memoryAbsCGPath, nbytes)
	_ = asyncworker.EmitAsyncedMetrics(ctx, metrics.ConvertMapToTags(map[string]string{
		"relCgroupPath": relCgroupPath,
		"succeeded":     fmt.Sprintf("%v", err == nil),
	})...)
	return err
}

func SetExtraCGMemLimitWithTimeoutAndAbsCGPath(timeoutSecs int, absCgroupPath string, nbytes int64) error {
	if nbytes == 0 {
		return fmt.Errorf("invalid memory limit nbytes: %d", nbytes)
	}

	var (
		startTime  = time.Now()
		cgroupFile string
	)

	if common.CheckCgroup2UnifiedMode() {
		if nbytes == 0 {
			general.Infof("[SetExtraCGMemLimitWithTimeoutAndAbsCGPath] skip drop cache on %s since nbytes is zero", absCgroupPath)
			return nil
		}
		// cgv2
		cgroupFile = "memory.max"
	} else {
		// cgv1
		cgroupFile = "memory.limit_in_bytes"
	}

	cmd := fmt.Sprintf("timeout %d echo %d > %s", timeoutSecs, nbytes, filepath.Join(absCgroupPath, cgroupFile))

	_, err := exec.Command("bash", "-c", cmd).Output()

	delta := time.Since(startTime).Seconds()
	general.Infof("[SetExtraCGMemLimitWithTimeoutAndAbsCGPath] it takes %v to do \"%s\" on cgroup: %s", delta, cmd, absCgroupPath)
	_ = eventbus.GetDefaultEventBus().Publish(consts.TopicNameApplyCGroup, eventbus.RawCGroupEvent{
		BaseEventImpl: eventbus.BaseEventImpl{
			Time: startTime,
		},
		Cost:       time.Now().Sub(startTime),
		CGroupPath: absCgroupPath,
		CGroupFile: cgroupFile,
		Data:       strconv.Itoa(int(nbytes)),
	})

	// if this command timeout, a none-nil error will be returned,
	// but we should return error iff error returns without timeout
	if err != nil && int(delta) < timeoutSecs {
		return err
	}

	return nil
}

func SetSwapMaxWithAbsolutePathToParentCgroupRecursive(absCgroupPath string) error {
	if !common.CheckCgroup2UnifiedMode() {
		general.Infof("[SetSwapMaxWithAbsolutePathToParentCgroupRecursive] is not supported on cgroupv1")
		return nil
	}
	general.Infof("[SetSwapMaxWithAbsolutePathToParentCgroupRecursive] on cgroup: %s", absCgroupPath)
	swapMaxData := &common.MemoryData{SwapMaxInBytes: math.MaxInt64}
	err := GetManager().ApplyMemory(absCgroupPath, swapMaxData)
	if err != nil {
		return err
	}

	parentDir := filepath.Dir(absCgroupPath)
	if parentDir != absCgroupPath && parentDir != common.GetCgroupRootPath(common.CgroupSubsysMemory) {
		err = SetSwapMaxWithAbsolutePathToParentCgroupRecursive(parentDir)
		if err != nil {
			return err
		}
	}
	return nil
}

func SetSwapMaxWithAbsolutePathRecursive(absCgroupPath string) error {
	if !common.CheckCgroup2UnifiedMode() {
		general.Infof("[SetSwapMaxWithAbsolutePathRecursive] is not supported on cgroupv1")
		return nil
	}

	general.Infof("[SetSwapMaxWithAbsolutePathRecursive] on cgroup: %s", absCgroupPath)

	// set swap max to parent cgroups recursively
	if err := SetSwapMaxWithAbsolutePathToParentCgroupRecursive(filepath.Dir(absCgroupPath)); err != nil {
		return err
	}

	// set swap max to sub cgroups recursively
	err := filepath.Walk(absCgroupPath, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			general.Infof("prevent panic by handling failure accessing a path: %s, err: %v", path, err)
			return err
		}
		if info.IsDir() {
			memStats, err := GetMemoryWithAbsolutePath(path)
			if err != nil {
				return filepath.SkipDir
			}
			var diff int64 = math.MaxInt64
			if memStats.Limit-memStats.Usage < uint64(diff) {
				diff = int64(memStats.Limit - memStats.Usage)
			}
			swapMaxData := &common.MemoryData{SwapMaxInBytes: diff}
			err = GetManager().ApplyMemory(path, swapMaxData)
			if err != nil {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		general.Infof("error walking the path: %s, err: %v", absCgroupPath, err)
		return err
	}
	return nil
}

func DisableSwapMaxWithAbsolutePathRecursive(absCgroupPath string) error {
	if !common.CheckCgroup2UnifiedMode() {
		general.Infof("[DisableSwapMaxWithAbsolutePathRecursive] is not supported on cgroupv1")
		return nil
	}
	general.Infof("[DisableSwapMaxWithAbsolutePathRecursive] on cgroup: %s", absCgroupPath)
	// disable swap to sub cgroups recursively
	err := filepath.Walk(absCgroupPath, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			general.Infof("prevent panic by handling failure accessing a path: %s, err: %v", path, err)
			return err
		}
		if info.IsDir() {
			swapMaxData := &common.MemoryData{SwapMaxInBytes: -1}
			err = GetManager().ApplyMemory(path, swapMaxData)
			if err != nil {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		general.Infof("error walking the path: %s, err: %v ", absCgroupPath, err)
		return err
	}
	return nil
}

func MemoryOffloadingWithAbsolutePath(ctx context.Context, absCgroupPath string, nbytes int64, mems machine.CPUSet) error {
	startTime := time.Now()

	var cmd string
	if common.CheckCgroup2UnifiedMode() {
		if nbytes <= 0 {
			general.Infof("[MemoryOffloadingWithAbsolutePath] skip memory reclaim on %s since nbytes is not valid", absCgroupPath)
			return nil
		}
		// cgv2
		cmd = fmt.Sprintf("echo %d > %s", nbytes, filepath.Join(absCgroupPath, "memory.reclaim"))
	} else {
		// cgv1
		general.Infof("[MemoryOffloadingWithAbsolutePath] is not supported on cgroupv1")
		return nil
	}

	err := doReclaimMemory(cmd, mems)

	_ = asyncworker.EmitAsyncedMetrics(ctx, metrics.ConvertMapToTags(map[string]string{
		"absCGPath": absCgroupPath,
		"succeeded": fmt.Sprintf("%v", err == nil),
	})...)
	delta := time.Since(startTime).Seconds()
	general.Infof("[MemoryOffloadingWithAbsolutePath] it takes %v to do \"%s\" on cgroup: %s", delta, cmd, absCgroupPath)

	return err
}

func IsCgroupPath(path string) bool {
	var fstat syscall.Statfs_t
	err := syscall.Statfs(path, &fstat)
	if err != nil {
		general.ErrorS(err, "failed to Statfs", "path", path)
		return false
	}
	return fstat.Type == unix.CGROUP2_SUPER_MAGIC || fstat.Type == unix.CGROUP_SUPER_MAGIC
}

func GetEffectiveCPUSetWithAbsolutePath(absCgroupPath string) (machine.CPUSet, machine.CPUSet, error) {
	if !IsCgroupPath(absCgroupPath) {
		return machine.CPUSet{}, machine.CPUSet{}, fmt.Errorf("path %s is not a cgroup", absCgroupPath)
	}

	cpusetStat, err := GetCPUSetWithAbsolutePath(absCgroupPath)
	if err != nil {
		// if controller is disabled, we should walk the parent's dir.
		if os.IsNotExist(err) {
			return GetEffectiveCPUSetWithAbsolutePath(filepath.Dir(absCgroupPath))
		}
		return machine.CPUSet{}, machine.CPUSet{}, err
	}
	// if the cpus or mems is empty, they will inherit the parent's mask.
	cpus, err := machine.Parse(cpusetStat.EffectiveCPUs)
	if err != nil {
		return machine.CPUSet{}, machine.CPUSet{}, err
	}
	if cpus.IsEmpty() {
		cpus, _, err = GetEffectiveCPUSetWithAbsolutePath(filepath.Dir(absCgroupPath))
		if err != nil {
			return machine.CPUSet{}, machine.CPUSet{}, err
		}
	}
	mems, err := machine.Parse(cpusetStat.EffectiveMems)
	if err != nil {
		return machine.CPUSet{}, machine.CPUSet{}, err
	}
	if mems.IsEmpty() {
		_, mems, err = GetEffectiveCPUSetWithAbsolutePath(filepath.Dir(absCgroupPath))
		if err != nil {
			return machine.CPUSet{}, machine.CPUSet{}, err
		}
	}
	return cpus, mems, nil
}
