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

package reclaim

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

// ScaleNUMAHeadroom returns a scaled copy of per-NUMA headroom values.
// percentage must be non-negative; unitScale converts the input unit to the
// caller's output unit, such as cores to milliCPU.
func ScaleNUMAHeadroom(headroom map[int]float64, percentage, unitScale float64) map[int]float64 {
	if percentage < 0 || unitScale < 0 {
		return nil
	}
	scaled := make(map[int]float64, len(headroom))
	for numaID, value := range headroom {
		scaled[numaID] = value * unitScale * percentage / 100
	}
	return scaled
}

// ReclaimedHeadroom computes per-NUMA reclaimed headroom against an explicit
// consumer membership view. When Consumers is nil the computation falls back to
// the process-global reclaim registry (production default), which preserves the
// behavior of GetReclaimedNUMAHeadroom. Tests inject a local view so they do not
// mutate or serialize on the process-global registry.
type ReclaimedHeadroom struct {
	// Consumers reports whether a named consumer is registered. A nil value
	// consults the process-global registry.
	Consumers func(name string) bool
}

// Percentage returns the summed, capped reclaimed percentage for names under the
// configured consumer view.
func (r ReclaimedHeadroom) Percentage(d *dynamic.Configuration, names ...string) float64 {
	lookup := r.Consumers
	if lookup == nil {
		lookup = globalConsumerLookup
	}
	return summedReclaimedPercentage(lookup, d, names)
}

// Compute returns the per-NUMA headroom attributable to names. The result uses
// the same unit as headroom.
func (r ReclaimedHeadroom) Compute(headroom map[int]float64, d *dynamic.Configuration, names ...string) map[int]float64 {
	return ScaleNUMAHeadroom(headroom, r.Percentage(d, names...), 1)
}

// GetReclaimedNUMAHeadroom returns the per-NUMA headroom attributable to the
// configured consumers. The result uses the same unit as headroom.
func GetReclaimedNUMAHeadroom(
	headroom map[int]float64,
	dynamicConfig *dynamic.Configuration,
	consumerNames ...string,
) map[int]float64 {
	return ReclaimedHeadroom{}.Compute(headroom, dynamicConfig, consumerNames...)
}

// ValidateNUMAHeadroom verifies that all resource headroom maps are non-empty,
// have the same NUMA IDs, and contain no negative values.
func ValidateNUMAHeadroom(headrooms ...map[int]float64) error {
	if len(headrooms) == 0 || len(headrooms[0]) == 0 {
		return fmt.Errorf("NUMA headroom is empty")
	}
	for numaID, value := range headrooms[0] {
		if value < 0 {
			return fmt.Errorf("NUMA headroom is negative for NUMA %d", numaID)
		}
	}
	for _, headroom := range headrooms[1:] {
		if len(headroom) != len(headrooms[0]) {
			return fmt.Errorf("NUMA headroom IDs do not match")
		}
		for numaID, value := range headroom {
			if _, ok := headrooms[0][numaID]; !ok {
				return fmt.Errorf("NUMA headroom is missing NUMA %d", numaID)
			}
			if value < 0 {
				return fmt.Errorf("NUMA headroom is negative for NUMA %d", numaID)
			}
		}
	}
	return nil
}
