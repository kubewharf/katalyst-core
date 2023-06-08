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

package metric

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/cgroup"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/client"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/system"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	metricsNamMalachiteUnHealthy              = "malachite_unhealthy"
	metricsNameMalachiteGetSystemStatusFailed = "malachite_get_system_status_failed"
	metricsNameMalachiteGetPodStatusFailed    = "malachite_get_pod_status_failed"
)

type MetricsScope string

const (
	MetricsScopeNode      MetricsScope = "node"
	MetricsScopeNuma      MetricsScope = "numa"
	MetricsScopeCPU       MetricsScope = "cpu"
	MetricsScopeDevice    MetricsScope = "device"
	MetricsScopeContainer MetricsScope = "container"
)

type NotifiedRequest struct {
	MetricName string

	DeviceID string
	NumaID   int
	CoreID   int

	PodUID        string
	ContainerName string
	NumaNode      string
}

type NotifiedResponse struct {
	Req       NotifiedRequest
	Result    float64
	Timestamp time.Time
}

type NotifiedData struct {
	scope    MetricsScope
	req      NotifiedRequest
	response chan NotifiedResponse
}

// MetricsFetcher is used to get Node and Pod metrics.
type MetricsFetcher interface {
	// Run starts the preparing logic to collect node metadata.
	Run(ctx context.Context)

	// RegisterNotifier register a channel for raw metric, any time when metric
	// changes, send a data into this given channel along with current time, and
	// we will return a unique key to help with deRegister logic.
	//
	// this "current time" may not represent precisely time when this metric
	// is at, but it indeed is the most precise time katalyst system can provide.
	RegisterNotifier(scope MetricsScope, req NotifiedRequest, response chan NotifiedResponse) string
	DeRegisterNotifier(scope MetricsScope, key string)

	// GetNodeMetric get metric of node.
	GetNodeMetric(metricName string) (float64, error)
	// GetNumaMetric get metric of numa.
	GetNumaMetric(numaID int, metricName string) (float64, error)
	// GetDeviceMetric get metric of device.
	GetDeviceMetric(deviceName string, metricName string) (float64, error)
	// GetCPUMetric get metric of cpu.
	GetCPUMetric(coreID int, metricName string) (float64, error)
	// GetContainerMetric get metric of container.
	GetContainerMetric(podUID, containerName, metricName string) (float64, error)
	// GetContainerNumaMetric get metric of container per numa.
	GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (float64, error)

	// AggregatePodNumaMetric handles numa-level metric for all pods
	AggregatePodNumaMetric(podList []*v1.Pod, numaNode, metricName string, agg metric.Aggregator, filter metric.ContainerMetricFilter) float64
	// AggregatePodMetric handles metric for all pods
	AggregatePodMetric(podList []*v1.Pod, metricName string, agg metric.Aggregator, filter metric.ContainerMetricFilter) float64
	// AggregateCoreMetric handles metric for all cores
	AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg metric.Aggregator) float64
}

var (
	malachiteMetricsFetcherInitOnce sync.Once
)

// NewMalachiteMetricsFetcher returns the default implementation of MetricsFetcher.
func NewMalachiteMetricsFetcher(emitter metrics.MetricEmitter) MetricsFetcher {
	return &MalachiteMetricsFetcher{
		metricStore: metric.GetMetricStoreInstance(),
		emitter:     emitter,
		registered: map[MetricsScope]map[string]NotifiedData{
			MetricsScopeNode:      make(map[string]NotifiedData),
			MetricsScopeNuma:      make(map[string]NotifiedData),
			MetricsScopeCPU:       make(map[string]NotifiedData),
			MetricsScopeDevice:    make(map[string]NotifiedData),
			MetricsScopeContainer: make(map[string]NotifiedData),
		},
	}
}

type MalachiteMetricsFetcher struct {
	metricStore *metric.MetricStore
	emitter     metrics.MetricEmitter

	sync.RWMutex
	registered map[MetricsScope]map[string]NotifiedData
}

