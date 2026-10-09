/*
Copyright 2026 The Katalyst Authors.

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

package region

import (
	"errors"
	"fmt"
	"math"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var errIndicatorUnavailable = errors.New("indicator unavailable")

func (r *QoSRegionBase) getAverageCoreMetric(cpuSet machine.CPUSet, metricName string) (float64, error) {
	if cpuSet.IsEmpty() {
		return 0, fmt.Errorf("%w: empty cpu set for metric %s", errIndicatorUnavailable, metricName)
	}

	var sum float64
	validSamples := 0
	for _, cpuID := range cpuSet.ToSliceInt() {
		data, err := r.metaServer.GetCPUMetric(cpuID, metricName)
		if err != nil || math.IsNaN(data.Value) || math.IsInf(data.Value, 0) {
			continue
		}
		sum += data.Value
		validSamples++
	}
	if validSamples == 0 {
		return 0, fmt.Errorf(
			"%w: no valid samples for metric %s on cpus %s",
			errIndicatorUnavailable,
			metricName,
			cpuSet.String(),
		)
	}

	return sum / float64(validSamples), nil
}

func isIndicatorValueValid(
	regionType configapi.QoSRegionType,
	indicatorName workloadapis.ServiceSystemIndicatorName,
	value types.IndicatorValue,
) bool {
	if math.IsNaN(value.Current) || math.IsInf(value.Current, 0) ||
		math.IsNaN(value.Target) || math.IsInf(value.Target, 0) ||
		value.Target <= 0 {
		return false
	}
	if regionType == configapi.QoSRegionTypeDedicated &&
		indicatorName == workloadapis.ServiceSystemIndicatorNameCPUSchedWait {
		return value.Current >= 0
	}
	return value.Current > 0
}
