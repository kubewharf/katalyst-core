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

// metric type
const (
	Rate  = ".rate"
	Delta = ".delta"
)

// System compute metrics
const (
	MetricCPUUsageRatioSystem = "cpu.usage.ratio.system"

	MetricLoad1MinSystem  = "cpu.load.1min.system"
	MetricLoad5MinSystem  = "cpu.load.5min.system"
	MetricLoad15MinSystem = "cpu.load.15min.system"

	MetricProcsRunningSystem = "procs.running.system"

	MetricCPIAvgSystem = "cpi.avg.system"
)

// System memory metrics
const (
	MetricMemTotalSystem        = "mem.total.system"
	MetricMemUsedSystem         = "mem.used.system"
	MetricMemFreeSystem         = "mem.free.system"
	MetricMemShmemSystem        = "mem.shmem.system"
	MetricMemBufferSystem       = "mem.buffer.system"
	MetricMemPageCacheSystem    = "mem.pagecache.system"
	MetricMemAvailableSystem    = "mem.available.system"
	MetricMemActiveAnonSystem   = "mem.active.anon.system"
	MetricMemInactiveAnonSystem = "mem.inactive.anon.system"
	MetricMemActiveFileSystem   = "mem.active.file.system"
	MetricMemInactiveFileSystem = "mem.inactive.file.system"

	MetricMemDirtySystem            = "mem.dirty.system"
	MetricMemWritebackSystem        = "mem.writeback.system"
	MetricMemKswapdstealSystem      = "mem.kswapdsteal.system"
	MetricMemKswapdstealDeltaSystem = "mem.kswapdsteal.delta.system"

	MetricMemSwapTotalSystem       = "mem.swap.total.system"
	MetricMemSwapFreeSystem        = "mem.swap.free.system"
	MetricMemSlabReclaimableSystem = "mem.slab.reclaimable.system"

	MetricMemScaleFactorSystem = "mem.scale.factor.system"

	MetricMemUpdateTimeSystem = "mem.updatetime.system"

	MetricMemSockTCPSystem      = "mem.sock.tcp.system"
	MetricMemSockTCPLimitSystem = "mem.sock.tcp_limit.system"
	MetricMemSockUDPSystem      = "mem.sock.udp.system"
	MetricMemSockUDPLimitSystem = "mem.sock.udp_limit.system"
)

// System blkio metrics
const (
	MetricIOReadSystem  = "io.read.system"
	MetricIOWriteSystem = "io.write.system"
	MetricIOBusySystem  = "io.busy.system"

	MetricIOReadOpsSystem  = "io.read.ops.system"
	MetricIOWriteOpsSystem = "io.write.ops.system"
	MetricIOBusyRateSystem = "io.busy.rate.system"

	MetricIODiskType     = "io.disk.type"
	MetricIODiskWBTValue = "io.disk.wbt"
)

// System tcp metrics
const (
	MetricNetTcpDelayedAcks = "net.tcp.delay_acks"
	MetricNetTcpOverflows   = "net.tcp.listen_overflows"
	MetricNetTcpDrops       = "net.tcp.listen_drops"
	MetricNetTcpAbort       = "net.tcp.abort_on_memory"
	MetricNetTcpDrop        = "net.tcp.req_q_full_drop"
	MetricNetTcpRetran      = "net.tcp.retran"
	MetricNetTcpRetranSegs  = "net.tcp.retrans_segs"
	MetricNetTcpRecvPackets = "net.tcp.out_segs"
	MetricNetTcpCloseWait   = "net.tcp.close_wait"
)

// System network metrics
const (
	MetricNetReceiveBytes       = "net.tcp.receive_bytes"
	MetricNetReceivePackets     = "net.tcp.receive_packets"
	MetricNetReceiveErrs        = "net.tcp.receive_errs"
	MetricNetReceiveDrops       = "net.tcp.receive_drop"
	MetricNetReceiveFIFO        = "net.tcp.receive_fifo"
	MetricNetReceiveFrame       = "net.tcp.receive_frame"
	MetricNetReceiveCompressed  = "net.tcp.receive_compressed"
	MetricNetTransmitMulticast  = "net.tcp.receive_multicast"
	MetricNetTransmitBytes      = "net.tcp.transmit_bytes"
	MetricNetTransmitPackets    = "net.tcp.transmit_packets"
	MetricNetTransmitErrs       = "net.tcp.transmit_errs"
	MetricNetTransmitDrops      = "net.tcp.transmit_drop"
	MetricNetTransmitFIFO       = "net.tcp.transmit_fifo"
	MetricNetTransmitColls      = "net.tcp.transmit_colls"
	MetricNetTransmitCarrier    = "net.tcp.transmit_carrier"
	MetricNetTransmitCompressed = "net.tcp.transmit_compressed"
)

