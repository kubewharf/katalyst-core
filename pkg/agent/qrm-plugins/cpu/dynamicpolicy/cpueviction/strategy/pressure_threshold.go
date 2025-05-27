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

package strategy

import (
	"context"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/strategygroup"
)

var ThresholdMin = 0.4

func (p *NumaCPUPressureEviction) pullThresholds(_ context.Context) {
	dynamicConf := p.conf.DynamicAgentConfiguration.GetDynamicConfiguration()
	enabled, err := strategygroup.IsStrategyEnabledForNode(consts.StrategyNameNumaCpuPressureEviction,
		dynamicConf.NumaCPUPressureEvictionConfiguration.EnableEviction, p.conf)
	if err != nil {
		general.Errorf("failed to get eviction strategy: %v", err)
		return
	}
	if !enabled {
		general.Warningf("eviction strategy is disabled, skip pullThresholds")
		return
	}

	numaPressureConfig := getNumaPressureConfig(dynamicConf)
	cpuCodeName := helper.GetCpuCodeName(p.metaServer.MetricsFetcher)
	isVM, _ := helper.GetIsVm(p.metaServer.MetricsFetcher)

	thresholds := getOverLoadThreshold(dynamicConf.MetricThreshold, cpuCodeName, isVM)
	thresholds = convertThreshold(thresholds)
	expandedThresholds := expandThresholds(thresholds, numaPressureConfig.ExpandFactor)
	err = validateThresholds(thresholds)
	if err != nil {
		general.Warningf("%v", err.Error())
		return
	}

	// update
	p.Lock()
	defer p.Unlock()
	general.Infof("%v update enabled", EvictionNameNumaCpuPressure)
	p.enabled = enabled
	general.Infof("update thresholds to %v", expandedThresholds)
	p.thresholds = expandedThresholds
	general.Infof("update numaPressureConfig to %v", numaPressureConfig)
	p.numaPressureConfig = numaPressureConfig

	if p.metricsHistory.RingSize != numaPressureConfig.MetricRingSize {
		general.Infof("update metricsHistory ring size to %v", numaPressureConfig.MetricRingSize)
		p.metricsHistory = NewMetricHistory(numaPressureConfig.MetricRingSize)
	}
}

func expandThresholds(thresholds map[string]float64, expandFactor float64) map[string]float64 {
	for key, val := range thresholds {
		thresholds[key] = val * expandFactor
	}
	return thresholds
}

func validateThresholds(thresholds map[string]float64) error {
	for key, val := range thresholds {
		if val < ThresholdMin {
			return fmt.Errorf("%v threshold %v is lower than threshold %v", key, val, ThresholdMin)
		}
	}
	return nil
}

func getOverLoadThreshold(globalThresholds *metricthreshold.MetricThreshold, cpuCode string, isVM bool) map[string]float64 {
	modelThresholds, exists := globalThresholds.Threshold[cpuCode]
	if !exists {
		general.Warningf("no suitable threshold for cpuCode %v using default", isVM)
		modelThresholds = globalThresholds.Threshold[metricthreshold.DefaultCPUCodeName]
	}
	threshold, exists := modelThresholds[isVM]
	if !exists {
		general.Warningf("no suitable threshold for cpuCode %v isVm %v, using default", cpuCode, isVM)
		return modelThresholds[false]
	}
	return threshold
}

func convertThreshold(origin map[string]float64) map[string]float64 {
	res := map[string]float64{}
	for k, v := range origin {
		newKey, ok := metricthreshold.ThresholdNameToResourceName[k]
		if !ok {
			general.Warningf("no suitable threshold for %v", k)
			continue
		}
		res[newKey] = v
	}
	return res
}
