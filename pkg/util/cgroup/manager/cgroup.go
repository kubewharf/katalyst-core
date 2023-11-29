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
	"os/exec"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
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

func DropCacheWithTimeoutForContainer(ctx context.Context, podUID, containerId string, timeoutSecs int) error {
	cpusetAbsCGPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysMemory, podUID, containerId)
	if err != nil {
		return fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	err = DropCacheWithTimeoutWithRelativePath(timeoutSecs, cpusetAbsCGPath)
	_ = asyncworker.EmitAsyncedMetrics(ctx, metrics.ConvertMapToTags(map[string]string{
		"podUID":      podUID,
		"containerID": containerId,
		"succeeded":   fmt.Sprintf("%v", err == nil),
	})...)
	return err
}

func DropCacheWithTimeoutWithRelativePath(timeoutSecs int, absCgroupPath string) error {
	startTime := time.Now()

	cmd := fmt.Sprintf("timeout %d echo 0 > %s", timeoutSecs, filepath.Join(absCgroupPath, "memory.force_empty"))
	_, err := exec.Command("bash", "-c", cmd).Output()

	delta := time.Since(startTime).Seconds()
	klog.Infof("[DropCacheWithTimeoutWithRelativePath] it takes %v to drop cache of cgroup: %s", delta, absCgroupPath)

	// if this command timeout, a none-nil error will be returned,
	// but we should return error iff error returns without timeout
	if err != nil && int(delta) < timeoutSecs {
		return err
	}

	return nil
}
