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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// metricRequest indicates using request as estimation
	metricRequest string = "request"

	// referenceFallback indicates using pod estimation fallback value
	metricFallback string = "fallback"
)

const (
	estimationCPUFallbackValue            = 4.0
	estimationMemoryFallbackValue float64 = 8 << 30

	estimationSharedDedicateQoSContainerBufferRatio = 1.1
	estimationSystemQoSContainerBufferRatio         = 1.0
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

// EstimateContainerCPUUsage used to estimate non-reclaimed pods CPU usage.
// If reclaimEnable is true, it will estimate reclaimed pods CPU usage.
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
			if err != nil || metricValue <= 0 {
				continue
			}
			checkRequest = false
			if metricValue > estimation {
				estimation = metricValue
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

// EstimateContainerMemoryUsage used to estimate non-reclaimed pods memory usage.
// If reclaim disabled or metrics missed, memory usage will be regarded as Pod memory requests.
func EstimateContainerMemoryUsage(ci *types.ContainerInfo, metaReader metacache.MetaReader, reclaimEnable bool) (float64, error) {
	if ci == nil {
		return 0, fmt.Errorf("containerInfo nil")
	}

	if metaReader == nil {
		return 0, fmt.Errorf("metaCache nil")
	}

	var (
		estimation            float64 = 0
		reference             string
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
			general.Infof("pod %v container %v metric %v value %v, err %v", ci.PodName, ci.ContainerName, metricName, metricValue, err)
			if err != nil || metricValue <= 0 {
				continue
			}
			estimation += metricValue
		}

		if estimationBufferRatio > 0 {
			estimation = estimation * estimationBufferRatio
		}
	} else {
		estimation = ci.MemoryRequest
		general.Infof("pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricRequest, estimation)
	}

	if estimation <= 0 {
		estimation = estimationMemoryFallbackValue
		reference = metricFallback
		general.Infof("pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricFallback, estimationMemoryFallbackValue)
	}

	general.Infof("pod %v container %v estimation %.2f reference %v", ci.PodName, ci.ContainerName, estimation, reference)
	return estimation, nil
}