func (m *MalachiteMetricsFetcher) Run(ctx context.Context) {
	malachiteMetricsFetcherInitOnce.Do(func() {
		go wait.Until(func() { m.sample() }, time.Second*5, ctx.Done())
	})
}

func (m *MalachiteMetricsFetcher) RegisterNotifier(scope MetricsScope, req NotifiedRequest, response chan NotifiedResponse) string {
	if _, ok := m.registered[scope]; !ok {
		return ""
	}

	m.Lock()
	defer m.Unlock()

	randBytes := make([]byte, 30)
	rand.Read(randBytes)
	key := string(randBytes)

	m.registered[scope][key] = NotifiedData{
		scope:    scope,
		req:      req,
		response: response,
	}
	return key
}

func (m *MalachiteMetricsFetcher) DeRegisterNotifier(scope MetricsScope, key string) {
	m.Lock()
	defer m.Unlock()

	delete(m.registered[scope], key)
}

func (m *MalachiteMetricsFetcher) GetNodeMetric(metricName string) (float64, error) {
	return m.metricStore.GetNodeMetric(metricName)
}

func (m *MalachiteMetricsFetcher) GetNumaMetric(numaID int, metricName string) (float64, error) {
	return m.metricStore.GetNumaMetric(numaID, metricName)
}

func (m *MalachiteMetricsFetcher) GetDeviceMetric(deviceName string, metricName string) (float64, error) {
	return m.metricStore.GetDeviceMetric(deviceName, metricName)
}

func (m *MalachiteMetricsFetcher) GetCPUMetric(coreID int, metricName string) (float64, error) {
	return m.metricStore.GetCPUMetric(coreID, metricName)
}

func (m *MalachiteMetricsFetcher) GetContainerMetric(podUID, containerName, metricName string) (float64, error) {
	return m.metricStore.GetContainerMetric(podUID, containerName, metricName)
}

func (m *MalachiteMetricsFetcher) GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (float64, error) {
	return m.metricStore.GetContainerNumaMetric(podUID, containerName, numaNode, metricName)
}

func (m *MalachiteMetricsFetcher) AggregatePodNumaMetric(podList []*v1.Pod, numaNode, metricName string,
	agg metric.Aggregator, filter metric.ContainerMetricFilter) float64 {
	return m.metricStore.AggregatePodNumaMetric(podList, numaNode, metricName, agg, filter)
}

func (m *MalachiteMetricsFetcher) AggregatePodMetric(podList []*v1.Pod, metricName string,
	agg metric.Aggregator, filter metric.ContainerMetricFilter) float64 {
	return m.metricStore.AggregatePodMetric(podList, metricName, agg, filter)
}

func (m *MalachiteMetricsFetcher) AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg metric.Aggregator) float64 {
	return m.metricStore.AggregateCoreMetric(cpuset, metricName, agg)
}

func (m *MalachiteMetricsFetcher) sample() {
	klog.V(4).Infof("[malachite] heartbeat")

	if !m.checkMalachiteHealthy() {
		return
	}

	cur := time.Now()
	// Update system data
	m.updateSystemStats(cur)

	// Update pod data
	m.updatePodsCgroupData(cur)
}

// checkMalachiteHealthy is to check whether malachite is healthy
func (m *MalachiteMetricsFetcher) checkMalachiteHealthy() bool {
	_, err := client.DefaultClient.GetSystemStats(client.Compute)
	if err != nil {
		klog.Errorf("[malachite] malachite is unhealthy: %v", err)
		_ = m.emitter.StoreInt64(metricsNamMalachiteUnHealthy, 1, metrics.MetricTypeNameRaw)
		return false
	}

	return true
}

