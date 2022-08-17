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

package general

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// SmoothWindow is used to smooth the resource
type SmoothWindow interface {
	// GetWindowedResources receives a sample and returns the result after smoothing,
	// it can return nil if there are not enough samples in this window
	GetWindowedResources(value resource.Quantity) *resource.Quantity
}

type CappedSmoothWindow struct {
	sync.Mutex
	last    *resource.Quantity
	minStep resource.Quantity
	maxStep resource.Quantity
	SmoothWindow
}

// NewCappedSmoothWindow creates a capped SmoothWindow, which
func NewCappedSmoothWindow(minStep resource.Quantity, maxStep resource.Quantity, smoothWindow SmoothWindow) *CappedSmoothWindow {
	return &CappedSmoothWindow{minStep: minStep, maxStep: maxStep, SmoothWindow: smoothWindow}
}

// GetWindowedResources cap the value return by smooth window min to max
func (m *CappedSmoothWindow) GetWindowedResources(value resource.Quantity) *resource.Quantity {
	m.Lock()
	defer m.Unlock()

	cur := m.SmoothWindow.GetWindowedResources(value)
	if cur == nil {
		cur = m.last
	} else if m.last == nil {
		m.last = cur
	} else if cur.Cmp(*m.last) > 0 {
		step := cur.DeepCopy()
		step.Sub(*m.last)
		if step.Cmp(m.minStep) < 0 {
			cur = m.last
		} else if step.Cmp(m.maxStep) > 0 {
			m.last.Add(m.maxStep)
			cur = m.last
		} else {
			m.last = cur
		}
	} else {
		step := m.last.DeepCopy()
		step.Sub(*cur)
		if step.Cmp(m.minStep) < 0 {
			cur = m.last
		} else if step.Cmp(m.maxStep) > 0 {
			m.last.Sub(m.maxStep)
			cur = m.last
		} else {
			m.last = cur
		}
	}

	if cur == nil {
		return nil
	}

	ret := cur.DeepCopy()
	return &ret
}

type averageWithTTLSmoothWindow struct {
	sync.Mutex
	windowSize    int
	ttl           time.Duration
	usedMillValue bool

	index   int
	samples []*sample
}

type sample struct {
	value     resource.Quantity
	timestamp time.Time
}

// NewAverageWithTTLSmoothWindow create a smooth window with ttl and window size, and the window size
// is the sample count while the ttl is the valid lifetime of each sample, and the usedMillValue means
// whether calculate the result with milli-value.
func NewAverageWithTTLSmoothWindow(windowSize int, ttl time.Duration, usedMillValue bool) SmoothWindow {
	return &averageWithTTLSmoothWindow{
		windowSize:    windowSize,
		ttl:           ttl,
		usedMillValue: usedMillValue,
		index:         0,
		samples:       make([]*sample, windowSize),
	}
}

// GetWindowedResources inserts a sample, and returns the smoothed result by average all the valid samples.
func (w *averageWithTTLSmoothWindow) GetWindowedResources(value resource.Quantity) *resource.Quantity {
	w.Mutex.Lock()
	defer w.Mutex.Unlock()

	timestamp := time.Now()
	w.samples[w.index] = &sample{
		value:     value,
		timestamp: timestamp,
	}

	w.index++
	if w.index >= w.windowSize {
		w.index = 0
	}

	total := resource.Quantity{}
	count := int64(0)
	for _, s := range w.samples {
		if s != nil && s.timestamp.Add(w.ttl).After(timestamp) {
			total.Add(s.value)
			count++
		}
	}

	// if count of valid sample is not enough just return nil
	if count != int64(w.windowSize) {
		return nil
	}

	if w.usedMillValue {
		return resource.NewMilliQuantity(total.MilliValue()/count, value.Format)
	}

	return resource.NewQuantity(total.Value()/count, value.Format)
}
