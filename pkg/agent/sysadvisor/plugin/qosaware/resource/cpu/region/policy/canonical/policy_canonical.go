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
	"k8s.io/klog/v2"

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
	containerSet   map[string]map[string]struct{}
	metaCache      *metacache.MetaCache
}

func NewCanonicalPolicy(metaCache *metacache.MetaCache) *CanonicalPolicy {
	cp := &CanonicalPolicy{
		metaCache: metaCache,
	}
	return cp
}

func (p *CanonicalPolicy) SetContainerSet(containerSet map[string]map[string]struct{}) {
	p.containerSet = make(map[string]map[string]struct{})
	for podUID, v := range containerSet {
		p.containerSet[podUID] = make(map[string]struct{})
		for containerName := range v {
			p.containerSet[podUID][containerName] = struct{}{}
		}
	}
}

func (p *CanonicalPolicy) SetControlKnob(types.ControlKnob) {
}

func (p *CanonicalPolicy) SetIndicator(types.Indicator) {
}

func (p *CanonicalPolicy) SetTarget(types.Indicator) {
}

func (p *CanonicalPolicy) Update() {
	var (
		cpuEstimation float64 = 0
		containerCnt  float64 = 0
	)

	for podUID, v := range p.containerSet {
		for containerName := range v {
			ci, ok := p.metaCache.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu-canonical] illegal container info of %v/%v", podUID, containerName)
				continue
			}

			containerEstimation, reference := p.estimateContainer(ci)
			klog.Infof("[qosaware-cpu-canonical] pod %v container %v estimation %.2f reference %v", ci.PodName, containerName, containerEstimation, reference)

			cpuEstimation += containerEstimation
			containerCnt += 1
		}
	}
	klog.Infof("[qosaware-cpu-canonical] cpu requirement estimation: %.2f, #container %v", cpuEstimation, containerCnt)

	p.cpuRequirement = cpuEstimation
}

func (p *CanonicalPolicy) GetProvisionResult() interface{} {
	return p.cpuRequirement
}

func (p *CanonicalPolicy) estimateContainer(ci *types.ContainerInfo) (float64, string) {
	var (
		estimation   float64 = 0
		reference    string
		checkRequest bool = false
	)

	for _, metricName := range metricsToGather {
		metricValue, err := p.metaCache.GetContainerMetric(ci.PodUID, ci.ContainerName, metricName)
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

	return estimation, reference
}
