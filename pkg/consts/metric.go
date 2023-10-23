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

package consts

/*
 to clarity the detailed meaning for each metric and avoid ambiguity, we will have some appointments here
 - `usage` represents raw and absolute data, e.g. cpu cores
 - `ratio` represents percentage data, and will format in range [0,1], e.g. cpu ratio based on total requests
*/

// System compute metrics
const (
	MetricLoad1MinSystem  = "cpu.load.1min.system"
	MetricLoad5MinSystem  = "cpu.load.5min.system"
	MetricLoad15MinSystem = "cpu.load.15min.system"
)

// System memory metrics
const (
	MetricMemTotalSystem     = "mem.total.system"
	MetricMemUsedSystem      = "mem.used.system"
	MetricMemFreeSystem      = "mem.free.system"
	MetricMemShmemSystem     = "mem.shmem.system"
	MetricMemBufferSystem    = "mem.buffer.system"
	MetricMemPageCacheSystem = "mem.pagecache.system"
	MetricMemAvailableSystem = "mem.available.system"

	MetricMemDirtySystem       = "mem.dirty.system"
	MetricMemWritebackSystem   = "mem.writeback.system"
	MetricMemKswapdstealSystem = "mem.kswapdsteal.system"

	MetricMemSwapTotalSystem       = "mem.swap.total.system"
	MetricMemSwapFreeSystem        = "mem.swap.free.system"
	MetricMemSlabReclaimableSystem = "mem.slab.reclaimable.system"

	MetricMemScaleFactorSystem = "mem.scale.factor.system"
)

// System blkio metrics
const (
	MetricIOReadSystem  = "io.read.system"
	MetricIOWriteSystem = "io.write.system"
	MetricIOBusySystem  = "io.busy.system"

	MetricIOReadOpsSystem  = "io.read.ops.system"
	MetricIOWriteOpsSystem = "io.write.ops.system"
	MetricIOBusyRateSystem = "io.busy.rate.system"
)

// System numa metrics
const (
	MetricMemTotalNuma     = "mem.total.numa"
	MetricMemUsedNuma      = "mem.used.numa"
	MetricMemFreeNuma      = "mem.free.numa"
	MetricMemShmemNuma     = "mem.shmem.numa"
	MetricMemAvailableNuma = "mem.available.numa"
	MetricMemFilepageNuma  = "mem.filepage.numa"

	MetricMemBandwidthNuma       = "mem.bandwidth.numa"
	MetricMemBandwidthMaxNuma    = "mem.bandwidth.max.numa"
	MetricMemBandwidthTheoryNuma = "mem.bandwidth.theory.numa"
	MetricMemBandwidthReadNuma   = "mem.bandwidth.read.numa"
	MetricMemBandwidthWriteNuma  = "mem.bandwidth.write.numa"

	MetricMemLatencyReadNuma  = "mem.latency.read.numa"
	MetricMemLatencyWriteNuma = "mem.latency.write.numa"
)

// System cpu compute metrics
const (
	MetricCPUSchedwait   = "cpu.schedwait.cpu"
	MetricCPUUsageRatio  = "cpu.usage.ratio.cpu"
	MetricCPUIOWaitRatio = "cpu.iowait.ratio.cpu"
)

// container cpu metrics
const (
	MetricCPULimitContainer     = "cpu.limit.container"
	MetricCPUUsageContainer     = "cpu.usage.container"
	MetricCPUUsageUserContainer = "cpu.usage.user.container"
	MetricCPUUsageSysContainer  = "cpu.usage.sys.container"

	MetricCPUShareContainer           = "cpu.share.container"
	MetricCPUQuotaContainer           = "cpu.quota.container"
	MetricCPUPeriodContainer          = "cpu.period.container"
	MetricCPUNrThrottledContainer     = "cpu.nr.throttled.container"
	MetricCPUThrottledPeriodContainer = "cpu.throttled.period.container"
	MetricCPUThrottledTimeContainer   = "cpu.throttled.time.container"

	MetricCPUNrRunnableContainer        = "cpu.nr.runnable.container"
	MetricCPUNrUninterruptibleContainer = "cpu.nr.uninterruptible.container"
	MetricCPUNrIOWaitContainer          = "cpu.nr.iowait.container"

	MetricLoad1MinContainer  = "cpu.load.1min.container"
	MetricLoad5MinContainer  = "cpu.load.5min.container"
	MetricLoad15MinContainer = "cpu.load.15min.container"

	MetricOCRReadDRAMsContainer = "cpu.read.drams.container"
	MetricIMCWriteContainer     = "cpu.imc.write.container"
	MetricStoreAllInsContainer  = "cpu.store.allins.container"
	MetricStoreInsContainer     = "cpu.store.ins.container"

	MetricCPUUpdateTimeContainer = "cpu.updatetime.container"
)

// container memory metrics
const (
	MetricMemLimitContainer     = "mem.limit.container"
	MetricMemUsageContainer     = "mem.usage.container"
	MetricMemUsageUserContainer = "mem.usage.user.container"
	MetricMemUsageSysContainer  = "mem.usage.sys.container"
	MetricMemRssContainer       = "mem.rss.container"
	MetricMemCacheContainer     = "mem.cache.container"
	MetricMemShmemContainer     = "mem.shmem.container"

	MetricMemDirtyContainer       = "mem.dirty.container"
	MetricMemWritebackContainer   = "mem.writeback.container"
	MetricMemPgfaultContainer     = "mem.pgfault.container"
	MetricMemPgmajfaultContainer  = "mem.pgmajfault.container"
	MetricMemAllocstallContainer  = "mem.allocstall.container"
	MetricMemKswapdstealContainer = "mem.kswapdstall.container"

	MetricMemOomContainer         = "mem.oom.container"
	MetricMemScaleFactorContainer = "mem.scalefactor.container"

	MetricMemBandwidthReadContainer  = "mem.bandwidth.read.container"
	MetricMemBandwidthWriteContainer = "mem.bandwidth.write.container"
)