// Get raw system stats by malachite sdk and set to metricStore
func (m *MalachiteMetricsFetcher) updateSystemStats(cur time.Time) {
	systemComputeData, err := system.GetSystemComputeStats()
	if err != nil {
		klog.Errorf("[malachite] get system compute stats failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetSystemStatusFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "compute"})
	} else {
		m.processSystemComputeData(systemComputeData)
		m.processSystemCPUComputeData(systemComputeData)
	}

	systemMemoryData, err := system.GetSystemMemoryStats()
	if err != nil {
		klog.Errorf("[malachite] get system memory stats failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetSystemStatusFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "memory"})
	} else {
		m.processSystemMemoryData(systemMemoryData)
		m.processSystemNumaData(systemMemoryData)
	}

	systemIOData, err := system.GetSystemIOStats()
	if err != nil {
		klog.Errorf("[malachite] get system io stats failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetSystemStatusFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "io"})
	} else {
		m.processSystemIOData(systemIOData)
	}

	m.notifySystem(cur)
}

// Get raw cgroup data by malachite sdk and set container metrics to metricStore, GC not existed pod metrics
func (m *MalachiteMetricsFetcher) updatePodsCgroupData(cur time.Time) {
	podsContainersStats, err := cgroup.GetAllPodsContainersStats()
	if err != nil {
		klog.Errorf("[malachite] GetAllPodsContainersStats failed, error %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetPodStatusFailed, 1, metrics.MetricTypeNameCount)
	}

	podUIDSet := make(map[string]bool)
	for podUID, containerStats := range podsContainersStats {
		podUIDSet[podUID] = true
		for containerName, cgStats := range containerStats {
			m.processCgroupCPUData(podUID, containerName, cgStats)
			m.processCgroupMemoryData(podUID, containerName, cgStats)
			m.processCgroupBlkIOData(podUID, containerName, cgStats)
			m.processCgroupNetData(podUID, containerName, cgStats)
			m.processCgroupPerfData(podUID, containerName, cgStats)
			m.processCgroupPerNumaMemoryData(podUID, containerName, cgStats)
		}
	}
	m.metricStore.GCPodsMetric(podUIDSet)

	m.notifyPods(cur)
}

// notifySystem notifies system-related data
// todo: cur should be replaced with fetched timestamp
func (m *MalachiteMetricsFetcher) notifySystem(cur time.Time) {
	m.RLock()
	defer m.RUnlock()

	for _, reg := range m.registered[MetricsScopeNode] {
		v, err := m.metricStore.GetNodeMetric(reg.req.MetricName)
		if err != nil {
			continue
		}
		reg.response <- NotifiedResponse{
			Req:       reg.req,
			Result:    v,
			Timestamp: cur,
		}
	}

	for _, reg := range m.registered[MetricsScopeDevice] {
		v, err := m.metricStore.GetDeviceMetric(reg.req.DeviceID, reg.req.MetricName)
		if err != nil {
			continue
		}
		reg.response <- NotifiedResponse{
			Req:       reg.req,
			Result:    v,
			Timestamp: cur,
		}
	}

	for _, reg := range m.registered[MetricsScopeNuma] {
		v, err := m.metricStore.GetNumaMetric(reg.req.NumaID, reg.req.MetricName)
		if err != nil {
			continue
		}
		reg.response <- NotifiedResponse{
			Req:       reg.req,
			Result:    v,
			Timestamp: cur,
		}
	}

	for _, reg := range m.registered[MetricsScopeCPU] {
		v, err := m.metricStore.GetCPUMetric(reg.req.CoreID, reg.req.MetricName)
		if err != nil {
			continue
		}
		reg.response <- NotifiedResponse{
			Req:       reg.req,
			Result:    v,
			Timestamp: cur,
		}
	}
}

// notifySystem notifies pod-related data
// todo: cur should be replaced with fetched timestamp
func (m *MalachiteMetricsFetcher) notifyPods(cur time.Time) {
	m.RLock()
	defer m.RUnlock()

	for _, reg := range m.registered[MetricsScopeContainer] {
		v, err := m.metricStore.GetContainerMetric(reg.req.PodUID, reg.req.ContainerName, reg.req.MetricName)
		if err != nil {
			continue
		}
		reg.response <- NotifiedResponse{
			Req:       reg.req,
			Result:    v,
			Timestamp: cur,
		}

		if reg.req.NumaID == 0 {
			continue
		}

		v, err = m.metricStore.GetContainerNumaMetric(reg.req.PodUID, reg.req.ContainerName, fmt.Sprintf("%v", reg.req.NumaID), reg.req.MetricName)
		if err != nil {
			continue
		}
		reg.response <- NotifiedResponse{
			Req:       reg.req,
			Result:    v,
			Timestamp: cur,
		}
	}
}

func (m *MalachiteMetricsFetcher) processSystemComputeData(systemComputeData *system.SystemComputeData) {
	load := systemComputeData.Load
	m.metricStore.SetNodeMetric(consts.MetricLoad1MinSystem, load.One)
	m.metricStore.SetNodeMetric(consts.MetricLoad5MinSystem, load.Five)
	m.metricStore.SetNodeMetric(consts.MetricLoad15MinSystem, load.Fifteen)
}

func (m *MalachiteMetricsFetcher) processSystemMemoryData(systemMemoryData *system.SystemMemoryData) {
	mem := systemMemoryData.System
	m.metricStore.SetNodeMetric(consts.MetricMemTotalSystem, float64(mem.MemTotal<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemUsedSystem, float64(mem.MemUsed<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemFreeSystem, float64(mem.MemFree<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemShmemSystem, float64(mem.MemShm<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemBufferSystem, float64(mem.MemBuffers<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemAvailableSystem, float64(mem.MemAvailable<<10))

	m.metricStore.SetNodeMetric(consts.MetricMemDirtySystem, float64(mem.MemDirtyPageCache<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemWritebackSystem, float64(mem.MemWriteBackPageCache<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemKswapdstealSystem, float64(mem.VmstatPgstealKswapd))

	m.metricStore.SetNodeMetric(consts.MetricMemSwapTotalSystem, float64(mem.MemSwapTotal<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemSwapFreeSystem, float64(mem.MemSwapFree<<10))
	m.metricStore.SetNodeMetric(consts.MetricMemSlabReclaimableSystem, float64(mem.MemSlabReclaimable<<10))

	m.metricStore.SetNodeMetric(consts.MetricMemScaleFactorSystem, float64(mem.VMWatermarkScaleFactor))
}

func (m *MalachiteMetricsFetcher) processSystemIOData(systemIOData *system.SystemDiskIoData) {
	for _, device := range systemIOData.DiskIo {
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOReadSystem, float64(device.IoRead))
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOWriteSystem, float64(device.IoWrite))
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOBusySystem, float64(device.IoBusy))
	}
}

func (m *MalachiteMetricsFetcher) processSystemNumaData(systemMemoryData *system.SystemMemoryData) {
	for _, numa := range systemMemoryData.Numa {
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemTotalNuma, float64(numa.MemTotal<<10))
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemUsedNuma, float64(numa.MemUsed<<10))
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemFreeNuma, float64(numa.MemFree<<10))
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemShmemNuma, float64(numa.MemShmem<<10))
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemAvailableNuma, float64(numa.MemAvailable<<10))
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemFilepageNuma, float64(numa.MemFilePages<<10))

		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthNuma, numa.MemReadBandwidthMB/1024.0+numa.MemWriteBandwidthMB/1024.0)
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthMaxNuma, numa.MemTheoryMaxBandwidthMB*0.8/1024.0)
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthTheoryNuma, numa.MemTheoryMaxBandwidthMB/1024.0)
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthReadNuma, numa.MemReadBandwidthMB/1024.0)
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthWriteNuma, numa.MemWriteBandwidthMB/1024.0)

		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemLatencyReadNuma, numa.MemReadLatency)
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemLatencyWriteNuma, numa.MemWriteLatency)
	}
}

