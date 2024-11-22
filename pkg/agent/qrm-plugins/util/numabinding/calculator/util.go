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

package calculator

import (
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/state"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricsNameTryNUMABindingPodFailed  = "try_numa_binding_pod_failed"
	metricNameTryNUMABindingNodeFailed  = "try_numa_binding_node_failed"
	metricNameTryNUMABindingNodeSuccess = "try_numa_binding_node_success"

	metricNameCalculateTimeCost = "calculate_time_cost"
)

func checkAllNUMABindingResult(emitter metrics.MetricEmitter, calculator string, success bool, result allocation.PodAllocations) bool {
	unSuccessPods := make(map[string]types.NamespacedName)
	for podUID, alloc := range result {
		if alloc.BindingNUMA == -1 {
			unSuccessPods[podUID] = alloc.NamespacedName
		}
	}

	if len(unSuccessPods) > 0 || !success {
		general.Errorf("check calculator %s result failed, unSuccessPods: %v", calculator, unSuccessPods)
		for _, nn := range unSuccessPods {
			_ = emitter.StoreInt64(metricsNameTryNUMABindingPodFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "calculator", Val: calculator},
				metrics.MetricTag{Key: "podName", Val: nn.Name},
				metrics.MetricTag{Key: "podNamespace", Val: nn.Namespace},
				metrics.MetricTag{Key: "success", Val: strconv.FormatBool(success)})
		}
		_ = emitter.StoreInt64(metricNameTryNUMABindingNodeFailed, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "calculator", Val: calculator},
			metrics.MetricTag{Key: "success", Val: strconv.FormatBool(success)})
		return false
	}
	_ = emitter.StoreInt64(metricNameTryNUMABindingNodeSuccess, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "calculator", Val: calculator})
	return true
}

type withCheckAndExecutionTimeLogging struct {
	emitter metrics.MetricEmitter
	NUMABindingCalculator
}

func (w *withCheckAndExecutionTimeLogging) CalculateNUMABindingResult(current allocation.PodAllocations,
	numaAllocatable state.NUMAResource,
) (allocation.PodAllocations, bool, error) {
	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		general.InfoS("finished calculate numa result", "calculator", w.Name(), "costs", costs)
		_ = w.emitter.StoreInt64(metricNameCalculateTimeCost, costs.Microseconds(), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "calculator", Val: w.Name()})
	}()

	result, success, err := w.NUMABindingCalculator.CalculateNUMABindingResult(current, numaAllocatable)
	if err != nil {
		return nil, false, err
	}

	general.InfoS("calculate numa result", "calculator", w.Name(), "result", result, "success", success)

	return result, checkAllNUMABindingResult(w.emitter, w.Name(), success, result), nil
}

func WithCheckAndExecutionTimeLogging(calculator NUMABindingCalculator, emitter metrics.MetricEmitter) NUMABindingCalculator {
	return &withCheckAndExecutionTimeLogging{
		emitter:               emitter,
		NUMABindingCalculator: calculator,
	}
}
