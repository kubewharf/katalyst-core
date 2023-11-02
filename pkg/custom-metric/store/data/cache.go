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

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/internal"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricsNameKCMASStoreDataLatencySet = "kcmas_store_data_latency_set"
	metricsNameKCMASStoreDataLatencyGet = "kcmas_store_data_latency_get"

	metricsNameKCMASStoreDataSetCost = "kcmas_store_data_cost_set"
	metricsNameKCMASStoreDataGetCost = "kcmas_store_data_cost_get"

	metricsNameKCMASStoreDataLength    = "kcmas_store_data_length"
	metricsNameKCMASStoreWindowSeconds = "kcmas_store_data_window_seconds"
)

// CachedMetric stores all metricItems in an organized way.
// the aggregation logic will be performed in this package.
type CachedMetric struct {
	sync.RWMutex
	emitter   metrics.MetricEmitter
	metricMap map[types.MetricMeta]*objectMetricStore
}

func NewCachedMetric(metricsEmitter metrics.MetricEmitter) *CachedMetric {
	return &CachedMetric{
		emitter:   metricsEmitter,
		metricMap: make(map[types.MetricMeta]*objectMetricStore),
	}
}

func (c *CachedMetric) addNewMetricMeta(metricMeta types.MetricMetaImp) {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.metricMap[metricMeta]; !ok {
		c.metricMap[metricMeta] = newObjectMetricStore(metricMeta)
	}
}

func (c *CachedMetric) getObjectMetricStore(metricMeta types.MetricMetaImp) *objectMetricStore {
	c.RLock()
	defer c.RUnlock()

	return c.metricMap[metricMeta]
}

func (c *CachedMetric) AddSeriesMetric(sList ...types.Metric) {
	start := time.Now()

	defer func() {
		_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataSetCost, time.Now().Sub(start).Microseconds(), metrics.MetricTypeNameRaw)
	}()

	var needReAggregate []*internal.MetricImp
	for _, s := range sList {
		d, ok := s.(*types.SeriesMetric)
		if !ok || d == nil || len(d.GetItemList()) == 0 || d.GetName() == "" {
			continue
		}

		if _, ok := c.metricMap[d.MetricMetaImp]; !ok {
			c.addNewMetricMeta(d.MetricMetaImp)
		}
		objectMetricStore := c.getObjectMetricStore(d.MetricMetaImp)

		if !objectMetricStore.objectExists(d.ObjectMetaImp) {
			objectMetricStore.add(d.ObjectMetaImp, d.BasicMetric)
		}
		internalMetric := objectMetricStore.getInternalMetricImp(d.ObjectMetaImp)

		added := internalMetric.AddSeriesMetric(d)
		if len(added) > 0 {
			needReAggregate = append(needReAggregate, internalMetric)
			latestTimestamp := internalMetric.GetLatestTimestamp()
			costs := start.Sub(time.UnixMilli(latestTimestamp)).Microseconds()
			general.InfofV(6, "set cache,metric name: %v, series length: %v, add length:%v, latest timestamp: %v, costs: %v(microsecond)", d.MetricMetaImp.Name,
				s.Len(), len(added), latestTimestamp, costs)
			_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataLatencySet, costs, metrics.MetricTypeNameRaw,
				types.GenerateMetaTags(d.MetricMetaImp, d.ObjectMetaImp)...)
		}
	}

	for _, i := range needReAggregate {
		i.AggregateMetric()
	}
}

func (c *CachedMetric) AddAggregatedMetric(aList ...types.Metric) {
	for _, a := range aList {
		d, ok := a.(*types.AggregatedMetric)
		if !ok || d == nil || len(d.GetItemList()) != 1 || d.GetName() == "" {
			continue
		}

		baseMetricMetaImp := d.GetBaseMetricMetaImp()
		if _, ok := c.metricMap[baseMetricMetaImp]; !ok {
			c.addNewMetricMeta(baseMetricMetaImp)
		}
		objectMetricStore := c.getObjectMetricStore(baseMetricMetaImp)

		if !objectMetricStore.objectExists(d.ObjectMetaImp) {
			objectMetricStore.add(d.ObjectMetaImp, d.BasicMetric)
		}
		internalMetric := objectMetricStore.getInternalMetricImp(d.ObjectMetaImp)
		internalMetric.MergeAggregatedMetric(d)
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
	for metricMeta, objectMetricStore := range c.metricMap {
		if objectMetricStore.len() == 0 {
			continue
		}
		res = append(res, metricMeta.GetName())
	}
	return res
}

func (c *CachedMetric) GetMetric(namespace, metricName string, objName string, gr *schema.GroupResource, latest bool) ([]types.Metric, bool) {
	start := time.Now()
	originMetricName, aggName := types.ParseAggregator(metricName)

	defer func() {
		_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataGetCost, time.Now().Sub(start).Microseconds(), metrics.MetricTypeNameRaw)
	}()

	var res []types.Metric
	metricMeta := types.MetricMetaImp{
		Name:       originMetricName,
		Namespaced: namespace != "",
	}
	if gr != nil {
		metricMeta.ObjectKind = gr.String()
	}

	objectMetricStore := c.getObjectMetricStore(metricMeta)
	if objectMetricStore != nil {
		objectMetricStore.iterate(func(internalMetric *internal.MetricImp) {
			if internalMetric.GetObjectNamespace() != namespace || (objName != "" && internalMetric.GetObjectName() != objName) {
				return
			}

			var metricItem types.Metric
			var exist bool
			if aggName == "" {
				metricItem, exist = internalMetric.GetSeriesItems(latest)
			} else {
				metricItem, exist = internalMetric.GetAggregatedItems(aggName)
			}

			if exist && metricItem.Len() > 0 {
				res = append(res, metricItem)
				// TODO this metrics costs great mount of cpu resource
				//costs := now.Sub(time.UnixMilli(internal.seriesMetric.Values[internal.len()-1].Timestamp)).Microseconds()
				//_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataLatencyGet, costs, metrics.MetricTypeNameRaw, internal.generateTags()...)
			}
		})
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
		internalMap.iterate(func(internalMetric *internal.MetricImp) {
			if internalMetric.GetObjectNamespace() != namespace {
				return
			}

			metricItem, exist := internalMetric.GetSeriesItems(false)
			if exist && metricItem.Len() > 0 {
				res = append(res, metricItem.DeepCopy())
				costs := now.Sub(time.UnixMilli(internalMetric.GetLatestTimestamp())).Microseconds()
				_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataLatencyGet, costs, metrics.MetricTypeNameRaw, internalMetric.GenerateTags()...)
			}
		})
	}

	return res
}

