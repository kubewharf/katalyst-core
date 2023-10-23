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

package data

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	metricsNameKCMASStoreDataLatencySet = "kcmas_store_data_latency_set"
	metricsNameKCMASStoreDataLatencyGet = "kcmas_store_data_latency_get"

	metricsNameKCMASStoreDataLength    = "kcmas_store_data_length"
	metricsNameKCMASStoreWindowSeconds = "kcmas_store_data_window_seconds"
)

// CachedMetric stores all metricItems in an organized way.
// the aggregation logic will be performed in this package.
type CachedMetric struct {
	sync.RWMutex
	emitter   metrics.MetricEmitter
	metricMap map[types.MetricMeta]map[types.ObjectMeta]*internalMetricImp
}

func NewCachedMetric(metricsEmitter metrics.MetricEmitter) *CachedMetric {
	return &CachedMetric{
		emitter:   metricsEmitter,
		metricMap: make(map[types.MetricMeta]map[types.ObjectMeta]*internalMetricImp),
	}
}

func (c *CachedMetric) AddSeriesMetric(sList ...types.Metric) {
	now := time.Now()

	c.Lock()
	defer c.Unlock()

	var needReAggregate []*internalMetricImp
	for _, s := range sList {
		d, ok := s.(*types.SeriesMetric)
		if !ok || d == nil || len(d.GetItemList()) == 0 || d.GetName() == "" {
			continue
		}

		if _, ok := c.metricMap[d.MetricMetaImp]; !ok {
			c.metricMap[d.MetricMetaImp] = make(map[types.ObjectMeta]*internalMetricImp)
		}
		if _, ok := c.metricMap[d.MetricMetaImp][d.ObjectMetaImp]; !ok {
			c.metricMap[d.MetricMetaImp][d.ObjectMetaImp] = newInternalMetric(d.MetricMetaImp, d.ObjectMetaImp, d.BasicMetric)
		}

		added := c.metricMap[d.MetricMetaImp][d.ObjectMetaImp].addSeriesMetric(d)
		if len(added) > 0 {
			needReAggregate = append(needReAggregate, c.metricMap[d.MetricMetaImp][d.ObjectMetaImp])
			index := c.metricMap[d.MetricMetaImp][d.ObjectMetaImp].seriesMetric.Len() - 1
			costs := now.Sub(time.UnixMilli(c.metricMap[d.MetricMetaImp][d.ObjectMetaImp].seriesMetric.Values[index].Timestamp)).Microseconds()
			_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataLatencySet, costs, metrics.MetricTypeNameRaw,
				types.GenerateMetaTags(d.MetricMetaImp, d.ObjectMetaImp)...)
		}
	}

	for _, i := range needReAggregate {
		i.aggregateMetric()
	}
}

func (c *CachedMetric) AddAggregatedMetric(aList ...types.Metric) {
	c.Lock()
	defer c.Unlock()

	for _, a := range aList {
		d, ok := a.(*types.AggregatedMetric)
		if !ok || d == nil || len(d.GetItemList()) != 1 || d.GetName() == "" {
			continue
		}

		baseMetricMetaImp := d.GetBaseMetricMetaImp()
		if _, ok := c.metricMap[baseMetricMetaImp]; !ok {
			c.metricMap[baseMetricMetaImp] = make(map[types.ObjectMeta]*internalMetricImp)
		}
		if _, ok := c.metricMap[baseMetricMetaImp][d.ObjectMetaImp]; !ok {
			c.metricMap[baseMetricMetaImp][d.ObjectMetaImp] = newInternalMetric(baseMetricMetaImp, d.ObjectMetaImp, d.BasicMetric)
		}

		c.metricMap[baseMetricMetaImp][d.ObjectMetaImp].mergeAggregatedMetric(d)
	}
}

// ListAllMetricMeta returns all metric meta with a flattened slice
func (c *CachedMetric) ListAllMetricMeta(withObject bool) []types.MetricMeta {
	c.RLock()
	defer c.RUnlock()

	var res []types.MetricMeta
	for metricMeta := range c.metricMap {
		if (withObject && metricMeta.GetObjectKind() == "") ||
			(!withObject && metricMeta.GetObjectKind() != "") {
			continue
		}
		res = append(res, metricMeta)
	}
	return res
}

// ListAllMetricNames returns all metric with a flattened slice, but only contain names
func (c *CachedMetric) ListAllMetricNames() []string {
	c.RLock()
	defer c.RUnlock()

	var res []string
	for metricMeta, internalMap := range c.metricMap {
		if len(internalMap) == 0 {
			continue
		}
		res = append(res, metricMeta.GetName())
	}
	return res
}

