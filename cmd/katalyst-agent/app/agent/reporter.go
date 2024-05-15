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

	plugincache "k8s.io/kubernetes/pkg/kubelet/pluginmanager/cache"

	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/reporter"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/reporter/cnr"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	ReporterManagerAgent = "katalyst-agent-reporter"
)

func init() {
	reporter.RegisterReporterInitializer(util.CNRGroupVersionKind, cnr.NewCNRReporter)
}

func InitReporterManager(agentCtx *GenericContext, conf *config.Configuration,
	_ interface{}, _ string,
) (bool, Component, error) {
	reporterMgr, err := reporter.NewReporterManager(agentCtx.Client, agentCtx.MetaServer,
		agentCtx.EmitterPool.GetDefaultMetricsEmitter(), conf)
	if err != nil {
		return false, nil, fmt.Errorf("failed init reporter manager: %s", err)
	}

	reporterPluginMgr, err := fetcher.NewReporterPluginManager(reporterMgr,
		agentCtx.EmitterPool.GetDefaultMetricsEmitter(), agentCtx.MetaServer, conf)
	if err != nil {
		return false, ComponentStub{}, fmt.Errorf("failed init reporter plugin manager: %s", err)
	}

	agentCtx.PluginManager.AddHandler(reporterPluginMgr.GetHandlerType(), plugincache.PluginHandler(reporterPluginMgr))
	return true, reporterPluginMgr, nil
}