func (c *CachedMetric) GC(expiredTime time.Time) {
	c.gcWithTimestamp(expiredTime.UnixMilli())
}
func (c *CachedMetric) gcWithTimestamp(expiredTimestamp int64) {
	c.RLock()
	defer c.RUnlock()

	for _, objectMetricStore := range c.metricMap {
		objectMetricStore.iterate(func(internalMetric *internal.MetricImp) {
			internalMetric.GC(expiredTimestamp)
			if internalMetric.Len() != 0 {
				_ = c.emitter.StoreInt64(metricsNameKCMASStoreDataLength, int64(internalMetric.Len()),
					metrics.MetricTypeNameRaw, internalMetric.GenerateTags()...)
				_ = c.emitter.StoreInt64(metricsNameKCMASStoreWindowSeconds, (internalMetric.GetLatestTimestamp()-
					internalMetric.GetOldestTimestamp())/time.Second.Milliseconds(), metrics.MetricTypeNameRaw, internalMetric.GenerateTags()...)
			}
		})
	}
}

func (c *CachedMetric) Purge() {
	c.Lock()
	defer c.Unlock()

	for metricMeta, store := range c.metricMap {
		store.purge()
		if store.len() == 0 {
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
		for _, objectMetricStore := range c.metricMap {
			objectMetricStore.iterate(func(internalMetric *internal.MetricImp) {
				if metricItem, exist := internalMetric.GetSeriesItems(false); exist && metricItem.Len() > 0 {
					res = append(res, metricItem)
				}
			})
		}
	} else {
		for _, metricList := range metricLists {
			c.AddAggregatedMetric(metricList...)
		}
		for _, objectMetricStore := range c.metricMap {
			objectMetricStore.iterate(func(internalMetric *internal.MetricImp) {
				if metricItem, exist := internalMetric.GetAggregatedItems(aggName); exist && metricItem.Len() > 0 {
					res = append(res, metricItem)
				}
			})
		}
	}

	return res
}

type objectMetricStore struct {
	metricMeta types.MetricMetaImp
	objectMap  map[types.ObjectMeta]*internal.MetricImp
	sync.RWMutex
}

func newObjectMetricStore(metricMeta types.MetricMetaImp) *objectMetricStore {
	return &objectMetricStore{
		metricMeta: metricMeta,
		objectMap:  make(map[types.ObjectMeta]*internal.MetricImp),
	}
}

func (s *objectMetricStore) add(objectMeta types.ObjectMetaImp, basicMeta types.BasicMetric) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.objectMap[objectMeta]; !ok {
		s.objectMap[objectMeta] = internal.NewInternalMetric(s.metricMeta, objectMeta, basicMeta)
	}
}

func (s *objectMetricStore) objectExists(objectMeta types.ObjectMeta) (exist bool) {
	s.RLock()
	defer s.RUnlock()

	_, exist = s.objectMap[objectMeta]
	return
}

func (s *objectMetricStore) getInternalMetricImp(objectMeta types.ObjectMeta) *internal.MetricImp {
	s.RLock()
	defer s.RUnlock()

	return s.objectMap[objectMeta]
}

// this function is read only,please do not perform any write operation like add/delete to this object
func (s *objectMetricStore) iterate(f func(internalMetric *internal.MetricImp)) {
	s.RLock()
	defer s.RUnlock()

	for _, InternalMetricImp := range s.objectMap {
		f(InternalMetricImp)
	}
}

func (s *objectMetricStore) purge() {
	s.Lock()
	defer s.Unlock()

	for _, internalMetric := range s.objectMap {
		if internalMetric.Empty() {
			delete(s.objectMap, internalMetric.ObjectMetaImp)
		}
	}
}

func (s *objectMetricStore) len() int {
	return len(s.objectMap)
}
