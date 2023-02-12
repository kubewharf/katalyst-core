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
	"fmt"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/custom_metrics"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
)

// MetricProvider is a standard interface to query metric data;
// to simplify the implementation, MetricProvider use the standard kubernetes interface for
// custom-metrics and external-metrics; and provider.MetricsProvider may have different
// explanations for their parameters, we make the following appoints
//
// - GetMetricByName
// --- if metric name is nominated, ignore the selector;
// --- otherwise, return all the metrics matched with the metric selector;
// --- in all cases, we should check whether the objects (that own this metric) is matched (with name)
// --- only returns the latest value
//
// - GetMetricBySelector
// --- if metric name is nominated, ignore the selector;
// --- otherwise, return all the metrics matched with the metric selector;
// --- in all cases, we should check whether the objects (that own this metric) is matched (with label selector)
//
// - GetExternalMetric
// --- if metric name is nominated, ignore the metric selector;
// --- otherwise, return all the metrics matched with the metric selector;
//
type MetricProvider interface {
	provider.MetricsProvider
}

type MetricProviderImp struct {
	ctx      context.Context
	storeImp store.MetricStore
}

func NewMetricProviderImp(ctx context.Context, storeImp store.MetricStore) *MetricProviderImp {
	return &MetricProviderImp{
		ctx:      ctx,
		storeImp: storeImp,
	}
}

func (m *MetricProviderImp) GetMetricByName(ctx context.Context, namespacedName types.NamespacedName,
	info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValue, error) {
	klog.Infof("GetMetricByName: metric name %v, object %v, namespace %v, object name %v, metricSelector %v",
		info.Metric, info.GroupResource, namespacedName.Namespace, namespacedName.Name, metricSelector)

	internalList, err := m.storeImp.GetMetric(ctx, namespacedName.Namespace, info.Metric, namespacedName.Name,
		&info.GroupResource, nil, metricSelector, 1)
	if err != nil {
		klog.Errorf("GetMetric err: %v", err)
		return nil, err
	}

	var res *custom_metrics.MetricValue
	for _, internal := range internalList {
		if internal.GetObject() == "" || internal.GetObjectName() == "" {
			klog.Errorf("custom metric %v doesn't have object %v/%v",
				internal.Name, internal.GetObject(), internal.GetObjectName())
			continue
		}

		if res == nil {
			res = findMetricValueLatest(PackMetricValueList(internal))
		} else {
			res = findMetricValueLatest(append(PackMetricValueList(internal), *res))
		}
	}

	if res == nil {
		return nil, fmt.Errorf("no mtaching metric exists")
	}
	return res, nil
}

func (m *MetricProviderImp) GetMetricBySelector(ctx context.Context, namespace string, objSelector labels.Selector,
	info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValueList, error) {
	klog.Infof("GetMetricBySelector: metric name %v, object %v, namespace %v, objSelector %v, metricSelector %v",
		info.Metric, info.GroupResource, namespace, objSelector, metricSelector)

	internalList, err := m.storeImp.GetMetric(ctx, namespace, info.Metric, "", nil, objSelector, metricSelector, -1)
	if err != nil {
		klog.Errorf("GetMetric err: %v", err)
		return nil, err
	}

	var items []custom_metrics.MetricValue
	for _, internal := range internalList {
		if internal.GetObject() == "" || internal.GetObjectName() == "" {
			klog.Errorf("custom metric %v doesn't have object %v/%v",
				internal.Name, internal.GetObject(), internal.GetObjectName())
			continue
		}

		items = append(items, PackMetricValueList(internal)...)
	}
	return &custom_metrics.MetricValueList{
		Items: items,
	}, nil

}

func (m *MetricProviderImp) ListAllMetrics() []provider.CustomMetricInfo {
	klog.V(6).Info("ListAllMetrics")
	internalList, err := m.storeImp.ListMetricWithObjects(context.Background())
	if err != nil {
		klog.Errorf("ListAllMetrics err: %v", err)
		return []provider.CustomMetricInfo{}
	}

	infoMap := make(map[provider.CustomMetricInfo]interface{})
	for _, internal := range internalList {
		if internal.GetObject() == "" || internal.GetObjectName() == "" {
			continue
		}

		namespaced := true
		if internal.GetNamespace() == "" {
			namespaced = false
		}

		_, gr := schema.ParseResourceArg(internal.GetObject())
		infoMap[provider.CustomMetricInfo{
			GroupResource: gr,
			Namespaced:    namespaced,
			Metric:        internal.GetName(),
		}] = struct{}{}
	}

	var res []provider.CustomMetricInfo
	for info := range infoMap {
		res = append(res, info)
	}
	return res
}

func (m *MetricProviderImp) GetExternalMetric(ctx context.Context, namespace string, metricSelector labels.Selector,
	info provider.ExternalMetricInfo) (*external_metrics.ExternalMetricValueList, error) {
	klog.Infof("GetExternalMetric: metric name %v, namespace %v, metricSelector %v",
		info.Metric, namespace, metricSelector)

	internalList, err := m.storeImp.GetMetric(ctx, namespace, info.Metric, "", nil, nil, metricSelector, -1)
	if err != nil {
		klog.Errorf("GetMetric err: %v", err)
		return nil, err
	}

	var items []external_metrics.ExternalMetricValue
	for _, internal := range internalList {
		if internal.GetObject() != "" || internal.GetObjectName() != "" {
			klog.Errorf("internal metric %v has object %v/%v unexpectedly",
				internal.Name, internal.GetObject(), internal.GetObjectName())
			continue
		}

		items = append(items, PackExternalMetricValueList(internal)...)
	}
	return &external_metrics.ExternalMetricValueList{
		Items: items,
	}, nil
}

func (m *MetricProviderImp) ListAllExternalMetrics() []provider.ExternalMetricInfo {
	klog.V(6).Info("ListAllExternalMetrics")
	internalList, err := m.storeImp.ListMetricWithoutObjects(context.Background())
	if err != nil {
		klog.Errorf("ListAllExternalMetrics err: %v", err)
		return []provider.ExternalMetricInfo{}
	}

	infoMap := make(map[provider.ExternalMetricInfo]interface{})
	for _, internal := range internalList {
		if internal.GetObject() != "" || internal.GetObjectName() != "" {
			continue
		}

		infoMap[provider.ExternalMetricInfo{
			Metric: internal.GetName(),
		}] = struct{}{}
	}

	var res []provider.ExternalMetricInfo
	for info := range infoMap {
		res = append(res, info)
	}
	return res
}
