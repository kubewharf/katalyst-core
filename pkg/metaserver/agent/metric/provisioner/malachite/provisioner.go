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

package malachite

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/client"
	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	metricsNamMalachiteUnHealthy              = "malachite_unhealthy"
	metricsNameMalachiteGetSystemStatusFailed = "malachite_get_system_status_failed"
	metricsNameMalachiteGetPodStatusFailed    = "malachite_get_pod_status_failed"

	// Typically, katalyst's metric component does sampling per 10s.
	defaultMetricUpdateInterval = 10.0

	pageShift = 12

	malachiteProvisionerHealthCheckName = "malachite_provisioner_sample"
	malachiteProvisionTolerationTime    = 15 * time.Second
)

// NewMalachiteMetricsProvisioner returns the default implementation of MetricsFetcher.
func NewMalachiteMetricsProvisioner(baseConf *global.BaseConfiguration, _ *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter, fetcher pod.PodFetcher, metricStore *utilmetric.MetricStore,
) types.MetricsProvisioner {
	return &MalachiteMetricsProvisioner{
		malachiteClient: client.NewMalachiteClient(fetcher),
		metricStore:     metricStore,
		emitter:         emitter,
		baseConf:        baseConf,
	}
}

type MalachiteMetricsProvisioner struct {
	metricStore     *utilmetric.MetricStore
	malachiteClient *client.MalachiteClient
	baseConf        *global.BaseConfiguration
	emitter         metrics.MetricEmitter
	startOnce       sync.Once
	cpuToNumaMap    map[int]int
}

func (m *MalachiteMetricsProvisioner) Run(ctx context.Context) {
	m.startOnce.Do(func() {
		general.RegisterHeartbeatCheck(malachiteProvisionerHealthCheckName, malachiteProvisionTolerationTime,
			general.HealthzCheckStateNotReady, malachiteProvisionTolerationTime)
	})
	m.sample(ctx)
}

func (m *MalachiteMetricsProvisioner) sample(ctx context.Context) {
	klog.V(4).Infof("[malachite] heartbeat")

	if !m.checkMalachiteHealthy() {
		_ = general.UpdateHealthzState(malachiteProvisionerHealthCheckName, general.HealthzCheckStateNotReady, "malachite is not healthy")
		return
	}
	errList := make([]error, 0)

	// Update system data
	if err := m.updateSystemStats(); err != nil {
		errList = append(errList, err)
	}
	// Update pod data
	if err := m.updatePodsCgroupData(ctx); err != nil {
		errList = append(errList, err)
	}
	// Update top level cgroup of kubepods
	if err := m.updateCgroupData(); err != nil {
		errList = append(errList, err)
	}
	_ = general.UpdateHealthzStateByError(malachiteProvisionerHealthCheckName, errors.NewAggregate(errList))
}

// checkMalachiteHealthy is to check whether malachite is healthy
func (m *MalachiteMetricsProvisioner) checkMalachiteHealthy() bool {
	_, err := m.malachiteClient.GetSystemComputeStats()
	if err != nil {
		klog.Errorf("[malachite] malachite is unhealthy: %v", err)
		_ = m.emitter.StoreInt64(metricsNamMalachiteUnHealthy, 1, metrics.MetricTypeNameRaw)
		return false
	}

	return true
}

// Get raw system stats by malachite sdk and set to metricStore
func (m *MalachiteMetricsProvisioner) updateSystemStats() error {
	errList := make([]error, 0)
	systemComputeData, err := m.malachiteClient.GetSystemComputeStats()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[malachite] get system compute stats failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetSystemStatusFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "compute"})
	} else {
		m.processSystemComputeData(systemComputeData)
		m.processSystemCPUComputeData(systemComputeData)
	}

	systemMemoryData, err := m.malachiteClient.GetSystemMemoryStats()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[malachite] get system memory stats failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetSystemStatusFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "memory"})
	} else {
		m.processSystemMemoryData(systemMemoryData)
		m.processSystemNumaData(systemMemoryData, systemComputeData)
		m.processSystemExtFragData(systemMemoryData)
	}

	systemIOData, err := m.malachiteClient.GetSystemIOStats()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[malachite] get system io stats failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetSystemStatusFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "io"})
	} else {
		m.processSystemIOData(systemIOData)
	}

	systemNetData, err := m.malachiteClient.GetSystemNetStats()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[malachite] get system net stats failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetSystemStatusFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "net"})
	} else {
		m.processSystemNetData(systemNetData)
	}

	return errors.NewAggregate(errList)
}

func (m *MalachiteMetricsProvisioner) getCgroupPaths() []string {
	cgroupPaths := []string{m.baseConf.ReclaimRelativeRootCgroupPath, common.CgroupFsRootPathBurstable, common.CgroupFsRootPathBestEffort}
	for _, path := range m.baseConf.OptionalRelativeCgroupPaths {
		absPath := common.GetAbsCgroupPath(common.DefaultSelectedSubsys, path)
		if !general.IsPathExists(absPath) {
			general.Infof("cgroup path %v not existed, ignore it", path)
			continue
		}
		cgroupPaths = append(cgroupPaths, path)
	}
	for _, path := range m.baseConf.GeneralRelativeCgroupPaths {
		cgroupPaths = append(cgroupPaths, path)
	}

	dedupCgroupPaths := general.DedupStringSlice(cgroupPaths)
	return dedupCgroupPaths
}

func (m *MalachiteMetricsProvisioner) updateCgroupData() error {
	cgroupPaths := m.getCgroupPaths()
	errList := make([]error, 0)
	for _, path := range cgroupPaths {
		stats, err := m.malachiteClient.GetCgroupStats(path)
		if err != nil {
			errList = append(errList, err)
			general.Errorf("GetCgroupStats %v err %v", path, err)
			continue
		}
		m.processCgroupCPUData(path, stats)
		m.processCgroupMemoryData(path, stats)
		m.processCgroupBlkIOData(path, stats)
		m.processCgroupNetData(path, stats)
		m.processCgroupPerNumaMemoryData(path, stats)
	}

	return errors.NewAggregate(errList)
}

