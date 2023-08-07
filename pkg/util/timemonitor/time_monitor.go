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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// TimeMonitor is used to periodically monitor if time.Now().Sub(refreshTime) is greater than durationThreshold
type TimeMonitor struct {
	name              string
	refreshTime       time.Time
	durationThreshold time.Duration
	interval          time.Duration

	metricName string
	emitter    metrics.MetricEmitter

	sync.RWMutex
}

func NewTimeMonitor(name string, durationThreshold, interval time.Duration, metricName string, emitter metrics.MetricEmitter) *TimeMonitor {
	return &TimeMonitor{
		name:              name,
		durationThreshold: durationThreshold,
		interval:          interval,
		metricName:        metricName,
		emitter:           emitter,
	}
}

func (m *TimeMonitor) UpdateRefreshTime() {
	m.Lock()
	m.refreshTime = time.Now()
	general.Warningf("TimeMonitor: %s update refreshTime: %s", m.name, m.refreshTime)
	m.Unlock()
}

func (m *TimeMonitor) GetRefreshTime() time.Time {
	m.RLock()
	defer m.RUnlock()
	return m.refreshTime

}

func (m *TimeMonitor) monitorRefreshTime() {
	refreshTime := m.GetRefreshTime()

	if refreshTime.IsZero() || time.Since(refreshTime) > m.durationThreshold {
		general.Warningf("TimeMonitor: %s refreshTime stucks, refreshTime: %s", m.name, refreshTime)
		if m.emitter != nil && m.metricName != "" {
			_ = m.emitter.StoreInt64(m.metricName, 1, metrics.MetricTypeNameRaw)
		}
	}
}

// Run is blocking runned
func (m *TimeMonitor) Run(stopCh <-chan struct{}) {
	wait.Until(m.monitorRefreshTime, m.interval, stopCh)
}
