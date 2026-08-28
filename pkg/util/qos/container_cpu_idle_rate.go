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
	"encoding/json"

	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

// GetPodContainerCPUIdleRateConfig parses the per-container cpu idle rate config
// from an independent pod annotation.
//
// This helper is intentionally kept lightweight for now and only handles JSON
// parsing. The actual consumption path can be added later.
func GetPodContainerCPUIdleRateConfig(podAnnotations map[string]string, annotationKey string) (bool, katalystapiconsts.ContainerCPUIdleRateConfig, error) {
	if len(podAnnotations) == 0 {
		return false, nil, nil
	}

	annotationValue, ok := podAnnotations[annotationKey]
	if !ok || annotationValue == "" {
		return false, nil, nil
	}

	var cfg katalystapiconsts.ContainerCPUIdleRateConfig
	if err := json.Unmarshal([]byte(annotationValue), &cfg); err != nil {
		return true, nil, err
	}

	return true, cfg, nil
}

func GetPodContainerCPUIdleRateConfigFromAnnotation(podAnnotations map[string]string) (bool, katalystapiconsts.ContainerCPUIdleRateConfig, error) {
	return GetPodContainerCPUIdleRateConfig(podAnnotations, katalystapiconsts.PodAnnotationContainerCPUIdleRateKey)
}