func (m *MalachiteMetricsFetcher) processSystemCPUComputeData(systemComputeData *system.SystemComputeData) {
	for _, cpu := range systemComputeData.CPU {
		cpuID, err := strconv.Atoi(cpu.Name[3:])
		if err != nil {
			klog.Errorf("[malachite] parse cpu name %v with err: %v", cpu.Name, err)
			continue
		}
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUUsage, cpu.CPUUsage)
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUSchedwait, cpu.CPUSchedWait)
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUIOWaitRatio, cpu.CPUIowaitRatio)
	}
}

func (m *MalachiteMetricsFetcher) processCgroupCPUData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	m.processContainerMemBandwidth(podUID, containerName, cgStats)

	if cgStats.CgroupType == "V1" {
		cpu := cgStats.V1.Cpu
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPULimitContainer, float64(cpu.CfsQuotaUs)/float64(cpu.CfsPeriodUs))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer, float64(cpu.NewCPUBasicInfo.CPUUsage-cpu.OldCPUBasicInfo.CPUUsage)/(float64(cpu.NewCPUBasicInfo.UpdateTime-cpu.OldCPUBasicInfo.UpdateTime)*1e9))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageRatioContainer, cpu.CPUUsageRatio)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageUserContainer, cpu.CPUUserUsageRatio)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageSysContainer, cpu.CPUSysUsageRatio)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUShareContainer, float64(cpu.CPUShares))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUQuotaContainer, float64(cpu.CfsQuotaUs))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUPeriodContainer, float64(cpu.CfsPeriodUs))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrThrottledContainer, float64(cpu.CPUNrThrottled))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUThrottledPeriodContainer, float64(cpu.CPUNrPeriods))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUThrottledTimeContainer, float64(cpu.CPUThrottledTime))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrRunnableContainer, float64(cpu.TaskNrRunning))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrUninterruptibleContainer, float64(cpu.TaskNrUninterruptible))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrIOWaitContainer, float64(cpu.TaskNrIoWait))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad1MinContainer, cpu.Load.One)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad5MinContainer, cpu.Load.Five)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad15MinContainer, cpu.Load.Fifteen)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricOCRReadDRAMsContainer, float64(cpu.OCRReadDRAMs))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricIMCWriteContainer, float64(cpu.IMCWrites))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreAllInsContainer, float64(cpu.StoreAllInstructions))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreInsContainer, float64(cpu.StoreInstructions))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricUpdateTimeContainer, float64(cpu.UpdateTime))
	} else if cgStats.CgroupType == "V2" {
		cpu := cgStats.V2.Cpu
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageRatioContainer, cpu.CPUUsageRatio)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageUserContainer, cpu.CPUUserUsageRatio)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageSysContainer, cpu.CPUSysUsageRatio)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrRunnableContainer, float64(cpu.TaskNrRunning))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrUninterruptibleContainer, float64(cpu.TaskNrUninterruptible))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrIOWaitContainer, float64(cpu.TaskNrIoWait))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad1MinContainer, cpu.Load.One)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad5MinContainer, cpu.Load.Five)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad15MinContainer, cpu.Load.Fifteen)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricOCRReadDRAMsContainer, float64(cpu.OCRReadDRAMs))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricIMCWriteContainer, float64(cpu.IMCWrites))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreAllInsContainer, float64(cpu.StoreAllInstructions))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreInsContainer, float64(cpu.StoreInstructions))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricUpdateTimeContainer, float64(cpu.UpdateTime))
	}
}

