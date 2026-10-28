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
	"sync"
	"time"
)

// MultiMetricSample is one synchronized observation that carries several
// metrics collected at the same moment, so a joint threshold check can read
// all values from the same sample instead of counting independent metric rings.
type MultiMetricSample struct {
	// Values maps a metric name to its observed value.
	Values map[string]float64
	// Time is the collection timestamp in nanoseconds.
	Time int64
}

// MultiMetricRing is a fixed-size circular buffer of synchronized samples.
// Every slot holds a multi-metric sample; the ring itself is threshold agnostic,
// callers pass a predicate to decide whether a sample counts.
type MultiMetricRing struct {
	MaxLen       int
	Queue        []*MultiMetricSample
	CurrentIndex int

	sync.RWMutex
}

// NewMultiMetricRing creates an empty MultiMetricRing with the given capacity.
func NewMultiMetricRing(size int) *MultiMetricRing {
	return &MultiMetricRing{
		MaxLen:       size,
		Queue:        make([]*MultiMetricSample, size),
		CurrentIndex: -1,
	}
}

// Push records a synchronized sample; samples pushed at the same timestamp are
// deduplicated to keep one observation per collection round.
func (r *MultiMetricRing) Push(values map[string]float64) {
	r.Lock()
	defer r.Unlock()

	collectTime := time.Now().UnixNano()
	if r.CurrentIndex != -1 {
		latest := r.Queue[r.CurrentIndex]
		if latest != nil && latest.Time == collectTime {
			return
		}
	}

	r.CurrentIndex = (r.CurrentIndex + 1) % r.MaxLen
	r.Queue[r.CurrentIndex] = &MultiMetricSample{
		Values: values,
		Time:   collectTime,
	}
}

// Len returns the number of buffered samples.
func (r *MultiMetricRing) Len() int {
	r.RLock()
	defer r.RUnlock()

	count := 0
	for _, sample := range r.Queue {
		if sample != nil {
			count++
		}
	}
	return count
}

// Avg returns the mean of the given metric over buffered samples; a missing
// metric is treated as 0, and it returns 0 when the ring is empty.
func (r *MultiMetricRing) Avg(metric string) float64 {
	r.RLock()
	defer r.RUnlock()

	sum := 0.0
	count := 0
	for _, sample := range r.Queue {
		if sample == nil {
			continue
		}
		sum += sample.Values[metric]
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// CountWhere counts buffered samples for which fn returns true. The predicate
// receives the sample's values and decides whether the sample meets the condition.
func (r *MultiMetricRing) CountWhere(fn func(values map[string]float64) bool) int {
	r.RLock()
	defer r.RUnlock()

	count := 0
	for _, sample := range r.Queue {
		if sample == nil {
			continue
		}
		if fn(sample.Values) {
			count++
		}
	}
	return count
}