// Node filesystem metrics
const (
	MetricsNodeFsAvailable  = "available.fs.node"
	MetricsNodeFsCapacity   = "capacity.fs.node"
	MetricsNodeFsUsed       = "used.fs.node"
	MetricsNodeFsInodes     = "inodes.fs.node"
	MetricsNodeFsInodesFree = "free.inodes.fs.node"
	MetricsNodeFsInodesUsed = "used.inodes.fs.node"
)

// System Power metrics
const (
	MetricTotalPowerUsedWatts = "total.power.used.watts"
)

// Image filesystem metrics
const (
	MetricsImageFsAvailable  = "available.rootfs.system"
	MetricsImageFsCapacity   = "capacity.rootfs.system"
	MetricsImageFsUsed       = "used.rootfs.system"
	MetricsImageFsInodes     = "inodes.rootfs.system"
	MetricsImageFsInodesFree = "free.inodes.rootfs.system"
	MetricsImageFsInodesUsed = "used.inodes.rootfs.system"
)

// System numa metrics
const (
	MetricMemTotalNuma        = "mem.total.numa"
	MetricMemUsedNuma         = "mem.used.numa"
	MetricMemFreeNuma         = "mem.free.numa"
	MetricMemShmemNuma        = "mem.shmem.numa"
	MetricMemAvailableNuma    = "mem.available.numa"
	MetricMemFilepageNuma     = "mem.filepage.numa"
	MetricMemInactiveFileNuma = "mem.inactivefile.numa"

	MetricMemBandwidthNuma       = "mem.bandwidth.numa"
	MetricMemBandwidthMaxNuma    = "mem.bandwidth.max.numa"
	MetricMemBandwidthTheoryNuma = "mem.bandwidth.theory.numa"
	MetricMemBandwidthReadNuma   = "mem.bandwidth.read.numa"
	MetricMemBandwidthWriteNuma  = "mem.bandwidth.write.numa"

	MetricMemLatencyReadNuma      = "mem.latency.read.numa"
	MetricMemLatencyWriteNuma     = "mem.latency.write.numa"
	MetricMemAMDL3MissLatencyNuma = "mem.latency.amd.l3.miss"
	MetricMemFragScoreNuma        = "mem.frag.score.numa"

	MetricCPUUsageNuma = "cpu.usage.numa"
)

// System cpu compute metrics
const (
	MetricCPUSchedwait     = "cpu.schedwait.cpu"
	MetricCPUUsageRatio    = "cpu.usage.ratio.cpu"
	MetricCPUSysUsageRatio = "cpu.sys.usage.ratio.cpu"
	MetricCPUIOWaitRatio   = "cpu.iowait.ratio.cpu"
)

// container cpu metrics
const (
	MetricCPULimitContainer      = "cpu.limit.container"
	MetricCPUUsageContainer      = "cpu.usage.container"
	MetricCPUUsageUserContainer  = "cpu.usage.user.container"
	MetricCPUUsageSysContainer   = "cpu.usage.sys.container"
	MetricCPUUsageRatioContainer = "cpu.usage.ratio.container"

	MetricCPUShareContainer         = "cpu.share.container"
	MetricCPUQuotaContainer         = "cpu.quota.container"
	MetricCPUPeriodContainer        = "cpu.period.container"
	MetricCPUNrThrottledContainer   = "cpu.nr.throttled.container"
	MetricCPUNrPeriodContainer      = "cpu.nr.period.container"
	MetricCPUThrottledTimeContainer = "cpu.throttled.time.container"

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

	MetricCPUNrThrottledRateContainer   = MetricCPUNrThrottledContainer + Rate
	MetricCPUNrPeriodRateContainer      = MetricCPUNrPeriodContainer + Rate
	MetricCPUThrottledTimeRateContainer = MetricCPUThrottledTimeContainer + Rate

	MetricCPUUpdateTimeContainer = "cpu.updatetime.container"
)

