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

package sysadvisor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	pkgplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference"
	metacacheplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metacache"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/overcommitmentaware"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const initTimeout = 10 * time.Second

func init() {
	pkgplugin.RegisterAdvisorPlugin(types.AdvisorPluginNameQoSAware, qosaware.NewQoSAwarePlugin)
	pkgplugin.RegisterAdvisorPlugin(types.AdvisorPluginNameMetaCache, metacacheplugin.NewMetaCachePlugin)
	pkgplugin.RegisterAdvisorPlugin(types.AdvisorPluginNameMetricEmitter, metricemitter.NewCustomMetricEmitter)
	pkgplugin.RegisterAdvisorPlugin(types.AdvisorPluginNameInference, inference.NewInferencePlugin)
	pkgplugin.RegisterAdvisorPlugin(types.AdvisorPluginNameOvercommitAware, overcommitmentaware.NewOvercommitmentAwarePlugin)
	pkgplugin.RegisterAdvisorPlugin(types.AdvisorPluginNamePowerAware, poweraware.NewPowerAwarePlugin)
}

// AdvisorAgent for sysadvisor
type AdvisorAgent struct {
	// those are parameters that be passed to sysadvisor when starting agents.
	config     *config.Configuration
	extraConf  interface{}
	metaServer *metaserver.MetaServer
	emitPool   metricspool.MetricsEmitterPool

	plugins      []pkgplugin.SysAdvisorPlugin
	pluginsToRun []pkgplugin.SysAdvisorPlugin

	wgInitPlugin sync.WaitGroup
	mutex        sync.Mutex
}

// NewAdvisorAgent initializes the sysadvisor agent logic.
func NewAdvisorAgent(conf *config.Configuration, extraConf interface{}, metaServer *metaserver.MetaServer,
	emitPool metricspool.MetricsEmitterPool,
) (*AdvisorAgent, error) {
	agent := &AdvisorAgent{
		config:     conf,
		extraConf:  extraConf,
		metaServer: metaServer,
		emitPool:   emitPool,

		plugins:      make([]pkgplugin.SysAdvisorPlugin, 0),
		pluginsToRun: make([]pkgplugin.SysAdvisorPlugin, 0),
	}

	if err := agent.getAdvisorPlugins(pkgplugin.GetRegisteredAdvisorPlugins()); err != nil {
		return nil, err
	}

	agent.init()
	return agent, nil
}

func (m *AdvisorAgent) getAdvisorPlugins(SysAdvisorPluginInitializers map[string]pkgplugin.AdvisorPluginInitFunc) error {
	metaCache, err := metacache.NewMetaCacheImp(m.config, m.emitPool, m.metaServer.MetricsFetcher)
	if err != nil {
		return fmt.Errorf("new metacache failed: %v", err)
	}

	for pluginName, initFn := range SysAdvisorPluginInitializers {
		if !general.IsNameEnabled(pluginName, sets.NewString(), m.config.GenericSysAdvisorConfiguration.SysAdvisorPlugins) {
			klog.Warningf("[sysadvisor] %s plugin is disabled", pluginName)
			continue
		}

		klog.Infof("[sysadvisor] %s plugin is enabled", pluginName)
		curPlugin, err := initFn(pluginName, m.config, m.extraConf, m.emitPool, m.metaServer, metaCache)
		if err != nil {
			return fmt.Errorf("failed to start sysadvisor plugin %v: %v", pluginName, err)
		}

		m.plugins = append(m.plugins, curPlugin)
	}

	return nil
}

// Asynchronous initialization with timeout. Timeout plugin will neither be killed nor started.
func (m *AdvisorAgent) init() {
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
func (m *AdvisorAgent) Run(ctx context.Context) {
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
