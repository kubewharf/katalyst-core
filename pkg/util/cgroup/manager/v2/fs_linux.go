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
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	cgroupsv2 "github.com/containerd/cgroups/v2"
	libcgroups "github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/opencontainers/runc/libcontainer/cgroups/fscommon"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type manager struct{}

// NewManager return a manager for cgroupv2
func NewManager() *manager {
	return &manager{}
}

func (m *manager) ApplyMemory(absCgroupPath string, data *common.MemoryData) error {
	if data.LimitInBytes != 0 {
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "memory.max", numToStr(data.LimitInBytes)); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply memory max successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.LimitInBytes, oldData)
		}
	}

	if data.SoftLimitInBytes > 0 {
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "memory.low", numToStr(data.SoftLimitInBytes)); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply memory low successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.SoftLimitInBytes, oldData)
		}
	}

	if data.MinInBytes > 0 {
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "memory.min", numToStr(data.MinInBytes)); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply memory min successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.MinInBytes, oldData)
		}
	}

	if data.WmarkRatio != 0 {
		newRatio := fmt.Sprintf("%d", data.WmarkRatio)
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "memory.wmark_ratio", newRatio); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply memory wmark successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.WmarkRatio, oldData)
		}
	}

	if data.SwapMaxInBytes != 0 {
		// Do Not change swap max setting if SwapMaxInBytes equals to 0
		var swapMax int64 = 0
		if data.SwapMaxInBytes > 0 {
			swapMax = data.SwapMaxInBytes
		}
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "memory.swap.max", fmt.Sprintf("%d", swapMax)); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply memory swap max successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, swapMax, oldData)
		}
	}
	return nil
}

func (m *manager) ApplyCPU(absCgroupPath string, data *common.CPUData) error {
	lastErrors := []error{}
	cpuWeight := libcgroups.ConvertCPUSharesToCgroupV2Value(data.Shares)
	if cpuWeight != 0 {
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "cpu.weight", strconv.FormatUint(cpuWeight, 10)); err != nil {
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
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "cpu.max", str); err != nil {
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

		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "cpu.idle", strconv.FormatInt(cpuIdleValue, 10)); err != nil {
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
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "cpuset.cpus", data.CPUs); err != nil {
			return err
		} else if applied {
			klog.Infof("[CgroupV2] apply cpuset cpus successfully, cgroupPath: %s, data: %v, old data: %v\n", absCgroupPath, data.CPUs, oldData)
		}
	}

	if len(data.Migrate) != 0 {
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "cpuset.memory_migrate", "1"); err != nil {
			klog.Infof("[CgroupV2] apply cpuset memory migrate failed, cgroupPath: %s, data: %v, old data %v\n", absCgroupPath, data.Migrate, oldData)
		} else if applied {
			klog.Infof("[CgroupV2] apply cpuset memory migrate successfully, cgroupPath: %s, data: %v, old data %v\n", absCgroupPath, data.Migrate, oldData)
		}
	}

	if len(data.Mems) != 0 {
		if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "cpuset.mems", data.Mems); err != nil {
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

func (m *manager) ApplyIOCostQoS(absCgroupPath string, devID string, data *common.IOCostQoSData) error {
	if data == nil {
		return fmt.Errorf("ApplyIOCostQoS got nil data")
	}

	dataContent := data.String()
	if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "io.cost.qos", fmt.Sprintf("%s %s", devID, dataContent)); err != nil {
		return err
	} else if applied {
		klog.Infof("[CgroupV2] apply io.cost.qos data successfully,"+
			"cgroupPath: %s, data: %s, old data: %s\n", absCgroupPath, dataContent, oldData)
	}

	return nil
}

func (m *manager) ApplyIOCostModel(absCgroupPath string, devID string, data *common.IOCostModelData) error {
	if data == nil {
		return fmt.Errorf("ApplyIOCostModel got nil data")
	}

	dataContent := data.String()
	if data.CtrlMode == common.IOCostCtrlModeAuto {
		dataContent = "ctrl=auto"
	}

	if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "io.cost.model", fmt.Sprintf("%s %s", devID, dataContent)); err != nil {
		return err
	} else if applied {
		klog.Infof("[CgroupV2] apply io.cost.model data successfully,"+
			"cgroupPath: %s, data: %s, old data: %s\n", absCgroupPath, dataContent, oldData)
	}

	return nil
}

