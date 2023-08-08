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

import (
	"encoding/json"
)

type MalachiteCgroupResponse struct {
	Status int             `json:"status"`
	Data   CgroupDataInner `json:"data"`
}

type CgroupDataInner struct {
	MountPoint      string          `json:"mount_point"`
	UserPath        string          `json:"user_path"`
	CgroupType      string          `json:"cgroup_type"`
	SubSystemGroups json.RawMessage `json:"sub_system_groups"`
}

type MalachiteCgroupV1Info struct {
	Memory    *MemoryCgDataV1 `json:"memory"`
	Blkio     *BlkIOCgDataV1  `json:"blkio"`
	NetCls    *NetClsCgData   `json:"net_cls"`
	PerfEvent *PerfEventData  `json:"perf_event"`
	CpuSet    *CPUSetCgDataV1 `json:"cpuset"`
	Cpu       *CPUCgDataV1    `json:"cpu"`
}

type MalachiteCgroupV2Info struct {
	Memory    *MemoryCgDataV2 `json:"memory"`
	Blkio     *BlkIOCgDataV2  `json:"blkio"`
	NetCls    *NetClsCgData   `json:"net_cls"`
	PerfEvent *PerfEventData  `json:"perf_event"`
	CpuSet    *CPUSetCgDataV2 `json:"cpuset"`
	Cpu       *CPUCgDataV2    `json:"cpu"`
}

type MalachiteCgroupInfo struct {
	MountPoint string `json:"mount_point"`
	UserPath   string `json:"user_path"`
	CgroupType string `json:"cgroup_type"`
	V1         *MalachiteCgroupV1Info
	V2         *MalachiteCgroupV2Info
}

type SubSystemGroupsV1 struct {
	Memory    MemoryCg    `json:"memory"`
	Blkio     BlkioCg     `json:"blkio"`
	NetCls    NetClsCg    `json:"net_cls"`
	PerfEvent PerfEventCg `json:"perf_event"`
	Cpuset    CpusetCg    `json:"cpuset"`
	Cpuacct   CpuacctCg   `json:"cpuacct"`
}

type MemoryCg struct {
	V1 struct {
		MemoryV1Data MemoryCgDataV1 `json:"V1"`
	} `json:"Memory"`
}

type BlkioCg struct {
	V1 struct {
		BlkIOData BlkIOCgDataV1 `json:"V1"`
	} `json:"BlkIO"`
}

type CpusetCg struct {
	V1 struct {
		CPUSetData CPUSetCgDataV1 `json:"V1"`
	} `json:"CpuSet"`
}

type CpuacctCg struct {
	V1 struct {
		CPUData CPUCgDataV1 `json:"V1"`
	} `json:"Cpu"`
}

type NetClsCg struct {
	NetData NetClsCgData `json:"Net"`
}

type PerfEventCg struct {
	PerfEventData PerfEventData `json:"PerfEvent"`
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
	WatermarkScaleFactor      *uint         `json:"watermark_scale_factor"`
	OomCnt                    int           `json:"oom_cnt"`
	NumaStats                 []NumaStatsV1 `json:"numa_stat"`
	UpdateTime                int64         `json:"update_time"`
}

type NumaStatsV1 struct {
	NumaName                string `json:"numa_name"`
	Total                   int    `json:"total"`
	File                    int    `json:"file"`
	Anon                    int    `json:"anon"`
	Unevictable             int    `json:"unevictable"`
	HierarchicalTotal       int    `json:"hierarchical_total"`
	HierarchicalFile        int    `json:"hierarchical_file"`
	HierarchicalAnon        int    `json:"hierarchical_anon"`
	HierarchicalUnevictable int    `json:"hierarchical_unevictable"`
}

type DeviceIoDetails struct {
	Read  uint64 `json:"Read"`
	Write uint64 `json:"Write"`
	Sync  uint64 `json:"Sync"`
	Async uint64 `json:"Async"`
	Total uint64 `json:"Total"`
}

type BpfFsData struct {
	FsCreated    uint64 `json:"fs_created"`
	FsOpen       uint64 `json:"fs_open"`
	FsRead       uint64 `json:"fs_read"`
	FsReadBytes  uint64 `json:"fs_read_bytes"`
	FsWrite      uint64 `json:"fs_write"`
	FsWriteBytes uint64 `json:"fs_write_bytes"`
	FsFsync      uint64 `json:"fs_fsync"`
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
	UpdateTime   int64                      `json:"update_time"`
}

