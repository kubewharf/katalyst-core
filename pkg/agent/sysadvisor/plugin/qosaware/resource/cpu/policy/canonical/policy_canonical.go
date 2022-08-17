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

package canonical

import (
	"fmt"

	"k8s.io/klog/v2"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

const (
	// containerEstimationFallbackValue is the estimation value if all methods fail
	containerEstimationFallback float64 = 4.0

	// metricRequest indicates using request as estimation
	metricRequest string = "request"

	// referenceFallback indicates using pod estimation fallback value
	metricFallback string = "fallback"
)

var (
	metricsToGather []string = []string{
		consts.MetricCPUUsageContainer,
		consts.MetricLoad1MinContainer,
		consts.MetricLoad5MinContainer,
	}
)

type CanonicalPolicy struct {
	cpuRequirement float64
	metaCache      *metacache.MetaCache
}

func NewCanonicalPolicy(metaCache *metacache.MetaCache) *CanonicalPolicy {
	cp := &CanonicalPolicy{
		metaCache: metaCache,
	}
	return cp
}

func (cp *CanonicalPolicy) Update() {
	var (
		cpuEstimation float64 = 0
		containerCnt  float64 = 0
	)

	calculateContainerEstimationFunc := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		containerEstimation, reference, err := cp.estimateContainer(ci)
		if err != nil {
			return true
		}
		klog.Infof("[qosaware-cpu-canonical] pod %v container %v estimation %.2f reference %v", ci.PodName, containerName, containerEstimation, reference)
		cpuEstimation += containerEstimation
		containerCnt += 1
		return true
	}
	cp.metaCache.RangeContainer(calculateContainerEstimationFunc)
	klog.Infof("[qosaware-cpu-canonical] cpu requirement estimation: %.2f, #container %v", cpuEstimation, containerCnt)

	cp.cpuRequirement = cpuEstimation
}

func (cp *CanonicalPolicy) GetProvisionResult() interface{} {
	return cp.cpuRequirement
}

func (cp *CanonicalPolicy) estimateContainer(ci *types.ContainerInfo) (float64, string, error) {
	var (
		estimation   float64 = 0
		reference    string
		checkRequest bool = false
	)

	for _, metricName := range metricsToGather {
		if ci == nil || ci.QoSLevel != apiconsts.PodAnnotationQoSLevelSharedCores {
			return 0, reference, fmt.Errorf("illegal container")
		}

		metricValue, err := cp.metaCache.GetContainerMetric(ci.PodUID, ci.ContainerName, metricName)
		klog.Infof("[qosaware-cpu-canonical] pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricName, metricValue)
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
		request := ci.CPURequest
		klog.Infof("[qosaware-cpu-canonical] pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricRequest, request)
		if request > estimation {
			estimation = request
			reference = metricRequest
		}
	}

	if estimation <= 0 {
		estimation = containerEstimationFallback
		reference = metricFallback
		klog.Infof("[qosaware-cpu-canonical] pod %v container %v metric %v value %v", ci.PodName, ci.ContainerName, metricFallback, containerEstimationFallback)
	}

	return estimation, reference, nil
}
