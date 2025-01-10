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

package metric

import (
	"strconv"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func EmitCurrentPowerUSage(emitter metrics.MetricEmitter, currentWatts int) {
	_ = emitter.StoreInt64(metricPowerAwareCurrentPowerInWatt, int64(currentWatts), metrics.MetricTypeNameRaw)
}

func EmitPowerSpec(emitter metrics.MetricEmitter, powerSpec *spec.PowerSpec) {
	_ = emitter.StoreInt64(metricPowerSpecBudget,
		int64(powerSpec.Budget),
		metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			tagPowerAlert:      string(powerSpec.Alert),
			tagPowerInternalOp: powerSpec.InternalOp.String(),
		})...,
	)
}

func EmitPowerAdvice(emitter metrics.MetricEmitter, action action.PowerAction, mode string) {
	_ = emitter.StoreInt64(metricPowerAwareDesiredPowerInWatt,
		int64(action.Arg),
		metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			tagPowerActionOp:   action.Op.String(),
			tagPowerActionMode: mode,
		})...,
	)
}

func EmitEvictReq(emitter metrics.MetricEmitter, pods int) {
	_ = emitter.StoreInt64(metricPowerEvictReq, int64(pods), metrics.MetricTypeNameCount)
}

func EmitPowerCapInstruction(emitter metrics.MetricEmitter, instruction *capper.CapInstruction) {
	_ = emitter.StoreInt64(metricPowerCappingTarget,
		int64(instruction.RawTargetValue),
		metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			tagPowerCappingOpCode: string(instruction.OpCode),
		})...,
	)
	_ = emitter.StoreInt64(metricPowerCappingCurrent,
		int64(instruction.RawCurrentValue),
		metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			tagPowerCappingOpCode: string(instruction.OpCode),
		})...,
	)
}

func EmitPowerCapReset(emitter metrics.MetricEmitter) {
	_ = emitter.StoreInt64(metricPowerCappingReset, 1, metrics.MetricTypeNameCount)
}

func EmitErrorCode(emitter metrics.MetricEmitter, errorCode int) {
	_ = emitter.StoreInt64(metricPowerError, 1, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			tagErrorCode: strconv.Itoa(errorCode),
		})...,
	)
}
