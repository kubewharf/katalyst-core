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

type MalachiteSystemDiskIoResponse struct {
	Status int              `json:"status"`
	Data   SystemDiskIoData `json:"data"`
}

type DiskIo struct {
	PrimaryDeviceID   int    `json:"primary_device_id"`
	SecondaryDeviceID int    `json:"secondary_device_id"`
	DeviceName        string `json:"device_name"`
	IoRead            uint64 `json:"io_read"`
	IoWrite           uint64 `json:"io_write"`
	IoBusy            uint64 `json:"io_busy"`
}

type SystemDiskIoData struct {
	DiskIo     []DiskIo `json:"disk_io"`
	UpdateTime int64    `json:"update_time"`
}

type MalachiteSystemNetworkResponse struct {
	Status int               `json:"status"`
	Data   SystemNetworkData `json:"data"`
}

type SystemNetworkData struct {
	NetworkCard []NetworkCard `json:"networkcard"`
	TCP         TCP           `json:"tcp"`
	UpdateTime  int64         `json:"update_time"`
}

type NetworkCard struct {
	Name               string `json:"name"`
	ReceiveBytes       uint64 `json:"receive_bytes"`
	ReceivePackets     uint64 `json:"receive_packets"`
	ReceiveErrs        uint64 `json:"receive_errs"`
	ReceiveDrop        uint64 `json:"receive_drop"`
	ReceiveFifo        uint64 `json:"receive_fifo"`
	ReceiveFrame       uint64 `json:"receive_frame"`
	ReceiveCompressed  uint64 `json:"receive_compressed"`
	ReceiveMulticast   uint64 `json:"receive_multicast"`
	TransmitBytes      uint64 `json:"transmit_bytes"`
	TransmitPackets    uint64 `json:"transmit_packets"`
	TransmitErrs       uint64 `json:"transmit_errs"`
	TransmitDrop       uint64 `json:"transmit_drop"`
	TransmitFifo       uint64 `json:"transmit_fifo"`
	TransmitColls      uint64 `json:"transmit_colls"`
	TransmitCarrier    uint64 `json:"transmit_carrier"`
	TransmitCompressed uint64 `json:"transmit_compressed"`
}

type TCP struct {
	TCPDelayAcks       uint64  `json:"tcp_delay_acks"`
	TCPListenOverflows uint64  `json:"tcp_listen_overflows"`
	TCPListenDrops     uint64  `json:"tcp_listen_drops"`
	TCPAbortOnMemory   uint64  `json:"tcp_abort_on_memory"`
	TCPReqQFullDrop    uint64  `json:"tcp_req_q_full_drop"`
	TCPRetran          float64 `json:"tcp_retran"`
	TCPRetransSegs     uint64  `json:"tcp_retrans_segs"`
	TCPOldRetransSegs  uint64  `json:"tcp_old_retrans_segs"`
	TCPOutSegs         uint64  `json:"tcp_out_segs"`
	TCPOldOutSegs      uint64  `json:"tcp_old_out_segs"`
	TCPCloseWait       uint64  `json:"tcp_close_wait"`
}

type MalachiteSystemComputeResponse struct {
	Status int               `json:"status"`
	Data   SystemComputeData `json:"data"`
}

type SystemComputeData struct {
	Load       Load  `json:"load"`
	CPU        []CPU `json:"cpu"`
	GlobalCPU  CPU   `json:"global_cpu"`
	UpdateTime int64 `json:"update_time"`
}

type Load struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

type CPU struct {
	Name           string   `json:"name"`
	CPUUsage       float64  `json:"cpu_usage"`
	CPUIowaitRatio float64  `json:"cpu_iowait_ratio"`
	CPUSchedWait   float64  `json:"cpu_sched_wait"`
	CpiData        *CpiData `json:"cpi_data"`
}

type CpiData struct {
	Cpi          float64 `json:"cpi"`
	Instructions float64 `json:"instructions"`
	Cycles       float64 `json:"cycles"`
	L3Misses     float64 `json:"l3_misses"`
	Utilization  float64 `json:"utilization"`
}

type MalachiteSystemMemoryResponse struct {
	Status int              `json:"status"`
	Data   SystemMemoryData `json:"data"`
}

type SystemMemoryData struct {
	System     System `json:"system"`
	Numa       []Numa `json:"numa"`
	UpdateTime int64  `json:"update_time"`
}

type System struct {
	MemTotal               uint64  `json:"mem_total"`
	MemFree                uint64  `json:"mem_free"`
	MemUsed                uint64  `json:"mem_used"`
	MemShm                 uint64  `json:"mem_shm"`
	MemAvailable           uint64  `json:"mem_available"`
	MemBuffers             uint64  `json:"mem_buffers"`
	MemPageCache           uint64  `json:"mem_page_cache"`
	MemSlabReclaimable     uint64  `json:"mem_slab_reclaimable"`
	MemDirtyPageCache      uint64  `json:"mem_dirty_page_cache"`
	MemWriteBackPageCache  uint64  `json:"mem_write_back_page_cache"`
	MemSwapTotal           uint64  `json:"mem_swap_total"`
	MemSwapFree            uint64  `json:"mem_swap_free"`
	MemUtil                float64 `json:"mem_util"`
	VMWatermarkScaleFactor uint64  `json:"vm_watermark_scale_factor"`
	VmstatPgstealKswapd    uint64  `json:"vmstat_pgsteal_kswapd"`
}

type CPUList struct {
	Meta  string `json:"meta"`
	Inner []int  `json:"inner"`
}

type Numa struct {
	ID                      int     `json:"id"`
	Path                    string  `json:"path"`
	CPUList                 CPUList `json:"cpu_list"`
	MemFree                 uint64  `json:"mem_free"`
	MemUsed                 uint64  `json:"mem_used"`
	MemTotal                uint64  `json:"mem_total"`
	MemShmem                uint64  `json:"mem_shmem"`
	MemAvailable            uint64  `json:"mem_available"`
	MemFilePages            uint64  `json:"mem_file_pages"`
	MemMaxBandwidthMB       float64 `json:"mem_mx_bandwidth_mb"`
	MemReadBandwidthMB      float64 `json:"mem_read_bandwidth_mb"`
	MemReadLatency          float64 `json:"mem_read_latency"`
	MemTheoryMaxBandwidthMB float64 `json:"mem_theory_mx_bandwidth_mb"`
	MemWriteBandwidthMB     float64 `json:"mem_write_bandwidth_mb"`
	MemWriteLatency         float64 `json:"mem_write_latency"`
}

type Some struct {
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`
	Total  float64 `json:"total"`
}

type Full struct {
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`
	Total  float64 `json:"total"`
}

type Pressure struct {
	Some Some `json:"some"`
	Full Full `json:"full"`
}