// container memory metrics
const (
	MetricMemLimitContainer     = "mem.limit.container"
	MetricMemTCPLimitContainer  = "mem.tcp.limit.container"
	MetricMemUsageContainer     = "mem.usage.container"
	MetricMemUsageUserContainer = "mem.usage.user.container"
	MetricMemUsageKernContainer = "mem.usage.kern.container"
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

	MetricMemPgfaultRateContainer    = MetricMemPgfaultContainer + Rate
	MetricMemPgmajfaultRateContainer = MetricMemPgmajfaultContainer + Rate
	MetricMemOomRateContainer        = MetricMemOomContainer + Rate

	MetricMemUpdateTimeContainer         = "mem.updatetime.container"
	MetricMemPgstealContainer            = "mem.pgsteal.container"
	MetricMemPgscanContainer             = "mem.pgscan.container"
	MetricMemWorkingsetRefaultContainer  = "mem.workingsetrefault.container"
	MetricMemWorkingsetActivateContainer = "mem.workingsetactivate.container"
	MetricMemPsiAvg60Container           = "mem.psiavg60.container"
	MetricMemInactiveAnonContainer       = "mem.inactiveanon.container"
	MetricMemInactiveFileContainer       = "mem.inactivefile.container"
	MetricMemMappedContainer             = "mem.mapped.container"
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
	MetricNetTcpSendBytesContainer   = "net.tcp.send.bytes.container"
	MetricNetTcpSendPacketsContainer = "net.tcp.send.packets.container"
	MetricNetTcpRecvBytesContainer   = "net.tcp.recv.bytes.container"
	MetricNetTcpRecvPacketsContainer = "net.tcp.recv.packets.container"

	MetricNetTcpSendBPSContainer = "net.tcp.send.bps.container"
	MetricNetTcpSendPpsContainer = "net.tcp.send.pps.container"
	MetricNetTcpRecvBPSContainer = "net.tcp.recv.bps.container"
	MetricNetTcpRecvPpsContainer = "net.tcp.recv.pps.container"

	MetricNetworkUpdateTimeContainer = "net.updatetime.container"
)

// container perf metrics
const (
	MetricCPUCPIContainer          = "cpu.cpi.container"
	MetricCPUCyclesContainer       = "cpu.cycles.container"
	MetricCPUInstructionsContainer = "cpu.instructions.container"
	MetricCPUICacheMissContainer   = "cpu.icachemiss.container"
	MetricCPUL2CacheMissContainer  = "cpu.l2cachemiss.container"
	MetricCPUL3CacheMissContainer  = "cpu.l3cachemiss.container"

	MetricCPUCyclesRateContainer       = MetricCPUCyclesContainer + Rate
	MetricCPUInstructionsRateContainer = MetricCPUInstructionsContainer + Rate
	MetricCPUICacheMissRateContainer   = MetricCPUICacheMissContainer + Rate
	MetricCPUL2CacheMissRateContainer  = MetricCPUL2CacheMissContainer + Rate
	MetricCPUL3CacheMissRateContainer  = MetricCPUL3CacheMissContainer + Rate
)

// container per numa metrics
const (
	MetricsMemTotalPerNumaContainer   = "mem.total.numa.container"
	MetricsMemFilePerNumaContainer    = "mem.file.numa.container"
	MetricsMemAnonPerNumaContainer    = "mem.anon.numa.container"
	MetricsCPUUsageCountNUMAContainer = "cpu.usage.count.numa.container"
	MetricsCPUUsageNUMAContainer      = "cpu.usage.numa.container"
)

// container rootfs metrics
const (
	MetricsContainerRootfsAvailable  = "available.rootfs.container"
	MetricsContainerRootfsCapacity   = "capacity.rootfs.container"
	MetricsContainerRootfsUsed       = "used.rootfs.container"
	MetricsContainerRootfsInodes     = "inodes.rootfs.container"
	MetricsContainerRootfsInodesFree = "free.inodes.rootfs.container"
	MetricsContainerRootfsInodesUsed = "used.inodes.rootfs.container"
)

// container logs metrics
const (
	MetricsLogsAvailable  = "available.logs.container"
	MetricsLogsCapacity   = "capacity.logs.container"
	MetricsLogsInodes     = "inodes.logs.container"
	MetricsLogsInodesFree = "free.inodes.logs.container"
	MetricsLogsInodesUsed = "used.inodes.logs.container"
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

	MetricMemOomCgroup                = "mem.oom.cgroup"
	MetricMemScaleFactorCgroup        = "mem.scalefactor.cgroup"
	MetricMemPgstealCgroup            = "mem.pgsteal.cgroup"
	MetricMemPgscanCgroup             = "mem.pgscan.cgroup"
	MetricMemWorkingsetRefaultCgroup  = "mem.workingsetrefault.cgroup"
	MetricMemWorkingsetActivateCgroup = "mem.workingsetactivate.cgroup"
	MetricMemPsiAvg60Cgroup           = "mem.psiavg60.cgroup"
	MetricMemInactiveAnonCgroup       = "mem.inactiveanon.cgroup"
	MetricMemInactiveFileCgroup       = "mem.inactivefile.cgroup"
	MetricMemMappedCgroup             = "mem.mapped.cgroup"
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

// Pod volume metrics
const (
	MetricsPodVolumeAvailable  = "available.volume.pod.container"
	MetricsPodVolumeCapacity   = "capacity.volume.pod.container"
	MetricsPodVolumeUsed       = "used.volume.pod.container"
	MetricsPodVolumeInodes     = "inodes.volume.pod.container"
	MetricsPodVolumeInodesFree = "free.inodes.volume.pod.container"
	MetricsPodVolumeInodesUsed = "used.inodes.volume.pod.container"
)
