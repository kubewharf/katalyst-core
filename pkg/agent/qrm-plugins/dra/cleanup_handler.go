/*
Copyright 2026 The Katalyst Authors.

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

package dra

import (
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	phconsts "github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler/consts"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	draPluginName                 = "qrm_compute_domain_dra_plugin"
	clearResidualStateHandlerName = draPluginName + "_clear_residual_state"
	healthCheckTolerationTimes    = int64(3)
)

var draPeriodicalHandlerGroupName = strings.Join(
	[]string{
		draPluginName,
		phconsts.PeriodicalHandlersGroupNameSuffix,
	},
	phconsts.GroupNameSeparator,
)

func (p *StaticPolicy) registerClearResidualState() error {
	return periodicalhandler.RegisterPeriodicalHandlerWithHealthz(
		clearResidualStateHandlerName,
		general.HealthzCheckStateNotReady,
		draPeriodicalHandlerGroupName,
		p.clearResidualState,
		stateCheckPeriod,
		healthCheckTolerationTimes,
	)
}

func (p *StaticPolicy) startCleanup() {
	periodicalhandler.ReadyToStartHandlersByGroup(
		draPeriodicalHandlerGroupName,
	)
}

func (p *StaticPolicy) stopCleanup() {
	periodicalhandler.StopHandlersByGroup(
		draPeriodicalHandlerGroupName,
	)
}

func (p *StaticPolicy) clearResidualState(
	_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	err := p.cleanupResidualClaims()
	_ = general.UpdateHealthzStateByError(
		clearResidualStateHandlerName,
		err,
	)
}
