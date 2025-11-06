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

package qos

import (
	"fmt"
	"math"
	"strconv"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func AnnotationsIndicateNUMANotShare(annotations map[string]string) bool {
	return annotations[consts.PodAnnotationCPUEnhancementNUMAShare] ==
		consts.PodAnnotationCPUEnhancementNUMAShareDisable
}

// GetPodCPUSuppressionToleranceRate parses cpu suppression tolerance rate for the given pod,
// and cpu suppression is only supported for reclaim pods. if the given is not nominated with
// cpu suppression, return max to indicate that it can be suppressed for any degree.
func GetPodCPUSuppressionToleranceRate(qosConf *generic.QoSConfiguration, pod *v1.Pod) (float64, error) {
	qosLevel, _ := qosConf.GetQoSLevel(pod, map[string]string{})
	if qosLevel != consts.PodAnnotationQoSLevelReclaimedCores {
		return 0, fmt.Errorf("qos level %s not support cpu suppression", qosLevel)
	}

	cpuEnhancement := qosConf.GetQoSEnhancementKVs(pod, map[string]string{}, consts.PodAnnotationCPUEnhancementKey)
	suppressionToleranceRateStr, ok := cpuEnhancement[consts.PodAnnotationCPUEnhancementSuppressionToleranceRate]
	if ok {
		suppressionToleranceRate, err := strconv.ParseFloat(suppressionToleranceRateStr, 64)
		if err != nil {
			return 0, err
		}
		return suppressionToleranceRate, nil
	}

	return math.MaxFloat64, nil
}

// GetPodCPUBurstPolicy gets the cpu burst policy for the given pod by parsing the cpu enhancement keys. If the given pod is
// dedicated, the default policy is static. If cpu burst policy key does not exist, the policy is none.
func GetPodCPUBurstPolicy(qosConf *generic.QoSConfiguration, pod *v1.Pod) string {
	qosLevel, _ := qosConf.GetQoSLevel(pod, map[string]string{})
	cpuEnhancement := qosConf.GetQoSEnhancementKVs(pod, map[string]string{}, consts.PodAnnotationCPUEnhancementKey)
	cpuBurstPolicy, ok := cpuEnhancement[consts.PodAnnotationCPUEnhancementCPUBurstPolicy]

	// Dedicated pods have cpu burst policy that is defaulted to static
	if qosLevel == consts.PodAnnotationQoSLevelDedicatedCores && (cpuBurstPolicy == consts.PodAnnotationCPUEnhancementCPUBurstPolicyNone || !ok) {
		return consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic
	}

	if !ok {
		return consts.PodAnnotationCPUEnhancementCPUBurstPolicyNone
	}

	return cpuBurstPolicy
}

// GetPodCPUBurstPercent parses cpu burst percent for the given pod by parsing the cpu enhancement keys.
// If the cpu burst percent key does not exist, the default percent is 100.
func GetPodCPUBurstPercent(qosConf *generic.QoSConfiguration, pod *v1.Pod) (float64, error) {
	cpuEnhancement := qosConf.GetQoSEnhancementKVs(pod, map[string]string{}, consts.PodAnnotationCPUEnhancementKey)
	cpuBurstPercentStr, ok := cpuEnhancement[consts.PodAnnotationCPUEnhancementCPUBurstPercent]

	// Default cpu burst percent is 100
	if !ok {
		return 100, nil
	}

	cpuBurstPercent, err := strconv.ParseFloat(cpuBurstPercentStr, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse cpuBurstPercent: %v", err)
	}

	// cpu burst percent should be in range [0, 100]
	if cpuBurstPercent > 100 {
		return 100, nil
	}

	return cpuBurstPercent, nil
}
