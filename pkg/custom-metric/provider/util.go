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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/custom_metrics"
	"k8s.io/metrics/pkg/apis/external_metrics"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
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
 those packing functions are helpers to pack out standard Item structs
*/

func PackMetricValueList(metricItem types.Metric, metricSelector labels.Selector) []custom_metrics.MetricValue {
	var res []custom_metrics.MetricValue
	for _, item := range metricItem.GetItemList() {
		res = append(res, *PackMetricValue(metricItem, item, metricSelector))
	}
	return res
}

func PackMetricValue(m types.Metric, item types.Item, metricSelector labels.Selector) *custom_metrics.MetricValue {
	result := &custom_metrics.MetricValue{
		DescribedObject: custom_metrics.ObjectReference{
			Kind:      m.GetObjectKind(),
			Namespace: m.GetObjectNamespace(),
			Name:      m.GetObjectName(),
		},
		Metric: custom_metrics.MetricIdentifier{
			Name: m.GetName(),
			Selector: &metav1.LabelSelector{
				MatchLabels: m.GetLabels(),
			},
		},
		Timestamp:     metav1.NewTime(time.UnixMilli(item.GetTimestamp())),
		WindowSeconds: item.GetWindowSeconds(),
		Value:         item.GetQuantity(),
	}
	// if user specifies the metric selector, return itself.
	if !metricSelector.Empty() {
		result.Metric.Selector = convertMetricLabelSelector(metricSelector)
	}

	return result
}

func convertMetricLabelSelector(metricSelector labels.Selector) *metav1.LabelSelector {
	requirements, _ := metricSelector.Requirements()
	selector := &metav1.LabelSelector{
		MatchExpressions: make([]metav1.LabelSelectorRequirement, 0),
	}

	for i := range requirements {
		selector.MatchExpressions = append(selector.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      requirements[i].Key(),
			Operator: metav1.LabelSelectorOperator(requirements[i].Operator()),
			Values:   requirements[i].Values().List(),
		})
	}
	return selector
}

func PackExternalMetricValueList(metricItem types.Metric) []external_metrics.ExternalMetricValue {
	var res []external_metrics.ExternalMetricValue
	for _, item := range metricItem.GetItemList() {
		res = append(res, *PackExternalMetricValue(metricItem, item))
	}
	return res
}

func PackExternalMetricValue(metricItem types.Metric, metric types.Item) *external_metrics.ExternalMetricValue {
	return &external_metrics.ExternalMetricValue{
		MetricName:    metricItem.GetName(),
		MetricLabels:  metricItem.GetLabels(),
		Timestamp:     metav1.NewTime(time.UnixMilli(metric.GetTimestamp())),
		WindowSeconds: metric.GetWindowSeconds(),
		Value:         metric.GetQuantity(),
	}
}
