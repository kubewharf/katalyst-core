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

package v1

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/containerd/cgroups"
	libcgroups "github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/opencontainers/runc/libcontainer/cgroups/fscommon"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type manager struct{}

// NewManager return a manager for cgroupv1
func NewManager() *manager {
	return &manager{}
}

func (m *manager) ApplyMemory(absCgroupPath string, data *common.MemoryData) error {
	if data.LimitInBytes > 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "memory.limit_in_bytes", strconv.FormatInt(data.LimitInBytes, 10)); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV1] apply memory limit_in_bytes successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.LimitInBytes, oldData)
		}
	}

	if data.WmarkRatio != 0 {
		newRatio := fmt.Sprintf("%d", data.WmarkRatio)
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "memory.wmark_ratio", newRatio); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV1] apply memory wmark successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.WmarkRatio, oldData)
		}
	}

	return nil
}

func (m *manager) ApplyCPU(absCgroupPath string, data *common.CPUData) error {
	lastErrors := []error{}
	if data.Shares != 0 {
		shares := data.Shares
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpu.shares", strconv.FormatUint(shares, 10)); err != nil {
			lastErrors = append(lastErrors, err)
		} else {
			sharesRead, err := fscommon.GetCgroupParamUint(absCgroupPath, "cpu.shares")
			if err != nil {
				lastErrors = append(lastErrors, err)
			} else {
				if shares > sharesRead {
					lastErrors = append(lastErrors, fmt.Errorf("the maximum allowed cpu-shares is %d", sharesRead))
				} else if shares < sharesRead {
					lastErrors = append(lastErrors, fmt.Errorf("the minimum allowed cpu-shares is %d", sharesRead))
				}
				if applied {
					klog.Infof("[CgroupV1] apply cpu share successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.Shares, oldData)
				}
			}
		}
	}

	if data.CpuPeriod != 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpu.cfs_period_us", strconv.FormatUint(data.CpuPeriod, 10)); err != nil {
			lastErrors = append(lastErrors, err)
		} else if applied {
			klog.Infof("[CgroupV1] apply cpu cfs_period successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.CpuPeriod, oldData)
		}
	}

	if data.CpuQuota != 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpu.cfs_quota_us", strconv.FormatInt(data.CpuQuota, 10)); err != nil {
			lastErrors = append(lastErrors, err)
		} else if applied {
			klog.Infof("[CgroupV1] apply cpu cfs_quota successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.CpuQuota, oldData)
		}
	}

	if data.CpuIdlePtr != nil {
		var cpuIdleValue int64
		if *data.CpuIdlePtr {
			cpuIdleValue = 1

		} else {
			cpuIdleValue = 0
		}

		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpu.idle", strconv.FormatInt(cpuIdleValue, 10)); err != nil {
			lastErrors = append(lastErrors, err)
		} else if applied {
			klog.Infof("[CgroupV1] apply cpu.idle successfully, cgroupPath: %s, data: %d, old data: %s\n", absCgroupPath, cpuIdleValue, oldData)
		}
	}

	if len(lastErrors) == 0 {
		return nil
	}

	errMsg := ""
	for i, err := range lastErrors {
		if i == 0 {
			errMsg = fmt.Sprintf("%d.%s", i, err.Error())
		} else {
			errMsg = fmt.Sprintf("%s, %d.%s", errMsg, i, err.Error())
		}
	}
	return fmt.Errorf("%s", errMsg)
}

func (m *manager) ApplyCPUSet(absCgroupPath string, data *common.CPUSetData) error {
	if len(data.CPUs) != 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpuset.cpus", data.CPUs); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV1] apply cpuset cpus successfully, cgroupPath: %s, data: %v, old data: %v\n",
				absCgroupPath, data.CPUs, oldData)
		}
	}

	if len(data.Migrate) != 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpuset.memory_migrate", "1"); err != nil {
			klog.Infof("[CgroupV1] apply cpuset memory migrate failed, cgroupPath: %s, data: %v, old data %v\n",
				absCgroupPath, data.Migrate, oldData)
		} else if applied {
			klog.Infof("[CgroupV1] apply cpuset memory migrate successfully, cgroupPath: %s, data: %v, old data %v\n",
				absCgroupPath, data.Migrate, oldData)
		}
	}

	if len(data.Mems) != 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpuset.mems", data.Mems); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV1] apply cpuset mems successfully, cgroupPath: %s, data: %v, old data: %v\n",
				absCgroupPath, data.Mems, oldData)
		}
	}

	return nil
}

func (m *manager) ApplyNetCls(absCgroupPath string, data *common.NetClsData) error {
	if data.ClassID != 0 {
		classID := fmt.Sprintf("%d", data.ClassID)
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "net_cls.classid", classID); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV1] apply net cls successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.ClassID, oldData)
		}
	}

	return nil
}

func (m *manager) ApplyUnifiedData(absCgroupPath, cgroupFileName, data string) error {
	if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, cgroupFileName, data); err != nil {
		return err
	} else if applied {
		klog.Infof("[CgroupV2] apply unified data successfully,"+
			" cgroupPath: %s, data: %v, old data: %v\n", path.Join(absCgroupPath, cgroupFileName), data, oldData)
	}

	return nil
}

