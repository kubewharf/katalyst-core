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

package systemservice

import (
	"context"
	"errors"
	"sort"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func (p *SystemServicePlugin) Enable(in bulkheadapi.HandlerContext) bool {
	return enableBulkheadSystemService(in.DynamicConf)
}

// PeriodicalHandler migrates every eligible root-cgroup PID into the target
// cgroup via identity-bound attach when the plugin's dynamic switch is
// enabled. When the switch transitions from enabled to disabled (or the
// first tick after restart observes disabled), it runs a one-shot reset
// that reads every PID currently listed in targetRel/cgroup.procs and
// reattaches it to the cpuset root. The reset does not recurse into child
// cgroups or filter PIDs by managed status. Subsequent ticks while disabled
// are no-ops.
func (p *SystemServicePlugin) PeriodicalHandler(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	enabled := enableBulkheadSystemService(in.DynamicConf)
	if !enabled {
		// Trigger a reset on enabled → disabled transition, or on the first
		// tick after restart if that first observation is disabled. Steady
		// disabled state is a no-op.
		needsReset := p.lastPeriodicalEnabled == nil || *p.lastPeriodicalEnabled
		if !needsReset {
			return nil
		}
		err := p.resetTargetToRoot(ctx, in)
		if err == nil {
			f := false
			p.lastPeriodicalEnabled = &f
		}
		return err
	}

	err := p.runMigrate(ctx, in)
	// Any observed enabled tick — including early returns from missing target
	// or listing errors — updates the tracker to true so a subsequent real
	// disable transition triggers reset.
	t := true
	p.lastPeriodicalEnabled = &t
	return err
}

func enableBulkheadSystemService(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadSystemService
}

const metricBulkheadSystemServiceResult = "bulkhead_system_service_result"

type systemServiceOperationError struct {
	controller string
	reason     string
	err        error
}

func (e *systemServiceOperationError) Error() string { return e.err.Error() }
func (e *systemServiceOperationError) Unwrap() error { return e.err }

func operationError(controller, reason string, err error) error {
	return &systemServiceOperationError{controller: controller, reason: reason, err: err}
}

// emitBulkheadSystemServiceFailures emits at most one sample for each
// controller/reason pair in a sweep. This preserves failure ownership without
// multiplying metrics by the number of affected PIDs.
func emitBulkheadSystemServiceFailures(emitter metrics.MetricEmitter, phase string, errs []error) {
	failures := make(map[string]systemServiceOperationError)
	for _, err := range errs {
		var operationErr *systemServiceOperationError
		if !errors.As(err, &operationErr) {
			operationErr = &systemServiceOperationError{
				controller: "all",
				reason:     "attach_error",
			}
		}
		key := operationErr.controller + "\x00" + operationErr.reason
		failures[key] = *operationErr
	}
	keys := make([]string, 0, len(failures))
	for key := range failures {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		failure := failures[key]
		emitBulkheadSystemServiceResult(emitter, phase, "failed", failure.reason, failure.controller)
	}
}

func emitBulkheadSystemServiceResult(emitter metrics.MetricEmitter, phase, status, reason, controller string) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadSystemServiceResult, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "phase", Val: phase},
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "reason", Val: reason},
		metrics.MetricTag{Key: "controller", Val: controller},
	)
}
