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

package bulkhead

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/registry"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type Manager struct {
	mu                           sync.Mutex
	plugins                      []bulkheadapi.Plugin
	defaultNonReclaimPoolMinSize int64
	lastCPUSetAdjustmentEnabled  map[string]bool
}

const (
	metricBulkheadHandlerResult = "bulkhead_handler_result"
	metricBulkheadViewChanged   = "bulkhead_view_changed"
)

func NewManager(conf *config.Configuration) (*Manager, error) {
	plugins, err := registry.NewDefaultPlugins(conf)
	if err != nil {
		return nil, err
	}
	var defaultNonReclaimPoolMinSize int64
	if conf != nil && conf.DynamicAgentConfiguration != nil {
		defaultConf := conf.DynamicAgentConfiguration.GetDynamicConfiguration()
		defaultNonReclaimPoolMinSize = bulkheadNonReclaimPoolMinSize(defaultConf)
	}
	return &Manager{
		plugins:                      plugins,
		defaultNonReclaimPoolMinSize: defaultNonReclaimPoolMinSize,
	}, nil
}

func (m *Manager) RunCPUSetAdjustmentHandlers(ctx context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	handlerCtx := bulkheadapi.HandlerContext{CPUSetAdjustmentHandlerCtx: in}
	if !bulkheadEnabled(in.DynamicConf) {
		// The global bulkhead switch is a hard gate: when it is off, do not run
		// plugin Enable/adjust/disabled handlers. Disabled handlers may write
		// cgroup or sysfs rollback state, which is still bulkhead-owned behavior
		// and can introduce unexpected changes after the user explicitly turns
		// bulkhead off.
		m.lastCPUSetAdjustmentEnabled = nil
		emitBulkheadViewChanged(handlerCtx.Emitter, false)
		return nil
	}
	if in.State != nil {
		nonReclaimPoolMinSize := bulkheadNonReclaimPoolMinSize(in.DynamicConf)
		if nonReclaimPoolMinSize <= 0 {
			nonReclaimPoolMinSize = m.defaultNonReclaimPoolMinSize
		}
		opts := bulkheadutils.CPUSetPartitionViewOptions{
			NonReclaimPoolMinSize: nonReclaimPoolMinSize,
		}
		if in.CoreConf != nil {
			opts.ReserveCPUReversely = in.CoreConf.EnableReserveCPUReversely
		}
		handlerCtx.View = bulkheadutils.BuildCPUSetPartitionView(in.State, in.Topology, opts)
	}
	currentEnabled := m.buildPluginEnabledState(handlerCtx)
	anyAdjusted := false

	for _, p := range m.plugins {
		if !currentEnabled[p.Name()] {
			if !m.needsDisabledReset(p.Name()) {
				continue
			}
			if err := p.CPUSetAdjustmentDisabledHandler(ctx, handlerCtx); err != nil {
				emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment_disabled", p.Name(), "failed", err.Error())
				return fmt.Errorf("bulkhead plugin %q disabled transition failed: %w", p.Name(), err)
			}
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment_disabled", p.Name(), "success", "")
			anyAdjusted = true
			continue
		}
		if err := p.CPUSetAdjustmentHandler(ctx, handlerCtx); err != nil {
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "failed", err.Error())
			return fmt.Errorf("bulkhead plugin %q cpuset adjustment failed: %w", p.Name(), err)
		}
		emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "success", "")
		anyAdjusted = true
	}
	emitBulkheadViewChanged(handlerCtx.Emitter, anyAdjusted)
	m.lastCPUSetAdjustmentEnabled = currentEnabled
	return nil
}

func (m *Manager) buildPluginEnabledState(in bulkheadapi.HandlerContext) map[string]bool {
	out := make(map[string]bool, len(m.plugins))
	for _, p := range m.plugins {
		out[p.Name()] = p.Enable(in)
	}
	return out
}

// needsDisabledReset reports whether a currently-disabled plugin should run its
// disabled reset handler. A nil lastCPUSetAdjustmentEnabled means we have no
// prior state (e.g. after restart) and must reset once to converge.
func (m *Manager) needsDisabledReset(name string) bool {
	return m.lastCPUSetAdjustmentEnabled == nil || m.lastCPUSetAdjustmentEnabled[name]
}

func bulkheadEnabled(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable
}

func bulkheadNonReclaimPoolMinSize(conf *dynamicconfig.Configuration) int64 {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return 0
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize
}

func (m *Manager) RunPeriodicalHandlers(
	coreConf *config.Configuration,
	extraConf interface{},
	dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncBulkhead, err)
		if err != nil {
			general.ErrorS(err, "bulkhead periodical handlers failed")
		}
	}()

	ctx := context.Background()
	var conf *dynamicconfig.Configuration
	if dynamicConf != nil {
		conf = dynamicConf.GetDynamicConfiguration()
	}
	if !bulkheadEnabled(conf) {
		// Keep the periodical path behind the same hard global gate as the
		// cpuset adjustment path. Periodical handlers may reconcile external
		// resources such as cpuset partitions or workqueue masks, so running them
		// while bulkhead is globally disabled would still mutate bulkhead-owned
		// state.
		return
	}
	handlerCtx := bulkheadapi.PeriodicalHandlerContext{
		CoreConf:    coreConf,
		ExtraConf:   extraConf,
		DynamicConf: conf,
		Emitter:     emitter,
		MetaServer:  metaServer,
	}
	var errs []error
	for _, p := range m.plugins {
		pluginCtx := handlerCtx
		if enabled, ok := m.lastCPUSetAdjustmentEnabled[p.Name()]; ok {
			pluginCtx.EffectiveEnabled = &enabled
		}
		if err := p.PeriodicalHandler(ctx, pluginCtx); err != nil {
			wrapped := fmt.Errorf("bulkhead plugin %q periodical failed: %w", p.Name(), err)
			general.ErrorS(wrapped, "bulkhead periodical handler failed")
			emitBulkheadPluginResult(emitter, "periodical", p.Name(), "failed", err.Error())
			errs = append(errs, wrapped)
			continue
		}
		emitBulkheadPluginResult(emitter, "periodical", p.Name(), "success", "")
	}
	err = apierrors.NewAggregate(errs)
}

func emitBulkheadPluginResult(emitter metrics.MetricEmitter, phase, plugin, status, reason string) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadHandlerResult, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "phase", Val: phase},
		metrics.MetricTag{Key: "plugin", Val: plugin},
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "reason", Val: metricutil.MetricTagValueFormat(reason)},
	)
}

func emitBulkheadViewChanged(emitter metrics.MetricEmitter, changed bool) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadViewChanged, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "changed", Val: strconv.FormatBool(changed)},
	)
}
