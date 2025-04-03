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
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/strategygroup"
)

func validateConf(conf *config.Configuration) (*strategygroup.StrategyGroup, error) {
	if conf == nil {
		return nil, fmt.Errorf("nil conf")
	} else if conf.AgentConfiguration == nil {
		return nil, fmt.Errorf("nil agent conf")
	} else if conf.AgentConfiguration.DynamicAgentConfiguration == nil {
		return nil, fmt.Errorf("nil dynamic agent conf")
	}

	dynamicConf := conf.GetDynamicConfiguration()
	if dynamicConf == nil {
		return nil, fmt.Errorf("nil dynamicConf")
	}

	strategyGroup := dynamicConf.StrategyGroup
	if strategyGroup == nil {
		return nil, fmt.Errorf("nil strategy group")
	}

	return strategyGroup, nil
}

func IsStrategyEnabledForNode(strategyName string, defaultValue bool, conf *config.Configuration) (bool, error) {
	strategyGroup, err := validateConf(conf)
	if err != nil {
		return defaultValue, fmt.Errorf("invalid conf: %v", err)
	}

	for _, strategy := range strategyGroup.EnabledStrategies {
		if strategy.Name != nil && *strategy.Name == strategyName {
			return true, nil
		}
	}

	return false, nil
}

func GetEnabledStrategiesForNode(conf *config.Configuration) ([]string, error) {
	strategyGroup, err := validateConf(conf)
	if err != nil {
		return nil, fmt.Errorf("invalid conf: %v", err)
	}

	enabledStrategies := make([]string, 0, len(strategyGroup.EnabledStrategies))
	for _, strategy := range strategyGroup.EnabledStrategies {
		if strategy.Name != nil {
			enabledStrategies = append(enabledStrategies, *strategy.Name)
		}
	}

	return enabledStrategies, nil
}
