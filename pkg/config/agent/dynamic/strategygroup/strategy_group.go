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

package strategygroup

import (
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type StrategyGroup struct {
	EnabledStrategies []v1alpha1.Strategy
}

func NewStrategyGroup() *StrategyGroup {
	return &StrategyGroup{
		EnabledStrategies: []v1alpha1.Strategy{},
	}
}

func (sg *StrategyGroup) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if dynamicSG := conf.StrategyGroup; dynamicSG != nil && dynamicSG.Status.EnabledStrategies != nil {
		sg.EnabledStrategies = make([]v1alpha1.Strategy, 0, len(dynamicSG.Status.EnabledStrategies))
		for _, strategy := range dynamicSG.Status.EnabledStrategies {
			sg.EnabledStrategies = append(sg.EnabledStrategies, *strategy.DeepCopy())
		}
	}
}
