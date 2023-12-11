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
	"github.com/kubewharf/katalyst-core/pkg/util/qos/helper"
)

// GetPodCPUSuppressionToleranceRate parses cpu suppression tolerance rate for the given pod,
// and cpu suppression is only supported for reclaim pods. if the given is not nominated with
// cpu suppression, return max to indicate that it can be suppressed for any degree.
func GetPodCPUSuppressionToleranceRate(qosConf *generic.QoSConfiguration, pod *v1.Pod) (float64, error) {
	qosLevel, _ := qosConf.GetQoSLevelForPod(pod)
	if qosLevel != consts.PodAnnotationQoSLevelReclaimedCores {
		return 0, fmt.Errorf("qos level %s not support cpu suppression", qosLevel)
	}

	cpuEnhancement := helper.ParseKatalystQOSEnhancement(qosConf.GetQoSEnhancementsForPod(pod), pod.Annotations,
		consts.PodAnnotationCPUEnhancementKey)
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
