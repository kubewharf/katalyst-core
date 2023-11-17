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

package borwein

import (
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
)

type BorweinConfiguration struct {
	BorweinParameters             map[string]*borweintypes.BorweinParameter
	NodeFeatureNames              []string
	ContainerFeatureNames         []string
	InferenceServiceSocketAbsPath string
}

func NewBorweinConfiguration() *BorweinConfiguration {
	return &BorweinConfiguration{
		BorweinParameters: map[string]*borweintypes.BorweinParameter{
			string(v1alpha1.TargetIndicatorNameCPUSchedWait): {
				AbnormalRatioThreshold: 0.12,
				OffsetMax:              250,
				OffsetMin:              -50,
				RampUpStep:             2,
				RampDownStep:           10,
				Version:                "default",
			},
		},
		NodeFeatureNames:      []string{"cpu_total", "specification", "vendor", "iobusy_online", "iobusy_offline", "mem_total", "mem_free", "mem_used", "mem_shm", "mem_available", "mem_buffers", "mem_page_cache", "mem_slab_reclaimable", "mem_dirty_page_cache", "mem_writeback_page_cache", "vm_watermark_scale_factor", "vm_stat_pgsteal_kswapd"},                                                                                                                                                                                                                                                                                                                                                                                                                                                    // todo: fill it with adaption table
		ContainerFeatureNames: []string{"psm", "paas_cluster", "physical_cluster", "deploy", "qos_level", "idc", "timestamp", "cpu_limit", "cpu_request", "cpu_usage", "cpu_user_usage", "cpu_sys_usage", "cfs_quota_us", "cfs_period_us", "cpu_shares", "cpu_nr_throttled", "cpu_nr_periods", "cpu_throttled_time", "cpu_load", "cpu_cpi", "cpu_instructions", "cpu_cycles", "cpu_l3_cache_miss", "memory_request", "mem_limit", "mem_usage", "mem_kern_usage", "mem_rss", "mem_cache", "mem_shmem", "mem_dirty", "mem_kswapd_steal", "mem_writeback", "mem_pgfault", "mem_pgmajfault", "mem_allocstall", "mem_oom_cnt", "mem_read_bandwidth", "mem_write_bandwidth", "vfs_read_iops", "vfs_write_iops", "vfs_read_bps", "vfs_write_bps", "net_send_bps", "net_recv_bps", "net_send_pps", "net_recv_pps"}, // todo: fill it with adaption table
	}
}
