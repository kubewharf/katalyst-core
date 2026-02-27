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
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type WatermarkCalculator struct {
	SwapEnabled          bool
	CGroupPath           string
	WatermarkScaleFactor uint64
	SingleReclaimFactor  float64
	SingleReclaimSize    uint64
}

func NewMemoryWatermarkCalculator(cgroupPath string, watermarkScaleFactor uint64, singleReclaimFactor float64, singleReclaimSize uint64) *WatermarkCalculator {
	return &WatermarkCalculator{
		SwapEnabled:          isSwapEnabled(),
		CGroupPath:           cgroupPath,
		WatermarkScaleFactor: watermarkScaleFactor,
		SingleReclaimFactor:  singleReclaimFactor,
		SingleReclaimSize:    singleReclaimSize,
	}
}

func (wmc *WatermarkCalculator) GetWatermark(capacity uint64) (uint64, uint64) {
	return wmc.GetLowWatermark(capacity), wmc.GetHighWatermark(capacity)
}

func (wmc *WatermarkCalculator) GetLowWatermark(capacity uint64) uint64 {
	return uint64(math.Ceil(float64(capacity * wmc.WatermarkScaleFactor / 10000)))
}

func (wmc *WatermarkCalculator) GetHighWatermark(capacity uint64) uint64 {
	return uint64(math.Ceil(float64(capacity * 2 * wmc.WatermarkScaleFactor / 10000)))
}

func (wmc *WatermarkCalculator) GetReclaimTarget(memLimit, memUsage uint64, reclaimableMax uint64) uint64 {
	highWatermark := wmc.GetHighWatermark(memLimit)
	reclaimTarget := highWatermark - (memLimit - memUsage)

	// calculate the reclaimTarget for the current container
	return general.MinUInt64(reclaimableMax, reclaimTarget)
}

func (wmc *WatermarkCalculator) GetReclaimSingleStepMax(memStats common.MemoryStats) uint64 {
	return general.MinUInt64(wmc.SingleReclaimSize, uint64(math.Ceil(wmc.SingleReclaimFactor*float64(memStats.FileCache))))
}

func (wmc *WatermarkCalculator) GetReclaimMax(memStats common.MemoryStats) uint64 {
	reclaimable := memStats.InactiveFile + memStats.ActiveFile

	if wmc.SwapEnabled {
		reclaimable += memStats.InactiveAnno + memStats.ActiveAnno
	}

	return reclaimable
}

// TODO 优化
func isSwapEnabled() bool {
	file, err := os.Open("/proc/swaps")
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	headerLine := true
	for scanner.Scan() {
		if headerLine {
			headerLine = false
			continue
		}

		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}

		sizeKiB, err := strconv.ParseUint(fields[2], 10, 64)
		if err == nil && sizeKiB > 0 {
			return true
		}
	}

	return false
}
