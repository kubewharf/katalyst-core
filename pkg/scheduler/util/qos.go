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

package util

import (
	"encoding/json"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

var (
	qosConfig        *generic.QoSConfiguration
	qosConfigSetOnce sync.Once
)

func SetQoSConfig(config *generic.QoSConfiguration) {
	qosConfigSetOnce.Do(func() {
		qosConfig = config
	})
}

func IsReclaimedPod(pod *v1.Pod) bool {
	ok, _ := qosConfig.CheckReclaimedQoSForPod(pod)
	return ok
}

func IsDedicatedPod(pod *v1.Pod) bool {
	ok, _ := qosConfig.CheckDedicatedQoSForPod(pod)
	return ok
}

func IsNumaBinding(pod *v1.Pod) bool {
	if pod == nil {
		return false
	}

	enhancementKey, ok := pod.Annotations[consts.PodAnnotationMemoryEnhancementKey]
	if !ok {
		return false
	}
	return memoryEnhancement(
		enhancementKey,
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationMemoryEnhancementNumaBindingEnable)
}

func IsExclusive(pod *v1.Pod) bool {
	if pod == nil {
		return false
	}

	enhancementKey, ok := pod.Annotations[consts.PodAnnotationMemoryEnhancementKey]
	if !ok {
		return false
	}
	return memoryEnhancement(
		enhancementKey,
		consts.PodAnnotationMemoryEnhancementNumaExclusive,
		consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable)
}

func memoryEnhancement(enhancementStr string, key, value string) bool {
	if key == "" {
		return true
	}
	if enhancementStr == "" {
		return false
	}

	var enhancement map[string]string
	if err := json.Unmarshal([]byte(enhancementStr), &enhancement); err != nil {
		klog.Errorf("failed to unmarshal %s: %v", enhancementStr, err)
		return false
	}

	enable, ok := enhancement[key]
	if !ok {
		return false
	}

	return enable == value
}
