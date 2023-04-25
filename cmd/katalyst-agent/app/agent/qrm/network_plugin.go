// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package qrm

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

const (
	QRMPluginNameNetwork = "qrm_network_plugin"
)

// networkPolicyInitializers is used to store the initializing function for network resource plugin policies
var networkPolicyInitializers sync.Map

// RegisterNetworkPolicyInitializer is used to register user-defined resource plugin init functions
func RegisterNetworkPolicyInitializer(name string, initFunc agent.InitFunc) {
	networkPolicyInitializers.Store(name, initFunc)
}

// getNetworkPolicyInitializers returns those policies with initialized functions
func getNetworkPolicyInitializers() map[string]agent.InitFunc {
	agents := make(map[string]agent.InitFunc)
	networkPolicyInitializers.Range(func(key, value interface{}) bool {
		agents[key.(string)] = value.(agent.InitFunc)
		return true
	})
	return agents
}

// InitQRMNetworkPlugins initializes the network QRM plugins
func InitQRMNetworkPlugins(agentCtx *agent.GenericContext, conf *config.Configuration, extraConf interface{}, agentName string) (bool, agent.Component, error) {
	initializers := getNetworkPolicyInitializers()
	policyName := conf.NetworkQRMPluginConfig.PolicyName

	initFunc, ok := initializers[policyName]
	if !ok {
		return false, agent.ComponentStub{}, fmt.Errorf("invalid policy name %v for network resource plugin", policyName)
	}

	return initFunc(agentCtx, conf, extraConf, agentName)
}