type BpfNetData struct {
	NetRx      uint64 `json:"net_rx"`
	NetRxBytes uint64 `json:"net_rx_bytes"`
	NetTx      uint64 `json:"net_tx"`
	NetTxBytes uint64 `json:"net_tx_bytes"`
}

type NetClsCgData struct {
	FullPath      string     `json:"full_path"`
	UserPath      string     `json:"user_path"`
	BpfNetData    BpfNetData `json:"bpf_net_data"`
	OldBpfNetData BpfNetData `json:"old_bpf_net_data"`
	UpdateTime    int64      `json:"update_time"`
}

type PerfEventData struct {
	UserPath           string  `json:"user_path"`
	FullPath           string  `json:"full_path"`
	Cpi                float64 `json:"cpi"`
	Cycles             float64 `json:"cycles"`
	Instructions       float64 `json:"instructions"`
	IcacheMiss         float64 `json:"icache_miss"`
	L2CacheMiss        float64 `json:"l2_cache_miss"`
	L3CacheMiss        float64 `json:"l3_cache_miss"`
	PhyCoreUtilization float64 `json:"utilization"`
	UpdateTime         int64   `json:"update_time"`
}

type Mems struct {
	Meta  string `json:"meta"`
	Inner []int  `json:"inner"`
}

type Cpus struct {
	Meta  string `json:"meta"`
	Inner []int  `json:"inner"`
}

type CPUSetCgDataV1 struct {
	FullPath   string `json:"full_path"`
	Mems       Mems   `json:"mems"`
	Cpus       Cpus   `json:"cpus"`
	UpdateTime int64  `json:"update_time"`
}

type CPUBasicInfo struct {
	CPUUsage    uint64 `json:"cpu_usage"`
	CPUUserTime uint64 `json:"cpu_user_time"`
	CPUSysTime  uint64 `json:"cpu_sys_time"`
	UpdateTime  uint64 `json:"update_time"`
}

// CPUCgDataV1
// for legacy reasons, those all represent cores rather than ratio, we need to transform in collector,
// and it will take effect for metric including: cpu_usage_ratio, cpu_user_usage_ratio, cpu_sys_usage_ratio
type CPUCgDataV1 struct {
	FullPath              string       `json:"full_path"`
	CfsQuotaUs            int64        `json:"cfs_quota_us"`
	CfsPeriodUs           int64        `json:"cfs_period_us"`
	CPUShares             uint64       `json:"cpu_shares"`
	OldCPUBasicInfo       CPUBasicInfo `json:"old_cpu_basic_info"`
	NewCPUBasicInfo       CPUBasicInfo `json:"new_cpu_basic_info"`
	CPUUsageRatio         float64      `json:"cpu_usage_ratio"`
	CPUUserUsageRatio     float64      `json:"cpu_user_usage_ratio"`
	CPUSysUsageRatio      float64      `json:"cpu_sys_usage_ratio"`
	CPUNrThrottled        uint64       `json:"cpu_nr_throttled"`
	CPUNrPeriods          uint64       `json:"cpu_nr_periods"`
	CPUThrottledTime      uint64       `json:"cpu_throttled_time"`
	TaskNrIoWait          uint64       `json:"task_nr_iowait"`
	TaskNrUninterruptible uint64       `json:"task_nr_unint"`
	TaskNrRunning         uint64       `json:"task_nr_runnable"`
	Load                  Load         `json:"load"`
	OCRReadDRAMs          uint64       `json:"ocr_read_drams"`
	IMCWrites             uint64       `json:"imc_writes"`
	StoreAllInstructions  uint64       `json:"store_all_ins"`
	StoreInstructions     uint64       `json:"store_ins"`
	UpdateTime            int64        `json:"update_time"`
	Cycles                uint64       `json:"cycles"`
	Instructions          uint64       `json:"instructions"`
}

type SubSystemGroupsV2 struct {
	Memory    MemoryCgV2  `json:"memory"`
	Blkio     BlkioCgV2   `json:"blkio"`
	NetCls    NetClsCg    `json:"net_cls"`
	PerfEvent PerfEventCg `json:"perf_event"`
	Cpuset    CpusetCgV2  `json:"cpuset"`
	Cpuacct   CpuacctCgV2 `json:"cpuacct"`
}

