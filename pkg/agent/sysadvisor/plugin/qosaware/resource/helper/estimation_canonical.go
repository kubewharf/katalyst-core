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

package helper

import (
	"fmt"
	"math"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	metaserverHelper "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// metricRequest indicates using request as estimation
	metricRequest string = "request"

	// referenceFallback indicates using pod estimation fallback value
	metricFallback string = "fallback"

	// metricRaw indicates using pod metrics value
	metricRaw string = "raw metrics"
)

const (
	estimationCPUFallbackValue            = 4.0
	estimationMemoryFallbackValue float64 = 8 << 30

	estimationSharedDedicateQoSContainerCPUUsageBufferRatio = 1.25
	estimationSharedDedicateQoSContainerBufferRatio         = 1.1
	estimationSystemQoSContainerBufferRatio                 = 1.0
)

var (
	cpuMetricsToGather = []string{
		consts.MetricCPUUsageContainer,
		consts.MetricLoad1MinContainer,
		consts.MetricLoad5MinContainer,
	}

	memoryMetricsToGatherForSharedAndDedicatedQoS = []string{
		consts.MetricMemRssContainer,
		consts.MetricMemCacheContainer,
		consts.MetricMemShmemContainer,
	}

	memoryMetricsToGatherForSystemQoS = []string{
		consts.MetricMemRssContainer,
	}
)

