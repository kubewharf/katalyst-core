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
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	SmoothWindowAggFuncAvg  = "average"
	SmoothWindowAggFuncPerc = "percentile"
)

// SmoothWindow is used to smooth the resource
type SmoothWindow interface {
	// GetWindowedResources receives a sample and returns the result after smoothing,
	// it can return nil if there are not enough samples in this window
	GetWindowedResources(value resource.Quantity) *resource.Quantity

	Empty() bool
}

type SmoothWindowOpts struct {
	WindowSize    int
	TTL           time.Duration
	UsedMillValue bool
	AggregateFunc string
	AggregateArgs string
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

type TTLSmoothWindow struct {
	sync.RWMutex
	windowSize    int
	ttl           time.Duration
	usedMillValue bool

	index   int
	samples []*sample
}

func (w *TTLSmoothWindow) getValidSamples() []resource.Quantity {
	w.RWMutex.RLock()
	defer w.RWMutex.RUnlock()

	timestamp := time.Now()

	validSamples := make([]resource.Quantity, 0)
	for _, s := range w.samples {
		if s != nil && s.timestamp.Add(w.ttl).After(timestamp) {
			validSamples = append(validSamples, s.value)
		}
	}
	return validSamples
}

func (w *TTLSmoothWindow) pushSample(value resource.Quantity) {
	w.RWMutex.Lock()
	defer w.RWMutex.Unlock()

	timestamp := time.Now()
	w.samples[w.index] = &sample{
		value:     value,
		timestamp: timestamp,
	}

	w.index++
	if w.index >= w.windowSize {
		w.index = 0
	}
}

func (w *TTLSmoothWindow) Empty() bool {
	validSamples := w.getValidSamples()
	return len(validSamples) == 0
}

func NewTTLSmoothWindow(windowSize int, ttl time.Duration, usedMillValue bool) *TTLSmoothWindow {
	return &TTLSmoothWindow{
		windowSize:    windowSize,
		ttl:           ttl,
		usedMillValue: usedMillValue,
		index:         0,
		samples:       make([]*sample, windowSize),
	}
}

type averageWithTTLSmoothWindow struct {
	*TTLSmoothWindow
}

func (w *averageWithTTLSmoothWindow) getValueByAvg(values []resource.Quantity) resource.Quantity {
	count := 0
	total := resource.Quantity{}

	for _, value := range values {
		count++
		total.Add(value)
	}

	avg := total.AsApproximateFloat64() / float64(count)
	return *resource.NewMilliQuantity(int64(avg*1000), resource.DecimalSI)
}

type sample struct {
	value     resource.Quantity
	timestamp time.Time
}

func NewAggregatorSmoothWindow(opts SmoothWindowOpts) SmoothWindow {
	switch opts.AggregateFunc {
	case SmoothWindowAggFuncAvg:
		return NewAverageWithTTLSmoothWindow(opts.WindowSize, opts.TTL, opts.UsedMillValue)
	case SmoothWindowAggFuncPerc:
		perc, err := strconv.ParseFloat(opts.AggregateArgs, 64)
		if err != nil {
			Errorf("failed to parse AggregateArgs %v, fallback to default aggregator", opts.AggregateFunc)
		} else {
			return NewPercentileWithTTLSmoothWindow(opts.WindowSize, opts.TTL, perc, opts.UsedMillValue)
		}
	}
	return NewAverageWithTTLSmoothWindow(opts.WindowSize, opts.TTL, opts.UsedMillValue)
}

// NewAverageWithTTLSmoothWindow create a smooth window with ttl and window size, and the window size
// is the sample count while the ttl is the valid lifetime of each sample, and the usedMillValue means
// whether calculate the result with milli-value.
func NewAverageWithTTLSmoothWindow(windowSize int, ttl time.Duration, usedMillValue bool) SmoothWindow {
	return &averageWithTTLSmoothWindow{
		TTLSmoothWindow: NewTTLSmoothWindow(windowSize, ttl, usedMillValue),
	}
}

// GetWindowedResources inserts a sample, and returns the smoothed result by average all the valid samples.
func (w *averageWithTTLSmoothWindow) GetWindowedResources(value resource.Quantity) *resource.Quantity {
	w.pushSample(value)
	validSamples := w.getValidSamples()

	// if count of valid sample is not enough just return nil
	if len(validSamples) != w.windowSize {
		return nil
	}

	v := w.getValueByAvg(validSamples)

	if w.usedMillValue {
		return resource.NewMilliQuantity(v.MilliValue(), value.Format)
	}

	return resource.NewQuantity(v.Value(), value.Format)
}

type percentileWithTTLSmoothWindow struct {
	*TTLSmoothWindow

	percentile float64
}

// NewPercentileWithTTLSmoothWindow create a smooth window with ttl and window size, and the window size
// is the sample count while the ttl is the valid lifetime of each sample, and the usedMillValue means
// whether calculate the result with milli-value.
func NewPercentileWithTTLSmoothWindow(windowSize int, ttl time.Duration, percentile float64, usedMillValue bool) SmoothWindow {
	return &percentileWithTTLSmoothWindow{
		TTLSmoothWindow: NewTTLSmoothWindow(windowSize, ttl, usedMillValue),
		percentile:      percentile,
	}
}

// GetWindowedResources inserts a sample, and returns the smoothed result by average all the valid samples.
func (w *percentileWithTTLSmoothWindow) GetWindowedResources(value resource.Quantity) *resource.Quantity {
	w.pushSample(value)
	validSamples := w.getValidSamples()

	// if count of valid sample is not enough just return nil
	if len(validSamples) != w.windowSize {
		return nil
	}

	v := w.getValueByPercentile(validSamples, w.percentile)

	if w.usedMillValue {
		return resource.NewMilliQuantity(v.MilliValue(), value.Format)
	}

	return resource.NewQuantity(v.Value(), value.Format)
}

func (w *percentileWithTTLSmoothWindow) getValueByPercentile(values []resource.Quantity, percentile float64) resource.Quantity {
	sort.Slice(values, func(i, j int) bool {
		return values[i].Cmp(values[j]) < 0
	})

	percentileIndex := int(math.Ceil(float64(len(values))*percentile/100.0) - 1)
	if percentileIndex < 0 {
		percentileIndex = 0
	} else if percentileIndex >= len(values) {
		percentileIndex = len(values) - 1
	}
	return values[percentileIndex]
}
