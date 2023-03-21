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

package provider

import (
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/custom_metrics"
	"k8s.io/metrics/pkg/apis/external_metrics"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
)

// findMetricValueLatest returns metric with the latest timestamp.
func findMetricValueLatest(metricName string, metricValueList []custom_metrics.MetricValue) *custom_metrics.MetricValue {
	var res *custom_metrics.MetricValue

	for i, metricValue := range metricValueList {
		if res == nil || res.Timestamp.UnixMilli() < metricValue.Timestamp.UnixMilli() {
			res = &metricValue

			if i != 0 {
				klog.Warningf("metric %v stores latest value with index %v instead of beginning", metricName, i)
			}
		}
	}

	return res
}

/*
 those packing functions are helpers to pack out standard Metric structs
*/

func PackMetricValueList(internal *data.InternalMetric) []custom_metrics.MetricValue {
	var res []custom_metrics.MetricValue
	for _, value := range internal.GetValues() {
		res = append(res, *PackMetricValue(internal, value))
	}
	return res
}

func PackMetricValue(internal *data.InternalMetric, value *data.InternalValue) *custom_metrics.MetricValue {
	return &custom_metrics.MetricValue{
		DescribedObject: custom_metrics.ObjectReference{
			Kind:      internal.GetObjectKind(),
			Namespace: internal.GetObjectNamespace(),
			Name:      internal.GetObjectName(),
		},
		Metric: custom_metrics.MetricIdentifier{
			Name: internal.GetName(),
			Selector: &metav1.LabelSelector{
				MatchLabels: internal.GetLabels(),
			},
		},
		Timestamp: metav1.NewTime(time.UnixMilli(value.GetTimestamp())),
		Value:     *resource.NewQuantity(value.GetValue(), resource.DecimalSI),
	}
}

func PackExternalMetricValueList(internal *data.InternalMetric) []external_metrics.ExternalMetricValue {
	var res []external_metrics.ExternalMetricValue
	for _, value := range internal.GetValues() {
		res = append(res, *PackExternalMetricValue(internal, value))
	}
	return res
}

func PackExternalMetricValue(internal *data.InternalMetric, value *data.InternalValue) *external_metrics.ExternalMetricValue {
	return &external_metrics.ExternalMetricValue{
		MetricName:   internal.GetName(),
		MetricLabels: internal.GetLabels(),
		Timestamp:    metav1.NewTime(time.UnixMilli(value.GetTimestamp())),
		Value:        *resource.NewQuantity(value.GetValue(), resource.DecimalSI),
	}
}
