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
	"time"
)

const (
	FakePodUID = ""
)

type NumaMetricHistory struct {
	// numa -> pod -> metric -> ring
	Inner    map[int]map[string]map[string]*MetricRing
	RingSize int
}

func NewMetricHistory(ringSize int) *NumaMetricHistory {
	return &NumaMetricHistory{
		Inner:    make(map[int]map[string]map[string]*MetricRing),
		RingSize: ringSize,
	}
}

func (m *NumaMetricHistory) Push(numaID int, podUID string, metricName string, podMetric, upperBound, lowerBound float64) {
	collectTime := time.Now().UnixNano()

	if m.Inner[numaID] == nil {
		m.Inner[numaID] = make(map[string]map[string]*MetricRing)
	}
	if m.Inner[numaID][podUID] == nil {
		m.Inner[numaID][podUID] = make(map[string]*MetricRing)
	}
	if m.Inner[numaID][podUID][metricName] == nil {
		m.Inner[numaID][podUID][metricName] = CreateMetricRing(m.RingSize)
	}
	snapshot := &MetricSnapshot{
		Info: MetricInfo{
			Name:       metricName,
			Value:      podMetric,
			UpperBound: upperBound,
			LowerBound: lowerBound,
		},
		Time: collectTime,
	}
	m.Inner[numaID][podUID][metricName].Push(snapshot)
}

func (m *NumaMetricHistory) PushNuma(numaID int, metricName string, podMetric, upperBound, lowerBound float64) {
	m.Push(numaID, FakePodUID, metricName, podMetric, upperBound, lowerBound)
}
