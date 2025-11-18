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

	"github.com/kubewharf/katalyst-core/pkg/util/general"

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

// GetPodCPUBurstPolicyFromCPUEnhancement gets the cpu burst policy for the given pod by parsing the cpu enhancement keys.
// All reclaimed cores pods should not have cpu burst enabled.
func GetPodCPUBurstPolicyFromCPUEnhancement(qosConf *generic.QoSConfiguration, pod *v1.Pod) string {
	qosLevel, _ := qosConf.GetQoSLevel(pod, map[string]string{})
	cpuEnhancement := qosConf.GetQoSEnhancementKVs(pod, map[string]string{}, consts.PodAnnotationCPUEnhancementKey)
	cpuBurstPolicy, ok := cpuEnhancement[consts.PodAnnotationCPUEnhancementCPUBurstPolicy]

	// Do not enable cpu burst for reclaimed cores pods even when the annotation is set
	if qosLevel == consts.PodAnnotationQoSLevelReclaimedCores && ok && cpuBurstPolicy != consts.PodAnnotationCPUEnhancementCPUBurstPolicyNone {
		general.Warningf("Reclaimed cores should not have cpu burst enabled")
		return consts.PodAnnotationCPUEnhancementCPUBurstPolicyNone
	}

	if !ok {
		return consts.PodAnnotationCPUEnhancementCPUBurstPolicyNone
	}

	return cpuBurstPolicy
}

// GetPodCPUBurstPercentFromCPUEnhancement parses cpu burst percent for the given pod by parsing the cpu enhancement keys.
func GetPodCPUBurstPercentFromCPUEnhancement(qosConf *generic.QoSConfiguration, pod *v1.Pod) (float64, bool, error) {
	cpuEnhancement := qosConf.GetQoSEnhancementKVs(pod, map[string]string{}, consts.PodAnnotationCPUEnhancementKey)
	cpuBurstPercentStr, ok := cpuEnhancement[consts.PodAnnotationCPUEnhancementCPUBurstPercent]

	if !ok {
		return 0, false, nil
	}

	cpuBurstPercent, err := strconv.ParseFloat(cpuBurstPercentStr, 64)
	if err != nil {
		return 0, false, fmt.Errorf("failed to parse cpuBurstPercent: %v", err)
	}

	// cpu burst percent should be in range [0, 100]
	if cpuBurstPercent > 100 {
		return 100, true, nil
	}

	return cpuBurstPercent, true, nil
}
