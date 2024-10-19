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

package rodan

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/rodan/client"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/rodan/types"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	pageShift = 12
)

type RodanMetricsProvisioner struct {
	client      *client.RodanClient
	metricStore *utilmetric.MetricStore
	podFetcher  pod.PodFetcher
	emitter     metrics.MetricEmitter

	synced bool
}

// NewRodanMetricsProvisioner returns the fetcher that fetch metrics by Inspector client
func NewRodanMetricsProvisioner(
	_ *global.BaseConfiguration,
	metricConf *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter,
	fetcher pod.PodFetcher,
	metricStore *utilmetric.MetricStore,
) metrictypes.MetricsProvisioner {
	return &RodanMetricsProvisioner{
		metricStore: metricStore,
		podFetcher:  fetcher,
		client:      client.NewRodanClient(fetcher, nil, metricConf.RodanServerPort),
		emitter:     emitter,
		synced:      false,
	}
}

func (i *RodanMetricsProvisioner) Run(ctx context.Context) {
	i.sample(ctx)
}

func (i *RodanMetricsProvisioner) sample(ctx context.Context) {
	i.updateNodeStats()
	i.updateNUMAStats()
	i.updateNodeCgroupStats()
	i.updateNodeSysctlStats()
	i.updateCoreStats()
	i.updatePodStats(ctx)

	i.synced = true
}

func (i *RodanMetricsProvisioner) HasSynced() bool {
	return i.synced
}

func (i *RodanMetricsProvisioner) updateNodeStats() {
	// update node memory stats
	nodeMemoryData, err := i.client.GetNodeMemoryStats()
	if err != nil {
		klog.Errorf("[inspector] get node memory stats failed, err: %v", err)
	} else {
		i.processNodeMemoryData(nodeMemoryData)
	}
}

// updateNodeCgroupStats update only besteffort and burstable QoS level cgroup stats
func (i *RodanMetricsProvisioner) updateNodeCgroupStats() {
	// update cgroup memory stats
	memoryCgroupData, err := i.client.GetNodeCgroupMemoryStats()
	if err != nil {
		klog.Errorf("[inspector] get memory cgroup stats failed, err: %v", err)
	} else {
		i.processCgroupMemoryData(memoryCgroupData)
	}
}

func (i *RodanMetricsProvisioner) updateNodeSysctlStats() {
	// update node sysctl data
	sysctlData, err := i.client.GetNodeSysctl()
	if err != nil {
		klog.Errorf("[inspector] get node sysctl failed, err: %v", err)
	} else {
		i.processNodeSysctlData(sysctlData)
	}
}

func (i *RodanMetricsProvisioner) updateNUMAStats() {
	// update NUMA memory stats
	NUMAMemoryData, err := i.client.GetNUMAMemoryStats()
	if err != nil {
		klog.Errorf("[inspector] get NUMA memory stats failed, err: %v", err)
	} else {
		i.processNUMAMemoryData(NUMAMemoryData)
	}
}

func (i *RodanMetricsProvisioner) updateCoreStats() {
	// update core CPU stats
	coreCPUData, err := i.client.GetCoreCPUStats()
	if err != nil {
		klog.Errorf("[inspector] get core CPU stats failed, err: %v", err)
	} else {
		i.processCoreCPUData(coreCPUData)
	}
}

func (i *RodanMetricsProvisioner) updatePodStats(ctx context.Context) {
	// list all pods
	pods, err := i.podFetcher.GetPodList(ctx, func(_ *v1.Pod) bool { return true })
	if err != nil {
		klog.Errorf("[inspector] GetPodList fail: %v", err)
		return
	}

	podUIDSet := make(map[string]bool)
	for _, pod := range pods {
		podUIDSet[string(pod.UID)] = true
		cpuStats, err := i.client.GetPodContainerCPUStats(ctx, string(pod.UID))
		if err != nil {
			klog.Errorf("[inspector] get container CPU stats failed, pod: %v, err: %v", pod.Name, err)
		} else {
			for containerName, containerCPUStats := range cpuStats {
				i.processContainerCPUData(string(pod.UID), containerName, containerCPUStats)
			}
		}

		cgroupMemStats, err := i.client.GetPodContainerCgroupMemStats(ctx, string(pod.UID))
		if err != nil {
			klog.Errorf("[inspector] get container cgroupmem stats failed, pod: %v, err: %v", pod.Name, err)
		} else {
			for containerName, containerCgroupMem := range cgroupMemStats {
				i.processContainerCgroupMemData(string(pod.UID), containerName, containerCgroupMem)
			}
		}

		loadStats, err := i.client.GetPodContainerLoadStats(ctx, string(pod.UID))
		if err != nil {
			klog.Errorf("[inspector] get container load stats failed, pod: %v, err: %v", pod.Name, err)
		} else {
			for containerName, containerLoad := range loadStats {
				i.processContainerLoadData(string(pod.UID), containerName, containerLoad)
			}
		}

		cghardware, err := i.client.GetPodContainerCghardwareStats(ctx, string(pod.UID))
		if err != nil {
			klog.Errorf("[inspector] get container cghardware failed, pod: %v, err: %v", pod.Name, err)
		} else {
			for containerName, containerCghardware := range cghardware {
				i.processContainerCghardwareData(string(pod.UID), containerName, containerCghardware)
			}
		}

		cgNumaStats, err := i.client.GetPodContainerCgNumaStats(ctx, string(pod.UID))
		if err != nil {
			klog.Errorf("[inspector] get container numa stats failed, pod: %v, err: %v", pod.Name, err)
		} else {
			for containerName, containerNumaStats := range cgNumaStats {
				i.processContainerNumaData(string(pod.UID), containerName, containerNumaStats)
			}
		}
	}
	i.metricStore.GCPodsMetric(podUIDSet)
}

