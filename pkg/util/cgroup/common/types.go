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

package common

import (
	"fmt"
)

const (
	// CgroupFSMountPoint default cgroup mount point
	CgroupFSMountPoint = "/sys/fs/cgroup"
	// DefaultSelectedSubsys cgroupv1 default select subsys
	DefaultSelectedSubsys = "cpu"
	// CgroupTasksFileV1 thread id file for cgroupv1
	CgroupTasksFileV1 = "tasks"
	// CgroupTasksFileV2 thread id file for cgroupv2
	CgroupTasksFileV2 = "cgroup.threads"

	CgroupSubsysCPUSet = "cpuset"
	CgroupSubsysMemory = "memory"
	CgroupSubsysCPU    = "cpu"
	CgroupSubsysIO     = "io"
	// CgroupSubsysNetCls is the net_cls sub-system
	CgroupSubsysNetCls = "net_cls"

	PodCgroupPathPrefix        = "pod"
	CgroupFsRootPath           = "/kubepods"
	CgroupFsRootPathBestEffort = "/kubepods/besteffort"
	CgroupFsRootPathBurstable  = "/kubepods/burstable"

	SystemdRootPath           = "/kubepods.slice"
	SystemdRootPathBestEffort = "/kubepods.slice/kubepods-besteffort.slice"
	SystemdRootPathBurstable  = "/kubepods.slice/kubepods-burstable.slice"
)

// defaultSelectedSubsysList cgroupv1 most common subsystems
var defaultSelectedSubsysList = []string{CgroupSubsysCPU, CgroupSubsysMemory, CgroupSubsysCPUSet}

// CgroupType defines the cgroup type that kubernetes version uses,
// and CgroupTypeCgroupfs will used as the default one.
type CgroupType string

const (
	CgroupTypeCgroupfs = "cgroupfs"
	CgroupTypeSystemd  = "systemd"
)

// MemoryData set cgroup memory data
type MemoryData struct {
	LimitInBytes       int64
	TCPMemLimitInBytes int64
	// SoftLimitInBytes for memory.low
	// Best effort memory protection, cgroup memory that will not be reclaimed in soft_limit_reclaim phase of kswapd.
	SoftLimitInBytes int64
	// MinInBytes for memory.min
	// cgroup memory that can never be reclaimed by kswapd.
	MinInBytes int64
	WmarkRatio int32
	// SwapMaxInBytes < 0 means disable cgroup-level swap
	SwapMaxInBytes int64
}

type PressureType int

const (
	SOME PressureType = iota
	FULL
)

// MemoryPressure get cgroup memory pressure
type MemoryPressure struct {
	Avg10  uint64
	Avg60  uint64
	Avg300 uint64
}

// CPUData set cgroup cpu data
type CPUData struct {
	Shares     uint64
	CpuPeriod  uint64
	CpuQuota   int64
	CpuIdlePtr *bool
}

// CPUSetData set cgroup cpuset data
type CPUSetData struct {
	CPUs    string
	Mems    string
	Migrate string
}

// NetClsData is the net class data.
type NetClsData struct {
	// ClassID is the class id of the container.
	ClassID uint32
	// CgroupID is used for cgroup v2.
	CgroupID uint64
	// Attributes are some optional attributes.
	Attributes map[string]string
}

type (
	IOCostCtrlMode string
	IOCostModel    string
)

const (
	IOCostCtrlModeAuto IOCostCtrlMode = "auto"
	IOCostCtrlModeUser IOCostCtrlMode = "user"
	IOCostModelLinear  IOCostModel    = "linear"
)

