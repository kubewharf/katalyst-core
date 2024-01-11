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

	"github.com/kubewharf/katalyst-core/pkg/agent/orm"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

const (
	ORMAgent = "katalyst-agent-orm"
)

func InitORM(agentCtx *GenericContext, conf *config.Configuration, _ interface{}, _ string) (bool, Component, error) {
	m, err := orm.NewManager(conf.PluginRegistrationDir+"/kubelet.sock", agentCtx.EmitterPool.GetDefaultMetricsEmitter(), agentCtx.MetaServer, conf)
	if err != nil {
		return false, ComponentStub{}, fmt.Errorf("failed to init ORM: %v", err)
	}

	if klog.V(5).Enabled() {
		klog.Infof("InitORM GetHandlerType: %v", m.GetHandlerType())
	}
	agentCtx.PluginManager.AddHandler(m.GetHandlerType(), plugincache.PluginHandler(m))
	return true, m, nil
}
