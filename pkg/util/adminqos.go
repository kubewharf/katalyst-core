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

package util

import "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"

func ConvertStringListToNumaEvictionRankingMetrics(metrics []string) []v1alpha1.NumaEvictionRankingMetric {
	res := make([]v1alpha1.NumaEvictionRankingMetric, 0, len(metrics))
	for _, metric := range metrics {
		res = append(res, v1alpha1.NumaEvictionRankingMetric(metric))
	}
	return res
}

func ConvertStringListToSystemEvictionRankingMetrics(metrics []string) []v1alpha1.SystemEvictionRankingMetric {
	res := make([]v1alpha1.SystemEvictionRankingMetric, 0, len(metrics))
	for _, metric := range metrics {
		res = append(res, v1alpha1.SystemEvictionRankingMetric(metric))
	}
	return res
}

func ConvertNumaEvictionRankingMetricsToStringList(metrics []v1alpha1.NumaEvictionRankingMetric) []string {
	res := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		res = append(res, string(metric))
	}
	return res
}

func ConvertSystemEvictionRankingMetricsToStringList(metrics []v1alpha1.SystemEvictionRankingMetric) []string {
	res := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		res = append(res, string(metric))
	}
	return res
}
