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

package poweraware

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	capserver "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper/server"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
	evictserver "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor/server"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/reader"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const metricName = "poweraware-advisor-plugin"

type powerAwarePlugin struct {
	name    string
	dryRun  bool
	advisor advisor.PowerAwareAdvisor
}

func (p powerAwarePlugin) Name() string {
	return p.name
}

func (p powerAwarePlugin) Init() error {
	general.Infof("pap initialized")

	if err := p.advisor.Init(); err != nil {
		klog.Errorf("pap: failed to initialize power advisor: %v", err)
		return errors.Wrap(err, "failed to initialize power advisor")
	}

	return nil
}

func (p powerAwarePlugin) Run(ctx context.Context) {
	general.Infof("pap running")
	p.advisor.Run(ctx)
	general.Infof("pap ran and finished")
}

func NewPowerAwarePlugin(
	pluginName string,
	conf *config.Configuration,
	_ interface{},
	emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer,
	_ metacache.MetaCache,
) (plugin.SysAdvisorPlugin, error) {
	emitter := emitterPool.GetDefaultMetricsEmitter().WithTags(metricName)

	var err error
	var podEvictor evictor.PodEvictor
	if conf.DisablePowerPressureEvict {
		podEvictor = evictor.NewNoopPodEvictor()
	} else {
		if podEvictor, err = evictserver.NewPowerPressureEvictionPlugin(conf, emitter); err != nil {
			return nil, errors.Wrap(err, "pap: failed to create power aware plugin")
		}
	}

	var powerCapper capper.PowerCapper
	if conf.DisablePowerCapping {
		powerCapper = capper.NewNoopCapper()
	} else {
		if powerCapper, err = capserver.NewPowerCapPlugin(conf, emitter); err != nil {
			return nil, errors.Wrap(err, "pap: failed to create power aware plugin")
		}
	}

	powerReader := reader.NewMetricStorePowerReader(metaServer)

	powerAdvisor := advisor.NewAdvisor(conf.PowerAwarePluginConfiguration.DryRun,
		conf.PowerAwarePluginConfiguration.AnnotationKeyPrefix,
		podEvictor,
		emitter,
		metaServer.NodeFetcher,
		conf.QoSConfiguration,
		metaServer.PodFetcher,
		powerReader,
		powerCapper,
	)

	return newPluginWithAdvisor(pluginName, conf, powerAdvisor)
}

func newPluginWithAdvisor(pluginName string, conf *config.Configuration, advisor advisor.PowerAwareAdvisor) (plugin.SysAdvisorPlugin, error) {
	return &powerAwarePlugin{
		name:    pluginName,
		dryRun:  conf.PowerAwarePluginConfiguration.DryRun,
		advisor: advisor,
	}, nil
}
