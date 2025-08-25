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

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/strategygroup"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

// mandatoryEnabledStrategies lists the names of strategies that must be enabled for the strategy group feature.
// These strategies are considered essential and will always be active.
var mandatoryEnabledStrategies = sets.NewString(
	consts.StrategyNameBorweinV2,
)

func validateConf(conf *config.Configuration) (*strategygroup.StrategyGroupConfiguration, error) {
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

	strategyGroup := dynamicConf.StrategyGroupConfiguration
	if strategyGroup == nil {
		return nil, fmt.Errorf("nil strategy group")
	}

	return strategyGroup, nil
}

// IsStrategyEnabledForNode checks if a specific strategy is enabled for the node.
// It takes the strategy name, a default value, and the configuration as input.
// It returns true if the strategy is enabled, otherwise it returns the default value. An error is returned if the configuration is invalid.
func IsStrategyEnabledForNode(strategyName string, defaultValue bool, conf *config.Configuration) (bool, error) {
	strategyGroup, err := validateConf(conf)
	if err != nil {
		return defaultValue, fmt.Errorf("invalid conf: %v", err)
	}

	if !isStrategyGroupEnabled(strategyGroup, strategyName) {
		return defaultValue, nil
	}

	for _, strategy := range strategyGroup.EnabledStrategies {
		if strategy.Name != nil && *strategy.Name == strategyName {
			return defaultValue, nil
		}
	}

	return false, nil
}

// GetEnabledStrategiesForNode returns a list of enabled strategies for the node.
// It takes the configuration as input.
// It returns a slice of strings containing the names of enabled strategies. An error is returned if the configuration is invalid.
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

// GetSpecificStrategyParam returns the parameter value for a specific strategy and whether it's enabled.
// It takes the strategy name, a default enable status, and the configuration as input.
// It returns the strategy parameter string, a boolean indicating if the strategy is enabled (or defaultEnable if not found/group disabled), and an error if the configuration is invalid.
func GetSpecificStrategyParam(strategyName string, defaultEnable bool, conf *config.Configuration) (string, bool, error) {
	strategyGroup, err := validateConf(conf)
	if err != nil {
		return "", false, fmt.Errorf("invalid conf: %v", err)
	}

	if !isStrategyGroupEnabled(strategyGroup, strategyName) {
		return "", defaultEnable, nil
	}

	for _, strategy := range strategyGroup.EnabledStrategies {
		if strategy.Name != nil && *strategy.Name == strategyName {
			return strategy.Parameters[strategyName], defaultEnable, nil
		}
	}

	return "", false, nil
}

func StrategyPolicyOverrideForNode(strategyPolicyMap map[string]string, defaultPolicy string, conf *config.Configuration) (string, error) {
	strategyGroup, err := validateConf(conf)
	if err != nil {
		return defaultPolicy, fmt.Errorf("invalid conf: %v", err)
	}

	for _, strategy := range strategyPolicyMap {
		if !isStrategyGroupEnabled(strategyGroup, strategy) {
			return defaultPolicy, nil
		}
	}

	for _, strategy := range strategyGroup.EnabledStrategies {
		if strategy.Name == nil {
			continue
		}

		override, ok := strategyPolicyMap[*strategy.Name]
		if ok {
			return override, nil
		}
	}

	return defaultPolicy, nil
}

func isStrategyGroupEnabled(strategyGroup *strategygroup.StrategyGroupConfiguration, strategyName string) bool {
	return strategyGroup.EnableStrategyGroup || mandatoryEnabledStrategies.Has(strategyName)
}
