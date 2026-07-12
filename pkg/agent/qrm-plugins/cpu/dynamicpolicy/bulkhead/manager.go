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
	bypassutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type Manager struct {
	mu                          sync.Mutex
	plugins                     []bulkheadapi.Plugin
	lastCPUSetAdjustmentView    *bulkheadutils.CPUSetPartitionView
	lastCPUSetAdjustmentEnabled map[string]bool
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
	return &Manager{
		plugins: plugins,
	}, nil
}

func (m *Manager) RunCPUSetAdjustmentHandlers(ctx context.Context, in bypassutil.BypassCPUSetAdjustmentHandlerCtx) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	handlerCtx := bulkheadapi.HandlerContext{BypassCPUSetAdjustmentHandlerCtx: in}
	if in.State != nil {
		handlerCtx.View = bulkheadutils.BuildCPUSetPartitionView(in.State, in.Topology)
	}
	currentEnabled := m.buildPluginEnabledState(handlerCtx.DynamicConf)
	if m.lastCPUSetAdjustmentEnabled != nil &&
		equalPluginEnabledState(m.lastCPUSetAdjustmentEnabled, currentEnabled) &&
		bulkheadutils.EqualCPUSetPartitionView(m.lastCPUSetAdjustmentView, handlerCtx.View) {
		emitBulkheadViewChanged(handlerCtx.Emitter, false)
		return nil
	}
	emitBulkheadViewChanged(handlerCtx.Emitter, true)

	for _, p := range m.plugins {
		if !currentEnabled[p.Name()] {
			continue
		}
		if err := p.CPUSetAdjustmentHandler(ctx, handlerCtx); err != nil {
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "failed", err.Error())
			return fmt.Errorf("bulkhead plugin %q cpuset adjustment failed: %w", p.Name(), err)
		}
		emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "success", "")
	}
	m.lastCPUSetAdjustmentEnabled = currentEnabled
	if handlerCtx.View == nil {
		m.lastCPUSetAdjustmentView = nil
		return nil
	}
	m.lastCPUSetAdjustmentView = handlerCtx.View.DeepCopy()
	return nil
}

func (m *Manager) buildPluginEnabledState(dynamicConf *dynamicconfig.Configuration) map[string]bool {
	out := make(map[string]bool, len(m.plugins))
	for _, p := range m.plugins {
		out[p.Name()] = p.Enable(dynamicConf)
	}
	return out
}

func equalPluginEnabledState(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for name, enabled := range a {
		if b[name] != enabled {
			return false
		}
	}
	return true
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
	handlerCtx := bulkheadapi.PeriodicalHandlerContext{
		CoreConf:    coreConf,
		ExtraConf:   extraConf,
		DynamicConf: dynamicConf.GetDynamicConfiguration(),
		Emitter:     emitter,
		MetaServer:  metaServer,
	}
	var errs []error
	for _, p := range m.plugins {
		if err := p.PeriodicalHandler(ctx, handlerCtx); err != nil {
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
		metrics.MetricTag{Key: "reason", Val: reason},
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
