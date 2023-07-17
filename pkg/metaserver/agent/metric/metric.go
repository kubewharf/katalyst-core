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

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/cgroup"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/client"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/system"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
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

// NotifiedRequest defines the structure as requests for notifier
type NotifiedRequest struct {
	MetricName string

	DeviceID string
	NumaID   int
	CoreID   int

	PodUID        string
	ContainerName string
	NumaNode      string
}

// NotifiedData defines the structure as response data for notifier
type NotifiedData struct {
	scope    MetricsScope
	req      NotifiedRequest
	response chan NotifiedResponse
}
type NotifiedResponse struct {
	Req NotifiedRequest
	metric.MetricData
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

	// RegisterExternalMetric register a function to set metric that can
	// only be obtained from external sources
	RegisterExternalMetric(f func(store *metric.MetricStore))

	// GetNodeMetric get metric of node.
	GetNodeMetric(metricName string) (metric.MetricData, error)
	// GetNumaMetric get metric of numa.
	GetNumaMetric(numaID int, metricName string) (metric.MetricData, error)
	// GetDeviceMetric get metric of device.
	GetDeviceMetric(deviceName string, metricName string) (metric.MetricData, error)
	// GetCPUMetric get metric of cpu.
	GetCPUMetric(coreID int, metricName string) (metric.MetricData, error)
	// GetContainerMetric get metric of container.
	GetContainerMetric(podUID, containerName, metricName string) (metric.MetricData, error)
	// GetContainerNumaMetric get metric of container per numa.
	GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (metric.MetricData, error)

	// AggregatePodNumaMetric handles numa-level metric for all pods
	AggregatePodNumaMetric(podList []*v1.Pod, numaNode, metricName string, agg metric.Aggregator, filter metric.ContainerMetricFilter) metric.MetricData
	// AggregatePodMetric handles metric for all pods
	AggregatePodMetric(podList []*v1.Pod, metricName string, agg metric.Aggregator, filter metric.ContainerMetricFilter) metric.MetricData
	// AggregateCoreMetric handles metric for all cores
	AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg metric.Aggregator) metric.MetricData

	// GetCgroupMetric get metric of cgroup path: /kubepods/burstable, /kubepods/besteffort, etc.
	GetCgroupMetric(cgroupPath, metricName string) (metric.MetricData, error)
	// GetCgroupNumaMetric get NUMA metric of qos class: /kubepods/burstable, /kubepods/besteffort, etc.
	GetCgroupNumaMetric(cgroupPath, numaNode, metricName string) (metric.MetricData, error)
}

