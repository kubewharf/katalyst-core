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

package util

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

type MetricInfo struct {
	Name       string
	Value      float64
	UpperBound float64
	LowerBound float64
}

type MetricSnapshot struct {
	Info MetricInfo
	Time int64
}

type MetricRing struct {
	MaxLen       int
	Queue        []*MetricSnapshot
	CurrentIndex int

	sync.RWMutex
}

// SubEntries is keyed by container name or empty string (for pool)
type SubEntries map[string]*MetricRing

func (se SubEntries) IsPoolEntry() bool {
	return len(se) == 1 && se[commonstate.FakedContainerName] != nil
}

// Entries are keyed by pod UID or pool name
type Entries map[string]SubEntries

type PoolMetricCollectHandler func(dynamicConfig *dynamic.Configuration, poolsUnderPressure bool,
	metricName string, metricValue float64, poolName string,
	poolSize int, collectTime int64, allowSharedCoresOverlapReclaimedCores bool)

func (ring *MetricRing) AvgAfterTimestampWithCountBound(ts int64, countBound int) (float64, error) {
	ring.RLock()
	defer ring.RUnlock()

	sum := 0.0
	count := 0
	for _, snapshot := range ring.Queue {
		if snapshot != nil && snapshot.Time > ts {
			sum += snapshot.Info.Value
			count++
		}
	}

	if count < countBound {
		return 0.0, fmt.Errorf("not enough timely data to calculate avg")
	}
	return sum / float64(count), nil
}

func (ring *MetricRing) Sum() float64 {
	ring.RLock()
	defer ring.RUnlock()

	sum := 0.0
	for _, snapshot := range ring.Queue {
		if snapshot != nil {
			sum += snapshot.Info.Value
		}
	}
	return sum
}

func (ring *MetricRing) Avg() float64 {
	length := len(ring.Queue)
	if length == 0 {
		return 0
	}
	return ring.Sum() / float64(length)
}

func (ring *MetricRing) Push(snapShot *MetricSnapshot) {
	ring.Lock()
	defer ring.Unlock()

	if ring.CurrentIndex != -1 && snapShot != nil {
		latestSnapShot := ring.Queue[ring.CurrentIndex]
		if latestSnapShot != nil && latestSnapShot.Time == snapShot.Time {
			return
		}
	}

	ring.CurrentIndex = (ring.CurrentIndex + 1) % ring.MaxLen
	ring.Queue[ring.CurrentIndex] = snapShot
}

func (ring *MetricRing) Len() int {
	ring.RLock()
	defer ring.RUnlock()

	count := 0
	for _, snapshot := range ring.Queue {
		if snapshot == nil {
			continue
		}
		count++
	}

	return count
}

func (ring *MetricRing) Count() (softOverCount, hardOverCount int) {
	ring.RLock()
	defer ring.RUnlock()

	for _, snapshot := range ring.Queue {
		if snapshot == nil {
			continue
		}

		if snapshot.Info.LowerBound > 0 {
			if snapshot.Info.Value > snapshot.Info.LowerBound {
				softOverCount++
			}
			if snapshot.Info.Value > snapshot.Info.UpperBound {
				hardOverCount++
			}
		}
	}
	return
}

// OverCount pass threshold from outside
func (ring *MetricRing) OverCount(threshold float64) (overCount int) {
	ring.RLock()
	defer ring.RUnlock()

	for _, snapshot := range ring.Queue {
		if snapshot == nil {
			continue
		}

		if snapshot.Info.Value > threshold {
			overCount++
		}
	}
	return
}

func CreateMetricRing(size int) *MetricRing {
	return &MetricRing{
		MaxLen:       size,
		Queue:        make([]*MetricSnapshot, size),
		CurrentIndex: -1,
	}
}
