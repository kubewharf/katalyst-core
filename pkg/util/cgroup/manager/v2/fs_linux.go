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

package v2

import (
	"errors"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	cgroupsv2 "github.com/containerd/cgroups/v2"
	"github.com/opencontainers/runc/libcontainer/cgroups/fscommon"
	"k8s.io/klog/v2"

	libcgroups "github.com/opencontainers/runc/libcontainer/cgroups"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type manager struct{}

// NewManager return a manager for cgroupv2
func NewManager() *manager {
	return &manager{}
}

func (m *manager) ApplyMemory(absCgroupPath string, data *common.MemoryData) error {
	if data.LimitInBytes > 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "memory.max", numToStr(data.LimitInBytes)); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply memory max successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.LimitInBytes, oldData)
		}
	}

	if data.WmarkRatio != 0 {
		newRatio := fmt.Sprintf("%d", data.WmarkRatio)
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "memory.wmark_ratio", newRatio); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply memory wmark successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.WmarkRatio, oldData)
		}
	}

	return nil
}

func (m *manager) ApplyCPU(absCgroupPath string, data *common.CPUData) error {
	lastErrors := []error{}
	cpuWeight := libcgroups.ConvertCPUSharesToCgroupV2Value(data.Shares)
	if cpuWeight != 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpu.weight", strconv.FormatUint(cpuWeight, 10)); err != nil {
			lastErrors = append(lastErrors, err)
		} else if applied {
			klog.Infof("[CgroupV2] apply cpu weight successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, cpuWeight, oldData)
		}
	}

	if data.CpuQuota != 0 || data.CpuPeriod != 0 {
		str := "max"
		if data.CpuQuota > 0 {
			str = strconv.FormatInt(data.CpuQuota, 10)
		}
		period := data.CpuPeriod
		if period == 0 {
			period = 100000
		}

		// refer to https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html
		str += " " + strconv.FormatUint(period, 10)
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpu.max", str); err != nil {
			lastErrors = append(lastErrors, err)
		} else if applied {
			klog.Infof("[CgroupV2] apply cpu max successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, str, oldData)
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
			klog.Infof("[CgroupV2] apply cpu.idle successfully, cgroupPath: %s, data: %d, old data: %s\n", absCgroupPath, cpuIdleValue, oldData)
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
			klog.Infof("[CgroupV2] apply cpuset cpus successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.CPUs, oldData)
		}
	}

	if len(data.Migrate) != 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpuset.memory_migrate", "1"); err != nil {
			klog.Infof("[CgroupV2] apply cpuset memory migrate failed, cgroupPath: %s, data: %v, old data %v\n", absCgroupPath, data.Migrate, oldData)
		} else if applied {
			klog.Infof("[CgroupV2] apply cpuset memory migrate successfully, cgroupPath: %s, data: %v, old data %v\n", absCgroupPath, data.Migrate, oldData)
		}
	}

	if len(data.Mems) != 0 {
		if err, applied, oldData := common.WriteFileIfChange(absCgroupPath, "cpuset.mems", data.Mems); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply cpuset mems successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.Mems, oldData)
		}
	}

	return nil
}

func (m *manager) ApplyNetCls(_ string, _ *common.NetClsData) error {
	return errors.New("cgroups v2 does not support net_cls cgroup, please use eBPF via external manager")
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

	limit := strings.Join([]string{moduleName, "max"}, ".")
	value, err := fscommon.GetCgroupParamUint(absCgroupPath, limit)
	if err != nil {
		return nil, fmt.Errorf("get memory %s err, %v", absCgroupPath, err)
	}
	memoryStats.Limit = value

	usageFile := strings.Join([]string{moduleName, "current"}, ".")
	usage, err := fscommon.GetCgroupParamUint(absCgroupPath, usageFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s, %v", usageFile, err)
	}
	memoryStats.Usage = usage

	return memoryStats, nil
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

func (m *manager) GetCPU(absCgroupPath string) (*common.CPUStats, error) {
	cpuStats := &common.CPUStats{}
	contents, err := ioutil.ReadFile(filepath.Join(absCgroupPath, "cpu.max")) //nolint:gosec
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(contents))
	parts := strings.Split(trimmed, " ")
	if len(parts) != 2 {
		return nil, fmt.Errorf("get cpu %s err, %v", absCgroupPath, err)
	}

	var quota int64
	if parts[0] == "max" {
		quota = math.MaxInt64
	} else {
		quota, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse int %s err, err %v", parts[0], err)
		}
	}

	period, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse uint %s err, err %v", parts[1], err)
	}

	cpuStats.CpuPeriod = period
	cpuStats.CpuQuota = quota
	return cpuStats, nil
}

