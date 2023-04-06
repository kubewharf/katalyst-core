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

package qosaware

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/server"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

const (
	// PluginNameQosAware is the name of QoSAwarePlugin
	PluginNameQosAware = "qos-aware-plugin"

	// MetricsNamePlugQoSAwareHearBeat is the heartbeat metrics of qos aware plugin
	MetricsNamePlugQoSAwareHearBeat = "plugin_qosaware_heartbeat"
)

// QoSAwarePlugin calculates node headroom and updates resource provision value suggestions
// using different algorithms configured by user. Resource headroom data will be reported by the
// reporter and provision result will be sync to QRM plugin by gRPC. To take different resource
// dimensions into consideration, implement calculators or algorithms for each resource and register
// them to qos aware plugin.
type QoSAwarePlugin struct {
	name   string
	period time.Duration

	resourceAdvisor  resource.ResourceAdvisor
	qrmServer        server.QRMServer
	headroomReporter reporter.HeadroomReporter

	metaCache metacache.MetaCache
	emitter   metrics.MetricEmitter
}

// NewQoSAwarePlugin creates a qos aware plugin with the specified config
func NewQoSAwarePlugin(conf *config.Configuration, extraConf interface{}, emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer, metaCache metacache.MetaCache) (plugin.SysAdvisorPlugin, error) {
	emitter := emitterPool.GetDefaultMetricsEmitter().WithTags("advisor-qosaware")

	resourceAdvisor, err := resource.NewResourceAdvisor(conf, extraConf, metaCache, metaServer, emitter)
	if err != nil {
		return nil, err
	}

	qrmServer, err := server.NewQRMServer(resourceAdvisor, conf, metaCache, emitter)
	if err != nil {
		return nil, err
	}

	headroomReporter, err := reporter.NewHeadroomReporter(emitter, metaServer, conf, resourceAdvisor)
	if err != nil {
		return nil, err
	}

	// add dynamic config watcher
	err = metaServer.ConfigurationManager.AddConfigWatcher(dynamic.AdminQoSConfigurationGVR)
	if err != nil {
		return nil, err
	}

	qap := &QoSAwarePlugin{
		name:   PluginNameQosAware,
		period: conf.SysAdvisorPluginsConfiguration.QoSAwarePluginConfiguration.SyncPeriod,

		resourceAdvisor:  resourceAdvisor,
		headroomReporter: headroomReporter,
		qrmServer:        qrmServer,

		metaCache: metaCache,
		emitter:   emitter,
	}

	return qap, nil
}

// Run starts the qos aware plugin, which periodically inspects cpu usage and takes measures.
func (qap *QoSAwarePlugin) Run(ctx context.Context) {
	go wait.UntilWithContext(ctx, qap.periodicWork, qap.period)

	go qap.qrmServer.Run(ctx)

	// Headroom reporter must run synchronously to be stopped gracefully
	qap.headroomReporter.Run(ctx)
}

// Name returns the name of qos aware plugin
func (qap *QoSAwarePlugin) Name() string {
	return qap.name
}

// Init initializes the qos aware plugin
func (qap *QoSAwarePlugin) Init() error {
	return nil
}

func (qap *QoSAwarePlugin) periodicWork(_ context.Context) {
	_ = qap.emitter.StoreInt64(MetricsNamePlugQoSAwareHearBeat, int64(qap.period.Seconds()), metrics.MetricTypeNameCount)

	qap.resourceAdvisor.Update()
}