// container blkio metrics
const (
	MetricBlkioReadIopsContainer  = "blkio.read.iops.container"
	MetricBlkioWriteIopsContainer = "blkio.write.iops.container"
	MetricBlkioReadBpsContainer   = "blkio.read.bps.container"
	MetricBlkioWriteBpsContainer  = "blkio.write.bps.container"

	MetricBlkioUpdateTimeContainer = "blkio.updatetime.container"
)

// container net metrics
const (
	MetricNetTcpSendByteContainer = "net.tcp.send.byte.container"
	MetricNetTcpSendPpsContainer  = "net.tcp.send.pps.container"
	MetricNetTcpRecvByteContainer = "net.tcp.recv.byte.container"
	MetricNetTcpRecvPpsContainer  = "net.tcp.recv.pps.container"
)

// container perf metrics
const (
	MetricCPUCPIContainer          = "cpu.cpi.container"
	MetricCPUCyclesContainer       = "cpu.cycles.container"
	MetricCPUInstructionsContainer = "cpu.instructions.container"
	MetricCPUICacheMissContainer   = "cpu.icachemiss.container"
	MetricCPUL2CacheMissContainer  = "cpu.l2cachemiss.container"
	MetricCPUL3CacheMissContainer  = "cpu.l3cachemiss.container"
)

// container per numa metrics
const (
	MetricsMemTotalPerNumaContainer = "mem.total.numa.container"
	MetricsMemFilePerNumaContainer  = "mem.file.numa.container"
	MetricsMemAnonPerNumaContainer  = "mem.anon.numa.container"
)

// Cgroup cpu metrics
const (
	MetricCPULimitCgroup     = "cpu.limit.cgroup"
	MetricCPUUsageCgroup     = "cpu.usage.cgroup"
	MetricCPUUsageUserCgroup = "cpu.usage.user.cgroup"
	MetricCPUUsageSysCgroup  = "cpu.usage.sys.cgroup"

	MetricCPUShareCgroup           = "cpu.share.cgroup"
	MetricCPUQuotaCgroup           = "cpu.quota.cgroup"
	MetricCPUPeriodCgroup          = "cpu.period.cgroup"
	MetricCPUNrThrottledCgroup     = "cpu.nr.throttled.cgroup"
	MetricCPUThrottledPeriodCgroup = "cpu.throttled.period.cgroup"
	MetricCPUThrottledTimeCgroup   = "cpu.throttled.time.cgroup"

	MetricCPUNrRunnableCgroup        = "cpu.nr.runnable.cgroup"
	MetricCPUNrUninterruptibleCgroup = "cpu.nr.uninterruptible.cgroup"
	MetricCPUNrIOWaitCgroup          = "cpu.nr.iowait.cgroup"

	MetricLoad1MinCgroup  = "cpu.load.1min.cgroup"
	MetricLoad5MinCgroup  = "cpu.load.5min.cgroup"
	MetricLoad15MinCgroup = "cpu.load.15min.cgroup"

	MetricUpdateTimeCgroup = "cpu.updatetime.cgroup"
)

// Cgroup memory metrics
const (
	MetricMemLimitCgroup     = "mem.limit.cgroup"
	MetricMemUsageCgroup     = "mem.usage.cgroup"
	MetricMemUsageUserCgroup = "mem.usage.user.cgroup"
	MetricMemUsageSysCgroup  = "mem.usage.sys.cgroup"
	MetricMemRssCgroup       = "mem.rss.cgroup"
	MetricMemCacheCgroup     = "mem.cache.cgroup"
	MetricMemShmemCgroup     = "mem.shmem.cgroup"

	MetricMemDirtyCgroup       = "mem.dirty.cgroup"
	MetricMemWritebackCgroup   = "mem.writeback.cgroup"
	MetricMemPgfaultCgroup     = "mem.pgfault.cgroup"
	MetricMemPgmajfaultCgroup  = "mem.pgmajfault.cgroup"
	MetricMemAllocstallCgroup  = "mem.allocstall.cgroup"
	MetricMemKswapdstealCgroup = "mem.kswapdstall.cgroup"

	MetricMemOomCgroup         = "mem.oom.cgroup"
	MetricMemScaleFactorCgroup = "mem.scalefactor.cgroup"
)

// Cgroup blkio metrics
const (
	MetricBlkioReadIopsCgroup  = "blkio.read.iops.cgroup"
	MetricBlkioWriteIopsCgroup = "blkio.write.iops.cgroup"
	MetricBlkioReadBpsCgroup   = "blkio.read.bps.cgroup"
	MetricBlkioWriteBpsCgroup  = "blkio.write.bps.cgroup"
)

// Cgroup net metrics
const (
	MetricNetTcpSendByteCgroup = "net.tcp.send.byte.cgroup"
	MetricNetTcpSendPpsCgroup  = "net.tcp.send.pps.cgroup"
	MetricNetTcpRecvByteCgroup = "net.tcp.recv.byte.cgroup"
	MetricNetTcpRecvPpsCgroup  = "net.tcp.recv.pps.cgroup"
)

// Cgroup per numa metrics
const (
	MetricsMemTotalPerNumaCgroup = "mem.total.numa.cgroup"
	MetricsMemFilePerNumaCgroup  = "mem.file.numa.cgroup"
	MetricsMemAnonPerNumaCgroup  = "mem.anon.numa.cgroup"
)
