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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/strategygroup"
	"github.com/kubewharf/katalyst-core/pkg/util/threshold"
)

func (p *NumaCPUPressureEviction) pullThresholds(_ context.Context) {
	dynamicConf := p.conf.DynamicAgentConfiguration.GetDynamicConfiguration()
	enabled, err := strategygroup.IsStrategyEnabledForNode(consts.StrategyNameNumaCpuPressureEviction,
		dynamicConf.NumaCPUPressureEvictionConfiguration.EnableEviction, p.conf)
	if err != nil {
		general.Errorf("failed to get eviction strategy: %v", err)
		return
	}

	p.Lock()
	p.enabled = enabled
	general.Infof("%v update enabled %v", EvictionNameNumaCpuPressure, enabled)
	p.Unlock()

	if !enabled {
		general.Warningf("eviction strategy is disabled, skip pullThresholds")
		return
	}

	numaPressureConfig := getNumaPressureConfig(dynamicConf)

	thresholds := threshold.GetMetricThresholds(targetMetrics, p.metaServer, dynamicConf.MetricThresholdConfiguration.Threshold)
	if thresholds == nil {
		general.Errorf("got no valid threshold")
		return
	}

	thresholds = convertThreshold(thresholds)
	expandedThresholds := threshold.ExpandThresholds(thresholds, numaPressureConfig.ExpandFactor)

	// update
	p.Lock()
	defer p.Unlock()
	general.Infof("update thresholds to %v", expandedThresholds)
	p.thresholds = expandedThresholds
	general.Infof("update numaPressureConfig to %v", numaPressureConfig)
	p.numaPressureConfig = numaPressureConfig

	if p.metricsHistory.RingSize != numaPressureConfig.MetricRingSize {
		general.Infof("update metricsHistory ring size to %v", numaPressureConfig.MetricRingSize)
		p.metricsHistory = NewMetricHistory(numaPressureConfig.MetricRingSize)
	}
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
