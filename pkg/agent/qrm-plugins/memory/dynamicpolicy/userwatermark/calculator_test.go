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

package userwatermark

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

const (
	TestCGroupPath = "/sys/fs/cgroup/test"

	TestWatermarkScaleFactor = 100
	TestSingleReclaimFactor  = 0.25
	TestSingleReclaimSize    = 4096
)

var calculatorMutex sync.Mutex

func NewDefaultMemoryWatermarkCalculator() *WatermarkCalculator {
	return NewMemoryWatermarkCalculator(TestCGroupPath, TestWatermarkScaleFactor, TestSingleReclaimFactor, TestSingleReclaimSize)
}

func TestNewMemoryWatermarkCalculator(t *testing.T) {
	t.Parallel()

	calc := NewMemoryWatermarkCalculator(TestCGroupPath, TestWatermarkScaleFactor, TestSingleReclaimFactor, TestSingleReclaimSize)
	assert.Equal(t, TestCGroupPath, calc.CGroupPath)
	assert.Equal(t, uint64(TestWatermarkScaleFactor), calc.WatermarkScaleFactor)
	assert.Equal(t, TestSingleReclaimFactor, calc.SingleReclaimFactor)
	assert.Equal(t, uint64(TestSingleReclaimSize), calc.SingleReclaimSize)
}

func TestWatermarkCalculator_GetLowAndHighWatermark(t *testing.T) {
	t.Parallel()

	wmc := NewDefaultMemoryWatermarkCalculator()
	capacity := uint64(1024 * 1024 * 1024) // 1GiB
	low := wmc.GetLowWatermark(capacity)
	high := wmc.GetHighWatermark(capacity)

	expectedLow := uint64(float64(capacity * wmc.WatermarkScaleFactor / 10000))
	expectedHigh := uint64(float64(capacity * 2 * wmc.WatermarkScaleFactor / 10000))

	assert.Equal(t, expectedLow, low)
	assert.Equal(t, expectedHigh, high)
	assert.True(t, high >= low)
}

func TestWatermarkCalculator_GetReclaimTarget(t *testing.T) {
	t.Parallel()

	wmc := NewDefaultMemoryWatermarkCalculator()
	memLimit := uint64(1000)

	// case 1: reclaimTarget > reclaimableMax
	memUsage := uint64(950)
	reclaimableMax := uint64(300)

	high := wmc.GetHighWatermark(memLimit)
	free := memLimit - memUsage
	expected := high - free

	if expected > reclaimableMax {
		expected = reclaimableMax
	}
	got := wmc.GetReclaimTarget(memLimit, memUsage, reclaimableMax)
	assert.Equal(t, expected, got)

	// case 2: reclaimTarget < reclaimableMax
	memUsage = 900
	reclaimableMax = 999

	high = wmc.GetHighWatermark(memLimit)
	free = memLimit - memUsage
	expected = high - free

	if expected > reclaimableMax {
		expected = reclaimableMax
	}

	got = wmc.GetReclaimTarget(memLimit, memUsage, reclaimableMax)
	assert.Equal(t, expected, got)

	// case 3: reclaimTarget == reclaimableMax
	memUsage = 800
	reclaimableMax = 900

	high = wmc.GetHighWatermark(memLimit)
	free = memLimit - memUsage
	expected = high - free

	if expected > reclaimableMax {
		expected = reclaimableMax
	}

	got = wmc.GetReclaimTarget(memLimit, memUsage, reclaimableMax)
	assert.Equal(t, expected, got)
}

func TestWatermarkCalculator_GetWatermark(t *testing.T) {
	t.Parallel()

	wmc := &WatermarkCalculator{WatermarkScaleFactor: 100}

	capacity := uint64(1024)

	low, high := wmc.GetWatermark(capacity)
	assert.Equal(t, wmc.GetLowWatermark(capacity), low)
	assert.Equal(t, wmc.GetHighWatermark(capacity), high)
}

func TestWatermarkCalculator_GetReclaimMax(t *testing.T) {
	t.Parallel()

	memStats := common.MemoryStats{
		InactiveFile: 100,
		ActiveFile:   50,
		InactiveAnno: 200,
		ActiveAnno:   70,
	}

	wmc := &WatermarkCalculator{SwapEnabled: false}
	assert.Equal(t, uint64(150), wmc.GetReclaimMax(memStats))

	wmc.SwapEnabled = true
	assert.Equal(t, uint64(420), wmc.GetReclaimMax(memStats))
}