// Get raw cgroup data by malachite sdk and set container metrics to metricStore, GC not existed pod metrics
func (m *MalachiteMetricsProvisioner) updatePodsCgroupData(ctx context.Context) error {
	podsContainersStats, err := m.malachiteClient.GetAllPodContainersStats(ctx)
	if err != nil {
		klog.Errorf("[malachite] GetAllPodsContainersStats failed, error %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetPodStatusFailed, 1, metrics.MetricTypeNameCount)
	}

	podUIDSet := make(map[string]bool)
	for podUID, containerStats := range podsContainersStats {
		podUIDSet[podUID] = true
		for containerName, cgStats := range containerStats {
			m.processContainerCPUData(podUID, containerName, cgStats)
			m.processContainerMemoryData(podUID, containerName, cgStats)
			m.processContainerBlkIOData(podUID, containerName, cgStats)
			m.processContainerNetData(podUID, containerName, cgStats)
			m.processContainerPerfData(podUID, containerName, cgStats)
			m.processContainerPerNumaMemoryData(podUID, containerName, cgStats)
		}
	}
	m.metricStore.GCPodsMetric(podUIDSet)
	return err
}

func (m *MalachiteMetricsProvisioner) processSystemComputeData(systemComputeData *malachitetypes.SystemComputeData) {
	if systemComputeData == nil {
		return
	}
	// todo, currently we only get a unified data for the whole system compute data
	updateTime := time.Unix(systemComputeData.UpdateTime, 0)

	globalCPU := systemComputeData.GlobalCPU
	m.metricStore.SetNodeMetric(consts.MetricCPUUsageRatioSystem,
		utilmetric.MetricData{Value: globalCPU.CPUUsage / 100, Time: &updateTime})

	load := systemComputeData.Load
	m.metricStore.SetNodeMetric(consts.MetricLoad1MinSystem,
		utilmetric.MetricData{Value: load.One, Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricLoad5MinSystem,
		utilmetric.MetricData{Value: load.Five, Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricLoad15MinSystem,
		utilmetric.MetricData{Value: load.Fifteen, Time: &updateTime})

	procsRunning := systemComputeData.ProcessStats.ProcessRunning
	m.metricStore.SetNodeMetric(consts.MetricProcsRunningSystem,
		utilmetric.MetricData{Value: float64(procsRunning), Time: &updateTime})
}

func (m *MalachiteMetricsProvisioner) processSystemMemoryData(systemMemoryData *malachitetypes.SystemMemoryData) {
	if systemMemoryData == nil {
		return
	}
	// todo, currently we only get a unified data for the whole system memory data
	updateTime := time.Unix(systemMemoryData.UpdateTime, 0)

	mem := systemMemoryData.System

	// updating on previous status
	// TODO delta func
	prevMemKswapdStealMetric, _ := m.metricStore.GetNodeMetric(consts.MetricMemKswapdstealSystem)
	m.metricStore.SetNodeMetric(consts.MetricMemKswapdstealDeltaSystem,
		utilmetric.MetricData{Value: float64(mem.VmstatPgstealKswapd) - prevMemKswapdStealMetric.Value, Time: &updateTime})

	// updating current status
	m.metricStore.SetNodeMetric(consts.MetricMemTotalSystem,
		utilmetric.MetricData{Value: float64(mem.MemTotal << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemUsedSystem,
		utilmetric.MetricData{Value: float64(mem.MemUsed << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemFreeSystem,
		utilmetric.MetricData{Value: float64(mem.MemFree << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemShmemSystem,
		utilmetric.MetricData{Value: float64(mem.MemShm << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemBufferSystem,
		utilmetric.MetricData{Value: float64(mem.MemBuffers << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemPageCacheSystem,
		utilmetric.MetricData{Value: float64(mem.MemPageCache << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemAvailableSystem,
		utilmetric.MetricData{Value: float64(mem.MemAvailable << 10), Time: &updateTime})

	m.metricStore.SetNodeMetric(consts.MetricMemActiveAnonSystem,
		utilmetric.MetricData{Value: float64(mem.MemActiveAnon << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemInactiveAnonSystem,
		utilmetric.MetricData{Value: float64(mem.MemInactiveAnon << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemActiveFileSystem,
		utilmetric.MetricData{Value: float64(mem.MemActiveFile << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemInactiveFileSystem,
		utilmetric.MetricData{Value: float64(mem.MemInactiveFile << 10), Time: &updateTime})

	m.metricStore.SetNodeMetric(consts.MetricMemDirtySystem,
		utilmetric.MetricData{Value: float64(mem.MemDirtyPageCache << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemWritebackSystem,
		utilmetric.MetricData{Value: float64(mem.MemWriteBackPageCache << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemKswapdstealSystem,
		utilmetric.MetricData{Value: float64(mem.VmstatPgstealKswapd), Time: &updateTime})

	m.metricStore.SetNodeMetric(consts.MetricMemSwapTotalSystem,
		utilmetric.MetricData{Value: float64(mem.MemSwapTotal << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemSwapFreeSystem,
		utilmetric.MetricData{Value: float64(mem.MemSwapFree << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemSlabReclaimableSystem,
		utilmetric.MetricData{Value: float64(mem.MemSlabReclaimable << 10), Time: &updateTime})

	m.metricStore.SetNodeMetric(consts.MetricMemScaleFactorSystem,
		utilmetric.MetricData{Value: float64(mem.VMWatermarkScaleFactor), Time: &updateTime})

	m.metricStore.SetNodeMetric(consts.MetricMemSockTCPSystem,
		utilmetric.MetricData{Value: float64(mem.MemSockTcp), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemSockTCPLimitSystem,
		utilmetric.MetricData{Value: float64(mem.MemSockTcpLimit), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemSockUDPSystem,
		utilmetric.MetricData{Value: float64(mem.MemSockUdp), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemSockUDPLimitSystem,
		utilmetric.MetricData{Value: float64(mem.MemSockUdpLimit), Time: &updateTime})

	// timestamp
	m.metricStore.SetNodeMetric(consts.MetricMemUpdateTimeSystem,
		utilmetric.MetricData{Value: float64(systemMemoryData.UpdateTime), Time: &updateTime})
}

func (m *MalachiteMetricsProvisioner) processSystemIOData(systemIOData *malachitetypes.SystemDiskIoData) {
	if systemIOData == nil {
		return
	}
	// todo, currently we only get a unified data for the whole system io data
	updateTime := time.Unix(systemIOData.UpdateTime, 0)

	// calculate rate of the metric, and tell the caller if it's a valid value.
	ioStatFunc := func(deviceName, metricName string, value float64) (float64, bool) {
		prevData, err := m.metricStore.GetDeviceMetric(deviceName, metricName)
		if err != nil || prevData.Time == nil {
			return 0, false
		}

		timestampDeltaInMill := updateTime.UnixMilli() - prevData.Time.UnixMilli()
		if timestampDeltaInMill == 0 {
			return prevData.Value, false
		}

		return (value - prevData.Value) / float64(timestampDeltaInMill), true
	}

	setStatMetricIfValid := func(deviceName, rawMetricName, metricName string, value, scale float64) {
		ioStatData, isValid := ioStatFunc(deviceName, rawMetricName, value)
		if !isValid {
			return
		}
		m.metricStore.SetDeviceMetric(deviceName, metricName,
			utilmetric.MetricData{
				Value: ioStatData * scale,
				Time:  &updateTime,
			})
	}

	for _, device := range systemIOData.DiskIo {
		setStatMetricIfValid(device.DeviceName, consts.MetricIOReadSystem, consts.MetricIOReadOpsSystem, float64(device.IoRead), 1000.0)
		setStatMetricIfValid(device.DeviceName, consts.MetricIOWriteSystem, consts.MetricIOWriteOpsSystem, float64(device.IoWrite), 1000.0)
		setStatMetricIfValid(device.DeviceName, consts.MetricIOBusySystem, consts.MetricIOBusyRateSystem, float64(device.IoBusy), 1.0)

		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOReadSystem,
			utilmetric.MetricData{Value: float64(device.IoRead), Time: &updateTime})
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOWriteSystem,
			utilmetric.MetricData{Value: float64(device.IoWrite), Time: &updateTime})
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOBusySystem,
			utilmetric.MetricData{Value: float64(device.IoBusy), Time: &updateTime})

		diskType := consts.DiskTypeUnknown
		if device.DiskType == "HDD" {
			diskType = consts.DiskTypeHDD
		} else if device.DiskType == "SSD" {
			diskType = consts.DiskTypeSSD
		} else if device.DiskType == "NVME" {
			diskType = consts.DiskTypeNVME
		} else if device.DiskType == "VIRTIO" {
			diskType = consts.DiskTypeVIRTIO
		}
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIODiskType,
			utilmetric.MetricData{Value: float64(diskType), Time: &updateTime})
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIODiskWBTValue,
			utilmetric.MetricData{Value: float64(device.WBTValue), Time: &updateTime})
	}
}

func (m *MalachiteMetricsProvisioner) processSystemNetData(systemNetData *malachitetypes.SystemNetworkData) {
	if systemNetData == nil {
		return
	}
	// todo, currently we only get a unified data for the whole system io data
	updateTime := time.Unix(systemNetData.UpdateTime, 0)

	m.metricStore.SetNodeMetric(consts.MetricNetTcpDelayedAcks,
		utilmetric.MetricData{Value: float64(systemNetData.TCP.TCPDelayAcks), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricNetTcpOverflows,
		utilmetric.MetricData{Value: float64(systemNetData.TCP.TCPListenOverflows), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricNetTcpDrops,
		utilmetric.MetricData{Value: float64(systemNetData.TCP.TCPListenDrops), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricNetTcpAbort,
		utilmetric.MetricData{Value: float64(systemNetData.TCP.TCPAbortOnMemory), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricNetTcpDrop,
		utilmetric.MetricData{Value: float64(systemNetData.TCP.TCPReqQFullDrop), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricNetTcpRetran,
		utilmetric.MetricData{Value: systemNetData.TCP.TCPRetran, Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricNetTcpRetranSegs,
		utilmetric.MetricData{Value: float64(systemNetData.TCP.TCPRetransSegs), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricNetTcpRecvPackets,
		utilmetric.MetricData{Value: float64(systemNetData.TCP.TCPOutSegs), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricNetTcpCloseWait,
		utilmetric.MetricData{Value: float64(systemNetData.TCP.TCPCloseWait), Time: &updateTime})

	for _, device := range systemNetData.NetworkCard {
		// for now, we will only consider standard network interface
		// todo, may need to use configurations in the future to filter
		if !strings.HasPrefix(device.Name, "eth") {
			continue
		}

		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetReceiveBytes,
			utilmetric.MetricData{Value: float64(device.ReceiveBytes), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetReceivePackets,
			utilmetric.MetricData{Value: float64(device.ReceivePackets), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetReceiveErrs,
			utilmetric.MetricData{Value: float64(device.ReceiveErrs), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetReceiveDrops,
			utilmetric.MetricData{Value: float64(device.ReceiveDrop), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetReceiveFIFO,
			utilmetric.MetricData{Value: float64(device.ReceiveFifo), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetReceiveFrame,
			utilmetric.MetricData{Value: float64(device.ReceiveFrame), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetReceiveCompressed,
			utilmetric.MetricData{Value: float64(device.ReceiveCompressed), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitMulticast,
			utilmetric.MetricData{Value: float64(device.ReceiveMulticast), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitBytes,
			utilmetric.MetricData{Value: float64(device.TransmitBytes), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitPackets,
			utilmetric.MetricData{Value: float64(device.TransmitPackets), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitErrs,
			utilmetric.MetricData{Value: float64(device.TransmitErrs), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitDrops,
			utilmetric.MetricData{Value: float64(device.TransmitDrop), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitFIFO,
			utilmetric.MetricData{Value: float64(device.TransmitFifo), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitColls,
			utilmetric.MetricData{Value: float64(device.TransmitColls), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitCarrier,
			utilmetric.MetricData{Value: float64(device.TransmitCarrier), Time: &updateTime})
		m.metricStore.SetNetworkMetric(device.Name, consts.MetricNetTransmitCompressed,
			utilmetric.MetricData{Value: float64(device.TransmitCompressed), Time: &updateTime})

	}
}

func (m *MalachiteMetricsProvisioner) processSystemNumaData(systemMemoryData *malachitetypes.SystemMemoryData, systemComputeData *malachitetypes.SystemComputeData) {
	// todo, currently we only get a unified data for the whole system memory data
	updateTime := time.Unix(systemMemoryData.UpdateTime, 0)

	if m.cpuToNumaMap == nil {
		cpuToNuma := make(map[int]int)
		for _, numaInfo := range systemMemoryData.Numa {
			for _, cpuID := range numaInfo.CPUList.Inner {
				cpuToNuma[cpuID] = numaInfo.ID
			}
		}
		m.cpuToNumaMap = cpuToNuma
	}

	cpuToNuma := make(map[int]int)
	for _, numaInfo := range systemMemoryData.Numa {
		for _, cpuID := range numaInfo.CPUList.Inner {
			cpuToNuma[cpuID] = numaInfo.ID
		}
	}

	for _, numa := range systemMemoryData.Numa {
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemTotalNuma,
			utilmetric.MetricData{Value: float64(numa.MemTotal << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemUsedNuma,
			utilmetric.MetricData{Value: float64(numa.MemUsed << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemFreeNuma,
			utilmetric.MetricData{Value: float64(numa.MemFree << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemShmemNuma,
			utilmetric.MetricData{Value: float64(numa.MemShmem << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemAvailableNuma,
			utilmetric.MetricData{Value: float64(numa.MemAvailable << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemFilepageNuma,
			utilmetric.MetricData{Value: float64(numa.MemFilePages << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemInactiveFileNuma,
			utilmetric.MetricData{Value: float64(numa.MemInactiveFile << 10), Time: &updateTime})

		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthNuma,
			utilmetric.MetricData{Value: numa.MemReadBandwidthMB/1024.0 + numa.MemWriteBandwidthMB/1024.0, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthMaxNuma,
			utilmetric.MetricData{Value: numa.MemTheoryMaxBandwidthMB * 0.8 / 1024.0, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthTheoryNuma,
			utilmetric.MetricData{Value: numa.MemTheoryMaxBandwidthMB / 1024.0, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthReadNuma,
			utilmetric.MetricData{Value: numa.MemReadBandwidthMB / 1024.0, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthWriteNuma,
			utilmetric.MetricData{Value: numa.MemWriteBandwidthMB / 1024.0, Time: &updateTime})

		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemLatencyReadNuma,
			utilmetric.MetricData{Value: numa.MemReadLatency, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemLatencyWriteNuma,
			utilmetric.MetricData{Value: numa.MemWriteLatency, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemAMDL3MissLatencyNuma,
			utilmetric.MetricData{Value: numa.AMDL3MissLatencyMax, Time: &updateTime})
	}

	numaCPUUsage := make(map[int]float64)
	for _, cpuInfo := range systemComputeData.CPU {
		cpuID, err := strconv.Atoi(cpuInfo.Name[3:])
		if err != nil {
			klog.Errorf("[malachite] parse cpu name %v with err: %v", cpuInfo.Name, err)
			continue
		}
		numaCPUUsage[cpuToNuma[cpuID]] += cpuInfo.CPUUsage / 100
	}

	for numaID, usage := range numaCPUUsage {
		m.metricStore.SetNumaMetric(numaID, consts.MetricCPUUsageNuma,
			utilmetric.MetricData{Value: usage, Time: &updateTime})
	}
}

func (m *MalachiteMetricsProvisioner) processSystemExtFragData(systemMemoryData *malachitetypes.SystemMemoryData) {
	updateTime := time.Unix(systemMemoryData.UpdateTime, 0)

	for _, numa := range systemMemoryData.ExtFrag {
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemFragScoreNuma,
			utilmetric.MetricData{Value: float64(numa.MemFragScore), Time: &updateTime})
	}
}

func (m *MalachiteMetricsProvisioner) processSystemCPUComputeData(systemComputeData *malachitetypes.SystemComputeData) {
	// todo, currently we only get a unified data for the whole system compute data
	updateTime := time.Unix(systemComputeData.UpdateTime, 0)
	if len(systemComputeData.CPUCodeName) > 0 {
		m.metricStore.SetByStringIndex(consts.MetricCPUCodeName, systemComputeData.CPUCodeName)
	}
	var cpiTotal, cpiCount float64
	for _, cpu := range systemComputeData.CPU {
		cpuID, err := strconv.Atoi(cpu.Name[3:])
		if err != nil {
			klog.Errorf("[malachite] parse cpu name %v with err: %v", cpu.Name, err)
			continue
		}

		// todo it's kind of confusing but the `cpu-usage` in `system-level` actually represents `ratio`,
		//  we will always rename metric in local store to replenish `ratio` to avoid ambiguity.
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUUsageRatio,
			utilmetric.MetricData{Value: cpu.CPUUsage / 100.0, Time: &updateTime})
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUSchedwait,
			utilmetric.MetricData{Value: cpu.CPUSchedWait * 1000, Time: &updateTime})
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUIOWaitRatio,
			utilmetric.MetricData{Value: cpu.CPUIowaitRatio, Time: &updateTime})

		if cpu.CpiData != nil {
			cpiTotal += cpu.CpiData.Cpi
			cpiCount += 1
		}
	}
	m.metricStore.SetNodeMetric(consts.MetricCPUUsageRatio,
		utilmetric.MetricData{Value: systemComputeData.GlobalCPU.CPUUsage / 100.0, Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricCPUSysUsageRatio,
		utilmetric.MetricData{Value: systemComputeData.GlobalCPU.CPUSysUsage / 100.0, Time: &updateTime})

	if cpiCount > 0 {
		m.metricStore.SetNodeMetric(consts.MetricCPIAvgSystem,
			utilmetric.MetricData{Value: cpiTotal / cpiCount, Time: &updateTime})
	}
}

func (m *MalachiteMetricsProvisioner) processCgroupCPUData(cgroupPath string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		cpu := cgStats.V1.Cpu
		updateTime := time.Unix(cgStats.V1.Cpu.UpdateTime, 0)

		// todo it's kind of confusing but the `cpu-usage-ratio` in `cgroup-level` actually represents `actual cores`,
		//  we will always rename metric in local store to eliminate `ratio` to avoid ambiguity.
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPULimitCgroup, utilmetric.MetricData{Value: float64(cpu.CfsQuotaUs) / float64(cpu.CfsPeriodUs), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: cpu.CPUUsageRatio, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageUserCgroup, utilmetric.MetricData{Value: cpu.CPUUserUsageRatio, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageSysCgroup, utilmetric.MetricData{Value: cpu.CPUSysUsageRatio, Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUShareCgroup, utilmetric.MetricData{Value: float64(cpu.CPUShares), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: float64(cpu.CfsQuotaUs), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: float64(cpu.CfsPeriodUs), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrThrottledCgroup, utilmetric.MetricData{Value: float64(cpu.CPUNrThrottled), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUThrottledPeriodCgroup, utilmetric.MetricData{Value: float64(cpu.CPUNrPeriods), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUThrottledTimeCgroup, utilmetric.MetricData{Value: float64(cpu.CPUThrottledTime / 1000), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrRunnableCgroup, utilmetric.MetricData{Value: float64(cpu.TaskNrRunning), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrUninterruptibleCgroup, utilmetric.MetricData{Value: float64(cpu.TaskNrUninterruptible), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrIOWaitCgroup, utilmetric.MetricData{Value: float64(cpu.TaskNrIoWait), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad1MinCgroup, utilmetric.MetricData{Value: cpu.Load.One, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad5MinCgroup, utilmetric.MetricData{Value: cpu.Load.Five, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad15MinCgroup, utilmetric.MetricData{Value: cpu.Load.Fifteen, Time: &updateTime})

	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		cpu := cgStats.V2.Cpu
		updateTime := time.Unix(cgStats.V2.Cpu.UpdateTime, 0)

		// todo it's kind of confusing but the `cpu-usage-ratio` in `cgroup-level` actually represents `actual cores`,
		//  we will always rename metric in local store to eliminate `ratio` to avoid ambiguity.
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: cpu.CPUUsageRatio, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageUserCgroup, utilmetric.MetricData{Value: cpu.CPUUserUsageRatio, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageSysCgroup, utilmetric.MetricData{Value: cpu.CPUSysUsageRatio, Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrRunnableCgroup, utilmetric.MetricData{Value: float64(cpu.TaskNrRunning), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrUninterruptibleCgroup, utilmetric.MetricData{Value: float64(cpu.TaskNrUninterruptible), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrIOWaitCgroup, utilmetric.MetricData{Value: float64(cpu.TaskNrIoWait), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad1MinCgroup, utilmetric.MetricData{Value: cpu.Load.One, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad5MinCgroup, utilmetric.MetricData{Value: cpu.Load.Five, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad15MinCgroup, utilmetric.MetricData{Value: cpu.Load.Fifteen, Time: &updateTime})
	}
}

func (m *MalachiteMetricsProvisioner) processCgroupMemoryData(cgroupPath string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		mem := cgStats.V1.Memory
		updateTime := time.Unix(cgStats.V1.Memory.UpdateTime, 0)

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemLimitCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.MemoryLimitInBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemUsageCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.MemoryUsageInBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemUsageUserCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.MemoryLimitInBytes - mem.KernMemoryUsageInBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemUsageSysCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.KernMemoryUsageInBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemRssCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.TotalRss)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemCacheCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.TotalCache)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemShmemCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.TotalShmem)})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemDirtyCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.TotalDirty)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemWritebackCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.TotalWriteback)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgfaultCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.TotalPgfault)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgmajfaultCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.TotalPgmajfault)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemAllocstallCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.TotalAllocstall)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemKswapdstealCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.KswapdSteal)})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemOomCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(mem.BpfMemStat.OomCnt)})
		// m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemScaleFactorCgroup, utilmetric.MetricData{Time: &updateTime, Value: general.UIntPointerToFloat64(mem.WatermarkScaleFactor)})
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		mem := cgStats.V2.Memory
		updateTime := time.Unix(cgStats.V2.Memory.UpdateTime, 0)
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemUsageCgroup, utilmetric.MetricData{Value: float64(mem.MemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemRssCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.Anon), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemCacheCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.File), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemShmemCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.Shmem), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgfaultCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.Pgfault), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgmajfaultCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.Pgmajfault), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemOomCgroup, utilmetric.MetricData{Value: float64(mem.BpfMemStat.OomCnt), Time: &updateTime})
		// m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemScaleFactorCgroup, utilmetric.MetricData{Value: general.UInt64PointerToFloat64(mem.WatermarkScaleFactor), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgstealCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.Pgsteal), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgscanCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.Pgscan), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemWorkingsetRefaultCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.WorkingsetRefault), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemWorkingsetActivateCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.WorkingsetActivate), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPsiAvg60Cgroup, utilmetric.MetricData{Value: mem.MemPressure.Some.Avg60, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemInactiveAnonCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.InactiveAnon), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemInactiveFileCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.InactiveFile), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemMappedCgroup, utilmetric.MetricData{Value: float64(mem.MemStats.FileMapped), Time: &updateTime})
	}
}

func (m *MalachiteMetricsProvisioner) processCgroupBlkIOData(cgroupPath string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		updateTime := time.Unix(cgStats.V1.Blkio.UpdateTime, 0)

		io := cgStats.V1.Blkio
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioReadIopsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsRead - io.OldBpfFsData.FsRead)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioWriteIopsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsWrite - io.OldBpfFsData.FsWrite)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioReadBpsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsReadBytes - io.OldBpfFsData.FsReadBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioWriteBpsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsWriteBytes - io.OldBpfFsData.FsWriteBytes)})
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		io := cgStats.V2.Blkio
		updateTime := time.Unix(cgStats.V2.Blkio.UpdateTime, 0)

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioReadIopsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsRead - io.OldBpfFsData.FsRead)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioWriteIopsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsWrite - io.OldBpfFsData.FsWrite)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioReadBpsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsReadBytes - io.OldBpfFsData.FsReadBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioWriteBpsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsWriteBytes - io.OldBpfFsData.FsWriteBytes)})
	}
}

func (m *MalachiteMetricsProvisioner) processCgroupNetData(cgroupPath string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}
	updateTime := time.Now()

	var net *malachitetypes.NetClsCgData
	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		net = cgStats.V1.NetCls
		updateTime = time.Unix(cgStats.V1.NetCls.UpdateTime, 0)
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		net = cgStats.V2.NetCls
		updateTime = time.Unix(cgStats.V2.NetCls.UpdateTime, 0)
	}
	if net == nil {
		return
	}
	m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricNetTcpSendByteCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(net.BpfNetData.NetTCPTxBytes - net.OldBpfNetData.NetTCPTxBytes)})
	m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricNetTcpSendPpsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(net.BpfNetData.NetTCPTx - net.OldBpfNetData.NetTCPTx)})
	m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricNetTcpRecvByteCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(net.BpfNetData.NetTCPRxBytes - net.OldBpfNetData.NetTCPRxBytes)})
	m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricNetTcpRecvPpsCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(net.BpfNetData.NetTCPRx - net.OldBpfNetData.NetTCPRx)})
}

func (m *MalachiteMetricsProvisioner) processCgroupPerNumaMemoryData(cgroupPath string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		numaStats := cgStats.V1.Memory.NumaStats
		updateTime := time.Unix(cgStats.V1.Memory.UpdateTime, 0)

		for _, data := range numaStats {
			numaIDStr := strings.TrimPrefix(data.NumaName, "N")
			numaID, err := strconv.Atoi(numaIDStr)
			if err != nil {
				klog.ErrorS(err, "failed to parse numa", "str", numaIDStr)
				continue
			}
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemTotalPerNumaCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(data.HierarchicalTotal << pageShift)})
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemFilePerNumaCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(data.HierarchicalFile << pageShift)})
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemAnonPerNumaCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(data.HierarchicalAnon << pageShift)})
		}
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		numaStats := cgStats.V2.Memory.MemNumaStats
		updateTime := time.Unix(cgStats.V2.Memory.UpdateTime, 0)

		for numa, data := range numaStats {
			numaIDStr := strings.TrimPrefix(numa, "N")
			numaID, err := strconv.Atoi(numaIDStr)
			if err != nil {
				klog.ErrorS(err, "failed to parse numaIDStr", "str", numaIDStr)
				continue
			}
			total := data.Anon + data.File + data.Unevictable
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemTotalPerNumaCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(total)})
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemFilePerNumaCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(data.File)})
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemAnonPerNumaCgroup, utilmetric.MetricData{Time: &updateTime, Value: float64(data.Anon)})
		}
	}
}

func (m *MalachiteMetricsProvisioner) processContainerCPUData(podUID, containerName string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	var (
		metricLastUpdateTime, _ = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricCPUUpdateTimeContainer)
		cyclesOld, _            = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricCPUCyclesContainer)
		instructionsOld, _      = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricCPUInstructionsContainer)
	)

	m.processContainerMemBandwidth(podUID, containerName, cgStats, metricLastUpdateTime.Value)
	m.processContainerCPURelevantRate(podUID, containerName, cgStats, metricLastUpdateTime.Value)

	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		cpu := cgStats.V1.Cpu
		updateTime := time.Unix(cgStats.V1.Cpu.UpdateTime, 0)

		// todo it's kind of confusing but the `cpu-usage-ratio` in `cgroup-level` actually represents `actual cores`,
		//  we will always rename metric in local store to eliminate `ratio` to avoid ambiguity.
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPULimitContainer,
			utilmetric.MetricData{Value: float64(cpu.CfsQuotaUs) / float64(cpu.CfsPeriodUs), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer,
			utilmetric.MetricData{Value: cpu.CPUUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageUserContainer,
			utilmetric.MetricData{Value: cpu.CPUUserUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageSysContainer,
			utilmetric.MetricData{Value: cpu.CPUSysUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageRatioContainer,
			utilmetric.MetricData{Value: cpu.CPUUsageRatio / (float64(cpu.CfsQuotaUs) / float64(cpu.CfsPeriodUs)), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUShareContainer,
			utilmetric.MetricData{Value: float64(cpu.CPUShares), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUQuotaContainer,
			utilmetric.MetricData{Value: float64(cpu.CfsQuotaUs), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUPeriodContainer,
			utilmetric.MetricData{Value: float64(cpu.CfsPeriodUs), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrThrottledContainer,
			utilmetric.MetricData{Value: float64(cpu.CPUNrThrottled), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrPeriodContainer,
			utilmetric.MetricData{Value: float64(cpu.CPUNrPeriods), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUThrottledTimeContainer,
			utilmetric.MetricData{Value: float64(cpu.CPUThrottledTime / 1000), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrRunnableContainer,
			utilmetric.MetricData{Value: float64(cpu.TaskNrRunning), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrUninterruptibleContainer,
			utilmetric.MetricData{Value: float64(cpu.TaskNrUninterruptible), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrIOWaitContainer,
			utilmetric.MetricData{Value: float64(cpu.TaskNrIoWait), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad1MinContainer,
			utilmetric.MetricData{Value: cpu.Load.One, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad5MinContainer,
			utilmetric.MetricData{Value: cpu.Load.Five, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad15MinContainer,
			utilmetric.MetricData{Value: cpu.Load.Fifteen, Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricOCRReadDRAMsContainer,
			utilmetric.MetricData{Value: float64(cpu.OcrReadDrams), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricIMCWriteContainer,
			utilmetric.MetricData{Value: float64(cpu.ImcWrites), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreAllInsContainer,
			utilmetric.MetricData{Value: float64(cpu.StoreAllIns), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreInsContainer,
			utilmetric.MetricData{Value: float64(cpu.StoreIns), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUpdateTimeContainer,
			utilmetric.MetricData{Value: float64(cpu.UpdateTime), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCyclesContainer,
			utilmetric.MetricData{Value: float64(cpu.Cycles), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUInstructionsContainer,
			utilmetric.MetricData{Value: float64(cpu.Instructions), Time: &updateTime})
		// L3Misses is similar to OcrReadDrams
		if cpu.L3Misses > 0 {
			m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUL3CacheMissContainer,
				utilmetric.MetricData{Value: float64(cpu.L3Misses), Time: &updateTime})
		} else if cpu.OcrReadDrams > 0 {
			m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUL3CacheMissContainer,
				utilmetric.MetricData{Value: float64(cpu.OcrReadDrams), Time: &updateTime})
		}

		if cyclesOld.Value > 0 && instructionsOld.Value > 0 {
			instructionDiff := float64(cpu.Instructions) - instructionsOld.Value
			if instructionDiff > 0 {
				cpi := (float64(cpu.Cycles) - cyclesOld.Value) / instructionDiff
				m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCPIContainer,
					utilmetric.MetricData{Value: cpi, Time: &updateTime})
			}
		}

		if m.cpuToNumaMap == nil {
			return
		}
		numaCPUUsage := make(map[int]uint64)
		for cpuID, usage := range cpu.PercpuUsage {
			numaID := m.cpuToNumaMap[cpuID]
			numaCPUUsage[numaID] += usage
		}
		for numaID, usage := range numaCPUUsage {
			numaCPUUsageOld, err := m.metricStore.GetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsCPUUsageCountNUMAContainer)
			if err == nil && numaCPUUsageOld.Time.Before(updateTime) {
				rate := (float64(usage) - numaCPUUsageOld.Value) / updateTime.Sub(*numaCPUUsageOld.Time).Seconds() / 1000000000
				m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsCPUUsageNUMAContainer, utilmetric.MetricData{Value: rate, Time: &updateTime})
			}

			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsCPUUsageCountNUMAContainer,
				utilmetric.MetricData{Value: float64(usage), Time: &updateTime})
		}
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		cpu := cgStats.V2.Cpu
		updateTime := time.Unix(cgStats.V2.Cpu.UpdateTime, 0)

		// todo it's kind of confusing but the `cpu-usage-ratio` in `cgroup-level` actually represents `actual cores`,
		//  we will always rename metric in local store to eliminate `ratio` to avoid ambiguity.
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer,
			utilmetric.MetricData{Value: cpu.CPUUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageUserContainer,
			utilmetric.MetricData{Value: cpu.CPUUserUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageSysContainer,
			utilmetric.MetricData{Value: cpu.CPUSysUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageRatioContainer,
			utilmetric.MetricData{Value: cpu.CPUUsageRatio / (float64(cpu.Max) / float64(cpu.MaxPeriod)), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrRunnableContainer,
			utilmetric.MetricData{Value: float64(cpu.TaskNrRunning), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrUninterruptibleContainer,
			utilmetric.MetricData{Value: float64(cpu.TaskNrUninterruptible), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrIOWaitContainer,
			utilmetric.MetricData{Value: float64(cpu.TaskNrIoWait), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUThrottledTimeContainer,
			utilmetric.MetricData{Value: float64(cpu.CPUStats.ThrottledUsec), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrThrottledContainer,
			utilmetric.MetricData{Value: float64(cpu.CPUStats.NrThrottled), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrPeriodContainer,
			utilmetric.MetricData{Value: float64(cpu.CPUStats.NrPeriods), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad1MinContainer,
			utilmetric.MetricData{Value: cpu.Load.One, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad5MinContainer,
			utilmetric.MetricData{Value: cpu.Load.Five, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad15MinContainer,
			utilmetric.MetricData{Value: cpu.Load.Fifteen, Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricOCRReadDRAMsContainer,
			utilmetric.MetricData{Value: float64(cpu.OcrReadDrams), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricIMCWriteContainer,
			utilmetric.MetricData{Value: float64(cpu.ImcWrites), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreAllInsContainer,
			utilmetric.MetricData{Value: float64(cpu.StoreAllIns), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreInsContainer,
			utilmetric.MetricData{Value: float64(cpu.StoreIns), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUpdateTimeContainer,
			utilmetric.MetricData{Value: float64(cpu.UpdateTime), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCyclesContainer,
			utilmetric.MetricData{Value: float64(cpu.Cycles), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUInstructionsContainer,
			utilmetric.MetricData{Value: float64(cpu.Instructions), Time: &updateTime})
		// L3Misses is similar to OcrReadDrams
		if cpu.L3Misses > 0 {
			m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUL3CacheMissContainer,
				utilmetric.MetricData{Value: float64(cpu.L3Misses), Time: &updateTime})
		} else if cpu.OcrReadDrams > 0 {
			m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUL3CacheMissContainer,
				utilmetric.MetricData{Value: float64(cpu.OcrReadDrams), Time: &updateTime})
		}
		if cyclesOld.Value > 0 && instructionsOld.Value > 0 {
			instructionDiff := float64(cpu.Instructions) - instructionsOld.Value
			if instructionDiff > 0 {
				cpi := (float64(cpu.Cycles) - cyclesOld.Value) / instructionDiff
				m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCPIContainer,
					utilmetric.MetricData{Value: cpi, Time: &updateTime})
			}
		}

		if m.cpuToNumaMap == nil {
			return
		}
		numaCPUUsage := make(map[int]uint64)
		for cpuID, usage := range cpu.PercpuUsage {
			numaID := m.cpuToNumaMap[cpuID]
			numaCPUUsage[numaID] += usage
		}
		for numaID, usage := range numaCPUUsage {
			numaCPUUsageOld, err := m.metricStore.GetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsCPUUsageCountNUMAContainer)
			if err == nil && numaCPUUsageOld.Time.Before(updateTime) {
				rate := (float64(usage) - numaCPUUsageOld.Value) / updateTime.Sub(*numaCPUUsageOld.Time).Seconds() / 1000000000
				m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsCPUUsageNUMAContainer, utilmetric.MetricData{Value: rate, Time: &updateTime})
			}

			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsCPUUsageCountNUMAContainer,
				utilmetric.MetricData{Value: float64(usage), Time: &updateTime})
		}
	}
}

func (m *MalachiteMetricsProvisioner) processContainerMemoryData(podUID, containerName string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	lastUpdateTimeMetric, _ := m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricMemUpdateTimeContainer)

	m.processContainerMemRelevantRate(podUID, containerName, cgStats, lastUpdateTimeMetric.Value)

	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		mem := cgStats.V1.Memory
		updateTime := time.Unix(cgStats.V1.Memory.UpdateTime, 0)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemLimitContainer,
			utilmetric.MetricData{Value: float64(mem.MemoryLimitInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemTCPLimitContainer,
			utilmetric.MetricData{Value: float64(mem.KernMemoryTcpLimitInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageContainer,
			utilmetric.MetricData{Value: float64(mem.MemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageUserContainer,
			utilmetric.MetricData{Value: float64(mem.MemoryLimitInBytes - mem.KernMemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageKernContainer,
			utilmetric.MetricData{Value: float64(mem.KernMemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemRssContainer,
			utilmetric.MetricData{Value: float64(mem.TotalRss), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemCacheContainer,
			utilmetric.MetricData{Value: float64(mem.TotalCache), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemShmemContainer,
			utilmetric.MetricData{Value: float64(mem.TotalShmem), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemDirtyContainer,
			utilmetric.MetricData{Value: float64(mem.Dirty), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemWritebackContainer,
			utilmetric.MetricData{Value: float64(mem.Writeback), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgfaultContainer,
			utilmetric.MetricData{Value: float64(mem.Pgfault), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgmajfaultContainer,
			utilmetric.MetricData{Value: float64(mem.Pgmajfault), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemAllocstallContainer,
			utilmetric.MetricData{Value: float64(mem.TotalAllocstall), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemKswapdstealContainer,
			utilmetric.MetricData{Value: float64(mem.KswapdSteal), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemOomContainer,
			utilmetric.MetricData{Value: float64(mem.BpfMemStat.OomCnt), Time: &updateTime})
		// m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemScaleFactorContainer,
		//	utilmetric.MetricData{Value: general.UIntPointerToFloat64(mem.WatermarkScaleFactor), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUpdateTimeContainer,
			utilmetric.MetricData{Value: float64(mem.UpdateTime), Time: &updateTime})
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		mem := cgStats.V2.Memory
		updateTime := time.Unix(cgStats.V2.Memory.UpdateTime, 0)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageContainer,
			utilmetric.MetricData{Value: float64(mem.MemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageKernContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.Kernel), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemRssContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.Anon), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemCacheContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.File), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemShmemContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.Shmem), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgfaultContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.Pgfault), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgmajfaultContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.Pgmajfault), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemMappedCgroup,
			utilmetric.MetricData{Value: float64(mem.MemStats.FileDirty), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemDirtyContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.FileDirty), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemOomContainer,
			utilmetric.MetricData{Value: float64(mem.BpfMemStat.OomCnt), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemWritebackContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.FileWriteback), Time: &updateTime})
		// m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemScaleFactorContainer,
		//	utilmetric.MetricData{Value: general.UInt64PointerToFloat64(mem.WatermarkScaleFactor), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUpdateTimeContainer,
			utilmetric.MetricData{Value: float64(mem.UpdateTime), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgstealContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.Pgsteal), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgscanContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.Pgscan), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemWorkingsetRefaultContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.WorkingsetRefault), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemWorkingsetActivateContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.WorkingsetActivate), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPsiAvg60Container,
			utilmetric.MetricData{Value: mem.MemPressure.Some.Avg60, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemInactiveAnonContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.InactiveAnon), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemInactiveFileContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.InactiveFile), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemMappedContainer,
			utilmetric.MetricData{Value: float64(mem.MemStats.FileMapped), Time: &updateTime})
	}
}

func (m *MalachiteMetricsProvisioner) processContainerBlkIOData(podUID, containerName string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	lastUpdateTime, _ := m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricBlkioUpdateTimeContainer)

	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		io := cgStats.V1.Blkio
		updateTime := time.Unix(io.UpdateTime, 0)
		updateTimestampInSec := updateTime.Unix()

		m.setContainerRateMetric(podUID, containerName, consts.MetricBlkioReadIopsContainer,
			func() float64 {
				return float64(uint64CounterDelta(io.OldBpfFsData.FsRead, io.BpfFsData.FsRead))
			},
			int64(lastUpdateTime.Value), updateTimestampInSec)
		m.setContainerRateMetric(podUID, containerName, consts.MetricBlkioWriteIopsContainer,
			func() float64 {
				return float64(uint64CounterDelta(io.OldBpfFsData.FsWrite, io.BpfFsData.FsWrite))
			},
			int64(lastUpdateTime.Value), updateTimestampInSec)
		m.setContainerRateMetric(podUID, containerName, consts.MetricBlkioReadBpsContainer,
			func() float64 {
				return float64(uint64CounterDelta(io.OldBpfFsData.FsReadBytes, io.BpfFsData.FsReadBytes))
			},
			int64(lastUpdateTime.Value), updateTimestampInSec)
		m.setContainerRateMetric(podUID, containerName, consts.MetricBlkioWriteBpsContainer,
			func() float64 {
				return float64(uint64CounterDelta(io.OldBpfFsData.FsWriteBytes, io.BpfFsData.FsWriteBytes))
			},
			int64(lastUpdateTime.Value), updateTimestampInSec)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioUpdateTimeContainer,
			utilmetric.MetricData{Value: float64(updateTimestampInSec), Time: &updateTime})
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		io := cgStats.V2.Blkio
		updateTime := time.Unix(io.UpdateTime, 0)
		updateTimestampInSec := updateTime.Unix()

		m.setContainerRateMetric(podUID, containerName, consts.MetricBlkioReadIopsContainer,
			func() float64 { return float64(uint64CounterDelta(io.OldBpfFsData.FsRead, io.BpfFsData.FsRead)) },
			int64(lastUpdateTime.Value), updateTimestampInSec)
		m.setContainerRateMetric(podUID, containerName, consts.MetricBlkioWriteIopsContainer,
			func() float64 { return float64(uint64CounterDelta(io.OldBpfFsData.FsWrite, io.BpfFsData.FsWrite)) },
			int64(lastUpdateTime.Value), updateTimestampInSec)
		m.setContainerRateMetric(podUID, containerName, consts.MetricBlkioReadBpsContainer,
			func() float64 {
				return float64(uint64CounterDelta(io.OldBpfFsData.FsReadBytes, io.BpfFsData.FsReadBytes))
			},
			int64(lastUpdateTime.Value), updateTimestampInSec)
		m.setContainerRateMetric(podUID, containerName, consts.MetricBlkioWriteBpsContainer,
			func() float64 {
				return float64(uint64CounterDelta(io.OldBpfFsData.FsWriteBytes, io.BpfFsData.FsWriteBytes))
			},
			int64(lastUpdateTime.Value), updateTimestampInSec)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioUpdateTimeContainer,
			utilmetric.MetricData{Value: float64(io.UpdateTime), Time: &updateTime})
	}
}

func (m *MalachiteMetricsProvisioner) processContainerNetData(podUID, containerName string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	var net *malachitetypes.NetClsCgData
	var updateTime time.Time
	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		net = cgStats.V1.NetCls
		updateTime = time.Unix(cgStats.V1.NetCls.UpdateTime, 0)
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		net = cgStats.V2.NetCls
		updateTime = time.Unix(cgStats.V2.NetCls.UpdateTime, 0)
	}
	if net == nil {
		return
	}

	lastUpdateTimeMetric, _ := m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricNetworkUpdateTimeContainer)
	m.processContainerNetRelevantRate(podUID, containerName, cgStats, lastUpdateTimeMetric.Value)

	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpRecvPacketsContainer, utilmetric.MetricData{
		Value: float64(net.BpfNetData.NetTCPRx),
		Time:  &updateTime,
	})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpSendPacketsContainer, utilmetric.MetricData{
		Value: float64(net.BpfNetData.NetTCPTx),
		Time:  &updateTime,
	})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpRecvBytesContainer, utilmetric.MetricData{
		Value: float64(net.BpfNetData.NetTCPRxBytes),
		Time:  &updateTime,
	})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpSendBytesContainer, utilmetric.MetricData{
		Value: float64(net.BpfNetData.NetTCPTxBytes),
		Time:  &updateTime,
	})

	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetworkUpdateTimeContainer, utilmetric.MetricData{
		Value: float64(updateTime.Unix()),
		Time:  &updateTime,
	})
}

// Currently, these valid perf event data are provided through types.MalachiteCgroupInfo.V1/V2.CPU by malachite.
// Keep an empty func here in case of that malachite provides more detailed perf event someday.
func (m *MalachiteMetricsProvisioner) processContainerPerfData(podUID, containerName string, cgStats *malachitetypes.MalachiteCgroupInfo) {
}

func (m *MalachiteMetricsProvisioner) processContainerPerNumaMemoryData(podUID, containerName string, cgStats *malachitetypes.MalachiteCgroupInfo) {
	if cgStats == nil {
		return
	}

	if cgStats.CgroupType == "V1" && cgStats.V1 != nil {
		numaStats := cgStats.V1.Memory.NumaStats
		updateTime := time.Unix(cgStats.V1.Memory.UpdateTime, 0)

		for _, data := range numaStats {
			numaName := strings.TrimPrefix(data.NumaName, "N")
			numaID, err := strconv.Atoi(numaName)
			if err != nil {
				klog.ErrorS(err, "failed to convert numaName %v", numaName)
				continue
			}
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemTotalPerNumaContainer,
				utilmetric.MetricData{Value: float64(data.Total << pageShift), Time: &updateTime})
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemFilePerNumaContainer,
				utilmetric.MetricData{Value: float64(data.File << pageShift), Time: &updateTime})
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemAnonPerNumaContainer,
				utilmetric.MetricData{Value: float64(data.Anon << pageShift), Time: &updateTime})
		}
	} else if cgStats.CgroupType == "V2" && cgStats.V2 != nil {
		numaStats := cgStats.V2.Memory.MemNumaStats
		updateTime := time.Unix(cgStats.V2.Memory.UpdateTime, 0)

		for numa, data := range numaStats {
			numaName := strings.TrimPrefix(numa, "N")
			total := data.Anon + data.File + data.Unevictable
			numaID, err := strconv.Atoi(numaName)
			if err != nil {
				klog.ErrorS(err, "failed to convert numaName %v", numaName)
				continue
			}
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemTotalPerNumaContainer,
				utilmetric.MetricData{Value: float64(total), Time: &updateTime})
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemFilePerNumaContainer,
				utilmetric.MetricData{Value: float64(data.File), Time: &updateTime})
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemAnonPerNumaContainer,
				utilmetric.MetricData{Value: float64(data.Anon), Time: &updateTime})
		}
	}
}
