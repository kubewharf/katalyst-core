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

package strategy

import (
	"sync"

	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
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
	return len(se) == 1 && se[advisorapi.FakedContainerName] != nil
}

// Entries are keyed by pod UID or pool name
type Entries map[string]SubEntries

type PoolMetricCollectHandler func(metricName string, metricValue float64, _ *state.AllocationInfo, collectTime int64)

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

func CreateMetricRing(size int) *MetricRing {
	return &MetricRing{
		MaxLen:       size,
		Queue:        make([]*MetricSnapshot, size),
		CurrentIndex: -1,
	}
}
