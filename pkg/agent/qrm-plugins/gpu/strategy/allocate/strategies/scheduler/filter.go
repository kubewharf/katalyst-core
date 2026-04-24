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

package scheduler

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// Filter filters the available GPU devices based on the allocation context.
// It reads the GPU selection result from the pod's annotations and returns the intersection
// with all available devices.
func (s *SchedulerStrategy) Filter(
	ctx *allocate.AllocationContext, allAvailableDevices []string,
) ([]string, error) {
	if ctx.ResourceReq == nil || ctx.GPUQRMPluginConfig == nil {
		return allAvailableDevices, nil
	}

	key := ctx.GPUQRMPluginConfig.GPUSelectionResultKey
	selectionResult, ok := ctx.ResourceReq.Annotations[key]
	if !ok || selectionResult == "" {
		return allAvailableDevices, nil
	}

	selectedDevices := strings.Split(selectionResult, ",")
	selectedSet := sets.NewString(selectedDevices...)
	availableSet := sets.NewString(allAvailableDevices...)

	intersection := availableSet.Intersection(selectedSet)
	if intersection.Len() == 0 {
		_ = ctx.Emitter.StoreInt64("gpu_scheduler_selection_mismatch", 1, metrics.MetricTypeNameCount)
		return allAvailableDevices, nil
	}
	return intersection.List(), nil
}
