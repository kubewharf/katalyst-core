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

import "github.com/kubewharf/katalyst-core/pkg/consts"

const (
	RodanServerPort = 9102

	NodeMemoryPath       = "/node/memory"
	NodeCgroupMemoryPath = "/node/qosgroupmem"

	NumaMemoryPath = "/node/numastat"

	NodeCPUPath    = "/node/nodecpu"
	NodeSysctlPath = "/node/sysctl"

	ContainerCPUPath          = "/container/cgcpu"
	ContainerCgroupMemoryPath = "/container/cgmem"
	ContainerNumaStatPath     = "/container/cgnumastat"
	ContainerLoadPath         = "/container/loadavg"
	ContainerCghardwarePath   = "/container/cghardware"
)

// read only
var MetricsMap = map[string]map[string]string{
	NodeMemoryPath: {
		"memory_memtotal":       consts.MetricMemTotalSystem,
		"memory_memfree":        consts.MetricMemFreeSystem,
		"memory_memused":        consts.MetricMemUsedSystem,
		"memory_cached":         consts.MetricMemPageCacheSystem,
		"memory_buffers":        consts.MetricMemBufferSystem,
		"memory_pgsteal_kswapd": consts.MetricMemKswapdstealSystem,
	},
	NodeCgroupMemoryPath: {
		"qosgroupmem_besteffort_memory_rss":   consts.MetricMemRssCgroup,
		"qosgroupmem_besteffort_memory_usage": consts.MetricMemUsageCgroup,
		"qosgroupmem_burstable_memory_rss":    consts.MetricMemRssCgroup,
		"qosgroupmem_burstable_memory_usage":  consts.MetricMemUsageCgroup,
	},
	NumaMemoryPath: {
		"memtotal": consts.MetricMemTotalNuma,
		"memfree":  consts.MetricMemFreeNuma,
	},
	NodeCPUPath: {
		"usage":      consts.MetricCPUUsageRatio,
		"sched_wait": consts.MetricCPUSchedwait,
	},
	NodeSysctlPath: {
		"sysctl_vm_watermark_scale_factor": consts.MetricMemScaleFactorSystem,
	},
	ContainerCPUPath: {
		"cgcpu_usage": consts.MetricCPUUsageContainer,
	},
	ContainerCgroupMemoryPath: {
		"cgmem_total_shmem": consts.MetricMemShmemContainer,
		"cgmem_total_rss":   consts.MetricMemRssContainer,
		"cgmem_total_cache": consts.MetricMemCacheContainer,
	},
	ContainerLoadPath: {
		"loadavg_nrrunning": consts.MetricCPUNrRunnableContainer,
		"loadavg_loadavg1":  consts.MetricLoad1MinContainer,
		"loadavg_loadavg5":  consts.MetricLoad5MinContainer,
		"loadavg_loadavg15": consts.MetricLoad15MinContainer,
	},
	ContainerNumaStatPath: {
		"filepage": consts.MetricsMemFilePerNumaContainer,
	},
	ContainerCghardwarePath: {
		"cghardware_cycles":       consts.MetricCPUCyclesContainer,
		"cghardware_instructions": consts.MetricCPUInstructionsContainer,
	},
}
