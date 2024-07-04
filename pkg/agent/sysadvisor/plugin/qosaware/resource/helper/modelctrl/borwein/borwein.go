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

package borwein

import (
	"fmt"
	"math"

	//nolint
	"github.com/golang/protobuf/proto"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweininfsvc "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/inferencesvc"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	borweinutils "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricBorweinIndicatorOffset = "borwein_indicator_offset"
)

type IndicatorOffsetUpdater func(podSet types.PodSet, currentIndicatorOffset float64,
	borweinParameter *borweintypes.BorweinParameter, metaReader metacache.MetaReader) (float64, error)

type BorweinController struct {
	regionName string
	regionType configapi.QoSRegionType
	conf       *config.Configuration

	borweinParameters       map[string]*borweintypes.BorweinParameter
	indicatorOffsets        map[string]float64
	metaReader              metacache.MetaReader
	emitter                 metrics.MetricEmitter
	indicatorOffsetUpdaters map[string]IndicatorOffsetUpdater
}

func NewBorweinController(regionName string, regionType configapi.QoSRegionType, ownerPoolName string,
	conf *config.Configuration, metaReader metacache.MetaReader, emitter metrics.MetricEmitter,
) *BorweinController {
	bc := &BorweinController{
		regionName:              regionName,
		regionType:              regionType,
		conf:                    conf,
		borweinParameters:       make(map[string]*borweintypes.BorweinParameter),
		indicatorOffsets:        make(map[string]float64),
		metaReader:              metaReader,
		indicatorOffsetUpdaters: make(map[string]IndicatorOffsetUpdater),
		emitter:                 emitter,
	}

	bc.indicatorOffsets[string(v1alpha1.ServiceSystemIndicatorNameCPUSchedWait)] = 0
	bc.indicatorOffsetUpdaters[string(v1alpha1.ServiceSystemIndicatorNameCPUSchedWait)] = updateCPUSchedWaitIndicatorOffset
	bc.borweinParameters = conf.BorweinConfiguration.BorweinParameters

	return bc
}

func updateCPUSchedWaitIndicatorOffset(podSet types.PodSet, currentIndicatorOffset float64,
	borweinParameter *borweintypes.BorweinParameter, metaReader metacache.MetaReader,
) (float64, error) {
	filteredObj, err := metaReader.GetFilteredInferenceResult(func(input interface{}) (interface{}, error) {
		cachedResult, ok := input.(*borweintypes.BorweinInferenceResults)
		if !ok || cachedResult == nil {
			return nil, fmt.Errorf("invalid input")
		}

		filteredResults := borweintypes.NewBorweinInferenceResults()

		for podUID := range cachedResult.Results {
			if podSet[podUID].Len() == 0 {
				continue
			}

			for _, containerName := range podSet[podUID].UnsortedList() {
				results := cachedResult.Results[podUID][containerName]
				if len(results) == 0 {
					continue
				}

				inferenceResults := make([]*borweininfsvc.InferenceResult, len(results))
				for idx, result := range results {
					if result == nil {
						continue
					}

					inferenceResults[idx] = proto.Clone(result).(*borweininfsvc.InferenceResult)
				}

				filteredResults.SetInferenceResults(podUID, containerName, inferenceResults...)
			}

			if len(filteredResults.Results[podUID]) == 0 {
				return nil, fmt.Errorf("there is no result for pod: %s", podUID)
			}
		}

		return filteredResults, nil
	}, borweinutils.GetInferenceResultKey(borweinconsts.ModelNameBorwein))
	if err != nil {
		return 0, fmt.Errorf("GetFilteredInferenceResult failed with error: %v", err)
	}

	filteredResult, ok := filteredObj.(*borweintypes.BorweinInferenceResults)
	if !ok {
		return 0, fmt.Errorf("GetFilteredInferenceResult return invalid result")
	}

	var classificationNormalCnt, classificationAbnormalCnt,
		regressionNormalCnt, regressionAbnormalCnt int

	filteredResult.RangeInferenceResults(func(podUID, containerName string, result *borweininfsvc.InferenceResult) {
		if result == nil {
			return
		}

		switch result.InferenceType {
		case borweininfsvc.InferenceType_ClassificationOverload:
			if result.Output >= result.Percentile {
				classificationAbnormalCnt += 1
			} else {
				classificationNormalCnt += 1
			}
			// todo: emit metrics

		case borweininfsvc.InferenceType_LatencyRegression:
			// regression prediction by default model isn't trusted
			if !result.IsDefault {
				if result.Output > result.Percentile {
					regressionAbnormalCnt += 1
				} else {
					regressionNormalCnt += 1
				}
				// todo: emit metrics
			}
		}
	})

	classificationCnt := classificationNormalCnt + classificationAbnormalCnt
	regressionCnt := regressionNormalCnt + regressionAbnormalCnt
	classificationAbnormalRatio := 0.0
	regressionAbnormalRatio := 0.0

	// Reset offset because of no classification prob result
	if classificationCnt <= 0 {
		general.Infof("non positive classification cnt, reset offset")
		// todo: emit metrics
		return 0, nil
	} else {
		classificationAbnormalRatio = float64(classificationAbnormalCnt) / float64(classificationCnt)
		// todo: emit metrics
	}

	if regressionCnt <= 0 {
		general.Infof("non positive regression cnt, skip regression abnormal ratio")
	} else {
		regressionAbnormalRatio = float64(regressionAbnormalCnt) / float64(regressionCnt)
		// todo: emit metrics
	}

	abnormalRatio := math.Max(classificationAbnormalRatio, regressionAbnormalRatio)
	if abnormalRatio <= borweinParameter.AbnormalRatioThreshold {
		currentIndicatorOffset += borweinParameter.RampUpStep
	} else {
		currentIndicatorOffset -= borweinParameter.RampDownStep
	}
	currentIndicatorOffset = general.Clamp(currentIndicatorOffset, borweinParameter.OffsetMin, borweinParameter.OffsetMax)
	general.Infof("classificationNormalCnt: %v, classificationAbnormalCnt: %v,"+
		" regressionNormalCnt: %v, regressionAbnormalCnt: %v, currentIndicatorOffset: %v",
		classificationNormalCnt, classificationAbnormalCnt,
		regressionNormalCnt, regressionAbnormalCnt, currentIndicatorOffset)

	return currentIndicatorOffset, nil
}

