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

package metricring

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const (
	testMetricRate  = "rate"
	testMetricUsage = "usage"
)

func TestMultiMetricRing_LenAndAvg(t *testing.T) {
	t.Parallel()

	ring := NewMultiMetricRing(3)

	// fewer samples than capacity
	ring.Push(map[string]float64{testMetricRate: 6, testMetricUsage: 0.5})
	time.Sleep(time.Millisecond)
	ring.Push(map[string]float64{testMetricRate: 9, testMetricUsage: 0.8})

	assert.Equal(t, 2, ring.Len())
	assert.InDelta(t, 7.5, ring.Avg(testMetricRate), 1e-9)
	assert.InDelta(t, 0.65, ring.Avg(testMetricUsage), 1e-9)

	// overflowing the capacity keeps Len at MaxLen and drops the oldest sample
	time.Sleep(time.Millisecond)
	ring.Push(map[string]float64{testMetricRate: 15, testMetricUsage: 0.2})
	time.Sleep(time.Millisecond)
	ring.Push(map[string]float64{testMetricRate: 12, testMetricUsage: 0.4})

	assert.Equal(t, 3, ring.Len())
	// oldest (6, 0.5) is evicted; average over (9,0.8),(15,0.2),(12,0.4)
	assert.InDelta(t, 12.0, ring.Avg(testMetricRate), 1e-9)
	assert.InDelta(t, 1.4/3, ring.Avg(testMetricUsage), 1e-9)
}

func TestMultiMetricRing_CountWhereJoint(t *testing.T) {
	t.Parallel()

	ring := NewMultiMetricRing(4)

	// sample 1: rate over but usage under -> not joint
	ring.Push(map[string]float64{testMetricRate: 9, testMetricUsage: 0.5})
	time.Sleep(time.Millisecond)
	// sample 2: usage over but rate under -> not joint (the staggered case)
	ring.Push(map[string]float64{testMetricRate: 3, testMetricUsage: 0.9})
	time.Sleep(time.Millisecond)
	// sample 3: both over -> joint soft
	ring.Push(map[string]float64{testMetricRate: 9, testMetricUsage: 0.9})
	time.Sleep(time.Millisecond)
	// sample 4: rate over hard, but usage under the soft threshold -> not joint soft
	ring.Push(map[string]float64{testMetricRate: 15, testMetricUsage: 0.5})

	softCount := ring.CountWhere(func(v map[string]float64) bool {
		return v[testMetricRate] > 5 && v[testMetricUsage] > 0.8
	})
	hardCount := ring.CountWhere(func(v map[string]float64) bool {
		return v[testMetricRate] > 10 && v[testMetricUsage] > 0.01
	})

	assert.Equal(t, 1, softCount)
	assert.Equal(t, 1, hardCount)
}

func TestMultiMetricRing_EmptyAvg(t *testing.T) {
	t.Parallel()

	ring := NewMultiMetricRing(2)
	assert.Equal(t, 0, ring.Len())
	assert.Equal(t, 0.0, ring.Avg(testMetricRate))
	assert.Equal(t, 0, ring.CountWhere(func(map[string]float64) bool { return true }))
}

func TestNumaMultiMetricRing_PushAndIsolation(t *testing.T) {
	t.Parallel()

	ring := NewNumaMultiMetricRing(2)

	ring.Push(0, "pod-a", map[string]float64{testMetricRate: 9})
	time.Sleep(time.Millisecond)
	ring.PushNuma(1, map[string]float64{testMetricUsage: 0.9})

	assert.Len(t, ring.Inner, 2)
	assert.Equal(t, 1, ring.Inner[0]["pod-a"].Len())
	assert.Equal(t, 1, ring.Inner[1][""].Len())

	// different numa / pod entries do not share a ring
	assert.Nil(t, ring.Inner[0]["pod-b"])
	assert.InDelta(t, 9.0, ring.Inner[0]["pod-a"].Avg(testMetricRate), 1e-9)
	assert.InDelta(t, 0.9, ring.Inner[1][""].Avg(testMetricUsage), 1e-9)
}
