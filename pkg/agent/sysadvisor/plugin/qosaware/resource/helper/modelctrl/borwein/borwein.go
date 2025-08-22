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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher/borwein/latencyregression"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/strategygroup"
)

const (
	metricBorweinIndicatorOffset       = "borwein_indicator_offset"
	metricBorweinInferenceResultRegion = "borwein_inference_result_region"
)

type IndicatorOffsetUpdater func(podSet types.PodSet, currentIndicatorOffset float64,
	borweinParameter *borweintypes.BorweinParameter, metaReader metacache.MetaReader,
	conf *config.Configuration, emitter metrics.MetricEmitter, regionName string) (float64, error)

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

	for _, indicator := range conf.BorweinConfiguration.TargetIndicators {
		general.Infof("Enable indicator %v offset update", indicator)
		bc.indicatorOffsets[indicator] = 0
	}
	bc.indicatorOffsetUpdaters[string(v1alpha1.ServiceSystemIndicatorNameCPUUsageRatio)] = updateCPUUsageIndicatorOffset
	bc.borweinParameters = conf.BorweinConfiguration.BorweinParameters

	return bc
}

func updateCPUUsageIndicatorOffset(podSet types.PodSet, _ float64, borweinParameter *borweintypes.BorweinParameter,
	metaReader metacache.MetaReader, conf *config.Configuration, emitter metrics.MetricEmitter, regionName string,
) (float64, error) {
	strategy, err := fetchBorweinV2Strategy(conf)
	if err != nil {
		general.Warningf("%v strategy is not enabled %v", consts.StrategyNameBorweinV2, err)
		return 0, err
	}

	ret, resultTimestamp, err := latencyregression.GetLatencyRegressionPredictResult(metaReader, conf.BorweinConfiguration.DryRun, podSet)
	if err != nil {
		general.Errorf("failed to get inference results of model(%s), error: %v", borweinconsts.ModelNameBorweinLatencyRegression, err)
		return 0, err
	}

	predictSum := 0.0
	containerCnt := 0.0
	// avg by node
	for _, containerData := range ret {
		for _, res := range containerData {
			predictSum += res.PredictValue
			containerCnt++
		}
	}

	if containerCnt == 0 {
		general.Warningf("got no valid containers, skip update")
		return 0, nil
	}
	predictAvg := predictSum / containerCnt

	_ = emitter.StoreFloat64(metricBorweinInferenceResultRegion,
		predictAvg,
		metrics.MetricTypeNameRaw,
		metrics.MetricTag{
			Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
			Val: fmt.Sprintf("%v", resultTimestamp),
		},
		metrics.MetricTag{
			Key: fmt.Sprintf("%s", "binding_numas"),
			Val: fmt.Sprintf("%s", getBindingNumas(metaReader, regionName)),
		},
	)

	targetOffset := latencyregression.MatchSlotOffset(predictAvg, strategy.StrategySlots,
		string(v1alpha1.ServiceSystemIndicatorNameCPUUsageRatio), borweinParameter)

	if specialOffset, match := latencyregression.MatchSpecialTimes(strategy.StrategySpecialTime,
		string(v1alpha1.ServiceSystemIndicatorNameCPUUsageRatio)); match {
		targetOffset = specialOffset
	}

	targetOffset = general.Clamp(targetOffset, borweinParameter.OffsetMin, borweinParameter.OffsetMax)
	general.InfoS("offset update by borwein",
		"indicator", string(v1alpha1.ServiceSystemIndicatorNameCPUUsageRatio),
		"predictedAvg", predictAvg,
		"targetOffset", targetOffset,
	)
	return targetOffset, nil
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
			bc.metaReader,
			bc.conf,
			bc.emitter,
			bc.regionName)
		if err != nil {
			general.Errorf("update indicator: %s offset failed with error: %v", indicatorName, err)
			continue
		}

		bc.indicatorOffsets[indicatorName] = updatedIndicatorOffset
		general.Infof("update indicator: %s offset from: %.2f to %.2f",
			indicatorName, currentIndicatorOffset, updatedIndicatorOffset)

		_ = bc.emitter.StoreFloat64(metricBorweinIndicatorOffset, updatedIndicatorOffset,
			metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
				"indicator_name": indicatorName,
				"binding_numas":  getBindingNumas(bc.metaReader, bc.regionName),
			})...)
	}
}

func (bc *BorweinController) updateBorweinParameters() types.Indicator {
	// todo: currently updateBorweinParameters based on config static value
	// maybe periodically updated by values from KCC
	return nil
}

func (bc *BorweinController) getUpdatedIndicators(indicators types.Indicator) types.Indicator {
	finalIndicators := make(types.Indicator, len(indicators))

	for indicatorName, indicatorValue := range indicators {
		if _, found := bc.indicatorOffsets[indicatorName]; !found {
			general.Infof("there is no offset for indicator: %s, use its original value(current: %.4f, target: %.4f) without updating",
				indicatorName, indicatorValue.Current, indicatorValue.Target)
		} else {
			general.Infof("update indicator: %s target: %.2f by offset: %.2f",
				indicatorName, indicatorValue.Target, bc.indicatorOffsets[indicatorName])
			indicatorValue.Target += bc.indicatorOffsets[indicatorName]
			print()
		}

		// restrict target in specific range
		bp := bc.borweinParameters[indicatorName]
		if bp != nil && bp.IndicatorMin != 0 && bp.IndicatorMax != 0 {
			indicatorValue.Target = general.Clamp(indicatorValue.Target, bp.IndicatorMin, bp.IndicatorMax)
			general.Infof("restricted indicator: %s target: %.2f ", indicatorName, indicatorValue.Target)
		}

		finalIndicators[indicatorName] = indicatorValue
	}

	return finalIndicators
}

func (bc *BorweinController) GetUpdatedIndicators(indicators types.Indicator, podSet types.PodSet) types.Indicator {
	borweinV2Enabled, err := strategygroup.IsStrategyEnabledForNode(consts.StrategyNameBorweinV2, bc.conf.EnableBorweinV2, bc.conf)
	if err != nil {
		general.Warningf("Failed to get %v strategy %v", consts.StrategyNameBorweinV2, err)
		return indicators
	}
	if !borweinV2Enabled {
		general.Warningf("%v strategy is not enabled", consts.StrategyNameBorweinV2)
		return indicators
	}

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

func fetchBorweinV2Strategy(conf *config.Configuration) (*latencyregression.BorweinStrategy, error) {
	// get strategy
	strategyName := consts.StrategyNameBorweinV2
	strategyContent, enabled, err := strategygroup.GetSpecificStrategyParam(strategyName, conf.EnableBorweinV2, conf)
	if err != nil {
		return nil, fmt.Errorf("get %v grep param error: %v", strategyName, err)
	}
	if !enabled {
		return nil, fmt.Errorf("%v strategy is not enabled", strategyName)
	}
	// unmarshall
	strategy, err := latencyregression.ParseStrategy(strategyContent)
	if err != nil {
		return nil, fmt.Errorf("parse %v strategy error: %v", strategyName, err)
	}
	// validate
	if len(strategy.StrategySlots) == 0 {
		return nil, fmt.Errorf("strategy slots is empty")
	}
	general.Infof("%v strategy: %+v", strategyName, strategy)
	return &strategy, nil
}

func getBindingNumas(metaReader metacache.MetaReader, regionName string) string {
	bindingNumas := ""
	if ri, exist := metaReader.GetRegionInfo(regionName); exist {
		bindingNumas = ri.BindingNumas.String()
	}
	return bindingNumas
}
