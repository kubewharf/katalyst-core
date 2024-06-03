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

package timemonitor

import (
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// TimeMonitor is used to periodically monitor if time.Now().Sub(refreshTime) is greater than durationThreshold
type TimeMonitor struct {
	name                string
	interval            time.Duration
	unhealthyMetricName string
	emitter             metrics.MetricEmitter
	sync.RWMutex

	unhealthyThreshold time.Duration

	healthyThreshold time.Duration

	healthy bool

	// cache is used a cyclic buffer - its first element (with the smallest
	// resourceVersion) is defined by startIndex, its last element is defined
	// by endIndex (if cache is full it will be startIndex + capacity).
	// Both startIndex and endIndex can be greater than buffer capacity -
	// you should always apply modulo capacity to get an index in cache array.
	refreshTimestampsBuffer []time.Time
	// index won't overflow in the foreseeable future.
	startIndex int
	endIndex   int
	capacity   int
}

func NewTimeMonitor(name string, interval, unhealthyThreshold, healthyThreshold time.Duration,
	unhealthyMetricName string, emitter metrics.MetricEmitter, capacity int, healthy bool,
) (*TimeMonitor, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("invalid capacity: %d", capacity)
	} else if emitter == nil {
		return nil, fmt.Errorf("invalid emitter")
	}

	tm := &TimeMonitor{
		name:                    name,
		interval:                interval,
		unhealthyMetricName:     unhealthyMetricName,
		emitter:                 emitter,
		unhealthyThreshold:      unhealthyThreshold,
		healthyThreshold:        healthyThreshold,
		capacity:                capacity,
		refreshTimestampsBuffer: make([]time.Time, capacity),
		healthy:                 healthy,
	}

	// UpdateRefreshTime at first,
	// to make monitorRefreshTime works when initial healthy status is true
	tm.UpdateRefreshTime()

	return tm, nil
}

func (m *TimeMonitor) isBufferFullLocked() bool {
	return m.endIndex == m.startIndex+m.capacity
}

func (m *TimeMonitor) UpdateRefreshTime() {
	m.Lock()
	if m.isBufferFullLocked() {
		m.startIndex++
	}
	refreshTime := time.Now()
	m.refreshTimestampsBuffer[m.endIndex%m.capacity] = refreshTime
	m.endIndex++
	m.Unlock()
	general.Warningf("TimeMonitor: %s refresh time: %s", m.name, refreshTime)
}

func (m *TimeMonitor) monitor() {
	m.Lock()
	defer m.Unlock()
	if m.healthy {
		newestTs := m.refreshTimestampsBuffer[(m.endIndex-1)%m.capacity]
		if time.Since(newestTs) > m.unhealthyThreshold {
			general.Warningf("TimeMonitor: %s newest refreshTime is outdated, refreshTime: %s; set status to unhealthy", m.name, newestTs)
			m.healthy = false
		}
	} else {
		if m.isBufferFullLocked() {
			oldestTs := m.refreshTimestampsBuffer[m.startIndex%m.capacity]
			if time.Since(oldestTs) <= m.healthyThreshold {
				general.Warningf("TimeMonitor: %s received %d events in %s seconds; set status to healthy", m.name, m.capacity, m.healthyThreshold)
				m.healthy = true
			}
		}
	}

	if !m.healthy {
		_ = m.emitter.StoreInt64(m.unhealthyMetricName, 1, metrics.MetricTypeNameRaw)
	}

	general.Infof("TimeMonitor: %s, healthy: %v", m.name, m.healthy)
}

// Run is blocking runned
func (m *TimeMonitor) Run(stopCh <-chan struct{}) {
	wait.Until(m.monitor, m.interval, stopCh)
}

func (m *TimeMonitor) GetHealthy() bool {
	if m == nil {
		return true
	}

	m.RLock()
	defer m.RUnlock()
	return m.healthy
}
