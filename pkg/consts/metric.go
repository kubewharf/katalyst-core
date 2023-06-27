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
	MetricCPUUsage       = "cpu.usage.cpu"
	MetricCPUSchedwait   = "cpu.schedwait.cpu"
	MetricCPUIOWaitRatio = "cpu.iowait.ratio.cpu"
)

// Cgroup cpu metrics
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

	MetricUpdateTimeContainer = "cpu.updatetime.container"
)

// Cgroup memory metrics
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

// Cgroup blkio metrics
const (
	MetricBlkioReadIopsContainer  = "blkio.read.iops.container"
	MetricBlkioWriteIopsContainer = "blkio.write.iops.container"
	MetricBlkioReadBpsContainer   = "blkio.read.bps.container"
	MetricBlkioWriteBpsContainer  = "blkio.write.bps.container"
)

// Cgroup net metrics
const (
	MetricNetTcpSendByteContainer = "net.tcp.send.byte.container"
	MetricNetTcpSendPpsContainer  = "net.tcp.send.pps.container"
	MetricNetTcpRecvByteContainer = "net.tcp.recv.byte.container"
	MetricNetTcpRecvPpsContainer  = "net.tcp.recv.pps.container"
)

// Cgroup perf metrics
const (
	MetricCPUCPIContainer          = "cpu.cpi.container"
	MetricCPUCyclesContainer       = "cpu.cycles.container"
	MetricCPUInstructionsContainer = "cpu.instructions.container"
	MetricCPUICacheMissContainer   = "cpu.icachemiss.container"
	MetricCPUL2CacheMissContainer  = "cpu.l2cachemiss.container"
	MetricCPUL3CacheMissContainer  = "cpu.l3cachemiss.container"
)

// Cgroup per numa metrics
const (
	MetricsMemTotalPerNumaContainer = "mem.total.numa.container"
	MetricsMemFilePerNumaContainer  = "mem.file.numa.container"
	MetricsMemAnonPerNumaContainer  = "mem.anon.numa.container"
)

// QoS class cpu metrics
const (
	MetricCPULimitQoSClass     = "cpu.limit.qosClass"
	MetricCPUUsageQoSClass     = "cpu.usage.qosClass"
	MetricCPUUsageUserQoSClass = "cpu.usage.user.qosClass"
	MetricCPUUsageSysQoSClass  = "cpu.usage.sys.qosClass"

	MetricCPUShareQoSClass           = "cpu.share.qosClass"
	MetricCPUQuotaQoSClass           = "cpu.quota.qosClass"
	MetricCPUPeriodQoSClass          = "cpu.period.qosClass"
	MetricCPUNrThrottledQoSClass     = "cpu.nr.throttled.qosClass"
	MetricCPUThrottledPeriodQoSClass = "cpu.throttled.period.qosClass"
	MetricCPUThrottledTimeQoSClass   = "cpu.throttled.time.qosClass"

	MetricCPUNrRunnableQoSClass        = "cpu.nr.runnable.qosClass"
	MetricCPUNrUninterruptibleQoSClass = "cpu.nr.uninterruptible.qosClass"
	MetricCPUNrIOWaitQoSClass          = "cpu.nr.iowait.qosClass"

	MetricLoad1MinQoSClass  = "cpu.load.1min.qosClass"
	MetricLoad5MinQoSClass  = "cpu.load.5min.qosClass"
	MetricLoad15MinQoSClass = "cpu.load.15min.qosClass"

	MetricUpdateTimeQoSClass = "cpu.updatetime.qosClass"
)

// Cgroup memory metrics
const (
	MetricMemLimitQoSClass     = "mem.limit.qosClass"
	MetricMemUsageQoSClass     = "mem.usage.qosClass"
	MetricMemUsageUserQoSClass = "mem.usage.user.qosClass"
	MetricMemUsageSysQoSClass  = "mem.usage.sys.qosClass"
	MetricMemRssQoSClass       = "mem.rss.qosClass"
	MetricMemCacheQoSClass     = "mem.cache.qosClass"
	MetricMemShmemQoSClass     = "mem.shmem.qosClass"

	MetricMemDirtyQoSClass       = "mem.dirty.qosClass"
	MetricMemWritebackQoSClass   = "mem.writeback.qosClass"
	MetricMemPgfaultQoSClass     = "mem.pgfault.qosClass"
	MetricMemPgmajfaultQoSClass  = "mem.pgmajfault.qosClass"
	MetricMemAllocstallQoSClass  = "mem.allocstall.qosClass"
	MetricMemKswapdstealQoSClass = "mem.kswapdstall.qosClass"

	MetricMemOomQoSClass         = "mem.oom.qosClass"
	MetricMemScaleFactorQoSClass = "mem.scalefactor.qosClass"
)

// Cgroup blkio metrics
const (
	MetricBlkioReadIopsQoSClass  = "blkio.read.iops.qosClass"
	MetricBlkioWriteIopsQoSClass = "blkio.write.iops.qosClass"
	MetricBlkioReadBpsQoSClass   = "blkio.read.bps.qosClass"
	MetricBlkioWriteBpsQoSClass  = "blkio.write.bps.qosClass"
)

// Cgroup net metrics
const (
	MetricNetTcpSendByteQoSClass = "net.tcp.send.byte.qosClass"
	MetricNetTcpSendPpsQoSClass  = "net.tcp.send.pps.qosClass"
	MetricNetTcpRecvByteQoSClass = "net.tcp.recv.byte.qosClass"
	MetricNetTcpRecvPpsQoSClass  = "net.tcp.recv.pps.qosClass"
)

// Cgroup per numa metrics
const (
	MetricsMemTotalPerNumaQoSClass = "mem.total.numa.qosClass"
	MetricsMemFilePerNumaQoSClass  = "mem.file.numa.qosClass"
	MetricsMemAnonPerNumaQoSClass  = "mem.anon.numa.qosClass"
)