func (m *manager) GetMetrics(relCgroupPath string, _ map[string]struct{}) (*common.CgroupMetrics, error) {
	c, err := cgroupsv2.LoadManager(common.CgroupFSMountPoint, relCgroupPath)
	if err != nil {
		return nil, err
	}

	stats, err := c.Stat()
	if err != nil {
		return nil, err
	}

	cm := &common.CgroupMetrics{
		Memory: &common.MemoryMetrics{},
		CPU:    &common.CPUMetrics{},
		Pid:    &common.PidMetrics{},
	}
	if stats.Memory == nil {
		klog.Infof("[cgroupv2] get cgroup stats memory nil, cgroupPath: %v\n", relCgroupPath)
	} else {
		cm.Memory.RSS = stats.Memory.Anon
		cm.Memory.Dirty = stats.Memory.FileDirty
		cm.Memory.WriteBack = stats.Memory.FileWriteback
		cm.Memory.Cache = stats.Memory.File

		cm.Memory.KernelUsage = stats.Memory.KernelStack + stats.Memory.Slab + stats.Memory.Sock
		cm.Memory.UsageUsage = stats.Memory.Usage
		cm.Memory.MemSWUsage = stats.Memory.SwapUsage
	}

	if stats.CPU == nil {
		klog.Infof("[cgroupv2] get cgroup stats cpu nil, cgroupPath: %v\n", relCgroupPath)
	} else {
		cm.CPU.UsageTotal = stats.CPU.UsageUsec * 1000
		cm.CPU.UsageUser = stats.CPU.UserUsec * 1000
		cm.CPU.UsageKernel = stats.CPU.SystemUsec * 1000
	}

	if stats.Pids == nil {
		klog.Infof("[cgroupv2] get cgroup stats pids nil, cgroupPath: %v\n", relCgroupPath)
	} else {
		cm.Pid.Current = stats.Pids.Current
		cm.Pid.Limit = stats.Pids.Limit
	}

	return cm, nil
}

// GetPids return pids in current cgroup
func (m *manager) GetPids(absCgroupPath string) ([]string, error) {
	pids, err := libcgroups.GetAllPids(absCgroupPath)
	if err != nil {
		return nil, err
	}
	return general.IntSliceToStringSlice(pids), nil
}

// GetTasks return all threads in current cgroup
func (m *manager) GetTasks(absCgroupPath string) ([]string, error) {
	var tasks []string
	err := filepath.Walk(absCgroupPath, func(p string, info os.FileInfo, iErr error) error {
		if iErr != nil {
			return iErr
		}
		if strings.HasSuffix(info.Name(), ".mount") {
			return filepath.SkipDir
		}
		if info.IsDir() || info.Name() != common.CgroupTasksFileV2 {
			return nil
		}

		pids, err := common.ReadTasksFile(p)
		if err != nil {
			return err
		}
		tasks = append(tasks, pids...)
		return nil
	})
	return tasks, err
}

func numToStr(value int64) (ret string) {
	switch {
	case value == 0:
		ret = ""
	case value == -1:
		ret = "max"
	default:
		ret = strconv.FormatInt(value, 10)
	}

	return ret
}