// EstimateContainerCPUUsage estimates non-reclaimed container cpu usage.
// Use cpu request if metrics are missing or reclaimEnable is false.
func EstimateContainerCPUUsage(ci *types.ContainerInfo, metaReader metacache.MetaReader, reclaimEnable bool) (float64, error) {
	if ci == nil {
		return 0, fmt.Errorf("containerInfo nil")
	}

	if metaReader == nil {
		return 0, fmt.Errorf("metaCache nil")
	}

	if ci.QoSLevel != apiconsts.PodAnnotationQoSLevelSharedCores && ci.QoSLevel != apiconsts.PodAnnotationQoSLevelDedicatedCores {
		return 0, nil
	}

	var (
		estimation float64 = 0
		reference  string
	)

	checkRequest := true
	if reclaimEnable {
		for _, metricName := range cpuMetricsToGather {
			metricValue, err := metaReader.GetContainerMetric(ci.PodUID, ci.ContainerName, metricName)
			general.Infof("pod %v container %v metric %v value %v, err %v", ci.PodName, ci.ContainerName, metricName, metricValue, err)
			if err != nil || metricValue.Value <= 0 {
				continue
			}
			checkRequest = false

			estimationMetric := metricValue.Value
			if metricName == consts.MetricCPUUsageContainer {
				estimationMetric *= estimationSharedDedicateQoSContainerCPUUsageBufferRatio
			}

			if estimationMetric > estimation {
				estimation = estimationMetric
				reference = metricName
			}
		}
	}

	if checkRequest {
		request := ci.CPURequest
		general.Infof("pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricRequest, request)
		if request > estimation {
			estimation = request
			reference = metricRequest
		}
	}

	if estimation <= 0 {
		estimation = estimationCPUFallbackValue
		reference = metricFallback
		general.Infof("pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricFallback, estimationCPUFallbackValue)
	}

	general.Infof("pod %v container %v estimation %.2f reference %v", ci.PodName, ci.ContainerName, estimation, reference)
	return estimation, nil
}

// EstimateContainerMemoryUsage estimates non-reclaimed container memory usage.
// Use memory request if metrics are missing or reclaimEnable is false.
func EstimateContainerMemoryUsage(ci *types.ContainerInfo, metaReader metacache.MetaReader, reclaimEnable bool) (float64, error) {
	if ci == nil {
		return 0, fmt.Errorf("containerInfo nil")
	}

	if metaReader == nil {
		return 0, fmt.Errorf("metaCache nil")
	}

	var (
		estimation            float64 = 0
		reference                     = metricRaw
		metricsToGather       []string
		estimationBufferRatio float64
	)

	switch ci.QoSLevel {
	case apiconsts.PodAnnotationQoSLevelSharedCores, apiconsts.PodAnnotationQoSLevelDedicatedCores:
		metricsToGather = memoryMetricsToGatherForSharedAndDedicatedQoS
		estimationBufferRatio = estimationSharedDedicateQoSContainerBufferRatio
	case apiconsts.PodAnnotationQoSLevelSystemCores:
		metricsToGather = memoryMetricsToGatherForSystemQoS
		estimationBufferRatio = estimationSystemQoSContainerBufferRatio
	default:
		return 0, nil
	}

	if reclaimEnable {
		for _, metricName := range metricsToGather {
			metricValue, err := metaReader.GetContainerMetric(ci.PodUID, ci.ContainerName, metricName)
			general.InfoS("container metric", "podName", ci.PodName, "containerName", ci.ContainerName,
				"metricName", metricName, "metricValue", general.FormatMemoryQuantity(metricValue.Value), "err", err)
			if err != nil || metricValue.Value <= 0 {
				continue
			}
			estimation += metricValue.Value
		}

		if estimationBufferRatio > 0 {
			estimation = estimation * estimationBufferRatio
		}
	} else {
		estimation = ci.MemoryRequest
		reference = metricRequest
	}

	if estimation <= 0 {
		estimation = estimationMemoryFallbackValue
		reference = metricFallback
	}

	general.InfoS("container memory estimation", "podName", ci.PodName, "containerName", ci.ContainerName,
		"reference", reference, "value", general.FormatMemoryQuantity(estimation))

	return estimation, nil
}

// UtilBasedCapacityOptions are options for estimate util based resource capacity
type UtilBasedCapacityOptions struct {
	TargetUtilization         float64
	MaxUtilization            float64
	MaxOversoldRate           float64
	MaxCapacity               float64
	NonReclaimUtilizationHigh float64
	NonReclaimUtilizationLow  float64
}

func GenerateUtilBasedCapacityOptions(dynamicConfig *dynamic.Configuration, capacity float64) UtilBasedCapacityOptions {
	return UtilBasedCapacityOptions{
		TargetUtilization:         dynamicConfig.TargetReclaimedCoreUtilization,
		MaxUtilization:            dynamicConfig.MaxReclaimedCoreUtilization,
		MaxOversoldRate:           dynamicConfig.CPUUtilBasedConfiguration.MaxOversoldRate,
		MaxCapacity:               dynamicConfig.MaxHeadroomCapacityRate * capacity,
		NonReclaimUtilizationHigh: dynamicConfig.NonReclaimUtilizationHigh,
		NonReclaimUtilizationLow:  dynamicConfig.NonReclaimUtilizationLow,
	}
}

// EstimateUtilBasedCapacity capacity by taking into account the difference between the current
// and target resource utilization of the workload pool
func EstimateUtilBasedCapacity(options UtilBasedCapacityOptions, reclaimMetrics *metaserverHelper.ReclaimMetrics,
	lastCapacityResult float64, lastOverload bool,
) (float64, bool, error) {
	if reclaimMetrics == nil {
		return 0, false, fmt.Errorf("reclaimMetrics is nil")
	}
	var oversold, result float64
	overload := false

	currentReclaimUtilization := reclaimMetrics.CgroupCPUUsage / reclaimMetrics.ReclaimedCoresSupply
	currentNonReclaimUtilization := (reclaimMetrics.PoolCPUUsage - reclaimMetrics.CgroupCPUUsage) / float64(reclaimMetrics.Size)

	defer func() {
		general.InfoS("[EstimateUtilBasedCapacity]", "reclaimMetrics", reclaimMetrics, "options", options,
			"currentReclaimUtilization", currentReclaimUtilization, "oversold", oversold, "lastCapacityResult", lastCapacityResult, "result", result)
	}()

	// calculate the resource that can be oversold to the workloads, and consider that the resource
	// utilization of the workload is proportional to its capacity.
	// if the maximum resource utilization is greater than zero, the oversold can be negative to reduce
	// reporting capacity to avoid too many workloads being scheduled to that machine.
	if options.TargetUtilization > currentReclaimUtilization {
		oversold = reclaimMetrics.ReclaimedCoresSupply * (options.TargetUtilization - currentReclaimUtilization)
	} else if options.MaxUtilization > 0 && currentReclaimUtilization > options.MaxUtilization {
		oversold = reclaimMetrics.ReclaimedCoresSupply * (options.MaxUtilization - currentReclaimUtilization)
	}

	// TODO: consider cpu PSI

	result = math.Max(lastCapacityResult+oversold, reclaimMetrics.ReclaimedCoresSupply)
	result = math.Min(result, reclaimMetrics.ReclaimedCoresSupply*options.MaxOversoldRate)
	if options.MaxCapacity > 0 {
		result = math.Min(result, options.MaxCapacity)
	}
	if currentNonReclaimUtilization > options.NonReclaimUtilizationHigh && !lastOverload ||
		currentNonReclaimUtilization > options.NonReclaimUtilizationLow && lastOverload {
		general.InfoS("overload", "currentNonReclaimUtilization", currentNonReclaimUtilization,
			"NonReclaimUtilizationHigh", options.NonReclaimUtilizationHigh, "NonReclaimUtilizationLow", options.NonReclaimUtilizationLow,
			"lastOverload", lastOverload)
		result = 0
		overload = true
	}

	return result, overload, nil
}