// NewMalachiteMetricsFetcher returns the default implementation of MetricsFetcher.
func NewMalachiteMetricsFetcher(emitter metrics.MetricEmitter, conf *config.Configuration) MetricsFetcher {
	return &MalachiteMetricsFetcher{
		metricStore: metric.NewMetricStore(),
		emitter:     emitter,
		conf:        conf,
		registeredNotifier: map[MetricsScope]map[string]NotifiedData{
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
	conf        *config.Configuration

	registeredMetric   []func(store *metric.MetricStore)
	registeredNotifier map[MetricsScope]map[string]NotifiedData

	sync.RWMutex
	startOnce sync.Once
	emitter   metrics.MetricEmitter
}

func (m *MalachiteMetricsFetcher) Run(ctx context.Context) {
	m.startOnce.Do(func() {
		go wait.Until(func() { m.sample() }, time.Second*5, ctx.Done())
	})
}

func (m *MalachiteMetricsFetcher) RegisterNotifier(scope MetricsScope, req NotifiedRequest,
	response chan NotifiedResponse) string {
	if _, ok := m.registeredNotifier[scope]; !ok {
		return ""
	}

	m.Lock()
	defer m.Unlock()

	randBytes := make([]byte, 30)
	rand.Read(randBytes)
	key := string(randBytes)

	m.registeredNotifier[scope][key] = NotifiedData{
		scope:    scope,
		req:      req,
		response: response,
	}
	return key
}

func (m *MalachiteMetricsFetcher) DeRegisterNotifier(scope MetricsScope, key string) {
	m.Lock()
	defer m.Unlock()

	delete(m.registeredNotifier[scope], key)
}

func (m *MalachiteMetricsFetcher) RegisterExternalMetric(f func(store *metric.MetricStore)) {
	m.registeredMetric = append(m.registeredMetric, f)
}

func (m *MalachiteMetricsFetcher) GetNodeMetric(metricName string) (metric.MetricData, error) {
	return m.metricStore.GetNodeMetric(metricName)
}

func (m *MalachiteMetricsFetcher) GetNumaMetric(numaID int, metricName string) (metric.MetricData, error) {
	return m.metricStore.GetNumaMetric(numaID, metricName)
}

func (m *MalachiteMetricsFetcher) GetDeviceMetric(deviceName string, metricName string) (metric.MetricData, error) {
	return m.metricStore.GetDeviceMetric(deviceName, metricName)
}

func (m *MalachiteMetricsFetcher) GetCPUMetric(coreID int, metricName string) (metric.MetricData, error) {
	return m.metricStore.GetCPUMetric(coreID, metricName)
}

func (m *MalachiteMetricsFetcher) GetContainerMetric(podUID, containerName, metricName string) (metric.MetricData, error) {
	return m.metricStore.GetContainerMetric(podUID, containerName, metricName)
}

func (m *MalachiteMetricsFetcher) GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (metric.MetricData, error) {
	return m.metricStore.GetContainerNumaMetric(podUID, containerName, numaNode, metricName)
}

func (m *MalachiteMetricsFetcher) GetCgroupMetric(cgroupPath, metricName string) (metric.MetricData, error) {
	return m.metricStore.GetCgroupMetric(cgroupPath, metricName)
}

func (m *MalachiteMetricsFetcher) GetCgroupNumaMetric(cgroupPath, numaNode, metricName string) (metric.MetricData, error) {
	return m.metricStore.GetCgroupNumaMetric(cgroupPath, numaNode, metricName)
}
func (m *MalachiteMetricsFetcher) AggregatePodNumaMetric(podList []*v1.Pod, numaNode, metricName string,
	agg metric.Aggregator, filter metric.ContainerMetricFilter) metric.MetricData {
	return m.metricStore.AggregatePodNumaMetric(podList, numaNode, metricName, agg, filter)
}

func (m *MalachiteMetricsFetcher) AggregatePodMetric(podList []*v1.Pod, metricName string,
	agg metric.Aggregator, filter metric.ContainerMetricFilter) metric.MetricData {
	return m.metricStore.AggregatePodMetric(podList, metricName, agg, filter)
}

func (m *MalachiteMetricsFetcher) AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg metric.Aggregator) metric.MetricData {
	return m.metricStore.AggregateCoreMetric(cpuset, metricName, agg)
}

func (m *MalachiteMetricsFetcher) sample() {
	klog.V(4).Infof("[malachite] heartbeat")

	if !m.checkMalachiteHealthy() {
		return
	}

	// Update system data
	m.updateSystemStats()
	// Update pod data
	m.updatePodsCgroupData()
	// Update top level cgroup of kubepods
	m.updateCgroupData()

	// after sampling, we should call the registered function to get external metric
	for _, f := range m.registeredMetric {
		f(m.metricStore)
	}
	m.notifySystem()
	m.notifyPods()
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
func (m *MalachiteMetricsFetcher) updateSystemStats() {
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
}

func (m *MalachiteMetricsFetcher) updateCgroupData() {
	cgroupPaths := []string{m.conf.ReclaimRelativeRootCgroupPath, common.CgroupFsRootPathBurstable, common.CgroupFsRootPathBestEffort}
	for _, path := range cgroupPaths {
		stats, err := cgroup.GetCgroupStats(path)
		if err != nil {
			general.Errorf("GetCgroupStats %v err %v", path, err)
			continue
		}
		m.processCgroupCPUData(path, stats)
		m.processCgroupMemoryData(path, stats)
		m.processCgroupBlkIOData(path, stats)
		m.processCgroupNetData(path, stats)
		m.processCgroupPerNumaMemoryData(path, stats)
	}
}

// Get raw cgroup data by malachite sdk and set container metrics to metricStore, GC not existed pod metrics
func (m *MalachiteMetricsFetcher) updatePodsCgroupData() {
	podsContainersStats, err := cgroup.GetAllPodsContainersStats()
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
}

// notifySystem notifies system-related data
func (m *MalachiteMetricsFetcher) notifySystem() {
	now := time.Now()
	m.RLock()
	defer m.RUnlock()

	for _, reg := range m.registeredNotifier[MetricsScopeNode] {
		v, err := m.metricStore.GetNodeMetric(reg.req.MetricName)
		if err != nil {
			continue
		} else if v.Time == nil {
			v.Time = &now
		}
		reg.response <- NotifiedResponse{
			Req:        reg.req,
			MetricData: v,
		}
	}

	for _, reg := range m.registeredNotifier[MetricsScopeDevice] {
		v, err := m.metricStore.GetDeviceMetric(reg.req.DeviceID, reg.req.MetricName)
		if err != nil {
			continue
		} else if v.Time == nil {
			v.Time = &now
		}
		reg.response <- NotifiedResponse{
			Req:        reg.req,
			MetricData: v,
		}
	}

	for _, reg := range m.registeredNotifier[MetricsScopeNuma] {
		v, err := m.metricStore.GetNumaMetric(reg.req.NumaID, reg.req.MetricName)
		if err != nil {
			continue
		} else if v.Time == nil {
			v.Time = &now
		}
		reg.response <- NotifiedResponse{
			Req:        reg.req,
			MetricData: v,
		}
	}

	for _, reg := range m.registeredNotifier[MetricsScopeCPU] {
		v, err := m.metricStore.GetCPUMetric(reg.req.CoreID, reg.req.MetricName)
		if err != nil {
			continue
		} else if v.Time == nil {
			v.Time = &now
		}
		reg.response <- NotifiedResponse{
			Req:        reg.req,
			MetricData: v,
		}
	}
}

// notifySystem notifies pod-related data
func (m *MalachiteMetricsFetcher) notifyPods() {
	now := time.Now()
	m.RLock()
	defer m.RUnlock()

	for _, reg := range m.registeredNotifier[MetricsScopeContainer] {
		v, err := m.metricStore.GetContainerMetric(reg.req.PodUID, reg.req.ContainerName, reg.req.MetricName)
		if err != nil {
			continue
		} else if v.Time == nil {
			v.Time = &now
		}
		reg.response <- NotifiedResponse{
			Req:        reg.req,
			MetricData: v,
		}

		if reg.req.NumaID == 0 {
			continue
		}

		v, err = m.metricStore.GetContainerNumaMetric(reg.req.PodUID, reg.req.ContainerName, fmt.Sprintf("%v", reg.req.NumaID), reg.req.MetricName)
		if err != nil {
			continue
		} else if v.Time == nil {
			v.Time = &now
		}
		reg.response <- NotifiedResponse{
			Req:        reg.req,
			MetricData: v,
		}
	}
}

func (m *MalachiteMetricsFetcher) processSystemComputeData(systemComputeData *system.SystemComputeData) {
	// todo, currently we only get a unified data for the whole system compute data
	updateTime := time.Unix(systemComputeData.UpdateTime, 0)

	load := systemComputeData.Load
	m.metricStore.SetNodeMetric(consts.MetricLoad1MinSystem,
		metric.MetricData{Value: load.One, Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricLoad5MinSystem,
		metric.MetricData{Value: load.Five, Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricLoad15MinSystem,
		metric.MetricData{Value: load.Fifteen, Time: &updateTime})
}

func (m *MalachiteMetricsFetcher) processSystemMemoryData(systemMemoryData *system.SystemMemoryData) {
	// todo, currently we only get a unified data for the whole system memory data
	updateTime := time.Unix(systemMemoryData.UpdateTime, 0)

	mem := systemMemoryData.System
	m.metricStore.SetNodeMetric(consts.MetricMemTotalSystem,
		metric.MetricData{Value: float64(mem.MemTotal << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemUsedSystem,
		metric.MetricData{Value: float64(mem.MemUsed << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemFreeSystem,
		metric.MetricData{Value: float64(mem.MemFree << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemShmemSystem,
		metric.MetricData{Value: float64(mem.MemShm << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemBufferSystem,
		metric.MetricData{Value: float64(mem.MemBuffers << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemAvailableSystem,
		metric.MetricData{Value: float64(mem.MemAvailable << 10), Time: &updateTime})

	m.metricStore.SetNodeMetric(consts.MetricMemDirtySystem,
		metric.MetricData{Value: float64(mem.MemDirtyPageCache << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemWritebackSystem,
		metric.MetricData{Value: float64(mem.MemWriteBackPageCache << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemKswapdstealSystem,
		metric.MetricData{Value: float64(mem.VmstatPgstealKswapd), Time: &updateTime})

	m.metricStore.SetNodeMetric(consts.MetricMemSwapTotalSystem,
		metric.MetricData{Value: float64(mem.MemSwapTotal << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemSwapFreeSystem,
		metric.MetricData{Value: float64(mem.MemSwapFree << 10), Time: &updateTime})
	m.metricStore.SetNodeMetric(consts.MetricMemSlabReclaimableSystem,
		metric.MetricData{Value: float64(mem.MemSlabReclaimable << 10), Time: &updateTime})

	m.metricStore.SetNodeMetric(consts.MetricMemScaleFactorSystem,
		metric.MetricData{Value: float64(mem.VMWatermarkScaleFactor), Time: &updateTime})
}

func (m *MalachiteMetricsFetcher) processSystemIOData(systemIOData *system.SystemDiskIoData) {
	// todo, currently we only get a unified data for the whole system io data
	updateTime := time.Unix(systemIOData.UpdateTime, 0)

	for _, device := range systemIOData.DiskIo {
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOReadSystem,
			metric.MetricData{Value: float64(device.IoRead), Time: &updateTime})
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOWriteSystem,
			metric.MetricData{Value: float64(device.IoWrite), Time: &updateTime})
		m.metricStore.SetDeviceMetric(device.DeviceName, consts.MetricIOBusySystem,
			metric.MetricData{Value: float64(device.IoBusy), Time: &updateTime})
	}
}

func (m *MalachiteMetricsFetcher) processSystemNumaData(systemMemoryData *system.SystemMemoryData) {
	// todo, currently we only get a unified data for the whole system memory data
	updateTime := time.Unix(systemMemoryData.UpdateTime, 0)

	for _, numa := range systemMemoryData.Numa {
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemTotalNuma,
			metric.MetricData{Value: float64(numa.MemTotal << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemUsedNuma,
			metric.MetricData{Value: float64(numa.MemUsed << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemFreeNuma,
			metric.MetricData{Value: float64(numa.MemFree << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemShmemNuma,
			metric.MetricData{Value: float64(numa.MemShmem << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemAvailableNuma,
			metric.MetricData{Value: float64(numa.MemAvailable << 10), Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemFilepageNuma,
			metric.MetricData{Value: float64(numa.MemFilePages << 10), Time: &updateTime})

		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthNuma,
			metric.MetricData{Value: numa.MemReadBandwidthMB/1024.0 + numa.MemWriteBandwidthMB/1024.0, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthMaxNuma,
			metric.MetricData{Value: numa.MemTheoryMaxBandwidthMB * 0.8 / 1024.0, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthTheoryNuma,
			metric.MetricData{Value: numa.MemTheoryMaxBandwidthMB / 1024.0, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthReadNuma,
			metric.MetricData{Value: numa.MemReadBandwidthMB / 1024.0, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemBandwidthWriteNuma,
			metric.MetricData{Value: numa.MemWriteBandwidthMB / 1024.0, Time: &updateTime})

		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemLatencyReadNuma,
			metric.MetricData{Value: numa.MemReadLatency, Time: &updateTime})
		m.metricStore.SetNumaMetric(numa.ID, consts.MetricMemLatencyWriteNuma,
			metric.MetricData{Value: numa.MemWriteLatency, Time: &updateTime})
	}
}

func (m *MalachiteMetricsFetcher) processSystemCPUComputeData(systemComputeData *system.SystemComputeData) {
	// todo, currently we only get a unified data for the whole system compute data
	updateTime := time.Unix(systemComputeData.UpdateTime, 0)

	for _, cpu := range systemComputeData.CPU {
		cpuID, err := strconv.Atoi(cpu.Name[3:])
		if err != nil {
			klog.Errorf("[malachite] parse cpu name %v with err: %v", cpu.Name, err)
			continue
		}
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUUsage,
			metric.MetricData{Value: cpu.CPUUsage, Time: &updateTime})
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUSchedwait,
			metric.MetricData{Value: cpu.CPUSchedWait, Time: &updateTime})
		m.metricStore.SetCPUMetric(cpuID, consts.MetricCPUIOWaitRatio,
			metric.MetricData{Value: cpu.CPUIowaitRatio, Time: &updateTime})
	}
}

func (m *MalachiteMetricsFetcher) processCgroupCPUData(cgroupPath string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		cpu := cgStats.V1.Cpu
		updateTime := time.Unix(cgStats.V1.Cpu.UpdateTime, 0)

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPULimitCgroup, metric.MetricData{Value: float64(cpu.CfsQuotaUs) / float64(cpu.CfsPeriodUs), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageCgroup, metric.MetricData{Value: cpu.CPUUsageRatio, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageUserCgroup, metric.MetricData{Value: cpu.CPUUserUsageRatio, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageSysCgroup, metric.MetricData{Value: cpu.CPUSysUsageRatio, Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUShareCgroup, metric.MetricData{Value: float64(cpu.CPUShares), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUQuotaCgroup, metric.MetricData{Value: float64(cpu.CfsQuotaUs), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUPeriodCgroup, metric.MetricData{Value: float64(cpu.CfsPeriodUs), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrThrottledCgroup, metric.MetricData{Value: float64(cpu.CPUNrThrottled), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUThrottledPeriodCgroup, metric.MetricData{Value: float64(cpu.CPUNrPeriods), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUThrottledTimeCgroup, metric.MetricData{Value: float64(cpu.CPUThrottledTime), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrRunnableCgroup, metric.MetricData{Value: float64(cpu.TaskNrRunning), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrUninterruptibleCgroup, metric.MetricData{Value: float64(cpu.TaskNrUninterruptible), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrIOWaitCgroup, metric.MetricData{Value: float64(cpu.TaskNrIoWait), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad1MinCgroup, metric.MetricData{Value: cpu.Load.One, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad5MinCgroup, metric.MetricData{Value: cpu.Load.Five, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad15MinCgroup, metric.MetricData{Value: cpu.Load.Fifteen, Time: &updateTime})

	} else if cgStats.CgroupType == "V2" {
		cpu := cgStats.V2.Cpu
		updateTime := time.Unix(cgStats.V2.Cpu.UpdateTime, 0)

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageCgroup, metric.MetricData{Value: cpu.CPUUsageRatio, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageUserCgroup, metric.MetricData{Value: cpu.CPUUserUsageRatio, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUUsageSysCgroup, metric.MetricData{Value: cpu.CPUSysUsageRatio, Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrRunnableCgroup, metric.MetricData{Value: float64(cpu.TaskNrRunning), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrUninterruptibleCgroup, metric.MetricData{Value: float64(cpu.TaskNrUninterruptible), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricCPUNrIOWaitCgroup, metric.MetricData{Value: float64(cpu.TaskNrIoWait), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad1MinCgroup, metric.MetricData{Value: cpu.Load.One, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad5MinCgroup, metric.MetricData{Value: cpu.Load.Five, Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricLoad15MinCgroup, metric.MetricData{Value: cpu.Load.Fifteen, Time: &updateTime})
	}
}

func (m *MalachiteMetricsFetcher) processCgroupMemoryData(cgroupPath string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		mem := cgStats.V1.Memory
		updateTime := time.Unix(cgStats.V1.Memory.UpdateTime, 0)

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemLimitCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.MemoryLimitInBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemUsageCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.MemoryUsageInBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemUsageUserCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.MemoryLimitInBytes - mem.KernMemoryUsageInBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemUsageSysCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.KernMemoryUsageInBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemRssCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.TotalRss)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemCacheCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.TotalCache)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemShmemCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.TotalShmem)})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemDirtyCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.TotalDirty)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemWritebackCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.TotalWriteback)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgfaultCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.TotalPgfault)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgmajfaultCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.TotalPgmajfault)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemAllocstallCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.TotalAllocstall)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemKswapdstealCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.KswapdSteal)})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemOomCgroup, metric.MetricData{Time: &updateTime, Value: float64(mem.OomCnt)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemScaleFactorCgroup, metric.MetricData{Time: &updateTime, Value: general.UIntPointerToFloat64(mem.WatermarkScaleFactor)})
	} else if cgStats.CgroupType == "V2" {
		mem := cgStats.V2.Memory
		updateTime := time.Unix(cgStats.V2.Memory.UpdateTime, 0)
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemUsageCgroup, metric.MetricData{Value: float64(mem.MemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemRssCgroup, metric.MetricData{Value: float64(mem.MemStats.Anon), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemCacheCgroup, metric.MetricData{Value: float64(mem.MemStats.File), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemShmemCgroup, metric.MetricData{Value: float64(mem.MemStats.Shmem), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgfaultCgroup, metric.MetricData{Value: float64(mem.MemStats.Pgfault), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemPgmajfaultCgroup, metric.MetricData{Value: float64(mem.MemStats.Pgmajfault), Time: &updateTime})

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemOomCgroup, metric.MetricData{Value: float64(mem.OomCnt), Time: &updateTime})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricMemScaleFactorCgroup, metric.MetricData{Value: general.UInt64PointerToFloat64(mem.WatermarkScaleFactor), Time: &updateTime})
	}
}

func (m *MalachiteMetricsFetcher) processCgroupBlkIOData(cgroupPath string, cgStats *cgroup.MalachiteCgroupInfo) {

	if cgStats.CgroupType == "V1" {
		updateTime := time.Unix(cgStats.V1.Blkio.UpdateTime, 0)

		io := cgStats.V1.Blkio
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioReadIopsCgroup, metric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsRead - io.OldBpfFsData.FsRead)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioWriteIopsCgroup, metric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsWrite - io.OldBpfFsData.FsWrite)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioReadBpsCgroup, metric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsReadBytes - io.OldBpfFsData.FsReadBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioWriteBpsCgroup, metric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsWriteBytes - io.OldBpfFsData.FsWriteBytes)})
	} else if cgStats.CgroupType == "V2" {
		io := cgStats.V2.Blkio
		updateTime := time.Unix(cgStats.V2.Blkio.UpdateTime, 0)

		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioReadIopsCgroup, metric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsRead - io.OldBpfFsData.FsRead)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioWriteIopsCgroup, metric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsWrite - io.OldBpfFsData.FsWrite)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioReadBpsCgroup, metric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsReadBytes - io.OldBpfFsData.FsReadBytes)})
		m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricBlkioWriteBpsCgroup, metric.MetricData{Time: &updateTime, Value: float64(io.BpfFsData.FsWriteBytes - io.OldBpfFsData.FsWriteBytes)})
	}
}

func (m *MalachiteMetricsFetcher) processCgroupNetData(cgroupPath string, cgStats *cgroup.MalachiteCgroupInfo) {
	updateTime := time.Now()

	var net *cgroup.NetClsCgData
	if cgStats.CgroupType == "V1" {
		net = cgStats.V1.NetCls
		updateTime = time.Unix(cgStats.V1.Blkio.UpdateTime, 0)
	} else if cgStats.CgroupType == "V2" {
		net = cgStats.V2.NetCls
		updateTime = time.Unix(cgStats.V2.Blkio.UpdateTime, 0)
	}
	if net == nil {
		return
	}
	m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricNetTcpSendByteCgroup, metric.MetricData{Time: &updateTime, Value: float64(net.BpfNetData.NetTxBytes - net.OldBpfNetData.NetTxBytes)})
	m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricNetTcpSendPpsCgroup, metric.MetricData{Time: &updateTime, Value: float64(net.BpfNetData.NetTx - net.OldBpfNetData.NetTx)})
	m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricNetTcpRecvByteCgroup, metric.MetricData{Time: &updateTime, Value: float64(net.BpfNetData.NetRxBytes - net.OldBpfNetData.NetRxBytes)})
	m.metricStore.SetCgroupMetric(cgroupPath, consts.MetricNetTcpRecvPpsCgroup, metric.MetricData{Time: &updateTime, Value: float64(net.BpfNetData.NetRx - net.OldBpfNetData.NetRx)})
}

func (m *MalachiteMetricsFetcher) processCgroupPerNumaMemoryData(cgroupPath string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		numaStats := cgStats.V1.Memory.NumaStats
		updateTime := time.Unix(cgStats.V1.Memory.UpdateTime, 0)

		for _, data := range numaStats {
			numaID := strings.TrimPrefix(data.NumaName, "N")
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemTotalPerNumaCgroup, metric.MetricData{Time: &updateTime, Value: float64(data.Total << 10)})
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemFilePerNumaCgroup, metric.MetricData{Time: &updateTime, Value: float64(data.File << 10)})
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemAnonPerNumaCgroup, metric.MetricData{Time: &updateTime, Value: float64(data.Anon << 10)})
		}
	} else if cgStats.CgroupType == "V2" {
		numaStats := cgStats.V2.Memory.MemNumaStats
		updateTime := time.Unix(cgStats.V2.Memory.UpdateTime, 0)

		for numa, data := range numaStats {
			numaID := strings.TrimPrefix(numa, "N")
			total := data.Anon + data.File + data.Unevictable
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemTotalPerNumaCgroup, metric.MetricData{Time: &updateTime, Value: float64(total << 10)})
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemFilePerNumaCgroup, metric.MetricData{Time: &updateTime, Value: float64(data.File << 10)})
			m.metricStore.SetCgroupNumaMetric(cgroupPath, numaID, consts.MetricsMemAnonPerNumaCgroup, metric.MetricData{Time: &updateTime, Value: float64(data.Anon << 10)})
		}
	}
}

func (m *MalachiteMetricsFetcher) processContainerCPUData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	m.processContainerMemBandwidth(podUID, containerName, cgStats)

	cyclesOld, _ := m.GetContainerMetric(podUID, containerName, consts.MetricCPUCyclesContainer)
	instructionsOld, _ := m.GetContainerMetric(podUID, containerName, consts.MetricCPUInstructionsContainer)

	if cgStats.CgroupType == "V1" {
		cpu := cgStats.V1.Cpu
		updateTime := time.Unix(cgStats.V1.Cpu.UpdateTime, 0)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPULimitContainer,
			metric.MetricData{Value: float64(cpu.CfsQuotaUs) / float64(cpu.CfsPeriodUs), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer,
			metric.MetricData{Value: cpu.CPUUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageUserContainer,
			metric.MetricData{Value: cpu.CPUUserUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageSysContainer,
			metric.MetricData{Value: cpu.CPUSysUsageRatio, Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUShareContainer,
			metric.MetricData{Value: float64(cpu.CPUShares), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUQuotaContainer,
			metric.MetricData{Value: float64(cpu.CfsQuotaUs), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUPeriodContainer,
			metric.MetricData{Value: float64(cpu.CfsPeriodUs), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrThrottledContainer,
			metric.MetricData{Value: float64(cpu.CPUNrThrottled), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUThrottledPeriodContainer,
			metric.MetricData{Value: float64(cpu.CPUNrPeriods), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUThrottledTimeContainer,
			metric.MetricData{Value: float64(cpu.CPUThrottledTime), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrRunnableContainer,
			metric.MetricData{Value: float64(cpu.TaskNrRunning), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrUninterruptibleContainer,
			metric.MetricData{Value: float64(cpu.TaskNrUninterruptible), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrIOWaitContainer,
			metric.MetricData{Value: float64(cpu.TaskNrIoWait), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad1MinContainer,
			metric.MetricData{Value: cpu.Load.One, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad5MinContainer,
			metric.MetricData{Value: cpu.Load.Five, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad15MinContainer,
			metric.MetricData{Value: cpu.Load.Fifteen, Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricOCRReadDRAMsContainer,
			metric.MetricData{Value: float64(cpu.OCRReadDRAMs), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricIMCWriteContainer,
			metric.MetricData{Value: float64(cpu.IMCWrites), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreAllInsContainer,
			metric.MetricData{Value: float64(cpu.StoreAllInstructions), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreInsContainer,
			metric.MetricData{Value: float64(cpu.StoreInstructions), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricUpdateTimeContainer,
			metric.MetricData{Value: float64(cpu.UpdateTime), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCyclesContainer,
			metric.MetricData{Value: float64(cpu.Cycles), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUInstructionsContainer,
			metric.MetricData{Value: float64(cpu.Instructions), Time: &updateTime})

		if cyclesOld.Value > 0 && instructionsOld.Value > 0 {
			instructionDiff := float64(cpu.Instructions) - instructionsOld.Value
			if instructionDiff > 0 {
				cpi := (float64(cpu.Cycles) - cyclesOld.Value) / instructionDiff
				m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCPIContainer,
					metric.MetricData{Value: cpi, Time: &updateTime})
			}
		}

	} else if cgStats.CgroupType == "V2" {
		cpu := cgStats.V2.Cpu
		updateTime := time.Unix(cgStats.V2.Cpu.UpdateTime, 0)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer,
			metric.MetricData{Value: cpu.CPUUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageUserContainer,
			metric.MetricData{Value: cpu.CPUUserUsageRatio, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUUsageSysContainer,
			metric.MetricData{Value: cpu.CPUSysUsageRatio, Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrRunnableContainer,
			metric.MetricData{Value: float64(cpu.TaskNrRunning), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrUninterruptibleContainer,
			metric.MetricData{Value: float64(cpu.TaskNrUninterruptible), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUNrIOWaitContainer,
			metric.MetricData{Value: float64(cpu.TaskNrIoWait), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad1MinContainer,
			metric.MetricData{Value: cpu.Load.One, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad5MinContainer,
			metric.MetricData{Value: cpu.Load.Five, Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricLoad15MinContainer,
			metric.MetricData{Value: cpu.Load.Fifteen, Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricOCRReadDRAMsContainer,
			metric.MetricData{Value: float64(cpu.OCRReadDRAMs), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricIMCWriteContainer,
			metric.MetricData{Value: float64(cpu.IMCWrites), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreAllInsContainer,
			metric.MetricData{Value: float64(cpu.StoreAllInstructions), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricStoreInsContainer,
			metric.MetricData{Value: float64(cpu.StoreInstructions), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricUpdateTimeContainer,
			metric.MetricData{Value: float64(cpu.UpdateTime), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCyclesContainer,
			metric.MetricData{Value: float64(cpu.Cycles), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUInstructionsContainer,
			metric.MetricData{Value: float64(cpu.Instructions), Time: &updateTime})

		if cyclesOld.Value > 0 && instructionsOld.Value > 0 {
			instructionDiff := float64(cpu.Instructions) - instructionsOld.Value
			if instructionDiff > 0 {
				cpi := (float64(cpu.Cycles) - cyclesOld.Value) / instructionDiff
				m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUCPIContainer,
					metric.MetricData{Value: cpi, Time: &updateTime})
			}
		}
	}
}

func (m *MalachiteMetricsFetcher) processContainerMemoryData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		mem := cgStats.V1.Memory
		updateTime := time.Unix(cgStats.V1.Memory.UpdateTime, 0)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemLimitContainer,
			metric.MetricData{Value: float64(mem.MemoryLimitInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageContainer,
			metric.MetricData{Value: float64(mem.MemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageUserContainer,
			metric.MetricData{Value: float64(mem.MemoryLimitInBytes - mem.KernMemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageSysContainer,
			metric.MetricData{Value: float64(mem.KernMemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemRssContainer,
			metric.MetricData{Value: float64(mem.TotalRss), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemCacheContainer,
			metric.MetricData{Value: float64(mem.TotalCache), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemShmemContainer,
			metric.MetricData{Value: float64(mem.TotalShmem), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemDirtyContainer,
			metric.MetricData{Value: float64(mem.TotalDirty), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemWritebackContainer,
			metric.MetricData{Value: float64(mem.TotalWriteback), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgfaultContainer,
			metric.MetricData{Value: float64(mem.TotalPgfault), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgmajfaultContainer,
			metric.MetricData{Value: float64(mem.TotalPgmajfault), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemAllocstallContainer,
			metric.MetricData{Value: float64(mem.TotalAllocstall), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemKswapdstealContainer,
			metric.MetricData{Value: float64(mem.KswapdSteal), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemOomContainer,
			metric.MetricData{Value: float64(mem.OomCnt), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemScaleFactorContainer,
			metric.MetricData{Value: general.UIntPointerToFloat64(mem.WatermarkScaleFactor), Time: &updateTime})
	} else if cgStats.CgroupType == "V2" {
		mem := cgStats.V2.Memory
		updateTime := time.Unix(cgStats.V2.Memory.UpdateTime, 0)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemUsageContainer,
			metric.MetricData{Value: float64(mem.MemoryUsageInBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemRssContainer,
			metric.MetricData{Value: float64(mem.MemStats.Anon), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemCacheContainer,
			metric.MetricData{Value: float64(mem.MemStats.File), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemShmemContainer,
			metric.MetricData{Value: float64(mem.MemStats.Shmem), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgfaultContainer,
			metric.MetricData{Value: float64(mem.MemStats.Pgfault), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemPgmajfaultContainer,
			metric.MetricData{Value: float64(mem.MemStats.Pgmajfault), Time: &updateTime})

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemOomContainer,
			metric.MetricData{Value: float64(mem.OomCnt), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMemScaleFactorContainer,
			metric.MetricData{Value: general.UInt64PointerToFloat64(mem.WatermarkScaleFactor), Time: &updateTime})
	}
}

func (m *MalachiteMetricsFetcher) processContainerBlkIOData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		io := cgStats.V1.Blkio
		updateTime := time.Unix(cgStats.V1.Blkio.UpdateTime, 0)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioReadIopsContainer,
			metric.MetricData{Value: float64(io.BpfFsData.FsRead - io.OldBpfFsData.FsRead), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioWriteIopsContainer,
			metric.MetricData{Value: float64(io.BpfFsData.FsWrite - io.OldBpfFsData.FsWrite), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioReadBpsContainer,
			metric.MetricData{Value: float64(io.BpfFsData.FsReadBytes - io.OldBpfFsData.FsReadBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioWriteBpsContainer,
			metric.MetricData{Value: float64(io.BpfFsData.FsWriteBytes - io.OldBpfFsData.FsWriteBytes), Time: &updateTime})
	} else if cgStats.CgroupType == "V2" {
		io := cgStats.V2.Blkio
		updateTime := time.Unix(cgStats.V2.Blkio.UpdateTime, 0)

		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioReadIopsContainer,
			metric.MetricData{Value: float64(io.BpfFsData.FsRead - io.OldBpfFsData.FsRead), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioWriteIopsContainer,
			metric.MetricData{Value: float64(io.BpfFsData.FsWrite - io.OldBpfFsData.FsWrite), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioReadBpsContainer,
			metric.MetricData{Value: float64(io.BpfFsData.FsReadBytes - io.OldBpfFsData.FsReadBytes), Time: &updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricBlkioWriteBpsContainer,
			metric.MetricData{Value: float64(io.BpfFsData.FsWriteBytes - io.OldBpfFsData.FsWriteBytes), Time: &updateTime})
	}
}

func (m *MalachiteMetricsFetcher) processContainerNetData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	var net *cgroup.NetClsCgData
	var updateTime time.Time
	if cgStats.CgroupType == "V1" {
		net = cgStats.V1.NetCls
		updateTime = time.Unix(cgStats.V1.NetCls.UpdateTime, 0)
	} else if cgStats.CgroupType == "V2" {
		net = cgStats.V2.NetCls
		updateTime = time.Unix(cgStats.V2.NetCls.UpdateTime, 0)
	}

	if net == nil {
		return
	}

	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpSendByteContainer,
		metric.MetricData{Value: float64(net.BpfNetData.NetTxBytes - net.OldBpfNetData.NetTxBytes), Time: &updateTime})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpSendPpsContainer,
		metric.MetricData{Value: float64(net.BpfNetData.NetTx - net.OldBpfNetData.NetTx), Time: &updateTime})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpRecvByteContainer,
		metric.MetricData{Value: float64(net.BpfNetData.NetRxBytes - net.OldBpfNetData.NetRxBytes), Time: &updateTime})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpRecvPpsContainer,
		metric.MetricData{Value: float64(net.BpfNetData.NetRx - net.OldBpfNetData.NetRx), Time: &updateTime})
}

func (m *MalachiteMetricsFetcher) processContainerPerfData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	var perf *cgroup.PerfEventData
	var updateTime time.Time
	if cgStats.CgroupType == "V1" {
		perf = cgStats.V1.PerfEvent
		updateTime = time.Unix(cgStats.V1.PerfEvent.UpdateTime, 0)
	} else if cgStats.CgroupType == "V2" {
		perf = cgStats.V2.PerfEvent
		updateTime = time.Unix(cgStats.V2.PerfEvent.UpdateTime, 0)
	}

	if perf == nil {
		return
	}

	// cpu cycles and instructions are collected from container's cgroup data, and cpi is derived from them
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUICacheMissContainer,
		metric.MetricData{Value: perf.IcacheMiss, Time: &updateTime})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUL2CacheMissContainer,
		metric.MetricData{Value: perf.L2CacheMiss, Time: &updateTime})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricCPUL3CacheMissContainer,
		metric.MetricData{Value: perf.L3CacheMiss, Time: &updateTime})
}

func (m *MalachiteMetricsFetcher) processContainerPerNumaMemoryData(podUID, containerName string, cgStats *cgroup.MalachiteCgroupInfo) {
	if cgStats.CgroupType == "V1" {
		numaStats := cgStats.V1.Memory.NumaStats
		updateTime := time.Unix(cgStats.V1.Memory.UpdateTime, 0)

		for _, data := range numaStats {
			numaID := strings.TrimPrefix(data.NumaName, "N")
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemTotalPerNumaContainer,
				metric.MetricData{Value: float64(data.Total << 10), Time: &updateTime})
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemFilePerNumaContainer,
				metric.MetricData{Value: float64(data.File << 10), Time: &updateTime})
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemAnonPerNumaContainer,
				metric.MetricData{Value: float64(data.Anon << 10), Time: &updateTime})
		}
	} else if cgStats.CgroupType == "V2" {
		numaStats := cgStats.V2.Memory.MemNumaStats
		updateTime := time.Unix(cgStats.V2.Memory.UpdateTime, 0)

		for numa, data := range numaStats {
			numaID := strings.TrimPrefix(numa, "N")
			total := data.Anon + data.File + data.Unevictable
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemTotalPerNumaContainer,
				metric.MetricData{Value: float64(total << 10), Time: &updateTime})
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemFilePerNumaContainer,
				metric.MetricData{Value: float64(data.File << 10), Time: &updateTime})
			m.metricStore.SetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemAnonPerNumaContainer,
				metric.MetricData{Value: float64(data.Anon << 10), Time: &updateTime})
		}
	}
}
