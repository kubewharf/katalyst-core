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
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/metrics/pkg/apis/custom_metrics"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"
)

type DummyMetricQuery struct{}

var _ MetricProvider = DummyMetricQuery{}

func (d DummyMetricQuery) GetMetricByName(_ context.Context, _ types.NamespacedName,
	_ provider.CustomMetricInfo, _ labels.Selector,
) (*custom_metrics.MetricValue, error) {
	return &custom_metrics.MetricValue{
		DescribedObject: custom_metrics.ObjectReference{
			Kind:      "pod",
			Namespace: "my-namespace",
			Name:      "my-pod",
		},
		Metric: custom_metrics.MetricIdentifier{
			Name: "cpu-usage",
		},
		Timestamp: metav1.Time{Time: time.Now()},
		Value:     *resource.NewQuantity(1, resource.DecimalSI),
	}, nil
}

func (d DummyMetricQuery) GetMetricBySelector(_ context.Context, _ string, _ labels.Selector,
	_ provider.CustomMetricInfo, _ labels.Selector,
) (*custom_metrics.MetricValueList, error) {
	return &custom_metrics.MetricValueList{
		Items: []custom_metrics.MetricValue{
			{
				DescribedObject: custom_metrics.ObjectReference{
					Kind:      "pod",
					Namespace: "my-namespace",
					Name:      "my-pod",
				},
				Metric: custom_metrics.MetricIdentifier{
					Name: "cpu-usage",
				},
				Timestamp: metav1.Time{Time: time.Now()},
				Value:     *resource.NewQuantity(1, resource.DecimalSI),
			},
		},
	}, nil
}

func (d DummyMetricQuery) ListAllMetrics() []provider.CustomMetricInfo {
	return []provider.CustomMetricInfo{
		{
			GroupResource: schema.GroupResource{Resource: "pod"},
			Namespaced:    true,
			Metric:        "cpu-usage",
		},
	}
}

func (d DummyMetricQuery) GetExternalMetric(_ context.Context, _ string, _ labels.Selector,
	_ provider.ExternalMetricInfo,
) (*external_metrics.ExternalMetricValueList, error) {
	return &external_metrics.ExternalMetricValueList{
		Items: []external_metrics.ExternalMetricValue{
			{
				MetricName: "my-external-metric",
				MetricLabels: map[string]string{
					"foo": "bar",
				},
				Value: *resource.NewQuantity(42, resource.DecimalSI),
			},
		},
	}, nil
}

func (d DummyMetricQuery) ListAllExternalMetrics() []provider.ExternalMetricInfo {
	return []provider.ExternalMetricInfo{
		{
			Metric: "my-external-metric",
		},
	}
}
