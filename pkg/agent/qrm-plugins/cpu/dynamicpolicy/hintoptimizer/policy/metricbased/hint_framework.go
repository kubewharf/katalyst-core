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

package metricbased

import (
	"math"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// NumaScoreList declares a list of nodes and their scores.
type NumaScoreList []NumaScore

// NumaScore is a struct with numa name and score.
type NumaScore struct {
	Name  int
	Index int
	Score int64
}

type Filter func(request float64, nodeID int, machineState state.NUMANodeMap) bool

type Scorer struct {
	ScoreFunc ScoreFunc
	// todo add weight
	// Weight    int
}

type ScoreFunc func(request float64, nodeID int, machineState state.NUMANodeMap) int

func (o *metricBasedHintOptimizer) requestSufficientFilter() Filter {
	return func(request float64, numa int, machineState state.NUMANodeMap) bool {
		available := machineState[numa].GetAvailableCPUQuantity(o.reservedCPUs)
		res := true
		if !cpuutil.CPUIsSufficient(request, available) {
			general.Warningf("numa_binding shared_cores container skip NUMA: %d available: %.3f request: %.3f",
				numa, available, request)
			res = false
		}
		general.InfoS("request filter", "res", res, "numa", numa, "request", request, "available", available)
		return res
	}
}

func (o *metricBasedHintOptimizer) usageFilter() Filter {
	return func(request float64, numa int, machineState state.NUMANodeMap) bool {
		usage, err := o.getNumaUsage(numa, machineState)
		if err != nil {
			general.Errorf("cannot get usage for numa %v %v", numa, err)
			return false
		}
		usageAfterSched := usage + request

		allocatable := float64(machineState[numa].GetFilteredDefaultCPUSet(nil, nil).Difference(o.reservedCPUs).Size())

		metricThreshold := o.conf.DynamicAgentConfiguration.GetDynamicConfiguration().MetricThresholdConfiguration
		threshold, err := o.getNUMACpuUsageThreshold(metricThreshold, o.usageThresholdExpandFactor)
		if err != nil {
			general.Errorf("cannot get threshold %v", err)
			return false
		}

		thresholdValue := allocatable * threshold
		res := usageAfterSched <= thresholdValue
		general.InfoS("usage filter", "res", res, "numa", numa, "request", request,
			"usage", usage, "usageAfterSched", usageAfterSched, "allocatable", allocatable,
			"thresholdValue", thresholdValue)
		return res
	}
}

func (o *metricBasedHintOptimizer) hybridRequestScorer() Scorer {
	return Scorer{
		ScoreFunc: func(request float64, numa int, machineState state.NUMANodeMap) int {
			available := machineState[numa].GetAvailableCPUQuantity(o.reservedCPUs)
			allocatable := float64(machineState[numa].GetFilteredDefaultCPUSet(nil, nil).Difference(o.reservedCPUs).Size())
			allocated := allocatable - available + request

			thresholdValue := allocatable * o.requestScoreThreshold
			var score float64

			if allocated > thresholdValue {
				// the more allocated, the lower the score, the lowest is 0, the highest is 50
				excessRatio := (allocated - thresholdValue) / (allocatable - thresholdValue)
				score = 50 - 50*math.Min(excessRatio, 1.0)
			} else {
				// the more allocated, the higher the score, the lowest is 50, the highest is 100
				utilRatio := allocated / thresholdValue
				score = 50 + 50*utilRatio
			}
			general.InfoS("hybrid request scorer", "score", score, "numa", numa, "request", request, "allocated",
				allocated, "allocatable", allocatable, "available", available, "thresholdValue", thresholdValue,
				"requestScoreThreshold", o.requestScoreThreshold)
			return int(score)
		},
	}
}

func (o *metricBasedHintOptimizer) hybridUsageScorer() Scorer {
	return Scorer{
		ScoreFunc: func(request float64, numa int, machineState state.NUMANodeMap) int {
			usage, err := o.getNumaUsage(numa, machineState)
			if err != nil {
				general.Errorf("cannot get usage for numa %v %v", numa, err)
				return 0
			}

			usageAfterSched := usage + request

			allocatable := float64(machineState[numa].GetFilteredDefaultCPUSet(nil, nil).Difference(o.reservedCPUs).Size())

			metricThreshold := o.conf.DynamicAgentConfiguration.GetDynamicConfiguration().MetricThresholdConfiguration
			threshold, err := o.getNUMACpuUsageThreshold(metricThreshold, 1)
			if err != nil {
				general.Errorf("cannot get threshold %v", err)
				return 0
			}

			thresholdValue := allocatable * threshold

			var score float64

			if usageAfterSched > thresholdValue {
				// the more usage, the lower the score, the lowest is 0, the highest is 50
				excessRatio := (usageAfterSched - thresholdValue) / (1 - thresholdValue)
				score = 50 - 50*math.Min(excessRatio, 1.0)
			} else {
				// the more usage, the higher the score, the lowest is 50, the highest is 100
				utilRatio := usageAfterSched / thresholdValue
				score = 50 + 50*utilRatio
			}
			general.InfoS("hybrid usage scorer", "score", score, "numa", numa, "request",
				request, "usage", usage, "usageAfterSched", usageAfterSched, "allocatable", allocatable,
				"thresholdValue", thresholdValue, "threshold", threshold)
			return int(score)
		},
	}
}
