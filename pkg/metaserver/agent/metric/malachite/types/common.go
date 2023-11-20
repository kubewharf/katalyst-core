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

import "encoding/json"

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

type MalachiteCgroupInfo struct {
	MountPoint string `json:"mount_point"`
	UserPath   string `json:"user_path"`
	CgroupType string `json:"cgroup_type"`
	V1         *MalachiteCgroupV1Info
	V2         *MalachiteCgroupV2Info
}

type NetClsCg struct {
	NetData NetClsCgData `json:"Net"`
}

type CPUStats struct {
	UsageUsec     uint64 `json:"usage_usec"`
	UserUsec      uint64 `json:"user_usec"`
	SystemUsec    uint64 `json:"system_usec"`
	NrPeriods     uint64 `json:"nr_periods"`
	NrThrottled   uint64 `json:"nr_throttled"`
	ThrottledUsec uint64 `json:"throttled_usec"`
}

type DeviceIoDetails struct {
	Data map[string]uint64
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

type BpfIOData struct {
	WaitSumTime    uint64 `json:"wait_sum_time"`
	WriteBackPages uint64 `json:"writeback_pages"`
}

type Mems struct {
	Meta  string `json:"meta"`
	Inner []int  `json:"inner"`
}

type Cpus struct {
	Meta  string `json:"meta"`
	Inner []int  `json:"inner"`
}

type NetClsCgData struct {
	FullPath      string     `json:"full_path"`
	UserPath      string     `json:"user_path"`
	BpfNetData    BpfNetData `json:"bpf_net_data"`
	OldBpfNetData BpfNetData `json:"old_bpf_net_data"`
	UpdateTime    int64      `json:"update_time"`
}

type BpfNetData struct {
	NetTCPRx      uint64 `json:"net_tcp_rx"`
	NetTCPRxBytes uint64 `json:"net_tcp_rx_bytes"`
	NetUDPRx      uint64 `json:"net_udp_rx"`
	NetUDPRxBytes uint64 `json:"net_udp_rx_bytes"`
	NetTCPTx      uint64 `json:"net_tcp_tx"`
	NetTCPTxBytes uint64 `json:"net_tcp_tx_bytes"`
	NetUDPTx      uint64 `json:"net_udp_tx"`
	NetUDPTxBytes uint64 `json:"net_udp_tx_bytes"`
}

type BpfMemData struct {
	OomCnt               uint64 `json:"mem_oom_cnt"`
	MemReclaimCnt        uint64 `json:"mem_reclaim_cnt"`
	MemReclaimTime       uint64 `json:"mem_reclaim_time"`
	MemCompactCnt        uint64 `json:"mem_compact_cnt"`
	MemCompactFailCnt    uint64 `json:"mem_compact_fail_cnt"`
	MemCompactSuccessCnt uint64 `json:"mem_compact_success_cnt"`
	MemCompactTime       uint64 `json:"mem_compact_time"`
	MemAllocCnt          uint64 `json:"mem_alloc_cnt"`
	MemAllocTime         uint64 `json:"mem_alloc_time"`
	MemBalanceDirty      uint64 `json:"mem_balance_dirty"`
}