func (bc *BorweinController) updateIndicatorOffsets(podSet types.PodSet) {
	if bc.metaReader == nil {
		general.Errorf("BorweinController got nil metaReader")
		return
	}

	for indicatorName, currentIndicatorOffset := range bc.indicatorOffsets {

		if bc.indicatorOffsetUpdaters[indicatorName] == nil {
			general.Errorf("there is no updater for indicator: %s", indicatorName)
			continue
		} else if bc.borweinParameters[indicatorName] == nil {
			general.Errorf("there is no borwein params for indicator: %s", indicatorName)
			continue
		}

		updatedIndicatorOffset, err := bc.indicatorOffsetUpdaters[indicatorName](podSet,
			currentIndicatorOffset,
			bc.borweinParameters[indicatorName],
			bc.metaReader)
		if err != nil {
			general.Errorf("update indicator: %s offset failed with error: %v", indicatorName, err)
			continue
		}

		bc.indicatorOffsets[indicatorName] = updatedIndicatorOffset
		general.Infof("update indicator: %s offset from: %.2f to %2.f",
			indicatorName, currentIndicatorOffset, updatedIndicatorOffset)
		bc.emitter.StoreFloat64(metricBorweinIndicatorOffset, bc.indicatorOffsets[indicatorName],
			metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
				"indicator_name": indicatorName,
			})...)
	}
}

func (bc *BorweinController) updateBorweinParameters() types.Indicator {
	// todo: currently updateBorweinParameters based on config static value
	// maybe periodically updated by values from KCC
	return nil
}

func (bc *BorweinController) getUpdatedIndicators(indicators types.Indicator) types.Indicator {
	updatedIndicators := make(types.Indicator, len(indicators))

	// update target indicators by bc.indicatorOffsets
	for indicatorName, indicatorValue := range indicators {
		if _, found := bc.indicatorOffsets[indicatorName]; !found {
			general.Infof("there is no offset for indicator: %s, use its original value(current: %.2f, target: %.2f) without updating",
				indicatorName, indicatorValue.Current, indicatorValue.Target)
			updatedIndicators[indicatorName] = indicatorValue
			continue
		}

		general.Infof("update indicator: %s taget: %.2f by offset: %.2f",
			indicatorName, indicators[indicatorName].Target,
			bc.indicatorOffsets[indicatorName])

		indicatorValue.Target += bc.indicatorOffsets[indicatorName]

		updatedIndicators[indicatorName] = indicatorValue
	}
	return updatedIndicators
}

func (bc *BorweinController) GetUpdatedIndicators(indicators types.Indicator, podSet types.PodSet) types.Indicator {
	bc.updateIndicatorOffsets(podSet)
	return bc.getUpdatedIndicators(indicators)
}

func (bc *BorweinController) ResetIndicatorOffsets() {
	for indicatorName, currentIndicatorOffset := range bc.indicatorOffsets {
		general.Infof("reset indicator: %s offset from %.2f to 0",
			indicatorName, currentIndicatorOffset)

		bc.indicatorOffsets[indicatorName] = 0
	}
}
