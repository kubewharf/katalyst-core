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

// TODO better to have malachite provide a official data struct
// and detailed meaning of fields

type MalachiteCgroupV2Info struct {
	Memory *MemoryCgDataV2 `json:"memory"`
	Blkio  *BlkIOCgDataV2  `json:"blkio"`
	NetCls *NetClsCgData   `json:"net_cls"`
	CpuSet *CPUSetCgDataV2 `json:"cpuset"`
	Cpu    *CPUCgDataV2    `json:"cpu"`
}

type SubSystemGroupsV2 struct {
	NetCls  NetClsCg    `json:"NetCls"`
	Blkio   BlkioCgV2   `json:"Blkio"`
	Cpuacct CpuacctCgV2 `json:"Cpuacct"`
	Cpuset  CpusetCgV2  `json:"Cpuset"`
	Memory  MemoryCgV2  `json:"Memory"`
}

type BlkioCgV2 struct {
	V2 struct {
		BlkIOData BlkIOCgDataV2 `json:"V2"`
	} `json:"BlkIO"`
}

type CpuacctCgV2 struct {
	V2 struct {
		CPUData CPUCgDataV2 `json:"V2"`
	} `json:"Cpu"`
}

type CpusetCgV2 struct {
	V2 struct {
		CPUSetData CPUSetCgDataV2 `json:"V2"`
	} `json:"CpuSet"`
}

type MemoryCgV2 struct {
	V2 struct {
		MemoryData MemoryCgDataV2 `json:"V2"`
	} `json:"Memory"`
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
	BpfIoLatency BpfIoLatencyV2             `json:"bpf_io_latency"`
	BpfIOData    BpfIOData                  `json:"bpf_io_data"`
	UpdateTime   int64                      `json:"update_time"`
}

type CPUCgDataV2 struct {
	FullPath              string   `json:"full_path"`
	CPUStats              CPUStats `json:"cpu_stats"`
	CPUPressure           Pressure `json:"cpu_pressure"`
	Weight                int      `json:"weight"`
	WeightNice            int      `json:"weight_nice"`
	MaxBurst              int      `json:"max_burst"`
	Max                   uint64   `json:"max"`
	MaxPeriod             int64    `json:"max_period"`
	CPUUsageRatio         float64  `json:"cpu_usage_ratio"`
	CPUUserUsageRatio     float64  `json:"cpu_user_usage_ratio"`
	CPUSysUsageRatio      float64  `json:"cpu_sys_usage_ratio"`
	TaskNrIoWait          uint64   `json:"task_nr_iowait"`
	TaskNrUninterruptible uint64   `json:"task_nr_unint"`
	TaskNrRunning         uint64   `json:"task_nr_runnable"`
	TaskNrSleeping        uint64   `json:"task_nr_sleeping"`
	Load                  Load     `json:"load"`
	Cycles                uint64   `json:"cycles"`
	Instructions          uint64   `json:"instructions"`
	L3Misses              uint64   `json:"l3_misses"`
	OcrReadDrams          uint64   `json:"ocr_read_drams"`
	StoreIns              uint64   `json:"store_ins"`
	StoreAllIns           uint64   `json:"store_all_ins"`
	ImcWrites             uint64   `json:"imc_writes"`
	UpdateTime            int64    `json:"update_time"`
}

type CPUSetCgDataV2 struct {
	FullPath   string `json:"full_path"`
	Mems       Mems   `json:"mems"`
	Cpus       Cpus   `json:"cpus"`
	UpdateTime int64  `json:"update_time"`
}

type MemoryCgDataV2 struct {
	FullPath             string                 `json:"full_path"`
	UserPath             string                 `json:"user_path"`
	MemStats             MemStatsV2             `json:"mem_stats"`
	MemNumaStats         map[string]NumaStatsV2 `json:"mem_numa_stats"`
	MemPressure          Pressure               `json:"mem_pressure"`
	MemLocalEvents       MemLocalEventsV2       `json:"mem_local_events"`
	MemoryUsageInBytes   uint64                 `json:"memory_usage_in_bytes"`
	Max                  uint64                 `json:"max"`
	High                 uint64                 `json:"high"`
	Low                  uint64                 `json:"low"`
	Min                  uint64                 `json:"min"`
	SwapMax              uint64                 `json:"swap_max"`
	WatermarkScaleFactor interface{}            `json:"watermark_scale_factor"`
	BpfMemStat           BpfMemData             `json:"bpf_mem_data"`
	UpdateTime           int64                  `json:"update_time"`
}

