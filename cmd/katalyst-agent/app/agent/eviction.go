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

package agent

import (
	"fmt"

	"k8s.io/klog/v2"
	plugincache "k8s.io/kubernetes/pkg/kubelet/pluginmanager/cache"

	evict "github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

const (
	EvictionManagerAgent = "katalyst-agent-eviction"
)

func InitEvictionManager(agentCtx *GenericContext, conf *config.Configuration, _ interface{}, _ string) (bool, Component, error) {
	recorder := agentCtx.BroadcastAdapter.NewRecorder(EvictionManagerAgent)
	evictionMgr := evict.NewEvictionManager(agentCtx.Client, recorder, agentCtx.MetaServer,
		agentCtx.EmitterPool.GetDefaultMetricsEmitter(), conf)

	// add eviction configuration to dynamic configuration watch list
	err := agentCtx.MetaServer.AddConfigWatcher(dynamic.EvictionConfigurationGVR)
	if err != nil {
		return false, ComponentStub{}, fmt.Errorf("failed register dynamic config: %s", err)
	}

	// register eviction manager as dynamic config handler
	agentCtx.MetaServer.Register(evictionMgr)

	klog.Infof("starting eviction manager")

	agentCtx.PluginManager.AddHandler(evictionMgr.GetHandlerType(), plugincache.PluginHandler(evictionMgr))
	return true, evictionMgr, nil
}