func (m *manager) ApplyIOWeight(absCgroupPath string, devID string, weight uint64) error {
	dataContent := fmt.Sprintf("%s %d", devID, weight)

	curWight, found, err := m.GetDeviceIOWeight(absCgroupPath, devID)
	if err != nil {
		return fmt.Errorf("try GetDeviceIOWeight before ApplyIOWeight failed with error: %v", err)
	}

	if found && curWight == weight {
		klog.Infof("[CgroupV2] io.weight: %d in cgroupPath: %s for device: %s isn't changed, not to apply it",
			curWight, absCgroupPath, devID)
		return nil
	}

	if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, "io.weight", dataContent); err != nil {
		return err
	} else if applied {
		klog.Infof("[CgroupV2] apply io.weight for device: %s successfully,"+
			"cgroupPath: %s, added data: %s, old data: %s\n", devID, absCgroupPath, dataContent, oldData)
	}

	return nil
}

func (m *manager) ApplyUnifiedData(absCgroupPath, cgroupFileName, data string) error {
	if err, applied, oldData := common.InstrumentedWriteFileIfChange(absCgroupPath, cgroupFileName, data); err != nil {
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

func (m *manager) GetNumaMemory(absCgroupPath string) (map[int]*common.MemoryNumaMetrics, error) {
	const fileName = "memory.numa_stat"
	content, err := libcgroups.ReadFile(absCgroupPath, fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", fileName, err)
	}

	numaStat, err := common.ParseCgroupNumaValue(content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse numa stat: %w", err)
	}

	result := make(map[int]*common.MemoryNumaMetrics)
	if anonStat, ok := numaStat["anon"]; ok {
		for numaID, value := range anonStat {
			if _, ok := result[numaID]; !ok {
				result[numaID] = &common.MemoryNumaMetrics{
					Anon: value,
				}
			} else {
				result[numaID].Anon = value
			}
		}
	} else {
		general.Warningf("no anon in numa stat,cgroup path:%v", absCgroupPath)
	}

	if fileStat, ok := numaStat["file"]; ok {
		for numaID, value := range fileStat {
			if _, ok := result[numaID]; !ok {
				result[numaID] = &common.MemoryNumaMetrics{
					File: value,
				}
			} else {
				result[numaID].File = value
			}
		}
	} else {
		general.Warningf("no file in numa stat,cgroup path:%v", absCgroupPath)
	}

	return result, nil
}

func pressureTypeToString(t common.PressureType) string {
	switch t {
	case common.SOME:
		return "some"
	case common.FULL:
		return "full"
	default:
		return "unreachable"
	}
}

type PsiFormat int

const (
	UPSTREAM PsiFormat = iota
	MISSING
	INVALID
)

func getPsiFormat(lines []string) PsiFormat {
	if strings.Contains(lines[0], "avg10") {
		return UPSTREAM
	} else if len(lines) == 0 {
		return MISSING
	}
	return INVALID
}

func split(s, sep string) []string {
	return strings.Split(s, sep)
}

func atou64(s string) uint64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return uint64(f)
}

func readRespressureFromLines(lines []string, t common.PressureType) (*common.MemoryPressure, error) {
	typeName := pressureTypeToString(t)
	var pressureLineIndex int
	switch t {
	case common.SOME:
		pressureLineIndex = 0
	case common.FULL:
		pressureLineIndex = 1
	}

	switch getPsiFormat(lines) {
	case UPSTREAM:
		toks := split(lines[pressureLineIndex], " ")
		if toks[0] != typeName {
			return nil, errors.New("invalid type name")
		}
		avg10 := split(toks[1], "=")
		if avg10[0] != "avg10" {
			return nil, errors.New("invalid avg10 format")
		}
		avg60 := split(toks[2], "=")
		if avg60[0] != "avg60" {
			return nil, errors.New("invalid avg60 format")
		}
		avg300 := split(toks[3], "=")
		if avg300[0] != "avg300" {
			return nil, errors.New("invalid avg300 format")
		}
		total := split(toks[4], "=")
		if total[0] != "total" {
			return nil, errors.New("invalid total format")
		}
		return &common.MemoryPressure{
			Avg10:  atou64(avg10[1]),
			Avg60:  atou64(avg60[1]),
			Avg300: atou64(avg300[1]),
		}, nil

	case MISSING:
		return nil, errors.New("missing control file")
	case INVALID:
		return nil, errors.New("invalid format")
	}
	return nil, errors.New("unreachable")
}

func (m *manager) GetMemoryPressure(absCgroupPath string, t common.PressureType) (*common.MemoryPressure, error) {
	pressureFile := filepath.Join(absCgroupPath, "memory.pressure")

	file, err := os.Open(pressureFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return readRespressureFromLines(lines, t)
}

func (m *manager) GetCPUSet(absCgroupPath string) (*common.CPUSetStats, error) {
	cpusetStats := &common.CPUSetStats{}

	var err error
	cpusetStats.CPUs, err = fscommon.GetCgroupParamString(absCgroupPath, "cpuset.cpus")
	if err != nil {
		return nil, err
	}
	cpusetStats.EffectiveCPUs, err = fscommon.GetCgroupParamString(absCgroupPath, "cpuset.cpus.effective")
	if err != nil {
		return nil, err
	}

	cpusetStats.Mems, err = fscommon.GetCgroupParamString(absCgroupPath, "cpuset.mems")
	if err != nil {
		return nil, err
	}
	cpusetStats.EffectiveMems, err = fscommon.GetCgroupParamString(absCgroupPath, "cpuset.mems.effective")
	if err != nil {
		return nil, err
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

func (m *manager) GetIOCostQoS(absCgroupPath string) (map[string]*common.IOCostQoSData, error) {
	contents, err := ioutil.ReadFile(filepath.Join(absCgroupPath, "io.cost.qos"))
	if err != nil {
		return nil, err
	}

	rawStr := strings.TrimRight(string(contents), "\n")
	qosStrList := strings.Split(rawStr, "\n")

	devIDtoQoSData := make(map[string]*common.IOCostQoSData)
	for _, str := range qosStrList {
		if strings.TrimSpace(str) == "" {
			continue
		}

		devID, data, err := parseDeviceIOCostQoS(str)
		if err != nil {
			general.Errorf("invalid device io cost qos data: %s, err: %v", str, err)
			continue
		}

		devIDtoQoSData[devID] = data
	}

	return devIDtoQoSData, nil
}

func (m *manager) GetIOCostModel(absCgroupPath string) (map[string]*common.IOCostModelData, error) {
	contents, err := ioutil.ReadFile(filepath.Join(absCgroupPath, "io.cost.model"))
	if err != nil {
		return nil, err
	}

	rawStr := strings.TrimRight(string(contents), "\n")
	modelStrList := strings.Split(rawStr, "\n")

	devIDtoModelData := make(map[string]*common.IOCostModelData)
	for _, str := range modelStrList {
		if strings.TrimSpace(str) == "" {
			continue
		}

		devID, data, err := parseDeviceIOCostModel(str)
		if err != nil {
			general.Errorf("invalid device io cost model %s, err %v", str, err)
			continue
		}

		devIDtoModelData[devID] = data
	}

	return devIDtoModelData, nil
}

func (m *manager) GetDeviceIOWeight(absCgroupPath string, devID string) (uint64, bool, error) {
	ioWeightFile := path.Join(absCgroupPath, "io.weight")
	contents, err := ioutil.ReadFile(ioWeightFile)
	if err != nil {
		return 0, false, fmt.Errorf("failed to ReadFile %s, err %v", ioWeightFile, err)
	}

	rawStr := strings.TrimRight(string(contents), "\n")
	weightStrList := strings.Split(rawStr, "\n")

	var devWeight string
	for _, line := range weightStrList {
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 2 {
			log.Printf("invalid weight line %s in %s", line, ioWeightFile)
			continue
		}

		if fields[0] == devID {
			devWeight = fields[1]
			break
		}
	}

	if devWeight == "" {
		return 0, false, nil
	}

	weight, err := strconv.ParseUint(strings.TrimRight(devWeight, "\n"), 10, 64)
	return weight, true, err
}

func (m *manager) GetIOStat(absCgroupPath string) (map[string]map[string]string, error) {
	ioStatFile := path.Join(absCgroupPath, "io.stat")
	contents, err := ioutil.ReadFile(ioStatFile)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadFile %s, err %v", ioStatFile, err)
	}

	rawStr := strings.TrimRight(string(contents), "\n")
	ioStatStrList := strings.Split(rawStr, "\n")

	devIDtoIOStat := make(map[string]map[string]string)
	for _, line := range ioStatStrList {
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		fields := strings.Fields(line)

		if len(fields) == 0 {
			return nil, fmt.Errorf("got empty line")
		}

		devID := fields[0]

		if devIDtoIOStat[devID] == nil {
			devIDtoIOStat[devID] = make(map[string]string)
		}

		metrics := fields[1:]
		for _, m := range metrics {
			kv := strings.Split(m, "=")
			if len(kv) != 2 {
				return nil, fmt.Errorf("invalid metric %s in line %s", m, line)
			}

			devIDtoIOStat[devID][kv[0]] = kv[1]
		}
	}

	return devIDtoIOStat, nil
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

func parseDeviceIOCostQoS(str string) (string, *common.IOCostQoSData, error) {
	fields := strings.Fields(str)
	if len(fields) != 9 {
		return "", nil, fmt.Errorf("device io cost qos line(%s) does not has 9 fields", str)
	}

	devID := fields[0]
	others := fields[1:]
	ioCostQoSData := &common.IOCostQoSData{}
	for _, o := range others {
		kv := strings.Split(o, "=")
		if len(kv) != 2 {
			return "", nil, fmt.Errorf("invalid device io cost qos line(%s) with invalid option conf %s", str, o)
		}

		key := kv[0]
		valStr := kv[1]

		switch key {
		case "enable":
			val, err := strconv.ParseUint(valStr, 10, 32)
			if err != nil {
				return "", nil, fmt.Errorf("device io cost qos(%s) has invalid value(%s) for option %s", str, valStr, key)
			}
			ioCostQoSData.Enable = uint32(val)
		case "ctrl":
			ioCostQoSData.CtrlMode = common.IOCostCtrlMode(valStr)
		case "rpct":
			val, err := strconv.ParseFloat(valStr, 32)
			if err != nil {
				return "", nil, fmt.Errorf("device io cost qos(%s) has invalid value(%s) for option %s", str, valStr, key)
			}
			ioCostQoSData.ReadLatencyPercent = float32(val)
		case "rlat":
			val, err := strconv.ParseUint(valStr, 10, 32)
			if err != nil {
				return "", nil, fmt.Errorf("device io cost qos(%s) has invalid value(%s) for option %s", str, valStr, key)
			}
			ioCostQoSData.ReadLatencyUS = uint32(val)
		case "wpct":
			val, err := strconv.ParseFloat(valStr, 32)
			if err != nil {
				return "", nil, fmt.Errorf("device io cost qos(%s) has invalid value(%s) for option %s", str, valStr, key)
			}
			ioCostQoSData.WriteLatencyPercent = float32(val)
		case "wlat":
			val, err := strconv.ParseUint(valStr, 10, 32)
			if err != nil {
				return "", nil, fmt.Errorf("device io cost qos(%s) has invalid value(%s) for option %s", str, valStr, key)
			}
			ioCostQoSData.WriteLatencyUS = uint32(val)
		case "min":
			val, err := strconv.ParseFloat(valStr, 32)
			if err != nil {
				return "", nil, fmt.Errorf("device io cost qos(%s) has invalid value(%s) for option %s", str, valStr, key)
			}
			ioCostQoSData.VrateMin = float32(val)
		case "max":
			val, err := strconv.ParseFloat(valStr, 32)
			if err != nil {
				return "", nil, fmt.Errorf("device io cost qos(%s) has invalid value(%s) for option %s", str, valStr, key)
			}
			ioCostQoSData.VrateMax = float32(val)
		default:
			return "", nil, fmt.Errorf("device io cost qos(%s) has invalid option key %s", str, key)
		}
	}

	return devID, ioCostQoSData, nil
}

func parseDeviceIOCostModel(str string) (string, *common.IOCostModelData, error) {
	fields := strings.Fields(str)
	if len(fields) != 9 {
		return "", nil, fmt.Errorf("device io cost model(%s) does not has 9 fields", str)
	}

	devID := fields[0]
	others := fields[1:]
	ioCostModelData := &common.IOCostModelData{}
lineLoop:
	for _, o := range others {
		kv := strings.Split(o, "=")
		if len(kv) != 2 {
			return "", nil, fmt.Errorf("invalid device io cost model(%s) with invalid option conf %s", str, o)
		}

		key := kv[0]
		valStr := kv[1]

		switch key {
		case "ctrl":
			ioCostModelData.CtrlMode = common.IOCostCtrlMode(valStr)
			continue lineLoop
		case "model":
			ioCostModelData.Model = common.IOCostModel(valStr)
			continue lineLoop
		}

		val, err := strconv.ParseUint(valStr, 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("device io cost model(%s) has invalid value(%s) for option %s", str, valStr, key)
		}

		switch key {
		case "ctrl":
			ioCostModelData.CtrlMode = common.IOCostCtrlMode(valStr)
		case "model":
			ioCostModelData.Model = common.IOCostModel(valStr)
		case "rbps":
			ioCostModelData.ReadBPS = val
		case "rseqiops":
			ioCostModelData.ReadSeqIOPS = val
		case "rrandiops":
			ioCostModelData.ReadRandIOPS = val
		case "wbps":
			ioCostModelData.WriteBPS = val
		case "wseqiops":
			ioCostModelData.WriteSeqIOPS = val
		case "wrandiops":
			ioCostModelData.WriteRandIOPS = val
		default:
			return "", nil, fmt.Errorf("device io cost model(%s) has invalid option key %s", str, key)
		}
	}

	return devID, ioCostModelData, nil
}
