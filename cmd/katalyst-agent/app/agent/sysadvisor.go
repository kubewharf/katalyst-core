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
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	pkgplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	metacacheplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metacache"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	initTimeout   time.Duration = 10 * time.Second
	QoSSysAdvisor               = "katalyst-agent-advisor"
)

// AdvisorPluginInitFunc is used to initialize a particular inter SysAdvisor plugin.
type AdvisorPluginInitFunc func(conf *config.Configuration, extraConf interface{}, emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer, metaCache metacache.MetaCache) (pkgplugin.SysAdvisorPlugin, error)

// SysAdvisorAgent for sysadvisor
type SysAdvisorAgent struct {
	// those are parameters that be passed to sysadvisor when starting agents.
	agentCtx  *GenericContext
	config    *config.Configuration
	extraConf interface{}

	plugins      []pkgplugin.SysAdvisorPlugin
	pluginsToRun []pkgplugin.SysAdvisorPlugin

	wgInitPlugin sync.WaitGroup
	mutex        sync.Mutex
}

var sysadvisorPluginsDisabledByDefault = sets.NewString()

func newSysAdvisorPluginInitializers() map[string]AdvisorPluginInitFunc {
	sysadvisorPluginInitializers := make(map[string]AdvisorPluginInitFunc)
	sysadvisorPluginInitializers["qos_aware"] = qosaware.NewQoSAwarePlugin
	sysadvisorPluginInitializers["metacache"] = metacacheplugin.NewMetaCachePlugin
	sysadvisorPluginInitializers["metric_emitter"] = metricemitter.NewCustomMetricEmitter
	return sysadvisorPluginInitializers
}

// NewSysAdvisorAgent initializes the sysadvisor agent logic.
func NewSysAdvisorAgent(agentCtx *GenericContext, conf *config.Configuration, extraConf interface{}) (*SysAdvisorAgent, error) {
	agent := &SysAdvisorAgent{
		agentCtx:     agentCtx,
		config:       conf,
		extraConf:    extraConf,
		plugins:      make([]pkgplugin.SysAdvisorPlugin, 0),
		pluginsToRun: make([]pkgplugin.SysAdvisorPlugin, 0),
	}

	if err := agent.getSysAdvisorPlugins(newSysAdvisorPluginInitializers()); err != nil {
		return nil, err
	}

	agent.init()
	return agent, nil
}

func (m *SysAdvisorAgent) getSysAdvisorPlugins(SysAdvisorPluginInitializers map[string]AdvisorPluginInitFunc) error {
	metaCache, err := metacache.NewMetaCacheImp(m.config, m.agentCtx.MetaServer.MetricsFetcher)
	if err != nil {
		return fmt.Errorf("new metacache failed: %v", err)
	}

	for pluginName, initFn := range SysAdvisorPluginInitializers {
		if !general.IsNameEnabled(pluginName, sysadvisorPluginsDisabledByDefault, m.config.GenericSysAdvisorConfiguration.SysAdvisorPlugins) {
			klog.Warningf("[sysadvisor] %s plugin is disabled", pluginName)
			continue
		}

		klog.Infof("[sysadvisor] %s plugin is enabled", pluginName)
		curPlugin, err := initFn(m.config, m.extraConf, m.agentCtx.EmitterPool, m.agentCtx.MetaServer, metaCache)
		if err != nil {
			return fmt.Errorf("failed to start sysadvisor plugin %v: %v", pluginName, err)
		}

		m.plugins = append(m.plugins, curPlugin)
	}

	return nil
}

// Asynchronous initialization with timeout. Timeout plugin will neither be killed nor started.
func (m *SysAdvisorAgent) init() {
	for _, plugin := range m.plugins {
		p := context.TODO()
		c, cancel := context.WithTimeout(p, initTimeout)
		defer cancel()
		m.wgInitPlugin.Add(1)

		go func(ctx context.Context, plugin pkgplugin.SysAdvisorPlugin) {
			defer m.wgInitPlugin.Done()

			ch := make(chan error, 1)
			go func(plugin pkgplugin.SysAdvisorPlugin) {
				err := plugin.Init()
				ch <- err
			}(plugin)

			for {
				select {
				case err := <-ch:
					if err != nil {
						klog.Errorf("[sysadvisor] initialize plugin %v with error: %v; do not start it", plugin.Name(), err)
					} else {
						m.mutex.Lock()
						m.pluginsToRun = append(m.pluginsToRun, plugin)
						m.mutex.Unlock()
						klog.Infof("[sysadvisor] plugin %v initialized", plugin.Name())
					}
					return
				case <-ctx.Done():
					klog.Errorf("[sysadvisor] initialize plugin %v timeout, limit %v; ignore and do not start it", plugin.Name(), initTimeout)
					return
				}
			}
		}(c, plugin)
	}
	m.wgInitPlugin.Wait()
}

// Run starts sysadvisor agent
func (m *SysAdvisorAgent) Run(ctx context.Context) {
	wg := sync.WaitGroup{}
	// sysadvisor plugin can both run synchronously or asynchronously
	for _, plugin := range m.pluginsToRun {
		wg.Add(1)
		go func(plugin pkgplugin.SysAdvisorPlugin) {
			defer wg.Done()
			klog.Infof("[sysadvisor] start plugin %v", plugin.Name())
			plugin.Run(ctx)
		}(plugin)
	}

	wg.Wait()
	<-ctx.Done()
}

func InitSysAdvisor(agentCtx *GenericContext, conf *config.Configuration, extraConf interface{}, _ string) (bool, Component, error) {
	sysadvisorAgent, err := NewSysAdvisorAgent(agentCtx, conf, extraConf)
	if err != nil {
		return false, nil, fmt.Errorf("failed init sysadvisor plugin agent: %s", err)
	}

	return true, sysadvisorAgent, nil
}
