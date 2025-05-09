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

package types

import (
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
)

type BorweinParameter struct {
	OffsetMax    float64 `json:"offset_max"`
	OffsetMin    float64 `json:"offset_min"`
	RampUpStep   float64 `json:"ramp_up_step"`
	RampDownStep float64 `json:"ramp_down_step"`
	Version      string  `json:"version"`
	IndicatorMax float64 `json:"indicator_max"`
	IndicatorMin float64 `json:"indicator_min"`
}

// BorweinInferenceResults is a descriptor for borwein inference results.
type BorweinInferenceResults struct {
	Timestamp int64 // milli second
	// the first key is the pod UID
	// the second key is the container name
	// the value array holds the all inference result for a container.
	Results map[string]map[string][]*borweininfsvc.InferenceResult
}

func NewBorweinInferenceResults() *BorweinInferenceResults {
	return &BorweinInferenceResults{
		Timestamp: 0,
		Results:   map[string]map[string][]*borweininfsvc.InferenceResult{},
	}
}

func (bir *BorweinInferenceResults) SetInferenceResults(podUID string, containerName string, results ...*borweininfsvc.InferenceResult) {
	podContainerResults, ok := bir.Results[podUID]
	if !ok {
		podContainerResults = make(map[string][]*borweininfsvc.InferenceResult)
		bir.Results[podUID] = podContainerResults
	}

	podContainerResults[containerName] = make([]*borweininfsvc.InferenceResult, len(results))
	for idx, r := range results {
		podContainerResults[containerName][idx] = r
	}
}

func (bir *BorweinInferenceResults) RangeInferenceResults(fn func(podUID, containerName string, result *borweininfsvc.InferenceResult)) {
	for podUID, podContainerResults := range bir.Results {
		for containerName, containerResults := range podContainerResults {
			for _, result := range containerResults {
				fn(podUID, containerName, result)
			}
		}
	}
}
