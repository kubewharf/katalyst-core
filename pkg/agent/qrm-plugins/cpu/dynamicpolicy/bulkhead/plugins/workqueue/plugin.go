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

package workqueue

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilfs "github.com/kubewharf/katalyst-core/pkg/util/fs"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const WorkqueuePluginName = "workqueue"

var _ bulkheadapi.Plugin = (*WorkqueuePlugin)(nil)

type WorkqueuePlugin struct {
	cfg bulkheadconfig.BulkheadConfiguration
	fs  utilfs.FS
}

func NewWorkqueuePlugin(conf *config.Configuration) bulkheadapi.Plugin {
	var cfg bulkheadconfig.BulkheadConfiguration
	if conf != nil && conf.CPUQRMPluginConfig != nil && conf.CPUQRMPluginConfig.BulkheadConfiguration != nil {
		cfg = *conf.CPUQRMPluginConfig.BulkheadConfiguration
	}
	return &WorkqueuePlugin{
		cfg: cfg,
		fs:  utilfs.NewOSFS(),
	}
}

func (p *WorkqueuePlugin) Name() string { return WorkqueuePluginName }

func (p *WorkqueuePlugin) Enable(in bulkheadapi.HandlerContext) bool {
	return enableBulkheadWorkqueue(in.DynamicConf)
}

func (p *WorkqueuePlugin) CPUSetAdjustmentHandler(_ context.Context, in bulkheadapi.HandlerContext) error {
	if in.View == nil {
		return p.resetWorkqueue(in)
	}
	return p.reconcileWorkqueue(in)
}

func (p *WorkqueuePlugin) CPUSetAdjustmentDisabledHandler(_ context.Context, in bulkheadapi.HandlerContext) error {
	return p.resetWorkqueue(in)
}

func (p *WorkqueuePlugin) PeriodicalHandler(
	context.Context,
	bulkheadapi.PeriodicalHandlerContext,
) error {
	return nil
}

func (p *WorkqueuePlugin) reconcileWorkqueue(in bulkheadapi.HandlerContext) error {
	reclaim := in.View.ReclaimEffective
	if reclaim.IsEmpty() {
		return p.resetWorkqueue(in)
	}
	return p.writeWorkqueueMask(in, reclaim, "reclaim")
}

func (p *WorkqueuePlugin) resetWorkqueue(in bulkheadapi.HandlerContext) error {
	if in.Topology == nil {
		return nil
	}
	return p.writeWorkqueueMask(in, in.Topology.CPUDetails.CPUs(), "fallback")
}

func (p *WorkqueuePlugin) writeWorkqueueMask(in bulkheadapi.HandlerContext, cpus machine.CPUSet, reason string) error {
	if cpus.IsEmpty() {
		return nil
	}
	mask, err := general.ConvertIntSliceToBitmapString(cpus.ToSliceInt64())
	if err != nil {
		return fmt.Errorf("convert %s cpuset %s to workqueue mask: %w", reason, cpus.String(), err)
	}

	global := filepath.Join(p.cfg.BulkheadWorkqueueSysfsDir, "cpumask")
	if changed, err := utilfs.WriteStringIfChanged(p.fs, global, mask, 0o644); err != nil {
		emitBulkheadWorkqueueWriteResult(in.Emitter, "global", "", "failed", err.Error())
		return fmt.Errorf("write global workqueue cpumask: %w", err)
	} else if !changed {
		emitBulkheadWorkqueueWriteResult(in.Emitter, "global", "", "skipped", "unchanged")
	} else {
		emitBulkheadWorkqueueWriteResult(in.Emitter, "global", "", "success", "")
	}

	for _, name := range p.cfg.BulkheadWorkqueueNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		file := filepath.Join(p.cfg.BulkheadWorkqueueSysfsDir, name, "cpumask")
		if !p.fs.Exists(file) {
			emitBulkheadWorkqueueWriteResult(in.Emitter, "per_workqueue", name, "skipped", "file_absent")
			continue
		}
		if changed, err := utilfs.WriteStringIfChanged(p.fs, file, mask, 0o644); err != nil {
			emitBulkheadWorkqueueWriteResult(in.Emitter, "per_workqueue", name, "failed", err.Error())
			return fmt.Errorf("write workqueue %q cpumask: %w", name, err)
		} else if !changed {
			emitBulkheadWorkqueueWriteResult(in.Emitter, "per_workqueue", name, "skipped", "unchanged")
		} else {
			emitBulkheadWorkqueueWriteResult(in.Emitter, "per_workqueue", name, "success", "")
		}
	}
	return nil
}

func enableBulkheadWorkqueue(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadWorkqueue
}

const metricBulkheadWorkqueueWriteResult = "bulkhead_workqueue_write_result"

func emitBulkheadWorkqueueWriteResult(emitter metrics.MetricEmitter, scope, name, status, reason string) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadWorkqueueWriteResult, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "scope", Val: scope},
		metrics.MetricTag{Key: "name", Val: name},
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "reason", Val: reason},
	)
}
