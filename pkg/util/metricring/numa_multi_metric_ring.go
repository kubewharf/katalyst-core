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

// NumaMultiMetricRing groups per-node (or per-group) MultiMetricRing buffers,
// indexed as numa -> podUID -> ring. Each ring carries a synchronized set of
// metrics, which replaces splitting one observation into several per-metric rings.
type NumaMultiMetricRing struct {
	// Inner maps numaID to podUID to the corresponding MultiMetricRing.
	Inner    map[int]map[string]*MultiMetricRing
	RingSize int
}

// NewNumaMultiMetricRing creates an empty NumaMultiMetricRing with the given
// per-ring capacity.
func NewNumaMultiMetricRing(ringSize int) *NumaMultiMetricRing {
	return &NumaMultiMetricRing{
		Inner:    make(map[int]map[string]*MultiMetricRing),
		RingSize: ringSize,
	}
}

// Push records a synchronized sample for the given numa and podUID, lazily
// creating the underlying ring on first use.
func (m *NumaMultiMetricRing) Push(numaID int, podUID string, values map[string]float64) {
	if m.Inner[numaID] == nil {
		m.Inner[numaID] = make(map[string]*MultiMetricRing)
	}
	if m.Inner[numaID][podUID] == nil {
		m.Inner[numaID][podUID] = NewMultiMetricRing(m.RingSize)
	}
	m.Inner[numaID][podUID].Push(values)
}

// PushNuma records a synchronized sample for a numa-level (non-pod) entry,
// using the empty podUID.
func (m *NumaMultiMetricRing) PushNuma(numaID int, values map[string]float64) {
	m.Push(numaID, "", values)
}