type MemoryCgV2 struct {
	V2 struct {
		MemoryData MemoryCgDataV2 `json:"V2"`
	} `json:"Memory"`
}

type BlkioCgV2 struct {
	V2 struct {
		BlkIOData BlkIOCgDataV2 `json:"V2"`
	} `json:"BlkIO"`
}

type CpusetCgV2 struct {
	V2 struct {
		CPUSetData CPUSetCgDataV2 `json:"V2"`
	} `json:"CpuSet"`
}

type CpuacctCgV2 struct {
	V2 struct {
		CPUData CPUCgDataV2 `json:"V2"`
	} `json:"Cpu"`
}

type MemoryCgDataV2 struct {
	FullPath             string                 `json:"full_path"`
	UserPath             string                 `json:"user_path"`
	MemStats             MemStats               `json:"mem_stats"`
	MemNumaStats         map[string]NumaStatsV2 `json:"mem_numa_stats"`
	MemPressure          Pressure               `json:"mem_pressure"`
	MemLocalEvents       MemLocalEvents         `json:"mem_local_events"`
	Max                  uint64                 `json:"max"`  //18446744073709551615(u64_max) means unlimited
	High                 uint64                 `json:"high"` //18446744073709551615(u64_max) means unlimited
	Low                  uint64                 `json:"low"`
	Min                  uint64                 `json:"min"`
	SwapMax              uint64                 `json:"swap_max"` //18446744073709551615(u64_max) means unlimited
	WatermarkScaleFactor *uint64                `json:"watermark_scale_factor"`
	OomCnt               uint64                 `json:"oom_cnt"`
	MemoryUsageInBytes   uint64                 `json:"memory_usage_in_bytes"`
	UpdateTime           int64                  `json:"update_time"`
}

type BlkIOCgDataV2 struct {
	FullPath     string                     `json:"full_path"`
	UserPath     string                     `json:"user_path"`
	IoStat       map[string]DeviceIoDetails `json:"io_stat"`
	IoMax        map[string]uint64          `json:"io_max"`
	IoPressure   Pressure                   `json:"io_pressure"`
	IoLatency    map[string]uint64          `json:"io_latency"`
	IoWeight     map[string]uint64          `json:"io_weight"`
	BpfFsData    BpfFsData                  `json:"bpf_fs_data"`
	OldBpfFsData BpfFsData                  `json:"old_bpf_fs_data"`
	BpfIoLatency BpfIoLatency               `json:"bpf_io_latency"`
	UpdateTime   int64                      `json:"update_time"`
}

// CPUCgDataV2
// for legacy reasons, those all represent cores rather than ratio, we need to transform in collector,
// and it will take effect for metric including: cpu_usage_ratio, cpu_user_usage_ratio, cpu_sys_usage_ratio
type CPUCgDataV2 struct {
	FullPath              string   `json:"full_path"`
	CPUStats              CPUStats `json:"cpu_stats"`
	CPUPressure           Pressure `json:"cpu_pressure"`
	Weight                int      `json:"weight"`
	WeightNice            int      `json:"weight_nice"`
	MaxBurst              int      `json:"max_burst"`
	Max                   uint64   `json:"max"` // 18446744073709551615(u64_max) means unlimited
	MaxPeriod             int64    `json:"max_period"`
	CPUUsageRatio         float64  `json:"cpu_usage_ratio"`
	CPUUserUsageRatio     float64  `json:"cpu_user_usage_ratio"`
	CPUSysUsageRatio      float64  `json:"cpu_sys_usage_ratio"`
	TaskNrIoWait          uint64   `json:"task_nr_iowait"`
	TaskNrUninterruptible uint64   `json:"task_nr_unint"`
	TaskNrRunning         uint64   `json:"task_nr_runnable"`
	Load                  Load     `json:"load"`
	OCRReadDRAMs          uint64   `json:"ocr_read_drams"`
	IMCWrites             uint64   `json:"imc_writes"`
	StoreAllInstructions  uint64   `json:"store_all_ins"`
	StoreInstructions     uint64   `json:"store_ins"`
	UpdateTime            int64    `json:"update_time"`
	Cycles                uint64   `json:"cycles"`
	Instructions          uint64   `json:"instructions"`
}

type CPUSetCgDataV2 struct {
	FullPath   string `json:"full_path"`
	Mems       Mems   `json:"mems"`
	Cpus       Cpus   `json:"cpus"`
	UpdateTime int64  `json:"update_time"`
}

