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
	"strconv"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

// IsPodNumaBinding checks whether the pod needs numa-binding
func IsPodNumaBinding(qosConf *generic.QoSConfiguration, pod *v1.Pod) bool {
	qosLevel, _ := qosConf.GetQoSLevelForPod(pod)
	if qosLevel != consts.PodAnnotationQoSLevelDedicatedCores {
		return false
	}

	memoryEnhancement := ParseKatalystQOSEnhancement(qosConf.GetQoSEnhancementsForPod(pod), consts.PodAnnotationMemoryEnhancementKey)
	numaBinding, ok := memoryEnhancement[consts.PodAnnotationMemoryEnhancementNumaBinding]
	if ok && numaBinding == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
		return true
	}

	return false
}

// GetRSSOverUseEvictEnabledAndThreshold checks whether the pod enable RSS overuse eviction and parse the user specified threshold
func GetRSSOverUseEvictEnabledAndThreshold(qosConf *generic.QoSConfiguration, pod *v1.Pod) (bool, float64) {
	memoryEnhancement := ParseKatalystQOSEnhancement(qosConf.GetQoSEnhancementsForPod(pod), consts.PodAnnotationMemoryEnhancementKey)
	thresholdStr, ok := memoryEnhancement[consts.PodAnnotationMemoryEnhancementRssOverUseThreshold]
	if !ok {
		return true, consts.PodAnnotationMemoryEnhancementRssOverUseThresholdNotSet
	}

	threshold, parseErr := strconv.ParseFloat(thresholdStr, 64)
	// don't perform evict for safety if user set an unresolvable threshold
	if parseErr != nil {
		return false, consts.PodAnnotationMemoryEnhancementRssOverUseThresholdNotSet
	}
	// don't perform evict for safety if user set an invalid threshold
	if !isValidRatioThreshold(threshold) {
		return false, consts.PodAnnotationMemoryEnhancementRssOverUseThresholdNotSet
	}

	return true, threshold
}

func isValidRatioThreshold(threshold float64) bool {
	return threshold > 0 && threshold <= 1
}
