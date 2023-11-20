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
	AbnormalRatioThreshold float64 `json:"abnormal_ratio_threshold"`
	OffsetMax              float64 `json:"offset_max"`
	OffsetMin              float64 `json:"offset_min"`
	RampUpStep             float64 `json:"ramp_up_step"`
	RampDownStep           float64 `json:"ramp_down_step"`
	Version                string  `json:"version"`
}

// BorweinInferenceResults is a descriptor for borwein inference results.
// the first key is the pod UID
// the second key is the container name
// the value array holds the all inference result for a container.
type BorweinInferenceResults map[string]map[string][]*borweininfsvc.InferenceResult
