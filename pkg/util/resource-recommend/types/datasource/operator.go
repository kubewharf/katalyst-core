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

package datasource

import (
	"sort"
)

type SamplesOverview struct {
	AvgValue            float64
	MinValue            float64
	MaxValue            float64
	Percentile50thValue float64
	Percentile90thValue float64
	LastTimestamp       int64
	FirstTimestamp      int64
	Count               int
}

func GetSamplesOverview(timeSeries *TimeSeries) *SamplesOverview {
	if timeSeries == nil || len(timeSeries.Samples) == 0 {
		return nil
	}
	samples := timeSeries.Samples
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].Value < samples[j].Value
	})
	aggregationSample := &SamplesOverview{
		Count:               len(samples),
		MinValue:            samples[0].Value,
		MaxValue:            samples[len(samples)-1].Value,
		Percentile50thValue: calculateSamplesPercentile(samples, 0.5),
		Percentile90thValue: calculateSamplesPercentile(samples, 0.9),
	}

	sumValue := 0.0
	for _, sample := range samples {
		if aggregationSample.LastTimestamp < sample.Timestamp {
			aggregationSample.LastTimestamp = sample.Timestamp
		}
		if aggregationSample.FirstTimestamp == 0 || aggregationSample.FirstTimestamp > sample.Timestamp {
			aggregationSample.FirstTimestamp = sample.Timestamp
		}
		sumValue += sample.Value
	}
	aggregationSample.AvgValue = sumValue / float64(len(timeSeries.Samples))
	return aggregationSample
}

func CalculateSamplesPercentile(samples []Sample, percentile float64) float64 {
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].Value < samples[j].Value
	})

	return calculateSamplesPercentile(samples, percentile)
}

func calculateSamplesPercentile(samples []Sample, percentile float64) float64 {
	length := float64(len(samples))
	index := (length - 1) * percentile

	// 判断 index 是否为整数
	if index == float64(int(index)) {
		return samples[int(index)].Value
	}

	lower := samples[int(index)].Value
	upper := samples[int(index)+1].Value
	return (lower + upper) / 2
}