func (m *manager) GetMemory(absCgroupPath string) (*common.MemoryStats, error) {
	memoryStats := &common.MemoryStats{}
	moduleName := "memory"

	limitFile := strings.Join([]string{moduleName, "limit_in_bytes"}, ".")
	limit, err := fscommon.GetCgroupParamUint(absCgroupPath, limitFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s, %v", limitFile, err)
	}
	memoryStats.Limit = limit

	usageFile := strings.Join([]string{moduleName, "usage_in_bytes"}, ".")
	usage, err := fscommon.GetCgroupParamUint(absCgroupPath, usageFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s, %v", usageFile, err)
	}
	memoryStats.Usage = usage

	return memoryStats, nil
}

func (m *manager) GetCPU(absCgroupPath string) (*common.CPUStats, error) {
	cpuStats := &common.CPUStats{}

	period, err := fscommon.GetCgroupParamUint(absCgroupPath, "cpu.cfs_period_us")
	if err != nil {
		return nil, fmt.Errorf("get cfs period %s err, %v", absCgroupPath, err)
	}

	quota, err := common.GetCgroupParamInt(absCgroupPath, "cpu.cfs_quota_us")
	if err != nil {
		return nil, fmt.Errorf("get cfs quota %s err, %v", absCgroupPath, err)
	}

	cpuStats.CpuPeriod = period
	cpuStats.CpuQuota = quota
	return cpuStats, nil
}

func (m *manager) GetCPUSet(absCgroupPath string) (*common.CPUSetStats, error) {
	cpusetStats := &common.CPUSetStats{}

	var err error
	cpusetStats.CPUs, err = fscommon.GetCgroupParamString(absCgroupPath, "cpuset.cpus")

	if err != nil {
		return nil, fmt.Errorf("read cpuset.cpus failed with error: %v", err)
	}

	cpusetStats.Mems, err = fscommon.GetCgroupParamString(absCgroupPath, "cpuset.mems")

	if err != nil {
		return nil, fmt.Errorf("read cpuset.mems failed with error: %v", err)
	}

	return cpusetStats, nil
}

func (m *manager) GetMetrics(relCgroupPath string, subsystemMap map[string]struct{}) (*common.CgroupMetrics, error) {
	errOmit := func(err error) error {
		return nil
	}

	subsystems := make(map[cgroups.Name]struct{})
	for subsys := range subsystemMap {
		subsystems[(cgroups.Name)(subsys)] = struct{}{}
	}

	control, err := cgroups.Load(newHierarchy(subsystems), cgroups.StaticPath(relCgroupPath))
	if err != nil {
		return nil, err
	}

	stats, err := control.Stat(errOmit)
	if err != nil {
		return nil, err
	}

	cm := &common.CgroupMetrics{
		Memory: &common.MemoryMetrics{},
		CPU:    &common.CPUMetrics{},
		Pid:    &common.PidMetrics{},
	}
	for subsys := range subsystems {
		switch subsys {
		case cgroups.Memory:
			if stats.Memory == nil {
				klog.Infof("[cgroupv1] get cgroup stats memory nil, cgroupPath: %v\n", relCgroupPath)
			} else {
				cm.Memory.RSS = stats.Memory.TotalRSS
				cm.Memory.Cache = stats.Memory.TotalCache
				cm.Memory.Dirty = stats.Memory.TotalDirty
				cm.Memory.WriteBack = stats.Memory.TotalWriteback
				cm.Memory.UsageUsage = stats.Memory.Usage.Usage
				cm.Memory.KernelUsage = stats.Memory.Kernel.Usage
				cm.Memory.MemSWUsage = stats.Memory.Swap.Usage
			}
		case cgroups.Cpu:
			if stats.CPU == nil {
				klog.Infof("[cgroupv1] get cgroup stats cpu nil, cgroupPath: %v\n", relCgroupPath)
			} else {
				cm.CPU.UsageTotal = stats.CPU.Usage.Total
				cm.CPU.UsageKernel = stats.CPU.Usage.Kernel
				cm.CPU.UsageUser = stats.CPU.Usage.User
			}
		case cgroups.Pids:
			if stats.Pids == nil {
				klog.Infof("[cgroupv1] get cgroup stats pids nil, cgroupPath: %v\n", relCgroupPath)
			} else {
				cm.Pid.Current = stats.Pids.Current
				cm.Pid.Limit = stats.Pids.Limit
			}
		}
	}
	return cm, nil
}

// GetPids return pids in current cgroup
func (m *manager) GetPids(absCgroupPath string) ([]string, error) {
	pids, err := libcgroups.GetPids(absCgroupPath)
	if err != nil {
		return nil, err
	}

	return general.IntSliceToStringSlice(pids), nil
}

// GetTasks return all threads in current cgroup
func (m *manager) GetTasks(absCgroupPath string) ([]string, error) {
	file := filepath.Join(absCgroupPath, common.CgroupTasksFileV1)
	tasks, err := common.ReadTasksFile(file)
	if err != nil {
		return nil, err
	}

	return tasks, nil
}

func newHierarchy(enabled map[cgroups.Name]struct{}) cgroups.Hierarchy {
	return func() ([]cgroups.Subsystem, error) {
		ss, err := cgroups.V1()
		if err != nil {
			return nil, err
		}
		var subsystems []cgroups.Subsystem
		for _, s := range ss {
			if _, ok := enabled[s.Name()]; ok {
				subsystems = append(subsystems, s)
			}
		}
		return subsystems, nil
	}
}
