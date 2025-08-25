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

package threshold

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	ScopeMetricThreshold = "metric-threshold"
)

var Min = 0.4

// GetMetricThreshold get threshold from npd and config
func GetMetricThreshold(thresholdName string, metaServer *metaserver.MetaServer,
	globalThresholds map[string]map[bool]map[string]float64,
) *float64 {
	// 1 get threshold from npd
	npdThresholds := getMetricThresholdFromNPD(metaServer)
	if threshold := getAndValidate(thresholdName, npdThresholds); threshold != nil {
		general.Infof("get threshold from npd: %v", *threshold)
		return threshold
	}

	// 2 get threshold from config
	configThresholds := getMetricThresholdFromConf(metaServer, globalThresholds)
	if threshold := getAndValidate(thresholdName, configThresholds); threshold != nil {
		general.Infof("get threshold from config: %v", *threshold)
		return threshold
	}

	return nil
}

// GetMetricThresholds get valid thresholds from npd and config
func GetMetricThresholds(thresholdNames []string, metaServer *metaserver.MetaServer,
	globalThresholds map[string]map[bool]map[string]float64,
) map[string]float64 {
	// 1 get threshold from npd
	npdThresholds := getMetricThresholdFromNPD(metaServer)
	if thresholds := getAndValidateBatch(thresholdNames, npdThresholds); thresholds != nil {
		general.Infof("get threshold from npd: %v", thresholds)
		return thresholds
	}

	// 2 get threshold from config
	configThresholds := getMetricThresholdFromConf(metaServer, globalThresholds)
	if thresholds := getAndValidateBatch(thresholdNames, configThresholds); thresholds != nil {
		general.Infof("get threshold from config: %v", thresholds)
		return thresholds
	}

	return nil
}

// GetMetricThresholdsAll get all valid thresholds from npd and config
func GetMetricThresholdsAll(metaServer *metaserver.MetaServer,
	globalThresholds map[string]map[bool]map[string]float64,
) map[string]float64 {
	if metaServer == nil {
		return nil
	}

	// 1 get threshold from npd
	npdThresholds := getMetricThresholdFromNPD(metaServer)
	if thresholds := getAndValidateAll(npdThresholds); thresholds != nil {
		general.Infof("get threshold from npd: %v", thresholds)
		return thresholds
	}

	// 2 get threshold from config
	configThresholds := getMetricThresholdFromConf(metaServer, globalThresholds)
	if thresholds := getAndValidateAll(configThresholds); thresholds != nil {
		general.Infof("get threshold from config: %v", thresholds)
		return thresholds
	}

	return nil
}

func getMetricThresholdFromNPD(metaServer *metaserver.MetaServer) map[string]float64 {
	res := map[string]float64{}
	if metaServer.NPDFetcher == nil {
		return res
	}
	npd, err := metaServer.GetNPD(context.Background())
	if err != nil {
		return res
	}
	nodeMetrics := util.ExtractNPDScopedNodeMetrics(&npd.Status, ScopeMetricThreshold)
	for _, metric := range nodeMetrics {
		res[metric.MetricName] = metric.Value.AsApproximateFloat64()
	}
	return res
}

func getMetricThresholdFromConf(metaServer *metaserver.MetaServer,
	globalThresholds map[string]map[bool]map[string]float64,
) map[string]float64 {
	cpuCodeName := helper.GetCpuCodeName(metaServer.MetricsFetcher)
	isVM, _ := helper.GetIsVM(metaServer.MetricsFetcher)

	modelThresholds, exists := globalThresholds[cpuCodeName]
	if !exists {
		general.Warningf("no suitable threshold for cpuCode %v using default", isVM)
		modelThresholds = globalThresholds[metricthreshold.DefaultCPUCodeName]
	}
	threshold, exists := modelThresholds[isVM]
	if !exists {
		general.Warningf("no suitable threshold for cpuCode %v isVm %v, using default", cpuCodeName, isVM)
		return modelThresholds[false]
	}
	return threshold
}

func getAndValidate(thresholdName string, thresholds map[string]float64) *float64 {
	threshold, ok := thresholds[thresholdName]
	if !ok || threshold == 0 {
		general.Errorf("got empty threshold for %v", thresholdName)
		return nil
	}
	if threshold < Min {
		general.Errorf("threshold %v is too small", thresholdName)
		return nil
	}
	return &threshold
}

// getAndValidateBatch only return valid thresholds
func getAndValidateBatch(thresholdNames []string, thresholds map[string]float64) map[string]float64 {
	res := map[string]float64{}
	for _, thresholdName := range thresholdNames {
		if threshold := getAndValidate(thresholdName, thresholds); threshold != nil {
			res[thresholdName] = *threshold
		}
	}
	if len(res) == 0 {
		return nil
	}
	return res
}

func getAndValidateAll(thresholds map[string]float64) map[string]float64 {
	res := map[string]float64{}
	for thresholdName := range thresholds {
		if threshold := getAndValidate(thresholdName, thresholds); threshold != nil {
			res[thresholdName] = *threshold
		}
	}
	if len(res) == 0 {
		return nil
	}
	return res
}

func ExpandThresholds(thresholds map[string]float64, expandFactor float64) map[string]float64 {
	for key, val := range thresholds {
		thresholds[key] = val * expandFactor
	}
	return thresholds
}
