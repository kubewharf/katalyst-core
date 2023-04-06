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

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

const (
	// metricRequest indicates using request as estimation
	metricRequest string = "request"

	// referenceFallback indicates using pod estimation fallback value
	metricFallback string = "fallback"
)

// estimationFallbackValues is the resource estimation value when all methods fail
var estimationFallbackValues = map[v1.ResourceName]float64{
	v1.ResourceCPU:    4.0,
	v1.ResourceMemory: 8 << 30,
}

// resourceMetricsToGather are the interested metrics for resource estimation
var resourceMetricsToGather = map[v1.ResourceName][]string{
	v1.ResourceCPU: {
		consts.MetricCPUUsageContainer,
		consts.MetricLoad1MinContainer,
		consts.MetricLoad5MinContainer,
	},
	v1.ResourceMemory: {
		consts.MetricMemRssContainer,
	},
}

func EstimateContainerResourceUsage(ci *types.ContainerInfo, resourceName v1.ResourceName, metaReader metacache.MetaReader) (float64, error) {
	if ci.QoSLevel != apiconsts.PodAnnotationQoSLevelSharedCores && ci.QoSLevel != apiconsts.PodAnnotationQoSLevelDedicatedCores {
		return 0, nil
	}
	metricsToGather, ok := resourceMetricsToGather[resourceName]
	if !ok || len(metricsToGather) == 0 {
		return 0, fmt.Errorf("failed to find metrics to gather for %v", resourceName)
	}
	if metaReader == nil {
		return 0, fmt.Errorf("metaCache nil")
	}

	var (
		estimation   float64 = 0
		reference    string
		checkRequest = false
	)

	for _, metricName := range metricsToGather {
		metricValue, err := metaReader.GetContainerMetric(ci.PodUID, ci.ContainerName, metricName)
		klog.Infof("[qosaware-canonical] pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricName, metricValue)
		if err != nil || metricValue <= 0 {
			checkRequest = true
			continue
		}
		if metricValue > estimation {
			estimation = metricValue
			reference = metricName
		}
	}

	if checkRequest {
		request := 0.0
		switch resourceName {
		case v1.ResourceCPU:
			request = ci.CPURequest
		case v1.ResourceMemory:
			request = ci.MemoryRequest
		default:
			return 0, fmt.Errorf("invalid resourceName %v", resourceName)
		}
		klog.Infof("[qosaware-canonical] pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricRequest, request)
		if request > estimation {
			estimation = request
			reference = metricRequest
		}
	}

	if estimation <= 0 {
		fallback, ok := estimationFallbackValues[resourceName]
		if !ok {
			return estimation, fmt.Errorf("failed to find estimation fallback value for %v", resourceName)
		}
		estimation = fallback
		reference = metricFallback
		klog.Infof("[qosaware-canonical] pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricFallback, fallback)
	}

	klog.Infof("[qosaware-canonical] pod %v container %v estimation %.2f reference %v", ci.PodName, ci.ContainerName, estimation, reference)

	return estimation, nil
}
