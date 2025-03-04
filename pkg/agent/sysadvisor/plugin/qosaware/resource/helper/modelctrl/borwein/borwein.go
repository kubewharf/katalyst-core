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
	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweintypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/types"
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

	bc.borweinParameters = conf.BorweinConfiguration.BorweinParameters

	return bc
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
