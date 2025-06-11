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
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/strategygroup"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type StrategyGroupOptions struct {
	EnableStrategyGroup bool
	EnabledStrategies   []string
}

func NewStrategyGroupOptions() *StrategyGroupOptions {
	return &StrategyGroupOptions{
		EnableStrategyGroup: false,
		EnabledStrategies: []string{
			consts.StrategyNameNone,
		},
	}
}

func (o *StrategyGroupOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("strategy_group")
	fs.BoolVar(&o.EnableStrategyGroup, "enable-strategy-group",
		o.EnableStrategyGroup, "if set true, we will enable strategy group")
	fs.StringSliceVar(&o.EnabledStrategies, "enabled-strategies",
		o.EnabledStrategies, "the strategies that will be enabled")
}

func (o *StrategyGroupOptions) ApplyTo(c *strategygroup.StrategyGroupConfiguration) error {
	c.EnableStrategyGroup = o.EnableStrategyGroup
	c.EnabledStrategies = make([]v1alpha1.Strategy, 0, len(o.EnabledStrategies))
	for _, strategy := range o.EnabledStrategies {
		c.EnabledStrategies = append(c.EnabledStrategies, v1alpha1.Strategy{
			Name: &strategy,
		})
	}
	return nil
}