type MemStatsV2 struct {
	Anon                   uint64 `json:"anon"`
	File                   uint64 `json:"file"`
	Kernel                 uint64 `json:"kernel"`
	KernelStack            uint64 `json:"kernel_stack"`
	Sock                   uint64 `json:"sock"`
	Shmem                  uint64 `json:"shmem"`
	FileMapped             uint64 `json:"file_mapped"`
	FileDirty              uint64 `json:"file_dirty"`
	FileWriteback          uint64 `json:"file_writeback"`
	AnonThp                uint64 `json:"anon_thp"`
	InactiveAnon           uint64 `json:"inactive_anon"`
	ActiveAnon             uint64 `json:"active_anon"`
	InactiveFile           uint64 `json:"inactive_file"`
	ActiveFile             uint64 `json:"active_file"`
	Unevictable            uint64 `json:"unevictable"`
	SlabReclaimable        uint64 `json:"slab_reclaimable"`
	SlabUnreclaimable      uint64 `json:"slab_unreclaimable"`
	Slab                   uint64 `json:"slab"`
	BgdReclaim             uint64 `json:"bgd_reclaim"`
	WorkingsetRefault      uint64 `json:"workingset_refault"`
	WorkingsetRefaultAnon  uint64 `json:"workingset_refault_anon"`
	WorkingsetRefaultFile  uint64 `json:"workingset_refault_file"`
	WorkingsetActivate     uint64 `json:"workingset_activate"`
	WorkingsetActivateAnon uint64 `json:"workingset_activate_anon"`
	WorkingsetActivateFile uint64 `json:"workingset_activate_file"`
	WorkingsetRestoreAnon  uint64 `json:"workingset_restore_anon"`
	WorkingsetRestoreFile  uint64 `json:"workingset_restore_file"`
	WorkingsetNodereclaim  uint64 `json:"workingset_nodereclaim"`
	Pgfault                uint64 `json:"pgfault"`
	Pgmajfault             uint64 `json:"pgmajfault"`
	Pgrefill               uint64 `json:"pgrefill"`
	Pgscan                 uint64 `json:"pgscan"`
	Pgsteal                uint64 `json:"pgsteal"`
	PgscanKswapd           uint64 `json:"pgscan_kswapd"`
	PgscanDirect           uint64 `json:"pgscan_direct"`
	PgstealKswapd          uint64 `json:"pgsteal_kswapd"`
	PgstealDirect          uint64 `json:"pgsteal_direct"`
	Pgactivate             uint64 `json:"pgactivate"`
	Pgdeactivate           uint64 `json:"pgdeactivate"`
	Pglazyfree             uint64 `json:"pglazyfree"`
	Pglazyfreed            uint64 `json:"pglazyfreed"`
	ThpFaultAlloc          uint64 `json:"thp_fault_alloc"`
	ThpCollapseAlloc       uint64 `json:"thp_collapse_alloc"`
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

type MemLocalEventsV2 struct {
	Low     uint64 `json:"low"`
	High    uint64 `json:"high"`
	Max     uint64 `json:"max"`
	Oom     uint64 `json:"oom"`
	OomKill uint64 `json:"oom_kill"`
}

type BpfIoLatencyV2 struct {
	Pcts          int             `json:"pcts"`
	SumLatency    SumLatencyV2    `json:"sum_latency"`
	DriverLatency DriverLatencyV2 `json:"driver_latency"`
}

type SumLatencyV2 struct {
	ReadLatency    int `json:"read_latency"`
	WriteLatency   int `json:"write_latency"`
	DiscardLatency int `json:"discard_latency"`
}

type DriverLatencyV2 struct {
	ReadLatency    int `json:"read_latency"`
	WriteLatency   int `json:"write_latency"`
	DiscardLatency int `json:"discard_latency"`
}
