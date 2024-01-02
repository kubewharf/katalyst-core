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

package types

type MalachiteCgroupV1Info struct {
	Memory *MemoryCgDataV1 `json:"memory"`
	Blkio  *BlkIOCgDataV1  `json:"blkio"`
	NetCls *NetClsCgData   `json:"net_cls"`
	CpuSet *CPUSetCgDataV1 `json:"cpuset"`
	Cpu    *CPUCgDataV1    `json:"cpu"`
}

type SubSystemGroupsV1 struct {
	NetCls  NetClsCg    `json:"NetCls"`
	Blkio   BlkioCgV1   `json:"Blkio"`
	Cpuacct CpuacctCgV1 `json:"Cpuacct"`
	Cpuset  CpusetCgV1  `json:"Cpuset"`
	Memory  MemoryCgV1  `json:"Memory"`
}

type CpuacctCgV1 struct {
	V1 struct {
		CPUData CPUCgDataV1 `json:"V1"`
	} `json:"Cpu"`
}

type MemoryCgV1 struct {
	V1 struct {
		MemoryV1Data MemoryCgDataV1 `json:"V1"`
	} `json:"Memory"`
}

type CpusetCgV1 struct {
	V1 struct {
		CPUSetData CPUSetCgDataV1 `json:"V1"`
	} `json:"CpuSet"`
}

type BlkioCgV1 struct {
	V1 struct {
		BlkIOData BlkIOCgDataV1 `json:"V1"`
	} `json:"BlkIO"`
}

type CPUCgDataV1 struct {
	FullPath              string         `json:"full_path"`
	CfsQuotaUs            int64          `json:"cfs_quota_us"`
	CfsPeriodUs           int64          `json:"cfs_period_us"`
	CPUShares             uint64         `json:"cpu_shares"`
	OldCPUBasicInfo       CPUBasicInfoV1 `json:"old_cpu_basic_info"`
	NewCPUBasicInfo       CPUBasicInfoV1 `json:"new_cpu_basic_info"`
	CPUUsageRatio         float64        `json:"cpu_usage_ratio"`
	CPUUserUsageRatio     float64        `json:"cpu_user_usage_ratio"`
	CPUSysUsageRatio      float64        `json:"cpu_sys_usage_ratio"`
	CPUNrThrottled        uint64         `json:"cpu_nr_throttled"`
	CPUNrPeriods          uint64         `json:"cpu_nr_periods"`
	CPUThrottledTime      uint64         `json:"cpu_throttled_time"`
	TaskNrIoWait          uint64         `json:"task_nr_iowait"`
	TaskNrUninterruptible uint64         `json:"task_nr_unint"`
	TaskNrRunning         uint64         `json:"task_nr_runnable"`
	TaskNrSleeping        uint64         `json:"task_nr_sleeping"`
	Load                  Load           `json:"load"`
	Cycles                uint64         `json:"cycles"`
	Instructions          uint64         `json:"instructions"`
	L3Misses              uint64         `json:"l3_misses"`
	OcrReadDrams          uint64         `json:"ocr_read_drams"`
	StoreIns              uint64         `json:"store_ins"`
	StoreAllIns           uint64         `json:"store_all_ins"`
	ImcWrites             uint64         `json:"imc_writes"`
	UpdateTime            int64          `json:"update_time"`
}

type MemoryCgDataV1 struct {
	FullPath                  string        `json:"full_path"`
	MemoryLimitInBytes        uint64        `json:"memory_limit_in_bytes"`
	MemoryUsageInBytes        uint64        `json:"memory_usage_in_bytes"`
	KernMemoryUsageInBytes    uint64        `json:"kern_memory_usage_in_bytes"`
	KernMemoryTcpLimitInBytes uint64        `json:"kern_tcp_memory_limit_in_bytes"`
	KernMemoryTcpUsageInBytes uint64        `json:"kern_tcp_memory_usage_in_bytes"`
	Cache                     uint64        `json:"cache"`
	Rss                       uint64        `json:"rss"`
	Shmem                     uint64        `json:"shmem"`
	Dirty                     uint64        `json:"dirty"`
	KswapdSteal               uint64        `json:"kswapd_steal"`
	Writeback                 uint64        `json:"writeback"`
	Pgfault                   uint64        `json:"pgfault"`
	Pgmajfault                uint64        `json:"pgmajfault"`
	Allocstall                uint64        `json:"allocstall"`
	TotalCache                uint64        `json:"total_cache"`
	TotalRss                  uint64        `json:"total_rss"`
	TotalShmem                uint64        `json:"total_shmem"`
	TotalDirty                uint64        `json:"total_dirty"`
	TotalKswapdSteal          uint64        `json:"total_kswapd_steal"`
	TotalWriteback            uint64        `json:"total_writeback"`
	TotalPgfault              uint64        `json:"total_pgfault"`
	TotalPgmajfault           uint64        `json:"total_pgmajfault"`
	TotalAllocstall           uint64        `json:"total_allocstall"`
	WatermarkScaleFactor      interface{}   `json:"watermark_scale_factor"`
	BpfMemStat                BpfMemData    `json:"bpf_mem_data"`
	NumaStats                 []NumaStatsV1 `json:"numa_stat"`
	UpdateTime                int64         `json:"update_time"`
}

type CPUSetCgDataV1 struct {
	FullPath   string `json:"full_path"`
	Mems       Mems   `json:"mems"`
	Cpus       Cpus   `json:"cpus"`
	UpdateTime int64  `json:"update_time"`
}

type BlkIOCgDataV1 struct {
	FullPath     string                     `json:"full_path"`
	UserPath     string                     `json:"user_path"`
	IopsDetails  map[string]DeviceIoDetails `json:"iops_details"`
	BpsDetails   map[string]DeviceIoDetails `json:"bps_details"`
	IopsTotal    uint64                     `json:"iops_total"`
	BpsTotal     uint64                     `json:"bps_total"`
	BpfFsData    BpfFsData                  `json:"bpf_fs_data"`
	OldBpfFsData BpfFsData                  `json:"old_bpf_fs_data"`
	BpfIOData    BpfIOData                  `json:"bpf_io_data"`
	UpdateTime   int64                      `json:"update_time"`
}

type NumaStatsV1 struct {
	NumaName                string `json:"numa_name"`
	Total                   uint64 `json:"total"`
	File                    uint64 `json:"file"`
	Anon                    uint64 `json:"anon"`
	Unevictable             uint64 `json:"unevictable"`
	HierarchicalTotal       uint64 `json:"hierarchical_total"`
	HierarchicalFile        uint64 `json:"hierarchical_file"`
	HierarchicalAnon        uint64 `json:"hierarchical_anon"`
	HierarchicalUnevictable uint64 `json:"hierarchical_unevictable"`
}

type CPUBasicInfoV1 struct {
	CPUUsage    uint64 `json:"cpu_usage"`
	CPUUserTime uint64 `json:"cpu_user_time"`
	CPUSysTime  uint64 `json:"cpu_sys_time"`
	UpdateTime  int64  `json:"update_time"`
}
