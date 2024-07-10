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

package resource_portrait

import (
	"fmt"
	"sort"
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimetrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
)

// generatePodMetrics will add new portraits based on old portraits and remove expired data.
func generatePodMetrics(algoConf *apiconfig.ResourcePortraitConfig, metrics []apimetrics.PodMetrics, timeSeriesData map[string][]timeSeriesItem, groupData map[string]float64) []apimetrics.PodMetrics {
	if algoConf == nil {
		return nil
	}

	currentPodMetricsMap := convertPodMetricsSliceToMap(metrics)
	timeSeriesData = aggreTimeSeries(algoConf, timeSeriesData)
	newPodMetricsMap := convertTimeseriesToPodMetricsMap(algoConf.Source, algoConf.AlgorithmConfig.Method, timeSeriesData, groupData)
	mergedPodMetricsMap := mergePodMetricMap(currentPodMetricsMap, newPodMetricsMap)
	return filterExpiredPodMetrics(convertPodMetricsMapToSortedSlice(mergedPodMetricsMap))
}

// convertPodMetricsSliceToMap converts slices of metrics into maps, with timestamps as keys.
func convertPodMetricsSliceToMap(metrics []apimetrics.PodMetrics) map[int64]apimetrics.PodMetrics {
	return lo.SliceToMap(metrics, func(item apimetrics.PodMetrics) (int64, apimetrics.PodMetrics) {
		return item.Timestamp.Time.Unix(), item
	})
}

// convertTimeseriesToPodMetricsMap converts time series data into metric maps.
func convertTimeseriesToPodMetricsMap(source, method string, timeSeriesData map[string][]timeSeriesItem, groupData map[string]float64) map[int64]apimetrics.PodMetrics {
	fakeContainerName := GenerateFakeContainerName(source, method)
	podMetricsMap := map[int64]apimetrics.PodMetrics{}
	for resourceName, timeSeries := range timeSeriesData {
		for _, item := range timeSeries {
			if _, ok := podMetricsMap[item.Timestamp]; !ok {
				podMetricsMap[item.Timestamp] = apimetrics.PodMetrics{
					Timestamp: metav1.Time{Time: time.Unix(item.Timestamp, 0)},
					Containers: []apimetrics.ContainerMetrics{
						{
							Name:  fakeContainerName,
							Usage: map[v1.ResourceName]resource.Quantity{},
						},
					},
				}
			}
			podMetricsMap[item.Timestamp].Containers[0].Usage[v1.ResourceName(resourceName)] = *resource.NewMilliQuantity(int64(item.Value*1000), resource.DecimalSI)
			for groupDataResourceName, groupDataValue := range groupData {
				podMetricsMap[item.Timestamp].Containers[0].Usage[v1.ResourceName(groupDataResourceName)] = *resource.NewMilliQuantity(int64(groupDataValue*1000), resource.DecimalSI)
			}
		}
	}
	return podMetricsMap
}

// mergePodMetricMap merges two metric maps.
func mergePodMetricMap(a map[int64]apimetrics.PodMetrics, b map[int64]apimetrics.PodMetrics) map[int64]apimetrics.PodMetrics {
	for k, va := range a {
		vb, ok := b[k]
		if !ok || len(va.Containers) == 0 || len(vb.Containers) == 0 {
			continue
		}

		for resourceName, quantity := range va.Containers[0].Usage {
			b[k].Containers[0].Usage[resourceName] = quantity
		}
	}
	return lo.Assign(a, b)
}

// convertPodMetricsMapToSortedSlice reconverts the metric map into slices and sorts them based on timestamp.
func convertPodMetricsMapToSortedSlice(podMetricsMap map[int64]apimetrics.PodMetrics) []apimetrics.PodMetrics {
	podMetrics := lo.MapToSlice(podMetricsMap, func(key int64, value apimetrics.PodMetrics) apimetrics.PodMetrics {
		return value
	})
	sort.Slice(podMetrics, func(i, j int) bool {
		return podMetrics[i].Timestamp.Before(&podMetrics[j].Timestamp)
	})
	return podMetrics
}

// filterExpiredPodMetrics is used to filter out metrics before the current time from the metric slice.
func filterExpiredPodMetrics(metrics []apimetrics.PodMetrics) []apimetrics.PodMetrics {
	now := time.Now()
	return lo.Filter(metrics, func(item apimetrics.PodMetrics, _ int) bool {
		return now.Before(item.Timestamp.Time)
	})
}

