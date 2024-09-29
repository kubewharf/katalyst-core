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

package borwein

import (
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
)

type BorweinConfiguration struct {
	BorweinParameters                  map[string]*borweintypes.BorweinParameter
	NodeFeatureNames                   []string
	ContainerFeatureNames              []string
	InferenceServiceSocketAbsPath      string
	ModelNameToInferenceSvcSockAbsPath map[string]string
}

func NewBorweinConfiguration() *BorweinConfiguration {
	return &BorweinConfiguration{
		BorweinParameters: map[string]*borweintypes.BorweinParameter{
			string(v1alpha1.ServiceSystemIndicatorNameCPUSchedWait): {
				AbnormalRatioThreshold: 0.12,
				OffsetMax:              250,
				OffsetMin:              -50,
				RampUpStep:             2,
				RampDownStep:           10,
				Version:                "default",
			},
		},
		NodeFeatureNames:      []string{},
		ContainerFeatureNames: []string{},
	}
}
