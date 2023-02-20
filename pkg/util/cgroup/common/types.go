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

	PodCgroupPathPrefix        = "pod"
	CgroupFsRootPath           = "/kubepods"
	CgroupFsRootPathBestEffort = "/kubepods/besteffort"
	CgroupFsRootPathBurstable  = "/kubepods/burstable"

	SystemdRootPath           = "/kubepods.slice"
	SystemdRootPathBestEffort = "/kubepods.slice/kubepods-besteffort.slice"
	SystemdRootPathBurstable  = "/kubepods.slice/kubepods-burstable.slice"
)

// CgroupType defines the cgroup type that kubernetes version uses,
// and CgroupTypeCgroupfs will used as the default one.
type CgroupType string

const (
	CgroupTypeCgroupfs = "cgroupfs"
	CgroupTypeSystemd  = "systemd"
)

// MemoryData set cgroup memory data
type MemoryData struct {
	LimitInBytes int64
	WmarkRatio   int32
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

// NetClsData set cgroup net data
type NetClsData struct {
	ClassID uint32

	// for cgroupv2
	CgroupID uint64 // cgroup path cid
	SvcID    uint64
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
	CPUs string
	Mems string
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
