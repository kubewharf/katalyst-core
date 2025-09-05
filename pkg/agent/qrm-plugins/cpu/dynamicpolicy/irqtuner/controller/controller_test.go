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

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCalculateCpuUtils(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		oldStats       map[int64]*machine.CPUStat
		newStats       map[int64]*machine.CPUStat
		cpus           []int64
		expectedUtils  int
		expectedAvgIrq int
	}{{
		name: "normal case with non-zero time diff",
		oldStats: map[int64]*machine.CPUStat{
			0: {Irq: 100, Softirq: 50, User: 200, Nice: 50, System: 150, Idle: 500},
		},
		newStats: map[int64]*machine.CPUStat{
			0: {Irq: 200, Softirq: 100, User: 300, Nice: 100, System: 200, Idle: 600},
		},
		cpus:           []int64{0},
		expectedUtils:  1,
		expectedAvgIrq: 33,
	}, {
		name: "zero total time diff",
		oldStats: map[int64]*machine.CPUStat{
			0: {Irq: 100, Softirq: 50, Idle: 500},
		},
		newStats: map[int64]*machine.CPUStat{
			0: {Irq: 100, Softirq: 50, Idle: 500},
		},
		cpus:           []int64{0},
		expectedUtils:  1,
		expectedAvgIrq: 0,
	}}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			utils, avg := calculateCpuUtils(tc.oldStats, tc.newStats, tc.cpus)
			assert.Len(t, utils, tc.expectedUtils)
			assert.Equal(t, tc.expectedAvgIrq, avg.IrqUtil)
		})
	}
}

func TestCalculateQueuePPS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		oldStats    *NicStats
		newStats    *NicStats
		timeDiff    float64
		expectedLen int
	}{{
		name: "valid pps calculation",
		oldStats: &NicStats{
			RxQueuePackets: map[int]uint64{0: 1000, 1: 2000},
		},
		newStats: &NicStats{
			RxQueuePackets: map[int]uint64{0: 2000, 1: 4000},
		},
		timeDiff:    10.0,
		expectedLen: 2,
	}, {
		name: "new packets less than old",
		oldStats: &NicStats{
			RxQueuePackets: map[int]uint64{0: 2000},
		},
		newStats: &NicStats{
			RxQueuePackets: map[int]uint64{0: 1000},
		},
		timeDiff:    10.0,
		expectedLen: 0,
	}, {
		name: "negative time diff",
		oldStats: &NicStats{
			RxQueuePackets: map[int]uint64{0: 1000},
		},
		newStats: &NicStats{
			RxQueuePackets: map[int]uint64{0: 2000},
		},
		timeDiff:    -5.0,
		expectedLen: 0,
	}}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := calculateQueuePPS(tc.oldStats, tc.newStats, tc.timeDiff)
			assert.Len(t, result, tc.expectedLen)
			if tc.name == "valid pps calculation" {
				assert.Equal(t, uint64(100), result[0].PPS)
				assert.Equal(t, uint64(200), result[1].PPS)
			}
		})
	}
}

func TestSortQueuePPSSliceInDecOrder(t *testing.T) {
	t.Parallel()
	queuePPS := []*QueuePPS{
		{QueueID: 0, PPS: 100},
		{QueueID: 1, PPS: 300},
		{QueueID: 2, PPS: 200},
	}
	sortQueuePPSSliceInDecOrder(queuePPS)
	assert.Equal(t, uint64(300), queuePPS[0].PPS)
	assert.Equal(t, uint64(200), queuePPS[1].PPS)
	assert.Equal(t, uint64(100), queuePPS[2].PPS)
}