func (m *MalachiteMetricsFetcher) processCgroupMemoryData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		mem := cgStats.V1.Memory
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemLimitContainer, float64(mem.MemoryLimitInBytes))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageContainer, float64(mem.MemoryUsageInBytes))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageUserContainer, float64(mem.MemoryLimitInBytes-mem.KernMemoryUsageInBytes))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageSysContainer, float64(mem.KernMemoryUsageInBytes))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemRssContainer, float64(mem.TotalRss))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemCacheContainer, float64(mem.TotalCache))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemShmemContainer, float64(mem.TotalShmem))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemDirtyContainer, float64(mem.TotalDirty))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemWritebackContainer, float64(mem.TotalWriteback))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgfaultContainer, float64(mem.TotalPgfault))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgmajfaultContainer, float64(mem.TotalPgmajfault))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemAllocstallContainer, float64(mem.TotalAllocstall))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemKswapdstealContainer, float64(mem.KswapdSteal))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemOomContainer, float64(mem.OomCnt))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemScaleFactorContainer, general.UIntPointerToFloat64(mem.WatermarkScaleFactor))
	} else if cgStats.CgroupType == "V2" {
		mem := cgStats.V2.Memory
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageContainer, float64(mem.MemoryUsageInBytes))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemShmemContainer, float64(mem.MemStats.Shmem))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgfaultContainer, float64(mem.MemStats.Pgfault))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgmajfaultContainer, float64(mem.MemStats.Pgmajfault))

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemOomContainer, float64(mem.OomCnt))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemScaleFactorContainer, general.UInt64PointerToFloat64(mem.WatermarkScaleFactor))
	}
}

