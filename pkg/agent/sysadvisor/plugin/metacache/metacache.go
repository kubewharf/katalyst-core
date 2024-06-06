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

package metacacheplugin

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// MetricsNamePlugMetaCacheHeartbeat is the heartbeat metrics of metacache plugin
	MetricsNamePlugMetaCacheHeartbeat = "plugin_metacache_heartbeat"
)

// MetaCachePlugin collects pod info from kubelet
type MetaCachePlugin struct {
	name   string
	period time.Duration

	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
	MetaWriter metacache.MetaWriter
}

// NewMetaCachePlugin creates a metacache plugin with the specified config
func NewMetaCachePlugin(pluginName string, conf *config.Configuration, _ interface{}, emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer, metaCache metacache.MetaCache,
) (plugin.SysAdvisorPlugin, error) {
	emitter := emitterPool.GetDefaultMetricsEmitter().WithTags("advisor-metacache")

	mcp := &MetaCachePlugin{
		name:       pluginName,
		period:     conf.SysAdvisorPluginsConfiguration.MetaCachePluginConfiguration.SyncPeriod,
		emitter:    emitter,
		metaServer: metaServer,
		MetaWriter: metaCache,
	}

	return mcp, nil
}

// Name returns the name of metacache
func (mcp *MetaCachePlugin) Name() string {
	return mcp.name
}

// Init initializes the metacache plugin
func (mcp *MetaCachePlugin) Init() error {
	return nil
}

// Run starts the metacache plugin
func (mcp *MetaCachePlugin) Run(ctx context.Context) {
	general.RegisterHeartbeatCheck(mcp.name, 3*mcp.period, general.HealthzCheckStateNotReady, 3*mcp.period)
	go wait.UntilWithContext(ctx, mcp.periodicWork, mcp.period)
}

func (mcp *MetaCachePlugin) periodicWork(_ context.Context) {
	_ = mcp.emitter.StoreInt64(MetricsNamePlugMetaCacheHeartbeat, int64(mcp.period.Seconds()), metrics.MetricTypeNameCount)

	// Fill missing container metadata from metaserver
	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		spec, err := mcp.metaServer.GetContainerSpec(podUID, containerName)
		if err != nil {
			klog.Errorf("[metacache] get container spec failed: %v, %v/%v", err, podUID, containerName)
			return true
		}

		// For these containers do not belong to NumaExclusive, assign the actual value to CPURequest of them.
		// Because CPURequest of containerInfo would be assigned as math.Ceil(Actual CPURequest).
		// As for NumaExclusive containers, the "math.Ceil(Actual CPURequest)" is acceptable.
		if ci.CPURequest <= 0 || !ci.IsDedicatedNumaExclusive() {
			ci.CPURequest = spec.Resources.Requests.Cpu().AsApproximateFloat64()
		}
		if ci.CPULimit <= 0 {
			ci.CPULimit = spec.Resources.Limits.Cpu().AsApproximateFloat64()
		}
		if ci.MemoryRequest <= 0 {
			ci.MemoryRequest = spec.Resources.Requests.Memory().AsApproximateFloat64()
		}
		if ci.MemoryLimit <= 0 {
			ci.MemoryLimit = spec.Resources.Limits.Memory().AsApproximateFloat64()
		}
		return true
	}
	err := mcp.MetaWriter.RangeAndUpdateContainer(f)
	_ = general.UpdateHealthzStateByError(mcp.name, err)
}
