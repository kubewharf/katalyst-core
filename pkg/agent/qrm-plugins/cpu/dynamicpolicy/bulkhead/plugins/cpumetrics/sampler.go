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

package cpumetrics

import (
	"math"
	"time"

	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type sample struct {
	value float64
	at    time.Time
}

type sourceCache map[string]map[int]sample

type runSamples struct {
	single       sourceCache
	cycles       map[int]sample
	instructions map[int]sample
}

type aggregateValues struct {
	cpuUsageRatio *float64
	ioWaitRatio   *float64
	schedWait     *float64
	irqRatio      *float64
	cpi           *float64
	l3Misses      *float64
}

type sourceAggregation uint8

const (
	sourceAverage sourceAggregation = iota
	sourceSum
)

type sourceDescriptor struct {
	sourceName string
	aggregate  sourceAggregation
	assign     func(*aggregateValues, float64)
}

var sourceDescriptors = []sourceDescriptor{
	{
		sourceName: pkgconsts.MetricCPUUsageRatio,
		aggregate:  sourceAverage,
		assign: func(values *aggregateValues, value float64) {
			values.cpuUsageRatio = float64Pointer(value)
		},
	},
	{
		sourceName: pkgconsts.MetricCPUIOWaitRatio,
		aggregate:  sourceAverage,
		assign: func(values *aggregateValues, value float64) {
			values.ioWaitRatio = float64Pointer(value)
		},
	},
	{
		sourceName: pkgconsts.MetricCPUSchedwait,
		aggregate:  sourceAverage,
		assign: func(values *aggregateValues, value float64) {
			values.schedWait = float64Pointer(value)
		},
	},
	{
		sourceName: pkgconsts.MetricCPUIrqRatio,
		aggregate:  sourceAverage,
		assign: func(values *aggregateValues, value float64) {
			values.irqRatio = float64Pointer(value)
		},
	},
	{
		sourceName: pkgconsts.MetricCPUL3Misses,
		aggregate:  sourceSum,
		assign: func(values *aggregateValues, value float64) {
			values.l3Misses = float64Pointer(value)
		},
	},
}

func sampleRun(fetcher metrictypes.MetricsFetcher, cpus machine.CPUSet) runSamples {
	cache := runSamples{
		single: make(sourceCache, len(sourceDescriptors)),
	}
	for _, descriptor := range sourceDescriptors {
		source := descriptor.sourceName
		first := readSource(fetcher, cpus, source)
		cache.single[source] = selectLatestGeneration(first, func() map[int]sample {
			return readSource(fetcher, cpus, source)
		})
	}
	cache.cycles, cache.instructions = readCPIPairs(fetcher, cpus)
	return cache
}

func readSource(fetcher metrictypes.MetricsFetcher, cpus machine.CPUSet, source string) map[int]sample {
	result := make(map[int]sample)
	for _, cpu := range cpus.ToSliceInt() {
		if value, ok := readSample(fetcher, cpu, source); ok {
			result[cpu] = value
		}
	}
	return result
}

func selectLatestGeneration(first map[int]sample, reread func() map[int]sample) map[int]sample {
	chosen := first
	if hasMultipleGenerations(first) {
		chosen = reread()
	}
	return retainLatestGeneration(chosen)
}

func readCPIPairs(fetcher metrictypes.MetricsFetcher, cpus machine.CPUSet) (map[int]sample, map[int]sample) {
	readPairs := func() (map[int]sample, map[int]sample, bool) {
		cycles := make(map[int]sample)
		instructions := make(map[int]sample)
		mismatched := false
		for _, cpu := range cpus.ToSliceInt() {
			cycleSample, cycleOK := readSample(fetcher, cpu, pkgconsts.MetricCPUCycles)
			instructionSample, instructionOK := readSample(fetcher, cpu, pkgconsts.MetricCPUInstructions)
			if !cycleOK || !instructionOK {
				continue
			}
			if !cycleSample.at.Equal(instructionSample.at) {
				mismatched = true
				continue
			}
			cycles[cpu] = cycleSample
			instructions[cpu] = instructionSample
		}
		return cycles, instructions, mismatched
	}

	cycles, instructions, mismatched := readPairs()
	if mismatched || hasMultipleGenerations(cycles) {
		cycles, instructions, _ = readPairs()
	}

	latest, ok := latestGeneration(cycles)
	if !ok {
		return map[int]sample{}, map[int]sample{}
	}
	filteredCycles := make(map[int]sample)
	filteredInstructions := make(map[int]sample)
	for cpu, cycleSample := range cycles {
		if cycleSample.at.Equal(latest) {
			filteredCycles[cpu] = cycleSample
			filteredInstructions[cpu] = instructions[cpu]
		}
	}
	return filteredCycles, filteredInstructions
}

func aggregateSamples(cache runSamples, cpus machine.CPUSet) aggregateValues {
	values := aggregateValues{}
	cpuIDs := cpus.ToSliceInt()
	for _, descriptor := range sourceDescriptors {
		samples := cache.single[descriptor.sourceName]
		value, ok := aggregateSource(samples, cpuIDs, descriptor.aggregate)
		if !ok {
			continue
		}
		descriptor.assign(&values, value)
	}

	if cpi, ok := aggregateCPI(cache, cpuIDs); ok {
		values.cpi = float64Pointer(cpi)
	}
	return values
}

func aggregateSource(samples map[int]sample, cpuIDs []int, aggregation sourceAggregation) (float64, bool) {
	if aggregation == sourceSum {
		sum := 0.0
		count := 0
		for _, cpu := range cpuIDs {
			value, ok := samples[cpu]
			if !ok {
				continue
			}
			sum += value.value
			if !isFinite(sum) {
				return 0, false
			}
			count++
		}
		return sum, count > 0
	}

	scale := 0.0
	count := 0
	for _, cpu := range cpuIDs {
		value, ok := samples[cpu]
		if !ok || !isFinite(value.value) {
			continue
		}
		scale = math.Max(scale, math.Abs(value.value))
		count++
	}
	if count == 0 {
		return 0, false
	}
	if scale == 0 {
		return 0, true
	}

	normalizedSum := 0.0
	for _, cpu := range cpuIDs {
		if value, ok := samples[cpu]; ok && isFinite(value.value) {
			normalizedSum += value.value / scale
		}
	}
	average := normalizedSum / float64(count) * scale
	return average, isFinite(average)
}

func aggregateCPI(cache runSamples, cpuIDs []int) (float64, bool) {
	scale := 0.0
	pairCount := 0
	for _, cpu := range cpuIDs {
		cycleSample, cycleOK := cache.cycles[cpu]
		instructionSample, instructionOK := cache.instructions[cpu]
		if !cycleOK || !instructionOK || !cycleSample.at.Equal(instructionSample.at) ||
			!isFinite(cycleSample.value) || !isFinite(instructionSample.value) {
			continue
		}
		scale = math.Max(scale, math.Abs(cycleSample.value))
		scale = math.Max(scale, math.Abs(instructionSample.value))
		pairCount++
	}
	if pairCount == 0 || scale == 0 {
		return 0, false
	}

	normalizedCycleSum := 0.0
	normalizedInstructionSum := 0.0
	for _, cpu := range cpuIDs {
		cycleSample, cycleOK := cache.cycles[cpu]
		instructionSample, instructionOK := cache.instructions[cpu]
		if !cycleOK || !instructionOK || !cycleSample.at.Equal(instructionSample.at) ||
			!isFinite(cycleSample.value) || !isFinite(instructionSample.value) {
			continue
		}
		normalizedCycleSum += cycleSample.value / scale
		normalizedInstructionSum += instructionSample.value / scale
	}
	if normalizedInstructionSum <= 0 {
		return 0, false
	}
	cpi := normalizedCycleSum / normalizedInstructionSum
	return cpi, isFinite(cpi)
}

func readSample(fetcher metrictypes.MetricsFetcher, cpu int, source string) (sample, bool) {
	data, err := fetcher.GetCPUMetric(cpu, source)
	if err != nil || data.Time == nil || !isFinite(data.Value) {
		return sample{}, false
	}
	return sample{value: data.Value, at: *data.Time}, true
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func hasMultipleGenerations(samples map[int]sample) bool {
	var first time.Time
	found := false
	for _, value := range samples {
		if !found {
			first = value.at
			found = true
			continue
		}
		if !value.at.Equal(first) {
			return true
		}
	}
	return false
}

func retainLatestGeneration(samples map[int]sample) map[int]sample {
	latest, ok := latestGeneration(samples)
	if !ok {
		return map[int]sample{}
	}
	filtered := make(map[int]sample)
	for cpu, value := range samples {
		if value.at.Equal(latest) {
			filtered[cpu] = value
		}
	}
	return filtered
}

func latestGeneration(samples map[int]sample) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, value := range samples {
		if !found || value.at.After(latest) {
			latest = value.at
			found = true
		}
	}
	return latest, found
}

func float64Pointer(value float64) *float64 {
	return &value
}