// IOCostQoSData is the io.cost.qos data supported in cgroupv2
type IOCostQoSData struct {
	Enable              uint32         `json:"enable"`                // Weight-based control enable
	CtrlMode            IOCostCtrlMode `json:"ctrl_mode"`             //"auto" or "user"
	ReadLatencyPercent  float32        `json:"read_latency_percent"`  // read latency percentile [0, 100]
	ReadLatencyUS       uint32         `json:"read_latency_us"`       // read latency threshold, unit microsecond
	WriteLatencyPercent float32        `json:"write_latency_percent"` // write latency percentile [0, 100]
	WriteLatencyUS      uint32         `json:"write_latency_us"`      // write latency threshold, unit microsecond
	VrateMin            float32        `json:"vrate_min"`             // vrate minimum scaling percentage [1, 10000]
	VrateMax            float32        `json:"vrate_max"`             // vrate maximum scaling percentage [1, 10000]
}

func (iocqd *IOCostQoSData) String() string {
	if iocqd == nil {
		return ""
	}

	if iocqd.Enable == 0 {
		return "enable=0"
	}

	return fmt.Sprintf("enable=1 ctrl=%s rpct=%.2f rlat=%d wpct=%.2f wlat=%d min=%.2f max=%.2f",
		iocqd.CtrlMode, iocqd.ReadLatencyPercent, iocqd.ReadLatencyUS, iocqd.WriteLatencyPercent, iocqd.WriteLatencyUS, iocqd.VrateMin, iocqd.VrateMax)
}

// IOCostModelData is the io.cost.model data supported in cgroupv2
type IOCostModelData struct {
	CtrlMode      IOCostCtrlMode `json:"ctrl_mode"`       //"auto" or "user"
	Model         IOCostModel    `json:"model"`           // The cost model in use - "linear"
	ReadBPS       uint64         `json:"read_bps"`        // read bytes per second
	ReadSeqIOPS   uint64         `json:"read_seq_iops"`   // sequential read IOPS
	ReadRandIOPS  uint64         `json:"read_rand_iops"`  // random read IOPS
	WriteBPS      uint64         `json:"write_bps"`       // wirte bytes per second
	WriteSeqIOPS  uint64         `json:"write_seq_iops"`  // sequential write IOPS
	WriteRandIOPS uint64         `json:"write_rand_iops"` // random write IOPS
}

func (iocmd *IOCostModelData) String() string {
	if iocmd == nil {
		return ""
	}

	return fmt.Sprintf("ctrl=%s model=%s rbps=%d rseqiops=%d rrandiops=%d wbps=%d wseqiops=%d wrandiops=%d",
		iocmd.CtrlMode, iocmd.Model, iocmd.ReadBPS, iocmd.ReadSeqIOPS, iocmd.ReadRandIOPS, iocmd.WriteBPS, iocmd.WriteSeqIOPS, iocmd.WriteRandIOPS)
}

// MemoryStats get cgroup memory data
type MemoryStats struct {
	Limit uint64
	Usage uint64
}

// CPUStats get cgroup cpu data
type CPUStats struct {
	CpuPeriod uint64
	CpuQuota  int64
}

// CPUSetStats get cgroup cpuset data
type CPUSetStats struct {
	CPUs          string
	EffectiveCPUs string
	Mems          string
	EffectiveMems string
}

// MemoryMetrics get memory cgroup metrics
type MemoryMetrics struct {
	RSS         uint64
	Cache       uint64
	Dirty       uint64
	WriteBack   uint64
	UsageUsage  uint64
	KernelUsage uint64
	// memory.memsw.usage_in_bytes Reports the total size in
	// bytes of the memory and swap space used by tasks in the cgroup.
	MemSWUsage uint64
}

// MemoryNumaMetrics get per-numa level memory cgroup metrics
type MemoryNumaMetrics struct {
	Anon uint64
	File uint64
}

// PidMetrics get pid cgroup metrics
type PidMetrics struct {
	Current uint64
	Limit   uint64
}

// CPUMetrics get cpu cgroup metrics
type CPUMetrics struct {
	UsageTotal  uint64
	UsageKernel uint64
	UsageUser   uint64
}

// CgroupMetrics cgroup metrics
type CgroupMetrics struct {
	CPU    *CPUMetrics
	Memory *MemoryMetrics
	Pid    *PidMetrics
}
