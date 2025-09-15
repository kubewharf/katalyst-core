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

package latencyregression

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type LatencyRegression struct {
	PredictValue float64 `json:"predict_value"`
	// Ignore is used to ignore the result of this container
	Ignore bool `json:"ignore"`
}

type BorweinStrategy struct {
	// StrategySlots indicates strategy slots based on given net quantile
	StrategySlots ClusterStrategy `json:"strategy_slots"`
	// StrategySpecialTime indicates strategy slots based on given special times
	StrategySpecialTime ClusterSpecialTimeStrategy `json:"strategy_special_time"`
}

// ClusterStrategy describe indicator level strategy
type ClusterStrategy map[string]StrategySlots

type StrategySlots []StrategySlot

type StrategySlot struct {
	// Slot indicates the quantile corresponding to the strategy slot
	Slot int `json:"slot"`
	// NET indicates the NET quantile value corresponding to the strategy slot
	// The full name of NET is normalized equivalent throughput, it defines the normalized equivalent throughput of a certain node at time t
	NET float64 `json:"net"`
	// Offset indicates how much offset the Indicator should adjust when current node NET value falls within the strategy slot
	Offset float64 `json:"offset"`
}

type GlobalSpecialTimeStrategy map[string]map[string]StrategySpecialTimeSlots

type ClusterSpecialTimeStrategy map[string]StrategySpecialTimeSlots

type StrategySpecialTimeSlots []StrategySpecialTimeSlot

type StrategySpecialTimeSlot struct {
	// TimeRange indicates the time range of the current strategy slot.
	TimeRange []string `json:"time_range"`
	// Offset indicates how much offset the Indicator should adjust when current node NET value falls within the strategy slot
	Offset float64 `json:"offset"`
}

func GetLatencyRegressionPredictResult(metaReader metacache.MetaReader, dryRun bool, podSet types.PodSet) (map[string]map[string]*LatencyRegression, int64, error) {
	if metaReader == nil {
		return nil, 0, fmt.Errorf("nil metaReader")
	}

	inferenceResultKey := borweinutils.GetInferenceResultKey(borweinconsts.ModelNameBorweinLatencyRegression)
	results, err := metaReader.GetInferenceResult(inferenceResultKey)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get inference results for %s, error: %v", inferenceResultKey, err)
	}

	ret := make(map[string]map[string]*LatencyRegression)
	var resultTimestamp int64

	switch typedResults := results.(type) {
	case *borweintypes.BorweinInferenceResults:
		resultTimestamp = typedResults.Timestamp

		typedResults.RangeInferenceResults(func(podUID, containerName string, result *borweininfsvc.InferenceResult) {
			if result == nil {
				return
			}
			if podSet != nil {
				if _, ok := podSet[podUID]; !ok {
					return
				}
			}

			specificResult := &LatencyRegression{}
			err := json.Unmarshal([]byte(result.GenericOutput), specificResult)
			if err != nil {
				general.Errorf("invalid generic output: %s for %s", result.GenericOutput, inferenceResultKey)
				return
			}
			if !dryRun && specificResult.Ignore {
				return
			}

			if ret[podUID] == nil {
				ret[podUID] = make(map[string]*LatencyRegression)
			}

			ret[podUID][containerName] = specificResult
		})
	default:
		return nil, 0, fmt.Errorf("invalid model result type: %T", typedResults)
	}
	return ret, resultTimestamp, nil
}

func MatchSlotOffset(nodeAvgNet float64, clusterStrategy ClusterStrategy, indicator string, borweinParameter *borweintypes.BorweinParameter) float64 {
	slots, ok := clusterStrategy[indicator]
	if !ok {
		general.Warningf("find no slot for indicator %v", indicator)
		return 0
	}

	sort.Slice(slots, func(i, j int) bool {
		return slots[i].NET < slots[j].NET
	})

	for _, slot := range slots {
		if nodeAvgNet < slot.NET {
			general.InfoS("predicted match offset", "nodeAvgNet", nodeAvgNet,
				"slot", slot.Slot, "net", slot.NET, "offset", slot.Offset)
			return slot.Offset
		}
	}

	if len(slots) > 0 {
		lastSlot := slots[len(slots)-1]
		general.Infof("match no slot, use last slot's offset: %v", lastSlot.Offset)
		return lastSlot.Offset
	}

	general.Warningf("slots is empty for indicator %v", indicator)
	return 0
}

func MatchSpecialTimes(clusterSpecialTimeStrategy ClusterSpecialTimeStrategy, indicator string) (float64, bool) {
	specialTimeSlots, ok := clusterSpecialTimeStrategy[indicator]
	if !ok {
		return 0, false
	}

	for _, specialTime := range specialTimeSlots {
		offset, match := InSpecialTime(specialTime)
		if !match {
			continue
		}
		general.InfoS("match special time", "timeRange", specialTime.TimeRange, "offset", specialTime.Offset)
		return offset, true
	}
	return 0, false
}

func InSpecialTime(specialTime StrategySpecialTimeSlot) (float64, bool) {
	if len(specialTime.TimeRange) != 2 {
		return 0, false
	}

	layout := "15:04"
	now := time.Now()
	loc := now.Location()

	start, err := time.ParseInLocation(layout, specialTime.TimeRange[0], loc)
	if err != nil {
		return 0, false
	}
	end, err := time.ParseInLocation(layout, specialTime.TimeRange[1], loc)
	if err != nil {
		return 0, false
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	startTime := today.Add(time.Duration(start.Hour())*time.Hour + time.Duration(start.Minute())*time.Minute)
	endTime := today.Add(time.Duration(end.Hour())*time.Hour + time.Duration(end.Minute())*time.Minute)

	if now.After(startTime) && now.Before(endTime) {
		return specialTime.Offset, true
	}

	return 0, false
}

func ParseStrategy(strategyParam string) (BorweinStrategy, error) {
	var ret BorweinStrategy
	err := json.Unmarshal([]byte(strategyParam), &ret)
	if err != nil {
		return BorweinStrategy{}, err
	}
	return ret, nil
}