type MemStats struct {
	Anon                  uint64 `json:"anon"`
	File                  uint64 `json:"file"`
	KernelStack           uint64 `json:"kernel_stack"`
	Sock                  uint64 `json:"sock"`
	Shmem                 uint64 `json:"shmem"`
	FileMapped            uint64 `json:"file_mapped"`
	FileDirty             uint64 `json:"file_dirty"`
	FileWriteback         uint64 `json:"file_writeback"`
	AnonThp               uint64 `json:"anon_thp"`
	InactiveAnon          uint64 `json:"inactive_anon"`
	ActiveAnon            uint64 `json:"active_anon"`
	InactiveFile          uint64 `json:"inactive_file"`
	ActiveFile            uint64 `json:"active_file"`
	Unevictable           uint64 `json:"unevictable"`
	SlabReclaimable       uint64 `json:"slab_reclaimable"`
	SlabUnreclaimable     uint64 `json:"slab_unreclaimable"`
	Slab                  uint64 `json:"slab"`
	BgdReclaim            uint64 `json:"bgd_reclaim"`
	WorkingsetRefault     uint64 `json:"workingset_refault"`
	WorkingsetActivate    uint64 `json:"workingset_activate"`
	WorkingsetNodereclaim uint64 `json:"workingset_nodereclaim"`
	Pgfault               uint64 `json:"pgfault"`
	Pgmajfault            uint64 `json:"pgmajfault"`
	Pgrefill              uint64 `json:"pgrefill"`
	Pgscan                uint64 `json:"pgscan"`
	Pgsteal               uint64 `json:"pgsteal"`
	Pgactivate            uint64 `json:"pgactivate"`
	Pgdeactivate          uint64 `json:"pgdeactivate"`
	Pglazyfree            uint64 `json:"pglazyfree"`
	Pglazyfreed           uint64 `json:"pglazyfreed"`
	ThpFaultAlloc         uint64 `json:"thp_fault_alloc"`
	ThpCollapseAlloc      uint64 `json:"thp_collapse_alloc"`
}

type NumaStatsV2 struct {
	Anon                  uint64 `json:"anon"`
	File                  uint64 `json:"file"`
	KernelStack           uint64 `json:"kernel_stack"`
	Shmem                 uint64 `json:"shmem"`
	FileMapped            uint64 `json:"file_mapped"`
	FileDirty             uint64 `json:"file_dirty"`
	FileWriteback         uint64 `json:"file_writeback"`
	AnonThp               uint64 `json:"anon_thp"`
	InactiveAnon          uint64 `json:"inactive_anon"`
	ActiveAnon            uint64 `json:"active_anon"`
	InactiveFile          uint64 `json:"inactive_file"`
	ActiveFile            uint64 `json:"active_file"`
	Unevictable           uint64 `json:"unevictable"`
	SlabReclaimable       uint64 `json:"slab_reclaimable"`
	SlabUnreclaimable     uint64 `json:"slab_unreclaimable"`
	WorkingsetRefault     uint64 `json:"workingset_refault"`
	WorkingsetActivate    uint64 `json:"workingset_activate"`
	WorkingsetNodereclaim uint64 `json:"workingset_nodereclaim"`
}

type MemLocalEvents struct {
	Low     uint64 `json:"low"`
	High    uint64 `json:"high"`
	Max     uint64 `json:"max"`
	Oom     uint64 `json:"oom"`
	OomKill uint64 `json:"oom_kill"`
}

type CPUStats struct {
	UsageUsec   uint64 `json:"usage_usec"`
	UserUsec    uint64 `json:"user_usec"`
	SystemUsec  uint64 `json:"system_usec"`
	NrPeriods   uint64 `json:"nr_periods"`
	NrThrottled uint64 `json:"nr_throttled"`
}

type BpfIoLatency struct {
	Pcts          int           `json:"pcts"`
	SumLatency    SumLatency    `json:"sum_latency"`
	DriverLatency DriverLatency `json:"driver_latency"`
}

type SumLatency struct {
	ReadLatency    int `json:"read_latency"`
	WriteLatency   int `json:"write_latency"`
	DiscardLatency int `json:"discard_latency"`
}

type DriverLatency struct {
	ReadLatency    int `json:"read_latency"`
	WriteLatency   int `json:"write_latency"`
	DiscardLatency int `json:"discard_latency"`
}