func aggreTimeSeries(algoConf *apiconfig.ResourcePortraitConfig, timeseries map[string][]timeSeriesItem) map[string][]timeSeriesItem {
	// Divide multiple indicator series into groups and aggregate each group into a single value.
	// For example, if the input data of the portrait is at the minute level(TimeWindow.Input), and you want the output data at the
	// hour level, you need to divide it into groups of 60 indicators(TimeWindow.Output).
	aggreTimeSeries := map[string][]timeSeriesItem{}
	for metric, timeSeries := range timeseries {
		var aggreRecord []timeSeriesItem
		items := make([]timeSeriesItem, 0, algoConf.AlgorithmConfig.TimeWindow.Output)
		for i := 0; i < len(timeSeries); i++ {
			items = append(items, timeSeries[i])
			if len(items) == algoConf.AlgorithmConfig.TimeWindow.Output {
				aggreRecord = append(aggreRecord, aggreSeries(items, algoConf.AlgorithmConfig.TimeWindow.Aggregator))
				items = make([]timeSeriesItem, 0, algoConf.AlgorithmConfig.TimeWindow.Output)
			}
		}
		if len(items) > 0 {
			aggreRecord = append(aggreRecord, aggreSeries(items, algoConf.AlgorithmConfig.TimeWindow.Aggregator))
		}
		aggreTimeSeries[metric] = aggreRecord
	}
	return aggreTimeSeries
}

func aggreSeries(timeSeries []timeSeriesItem, operator apiconfig.Aggregator) timeSeriesItem {
	switch operator {
	case apiconfig.Max:
		maxIndex := 0
		maxValue := timeSeries[0].Value
		for i := 1; i < len(timeSeries); i++ {
			if maxValue < timeSeries[i].Value {
				maxIndex = i
				maxValue = timeSeries[i].Value
			}
		}
		timeSeries[maxIndex].Timestamp = timeSeries[0].Timestamp
		return timeSeries[maxIndex]
	case apiconfig.Avg:
		fallthrough
	default:
		var sum float64
		for i := 0; i < len(timeSeries); i++ {
			sum += timeSeries[i].Value
		}
		timeSeries[0].Value = sum / float64(len(timeSeries))
		return timeSeries[0]
	}
}

func generateMetricsRefreshRecord(aggMetrics *apiworkload.AggPodMetrics) map[string]metav1.Time {
	if aggMetrics == nil {
		return nil
	}

	record := map[string]metav1.Time{}
	for _, item := range aggMetrics.Items {
		for _, container := range item.Containers {
			if _, ok := record[container.Name]; !ok {
				record[container.Name] = item.Timestamp
			}
		}
	}
	return record
}

func getAggMetricsFromSPD(spd *apiworkload.ServiceProfileDescriptor) *apiworkload.AggPodMetrics {
	for _, aggMetrics := range spd.Status.AggMetrics {
		if aggMetrics.Scope == ResourcePortraitPluginName {
			return aggMetrics.DeepCopy()
		}
	}
	return &apiworkload.AggPodMetrics{Scope: ResourcePortraitPluginName}
}

func filterResourcePortraitIndicators(rpIndicator *apiconfig.ResourcePortraitIndicators) *apiconfig.ResourcePortraitIndicators {
	if rpIndicator == nil {
		return nil
	}

	var newConfigs []apiconfig.ResourcePortraitConfig
	for _, config := range rpIndicator.Configs {
		if config.Source == "" || (len(config.Metrics) == 0 && len(config.CustomMetrics) == 0) {
			continue
		}
		if config.AlgorithmConfig.Method == "" || config.AlgorithmConfig.ResyncPeriod == 0 {
			continue
		}
		if config.AlgorithmConfig.TimeWindow.Input < resourcePortraitAlgorithmTimeWindowInputMin ||
			config.AlgorithmConfig.TimeWindow.Output < resourcePortraitAlgorithmTimeWindowOutputMin ||
			config.AlgorithmConfig.TimeWindow.HistorySteps < resourcePortraitAlgorithmTimeWindowHistoryStepsMin ||
			config.AlgorithmConfig.TimeWindow.PredictionSteps < resourcePortraitAlgorithmTimeWindowPredictionStepsMin {
			continue
		}
		if config.AlgorithmConfig.TimeWindow.Aggregator != apiconfig.Avg && config.AlgorithmConfig.TimeWindow.Aggregator != apiconfig.Max {
			continue
		}
		newConfigs = append(newConfigs, config)
	}
	rpIndicator.Configs = newConfigs
	return rpIndicator
}

func convertAlgorithmResultToAggMetrics(aggMetrics *apiworkload.AggPodMetrics, algoConf *apiconfig.ResourcePortraitConfig, timeseries map[string][]timeSeriesItem, groupData map[string]float64) *apiworkload.AggPodMetrics {
	return &apiworkload.AggPodMetrics{Aggregator: apiworkload.Aggregator(algoConf.AlgorithmConfig.TimeWindow.Aggregator), Items: generatePodMetrics(algoConf, aggMetrics.Items, timeseries, groupData)}
}

// GenerateFakeContainerName generates fake container name for resource portrait
func GenerateFakeContainerName(source, method string) string {
	return fmt.Sprintf("%s-%s", source, method)
}
