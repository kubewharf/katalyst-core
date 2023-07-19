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

package app

import (
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	_ "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu"
	_ "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory"
	_ "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	phconsts "github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler/consts"
)

// AgentStarter is used to start katalyst agents
type AgentStarter struct {
	Init      agent.InitFunc
	ExtraConf interface{}
}

// AgentsDisabledByDefault is the set of controllers which is disabled by default
var AgentsDisabledByDefault = sets.NewString()

// agentInitializers is used to store the initializing function for each agent
var agentInitializers sync.Map

func init() {
	agentInitializers.Store(agent.ReporterManagerAgent, AgentStarter{Init: agent.InitReporterManager})
	agentInitializers.Store(agent.EvictionManagerAgent, AgentStarter{Init: agent.InitEvictionManager})
	agentInitializers.Store(agent.QoSSysAdvisor, AgentStarter{Init: agent.InitSysAdvisor})
	agentInitializers.Store(phconsts.PeriodicalHandlerManagerName,
		AgentStarter{Init: periodicalhandler.NewPeriodicalHandlerManager})

	// qrm plugins are registered at top level of agent
	agentInitializers.Store(qrm.QRMPluginNameCPU, AgentStarter{Init: qrm.InitQRMCPUPlugins})
	agentInitializers.Store(qrm.QRMPluginNameMemory, AgentStarter{Init: qrm.InitQRMMemoryPlugins})
	agentInitializers.Store(qrm.QRMPluginNameNetwork, AgentStarter{Init: qrm.InitQRMNetworkPlugins})
}

// RegisterAgentInitializer is used to register user-defined agents
func RegisterAgentInitializer(name string, starter AgentStarter) {
	agentInitializers.Store(name, starter)
}

// GetAgentInitializers returns those agents with initialized functions
func GetAgentInitializers() map[string]AgentStarter {
	agents := make(map[string]AgentStarter)
	agentInitializers.Range(func(key, value interface{}) bool {
		agents[key.(string)] = value.(AgentStarter)
		return true
	})
	return agents
}
