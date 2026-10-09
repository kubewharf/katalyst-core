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

package reclaim

import (
	"reflect"
	"testing"
)

func TestScaleNUMAHeadroomAppliesPercentageAndUnitScale(t *testing.T) {
	t.Parallel()
	got := ScaleNUMAHeadroom(map[int]float64{
		0: 1.5,
		1: 2.333,
	}, 50, 1000)
	want := map[int]float64{
		0: 750,
		1: 1166.5,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NUMAHeadroomMilliCPU = %#v, want %#v", got, want)
	}
}

func TestScaleNUMAHeadroomAllowsPercentageAboveOneHundred(t *testing.T) {
	t.Parallel()
	if got := ScaleNUMAHeadroom(map[int]float64{0: 1}, 101, 1); !reflect.DeepEqual(got, map[int]float64{0: 1.01}) {
		t.Fatalf("ScaleNUMAHeadroom = %#v, want 1.01", got)
	}
}

func TestGetReclaimedNUMAHeadroomUsesConfiguredConsumerPercentage(t *testing.T) {
	t.Parallel()
	// Inject a local consumer view instead of mutating the process-global
	// registry: only "consumer-a" is registered for this test.
	view := ReclaimedHeadroom{Consumers: func(name string) bool { return name == "consumer-a" }}

	got := view.Compute(
		map[int]float64{0: 1.5, 1: 2},
		newDynamicWithPercentages(map[string]int{"consumer-a": 50}),
		"consumer-a",
	)
	want := map[int]float64{0: 0.75, 1: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetReclaimedNUMAHeadroom = %#v, want %#v", got, want)
	}
}

func TestGetReclaimedNUMAHeadroomOnlyCountsRegisteredConsumersAndCapsAtOneHundred(t *testing.T) {
	t.Parallel()
	// Local membership view: only consumer-a and consumer-b are registered;
	// "unknown" is not, so it contributes 0 and the sum is capped at 100.
	registered := map[string]bool{"consumer-a": true, "consumer-b": true}
	view := ReclaimedHeadroom{Consumers: func(name string) bool { return registered[name] }}

	got := view.Compute(
		map[int]float64{0: 1},
		newDynamicWithPercentages(map[string]int{
			"consumer-a": 60,
			"consumer-b": 60,
			"unknown":    80,
		}),
		"consumer-a",
		"unknown",
		"consumer-b",
	)
	want := map[int]float64{0: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetReclaimedNUMAHeadroom = %#v, want %#v", got, want)
	}
}

func TestValidateNUMAHeadroomRejectsMismatchedNUMAs(t *testing.T) {
	t.Parallel()
	if err := ValidateNUMAHeadroom(
		map[int]float64{0: 1},
		map[int]float64{1: 1},
	); err == nil {
		t.Fatalf("ValidateNUMAHeadroom succeeded for mismatched NUMAs")
	}
}