func (m *MalachiteMetricsFetcher) processCgroupBlkIOData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		io := cgStats.V1.Blkio
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioReadIopsContainer, float64(io.BpfFsData.FsRead-io.OldBpfFsData.FsRead))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioWriteIopsContainer, float64(io.BpfFsData.FsWrite-io.OldBpfFsData.FsWrite))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioReadBpsContainer, float64(io.BpfFsData.FsReadBytes-io.OldBpfFsData.FsReadBytes))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioWriteBpsContainer, float64(io.BpfFsData.FsWriteBytes-io.OldBpfFsData.FsWriteBytes))
	} else if cgStats.CgroupType == "V2" {
		io := cgStats.V2.Blkio
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioReadIopsContainer, float64(io.BpfFsData.FsRead-io.OldBpfFsData.FsRead))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioWriteIopsContainer, float64(io.BpfFsData.FsWrite-io.OldBpfFsData.FsWrite))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioReadBpsContainer, float64(io.BpfFsData.FsReadBytes-io.OldBpfFsData.FsReadBytes))
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioWriteBpsContainer, float64(io.BpfFsData.FsWriteBytes-io.OldBpfFsData.FsWriteBytes))
	}
}

func (m *MalachiteMetricsFetcher) processCgroupNetData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	var net *cgroup.NetClsCgData
	if cgStats.CgroupType == "V1" {
		net = cgStats.V1.NetCls
	} else if cgStats.CgroupType == "V2" {
		net = cgStats.V2.NetCls
	}
	if net == nil {
		return
	}
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpSendByteContainer, float64(net.BpfNetData.NetTxBytes-net.OldBpfNetData.NetTxBytes))
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpSendPpsContainer, float64(net.BpfNetData.NetTx-net.OldBpfNetData.NetTx))
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpRecvByteContainer, float64(net.BpfNetData.NetRxBytes-net.OldBpfNetData.NetRxBytes))
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpRecvPpsContainer, float64(net.BpfNetData.NetRx-net.OldBpfNetData.NetRx))
}

func (m *MalachiteMetricsFetcher) processCgroupPerfData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	var perf *cgroup.PerfEventData
	if cgStats.CgroupType == "V1" {
		perf = cgStats.V1.PerfEvent
	} else if cgStats.CgroupType == "V2" {
		perf = cgStats.V2.PerfEvent
	}
	if perf == nil {
		return
	}
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCpiContainer, perf.Cpi)
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCyclesContainer, perf.Cycles)
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUInstructionsContainer, perf.Instructions)
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUICacheMissContainer, perf.IcacheMiss)
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUL2CacheMissContainer, perf.L2CacheMiss)
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUL3CacheMissContainer, perf.L3CacheMiss)
}

func (m *MalachiteMetricsFetcher) processCgroupPerNumaMemoryData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		numaStats := cgStats.V1.Memory.NumaStats
		for _, data := range numaStats {
			numaID := strings.TrimPrefix(data.NumaName, "N")
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemTotalPerNumaContainer, float64(data.Total<<10))
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemFilePerNumaContainer, float64(data.File<<10))
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemAnonPerNumaContainer, float64(data.Anon<<10))
		}
	} else if cgStats.CgroupType == "V2" {
		numaStats := cgStats.V2.Memory.MemNumaStats
		for numa, data := range numaStats {
			numaID := strings.TrimPrefix(numa, "N")
			total := data.Anon + data.File + data.Unevictable
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemTotalPerNumaContainer, float64(total<<10))
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemFilePerNumaContainer, float64(data.File<<10))
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemAnonPerNumaContainer, float64(data.Anon<<10))
		}
	}
}
