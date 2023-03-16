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

package policy

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
	containerEstimationFallback float64 = 8 << 30

	// metricRequest indicates using request as estimation
	metricRequest string = "request"

	// referenceFallback indicates using fallback value as estimation
	metricFallback string = "fallback"
)

type PolicyCanonical struct {
	memoryRequirement float64
	metaCache         *metacache.MetaCache
}

func NewPolicyCanonical(metaCache *metacache.MetaCache) *PolicyCanonical {
	cp := &PolicyCanonical{
		metaCache: metaCache,
	}
	return cp
}

func (cp *PolicyCanonical) Update() {
	var (
		memoryEstimation float64 = 0
		containerCnt     float64 = 0
	)

	calculateContainerEstimationFunc := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		containerEstimation, reference, err := cp.estimateContainer(ci)
		if err != nil {
			return true
		}
		klog.Infof("[qosaware-memory-canonical] pod %v container %v estimation %.2e reference %v", ci.PodName, containerName, containerEstimation, reference)
		memoryEstimation += containerEstimation
		containerCnt += 1
		return true
	}
	cp.metaCache.RangeContainer(calculateContainerEstimationFunc)
	klog.Infof("[qosaware-memory-canonical] memory requirement estimation: %.2e, #container %v", memoryEstimation, containerCnt)

	cp.memoryRequirement = memoryEstimation
}

func (cp *PolicyCanonical) GetProvisionResult() interface{} {
	return cp.memoryRequirement
}

func (cp *PolicyCanonical) estimateContainer(ci *types.ContainerInfo) (float64, string, error) {
	var (
		metricName string = consts.MetricMemRssContainer
		reference  string = metricName
	)

	if ci == nil || ci.QoSLevel != apiconsts.PodAnnotationQoSLevelSharedCores {
		return 0, reference, fmt.Errorf("illegal container")
	}

	metricValue, err := cp.metaCache.GetContainerMetric(ci.PodUID, ci.ContainerName, metricName)
	if err != nil || metricValue <= 0 {
		metricValue = ci.MemoryRequest
		reference = metricRequest

		if metricValue <= 0 {
			metricValue = containerEstimationFallback
			reference = metricFallback
		}
	}

	return metricValue, reference, nil
}
