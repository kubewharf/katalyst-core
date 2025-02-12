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
	"strconv"
	"strings"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type LatencyRegression struct {
	PredictValue float64 `json:"predict_value"`
	// Ignore is used to ignore the result of this container
	Ignore bool `json:"ignore"`
}

type BorweinStrategy struct {
	StrategySlots       []StrategySlot      `json:"strategy_slots"`
	StrategySpecialTime StrategySpecialTime `json:"strategy_special_time"`
}

type StrategySlot struct {
	Slot   int     `json:"slot"`
	Net    float64 `json:"net"`
	Offset float64 `json:"offset"`
}

type StrategySpecialTime struct {
	// minute range e.g. 21:30-22:30
	TimeRange []string `json:"time_range"`
	Offset    float64  `json:"offset"`
}

func GetLatencyRegressionPredictResult(metaReader metacache.MetaReader) (map[string]map[string]*LatencyRegression, int64, error) {
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

			specificResult := &LatencyRegression{}
			err := json.Unmarshal([]byte(result.GenericOutput), specificResult)
			if err != nil {
				general.Errorf("invalid generic output: %s for %s", result.GenericOutput, inferenceResultKey)
				return
			}
			if specificResult.Ignore {
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

func MatchSlotOffset(nodeAvgNet float64, slots []StrategySlot, borweinParameter *borweintypes.BorweinParameter) float64 {
	for _, slot := range slots {
		if nodeAvgNet < slot.Net {
			return slot.Offset
		}
	}
	return borweinParameter.OffsetMax
}

func InSpecialTime(specialTime StrategySpecialTime) (float64, bool) {
	if len(specialTime.TimeRange) != 2 {
		return 0, false
	}
	now := time.Now()
	currentHour := now.Hour()
	currentMinute := now.Minute()
	currentTotalMinutes := currentHour*60 + currentMinute

	startParts := strings.Split(specialTime.TimeRange[0], ":")
	endParts := strings.Split(specialTime.TimeRange[1], ":")

	if len(startParts) != 2 || len(endParts) != 2 {
		return 0, false
	}

	startHour, err := strconv.Atoi(startParts[0])
	if err != nil {
		return 0, false
	}
	startMinute, err := strconv.Atoi(startParts[1])
	if err != nil {
		return 0, false
	}
	startTotalMinutes := startHour*60 + startMinute

	endHour, err := strconv.Atoi(endParts[0])
	if err != nil {
		return 0, false
	}
	endMinute, err := strconv.Atoi(endParts[1])
	if err != nil {
		return 0, false
	}
	endTotalMinutes := endHour*60 + endMinute

	if currentTotalMinutes >= startTotalMinutes && currentTotalMinutes <= endTotalMinutes {
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