func (i *RodanMetricsProvisioner) processNodeMemoryData(nodeMemoryData []types.Cell) {
	updateTime := time.Now()

	metricMap := types.MetricsMap[types.NodeMemoryPath]

	for _, cell := range nodeMemoryData {
		metricName, ok := metricMap[cell.Key]
		if !ok {
			continue
		}
		switch cell.Key {
		case "memory_pgsteal_kswapd":
			i.metricStore.SetNodeMetric(
				metricName,
				utilmetric.MetricData{Value: cell.Val, Time: &updateTime},
			)
		default:
			i.metricStore.SetNodeMetric(
				metricName,
				utilmetric.MetricData{Value: float64(int(cell.Val) << 10), Time: &updateTime},
			)
		}
	}
}

func (i *RodanMetricsProvisioner) processNodeSysctlData(nodeSysctlData []types.Cell) {
	updateTime := time.Now()

	metricMap := types.MetricsMap[types.NodeSysctlPath]

	for _, cell := range nodeSysctlData {
		metricName, ok := metricMap[cell.Key]
		if !ok {
			continue
		}

		i.metricStore.SetNodeMetric(
			metricName,
			utilmetric.MetricData{Value: cell.Val, Time: &updateTime},
		)

	}
}

func (i *RodanMetricsProvisioner) processCgroupMemoryData(cgroupMemoryData []types.Cell) {
	updateTime := time.Now()

	metricMap := types.MetricsMap[types.NodeCgroupMemoryPath]
	for _, cell := range cgroupMemoryData {
		metricName, ok := metricMap[cell.Key]
		if !ok {
			continue
		}

		switch cell.Key {
		case "qosgroupmem_besteffort_memory_rss", "qosgroupmem_besteffort_memory_usage":
			i.metricStore.SetCgroupMetric(common.CgroupFsRootPathBestEffort, metricName,
				utilmetric.MetricData{Value: cell.Val, Time: &updateTime})
		case "qosgroupmem_burstable_memory_rss", "qosgroupmem_burstable_memory_usage":
			i.metricStore.SetCgroupMetric(common.CgroupFsRootPathBurstable, metricName,
				utilmetric.MetricData{Value: cell.Val, Time: &updateTime})
		}
	}
}

func (i *RodanMetricsProvisioner) processNUMAMemoryData(NUMAMemoryData map[int][]types.Cell) {
	updateTime := time.Now()

	metricMap := types.MetricsMap[types.NumaMemoryPath]

	for numaID, cells := range NUMAMemoryData {
		for _, cell := range cells {
			metricName, ok := metricMap[cell.Key]
			if !ok {
				continue
			}

			i.metricStore.SetNumaMetric(
				numaID,
				metricName,
				utilmetric.MetricData{Value: cell.Val, Time: &updateTime},
			)
		}
	}
}

func (i *RodanMetricsProvisioner) processCoreCPUData(coreCPUData map[int][]types.Cell) {
	updateTime := time.Now()

	metricMap := types.MetricsMap[types.NodeCPUPath]

	for cpuID, coreData := range coreCPUData {
		for _, cell := range coreData {
			metricName, ok := metricMap[cell.Key]
			if !ok {
				continue
			}

			switch cell.Key {
			case "usage":
				// node cpu usage if cpuID == -1
				if cpuID == -1 {
					i.metricStore.SetNodeMetric(
						consts.MetricCPUUsageRatio,
						utilmetric.MetricData{Value: cell.Val / 100.0, Time: &updateTime},
					)
				} else {
					i.metricStore.SetCPUMetric(
						cpuID,
						consts.MetricCPUUsageRatio,
						utilmetric.MetricData{Value: cell.Val / 100.0, Time: &updateTime},
					)
				}
			case "sched_wait":
				i.metricStore.SetCPUMetric(
					cpuID,
					consts.MetricCPUSchedwait,
					utilmetric.MetricData{Value: cell.Val * 1000, Time: &updateTime},
				)
			default:
				i.metricStore.SetCPUMetric(
					cpuID,
					metricName,
					utilmetric.MetricData{Value: cell.Val, Time: &updateTime},
				)
			}
		}
	}
}

