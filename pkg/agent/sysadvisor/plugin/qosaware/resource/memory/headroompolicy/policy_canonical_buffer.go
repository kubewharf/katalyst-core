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

package headroompolicy

import (
	"context"
	"fmt"
	"math"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// getSystemMemoryInfo returns the total memory and available memory of the system in bytes
func (p *PolicyCanonical) calculateMemoryBuffer(estimateNonReclaimedRequirement float64) (float64, error) {
	var (
		systemMetrics               *systemMemoryMetrics
		systemQoSMetrics            *memoryMetrics
		sharedAndDedicateQoSMetrics *memoryMetrics
		nodeCPUMemoryRatio          float64
		err                         error
	)

	systemMetrics, err = p.getSystemMemoryInfo()
	if err != nil {
		return 0, err
	}

	sharedAndDedicateQoSMetrics, err = p.getMemoryMetrics(p.filterSharedAndDedicateQoSPods)
	if err != nil {
		return 0, err
	}

	systemQoSMetrics, err = p.getMemoryMetrics(p.filterSystemQoSPods)
	if err != nil {
		return 0, err
	}

	nodeCPUMemoryRatio, err = p.getNodeCPUMemoryRatio()
	if err != nil {
		return 0, err
	}

	// calculate system buffer with double scale_factor to make kswapd less happened
	systemFactor := systemMetrics.memoryTotal * 2 * systemMetrics.scaleFactor / 10000
	systemBuffer := systemMetrics.memoryFree*p.policyCanonicalConfig.FreeBasedRatio - systemFactor

	// calculate shared and dedicate qos total used
	sharedAndDedicateQoSTotalUsed := sharedAndDedicateQoSMetrics.rss + sharedAndDedicateQoSMetrics.shmem +
		sharedAndDedicateQoSMetrics.cache

	// calculate system qos extra usage with only shmem and cache
	systemQoSExtraUsage := systemQoSMetrics.cache + systemQoSMetrics.shmem

	// calculate non-reclaimed qos extra usage
	nonReclaimedQoSExtraUsage := estimateNonReclaimedRequirement - sharedAndDedicateQoSTotalUsed + systemQoSExtraUsage

	// calculate buffer using system buffer to subtract non-reclaimed qos extra usage
	buffer := math.Max(systemBuffer-nonReclaimedQoSExtraUsage, 0)

	// calculate cache oversold buffer if cache oversold rate is set and cpu/memory ratio is in range and memory utilization is not 0
	if p.policyCanonicalConfig.CacheBasedRatio > 0 && nodeCPUMemoryRatio > p.policyCanonicalConfig.CPUMemRatioLowerBound &&
		nodeCPUMemoryRatio < p.policyCanonicalConfig.CPUMemRatioUpperBound && systemMetrics.memoryUtilization > 0 {
		cacheBuffer := (systemMetrics.memoryTotal*(1-systemMetrics.memoryUtilization) - systemMetrics.memoryFree) *
			p.policyCanonicalConfig.CacheBasedRatio
		buffer = math.Max(buffer, cacheBuffer)
		general.Infof("cache oversold rate: %.2f, cache buffer: %.2e, buffer: %.2e",
			p.policyCanonicalConfig.CacheBasedRatio, cacheBuffer, buffer)
	}

	// add static oversold buffer
	result := buffer + p.policyCanonicalConfig.StaticBasedCapacity
	general.Infof("system buffer: %.2e, non-reclaimed QoS extra usage: %.2e, static oversold: %.2e, result: %.2e",
		systemBuffer, nonReclaimedQoSExtraUsage, p.policyCanonicalConfig.StaticBasedCapacity, result)
	return result, nil
}

type systemMemoryMetrics struct {
	memoryTotal       float64
	memoryFree        float64
	scaleFactor       float64
	memoryUtilization float64
}

// getSystemMemoryInfo get system memory info from meta server
func (p *PolicyCanonical) getSystemMemoryInfo() (*systemMemoryMetrics, error) {
	var (
		info systemMemoryMetrics
		err  error
	)

	info.memoryTotal, err = p.metaServer.GetNodeMetric(consts.MetricMemTotalSystem)
	if err != nil {
		return nil, err
	}

	info.memoryFree, err = p.metaServer.GetNodeMetric(consts.MetricMemFreeSystem)
	if err != nil {
		return nil, err
	}

	info.scaleFactor, err = p.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		return nil, err
	}

	used, err := p.metaServer.GetNodeMetric(consts.MetricMemUsedSystem)
	if err != nil {
		return nil, err
	}
	info.memoryUtilization = used / info.memoryTotal

	return &info, nil
}

// getNodeCPUMemoryRatio get node memory/cpu ratio from meta server
func (p *PolicyCanonical) getNodeCPUMemoryRatio() (float64, error) {
	cpuCapacity := p.metaServer.MachineInfo.NumCores
	memoryCapacity := p.metaServer.MemoryCapacity
	if memoryCapacity == 0 {
		return 0, fmt.Errorf("memory capacity is 0")
	}

	return float64(cpuCapacity) / (float64(memoryCapacity) / 1024 / 1024 / 1024), nil
}

type memoryMetrics struct {
	cache float64
	shmem float64
	rss   float64
}

// getMemoryMetrics get memory metrics from meta server with filter
func (p *PolicyCanonical) getMemoryMetrics(filter func(pod *v1.Pod) bool) (*memoryMetrics, error) {
	regionPods, err := p.metaServer.GetPodList(context.Background(), filter)
	if err != nil {
		return nil, err
	}

	cache := p.metaServer.AggregatePodMetric(regionPods, consts.MetricMemCacheContainer, metric.AggregatorSum, metric.DefaultContainerMetricFilter)
	shmem := p.metaServer.AggregatePodMetric(regionPods, consts.MetricMemShmemContainer, metric.AggregatorSum, metric.DefaultContainerMetricFilter)
	rss := p.metaServer.AggregatePodMetric(regionPods, consts.MetricMemRssContainer, metric.AggregatorSum, metric.DefaultContainerMetricFilter)
	return &memoryMetrics{
		cache: cache,
		shmem: shmem,
		rss:   rss,
	}, nil
}

// filterSystemQoSPods filter system qos pods
func (p *PolicyCanonical) filterSystemQoSPods(pod *v1.Pod) bool {
	if ok, err := p.qosConfig.CheckSystemQoSForPod(pod); err != nil {
		klog.Errorf("filter pod %v err: %v", pod.Name, err)
		return false
	} else {
		return ok
	}
}

// filterSharedAndDedicateQoSPods filter shared and dedicate qos pods
func (p *PolicyCanonical) filterSharedAndDedicateQoSPods(pod *v1.Pod) bool {
	isSharedQoS, err := p.qosConfig.CheckSharedQoSForPod(pod)
	if err != nil {
		klog.Errorf("filter pod %v err: %v", pod.Name, err)
		return false
	}

	isDedicateQoS, err := p.qosConfig.CheckDedicatedQoSForPod(pod)
	if err != nil {
		klog.Errorf("filter pod %v err: %v", pod.Name, err)
		return false
	}

	return isSharedQoS || isDedicateQoS
}