func (c *CachedMetric) GetMetric(namespace, metricName string, objName string, gr *schema.GroupResource, latest bool) ([]types.Metric, bool) {
	now := time.Now()
	originMetricName, aggName := types.ParseAggregator(metricName)

	c.RLock()
	defer c.RUnlock()

	var res []types.Metric
	metricMeta := types.MetricMetaImp{
		Name:       originMetricName,
		Namespaced: namespace != "",
	}
	if gr != nil {
		metricMeta.ObjectKind = gr.String()
	}

	if internalMap, ok := c.metricMap[metricMeta]; ok {
		for _, internal := range internalMap {
			if internal.GetObjectNamespace() != namespace || (objName != "" && internal.GetObjectName() != objName) {
				continue
			}

			var metricItem types.Metric
			var exist bool
			if aggName == "" {
				metricItem, exist = internal.getSeriesItems(latest)
			} else {
				metricItem, exist = internal.getAggregatedItems(aggName)
			}

			if exist && metricItem.Len() > 0 {
				res = append(res, metricItem.DeepCopy())
				costs := now.Sub(time.UnixMilli(internal.seriesMetric.Values[internal.len()-1].Timestamp)).Microseconds()
				_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataLatencyGet, costs, metrics.MetricTypeNameRaw, internal.generateTags()...)
			}
		}
		return res, true
	}

	return nil, false
}

// GetAllMetricsInNamespace & GetAllMetricsInNamespaceWithLimit may be too time-consuming,
// so we should ensure that client falls into this functions as less frequent as possible.
func (c *CachedMetric) GetAllMetricsInNamespace(namespace string) []types.Metric {
	now := time.Now()

	c.RLock()
	defer c.RUnlock()

	var res []types.Metric
	for _, internalMap := range c.metricMap {
		for _, internal := range internalMap {
			if internal.GetObjectNamespace() != namespace {
				continue
			}

			metricItem, exist := internal.getSeriesItems(false)
			if exist && metricItem.Len() > 0 {
				res = append(res, metricItem.DeepCopy())
				costs := now.Sub(time.UnixMilli(internal.seriesMetric.Values[internal.len()-1].Timestamp)).Microseconds()
				_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataLatencyGet, costs, metrics.MetricTypeNameRaw, internal.generateTags()...)
			}
		}
	}

	return res
}

func (c *CachedMetric) GC(expiredTime time.Time) {
	c.gcWithTimestamp(expiredTime.UnixMilli())
}
func (c *CachedMetric) gcWithTimestamp(expiredTimestamp int64) {
	c.Lock()
	defer c.Unlock()

	for metricMeta, internalMap := range c.metricMap {
		for objectMeta, internal := range internalMap {
			internal.gc(expiredTimestamp)
			if len(internal.seriesMetric.Values) == 0 {
				delete(internalMap, objectMeta)
			} else {
				_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataLength, int64(len(internal.seriesMetric.Values)),
					metrics.MetricTypeNameRaw, internal.generateTags()...)
				_ = c.emitter.StoreInt64(metricsNameKCMASStoreWindowSeconds, (internal.seriesMetric.Values[len(internal.seriesMetric.Values)-1].Timestamp-
					internal.seriesMetric.Values[0].Timestamp)/time.Second.Milliseconds(), metrics.MetricTypeNameRaw, internal.generateTags()...)
			}
		}
		if len(internalMap) == 0 {
			delete(c.metricMap, metricMeta)
		}
	}
}

// MergeInternalMetricList merges internal metric lists and sort them
// for series: if the same timestamp appears in different list, randomly choose one item.
// for aggregated: we will just skip the duplicated items
func MergeInternalMetricList(metricName string, metricLists ...[]types.Metric) []types.Metric {
	if len(metricLists) == 0 {
		return []types.Metric{}
	} else if len(metricLists) == 1 {
		return metricLists[0]
	}

	var res []types.Metric
	c := NewCachedMetric(metrics.DummyMetrics{})

	_, aggName := types.ParseAggregator(metricName)
	if len(aggName) == 0 {
		for _, metricList := range metricLists {
			c.AddSeriesMetric(metricList...)
		}
		for _, internalMap := range c.metricMap {
			for _, internal := range internalMap {
				if metricItem, exist := internal.getSeriesItems(false); exist && metricItem.Len() > 0 {
					res = append(res, metricItem)
				}
			}
		}
	} else {
		for _, metricList := range metricLists {
			c.AddAggregatedMetric(metricList...)
		}
		for _, internalMap := range c.metricMap {
			for _, internal := range internalMap {
				if metricItem, exist := internal.getAggregatedItems(aggName); exist && metricItem.Len() > 0 {
					res = append(res, metricItem)
				}
			}
		}
	}

	return res
}