func (i *RodanMetricsProvisioner) processContainerCPUData(podUID, containerName string, cpuData []types.Cell) {
	var (
		updateTime = time.Now()
		metricMap  = types.MetricsMap[types.ContainerCPUPath]
	)

	for _, cell := range cpuData {
		metricName, ok := metricMap[cell.Key]
		if !ok {
			continue
		}

		switch cell.Key {
		case "cgcpu_usage":
			i.metricStore.SetContainerMetric(
				podUID,
				containerName,
				metricName,
				utilmetric.MetricData{Value: cell.Val / 100.0, Time: &updateTime},
			)
		default:
			i.metricStore.SetContainerMetric(
				podUID,
				containerName,
				metricName,
				utilmetric.MetricData{Value: cell.Val, Time: &updateTime},
			)
		}
	}
}

func (i *RodanMetricsProvisioner) processContainerCghardwareData(podUID, containerName string, cghardwareData []types.Cell) {
	var (
		updateTime = time.Now()
		metricMap  = types.MetricsMap[types.ContainerCghardwarePath]

		cyclesOld, _         = i.metricStore.GetContainerMetric(podUID, containerName, consts.MetricCPUCyclesContainer)
		instructionsOld, _   = i.metricStore.GetContainerMetric(podUID, containerName, consts.MetricCPUInstructionsContainer)
		cycles, instructions float64
	)

	for _, cell := range cghardwareData {
		metricName, ok := metricMap[cell.Key]
		if !ok {
			continue
		}

		i.metricStore.SetContainerMetric(
			podUID,
			containerName,
			metricName,
			utilmetric.MetricData{Value: cell.Val, Time: &updateTime},
		)

		if cell.Key == "cycles" {
			cycles = cell.Val
		}
		if cell.Key == "instructions" {
			instructions = cell.Val
		}
	}
	if cyclesOld.Value > 0 && cycles > 0 && instructionsOld.Value > 0 && instructions > 0 {
		instructionDiff := instructions - instructionsOld.Value
		if instructionDiff > 0 {
			cpi := (cycles - cyclesOld.Value) / instructionDiff
			i.metricStore.SetContainerMetric(
				podUID,
				containerName,
				consts.MetricCPUCPIContainer,
				utilmetric.MetricData{Value: cpi, Time: &updateTime},
			)
		}
	}
}

func (i *RodanMetricsProvisioner) processContainerCgroupMemData(podUID, containerName string, cgroupMemData []types.Cell) {
	updateTime := time.Now()

	metricMap := types.MetricsMap[types.ContainerCgroupMemoryPath]

	for _, cell := range cgroupMemData {
		metricName, ok := metricMap[cell.Key]
		if !ok {
			continue
		}

		i.metricStore.SetContainerMetric(
			podUID,
			containerName,
			metricName,
			utilmetric.MetricData{Value: cell.Val, Time: &updateTime},
		)
	}
}

func (i *RodanMetricsProvisioner) processContainerLoadData(podUID, containerName string, loadData []types.Cell) {
	updateTime := time.Now()

	metricMap := types.MetricsMap[types.ContainerLoadPath]

	for _, cell := range loadData {
		metricName, ok := metricMap[cell.Key]
		if !ok {
			continue
		}

		switch cell.Key {
		case "loadavg_loadavg1", "loadavg_loadavg5", "loadavg_loadavg15":
			i.metricStore.SetContainerMetric(
				podUID,
				containerName,
				metricName,
				utilmetric.MetricData{Value: cell.Val / 100.0, Time: &updateTime},
			)
		default:
			i.metricStore.SetContainerMetric(
				podUID,
				containerName,
				metricName,
				utilmetric.MetricData{Value: cell.Val, Time: &updateTime},
			)
		}

	}
}

func (i *RodanMetricsProvisioner) processContainerNumaData(podUID, containerName string, containerNumaData map[int][]types.Cell) {
	updateTime := time.Now()

	metricMap := types.MetricsMap[types.ContainerNumaStatPath]

	for numaNode, cells := range containerNumaData {
		for _, cell := range cells {
			metricName, ok := metricMap[cell.Key]
			if !ok {
				continue
			}

			switch cell.Key {
			case "filepage":
				i.metricStore.SetContainerNumaMetric(podUID, containerName, numaNode, metricName,
					utilmetric.MetricData{Value: float64(int(cell.Val) << pageShift), Time: &updateTime})
			default:

			}
		}
	}
}
