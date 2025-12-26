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

package qrm

import (
	"fmt"
	"strings"
	"sync"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	phconsts "github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler/consts"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

const (
	QRMPluginNameGPU = "qrm_gpu_plugin"
)

var QRMGPUPluginPeriodicalHandlerGroupName = strings.Join([]string{
	QRMPluginNameGPU,
	phconsts.PeriodicalHandlersGroupNameSuffix,
}, phconsts.GroupNameSeparator)

// gpuPolicyInitializers is used to store the initializing function for gpu resource plugin policies
var gpuPolicyInitializers sync.Map

// RegisterGPUPolicyInitializer is used to register user-defined resource plugin init functions
func RegisterGPUPolicyInitializer(name string, initFunc agent.InitFunc) {
	gpuPolicyInitializers.Store(name, initFunc)
}

// getIOPolicyInitializers returns those policies with initialized functions
func getGPUPolicyInitializers() map[string]agent.InitFunc {
	agents := make(map[string]agent.InitFunc)
	gpuPolicyInitializers.Range(func(key, value interface{}) bool {
		agents[key.(string)] = value.(agent.InitFunc)
		return true
	})
	return agents
}

// InitQRMGPUPlugins initializes the gpu QRM plugins
func InitQRMGPUPlugins(agentCtx *agent.GenericContext, conf *config.Configuration, extraConf interface{}, agentName string) (bool, agent.Component, error) {
	initializers := getGPUPolicyInitializers()
	policyName := conf.GPUQRMPluginConfig.PolicyName

	initFunc, ok := initializers[policyName]
	if !ok {
		return false, agent.ComponentStub{}, fmt.Errorf("invalid policy name %v for gpu resource plugin", policyName)
	}

	return initFunc(agentCtx, conf, extraConf, agentName)
}
