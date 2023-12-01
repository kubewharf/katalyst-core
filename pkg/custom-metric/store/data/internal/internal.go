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

package internal

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/montanaflynn/stats"
	"k8s.io/apimachinery/pkg/labels"

	apimetric "github.com/kubewharf/katalyst-api/pkg/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var aggregateFuncMap map[string]aggregateFunc = map[string]aggregateFunc{
	apimetric.AggregateFunctionMax:    maxAgg,
	apimetric.AggregateFunctionMin:    minAgg,
	apimetric.AggregateFunctionAvg:    avgAgg,
	apimetric.AggregateFunctionP99:    p99Agg,
	apimetric.AggregateFunctionP90:    p90Agg,
	apimetric.AggregateFunctionLatest: latestAgg,
}

type aggregateFunc func(items []*types.SeriesItem) (float64, error)

func buildAggregatedIdentity(items []*types.SeriesItem) (types.AggregatedIdentity, error) {

	var latestTime = items[0].Timestamp
	var oldestTime = items[0].Timestamp

	for i := range items {
		latestTime = general.MaxInt64(latestTime, items[i].Timestamp)
		oldestTime = general.MinInt64(oldestTime, items[i].Timestamp)
	}

	identity := types.AggregatedIdentity{
		Count:         int64(len(items)),
		Timestamp:     latestTime,
		WindowSeconds: (latestTime - oldestTime) / time.Second.Milliseconds(),
	}

	return identity, nil
}

func maxAgg(items []*types.SeriesItem) (float64, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("empty sequence for max aggregate")
	}
	max := items[0].Value
	for i := range items {
		max = general.MaxFloat64(max, items[i].Value)
	}

	return max, nil
}

func minAgg(items []*types.SeriesItem) (float64, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("empty sequence for min aggregate")
	}
	max := items[0].Value
	for i := range items {
		max = general.MinFloat64(max, items[i].Value)
	}

	return max, nil
}

func avgAgg(items []*types.SeriesItem) (float64, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("empty sequence for avg aggregate")
	}
	var sum float64 = 0
	for i := range items {
		sum += items[i].Value
	}

	return sum / float64(len(items)), nil
}

func p99Agg(items []*types.SeriesItem) (float64, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("empty sequence for p99 aggregate")
	}

	var statsData stats.Float64Data
	for i := range items {
		statsData = append(statsData, items[i].Value)
	}

	if p99, err := statsData.Percentile(99); err != nil {
		return -1, fmt.Errorf("failed to get stats p99: %v", err)
	} else {
		return p99, nil
	}
}

func p90Agg(items []*types.SeriesItem) (float64, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("empty sequence for p90 aggregate")
	}

	var statsData stats.Float64Data
	for i := range items {
		statsData = append(statsData, items[i].Value)
	}

	if p90, err := statsData.Percentile(99); err != nil {
		return -1, fmt.Errorf("failed to get stats p90: %v", err)
	} else {
		return p90, nil
	}
}

func latestAgg(items []*types.SeriesItem) (float64, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("empty sequence for p90 aggregate")
	}

	latestItem := items[0]
	for i := range items {
		if latestItem.Timestamp < items[i].Timestamp {
			latestItem = items[i]
		}
	}

	return latestItem.Value, nil
}

// labeledMetricImp is used as an internal version of metricItem for only one combination of labels.
type labeledMetricImp struct {
	types.BasicMetric

	timestampSets map[int64]interface{}
	seriesMetric  *types.SeriesMetric
}

func newLabeledMetricImp(b types.BasicMetric) *labeledMetricImp {
	return &labeledMetricImp{
		BasicMetric:   b,
		timestampSets: make(map[int64]interface{}),
		seriesMetric:  types.NewSeriesMetric(),
	}
}

func (l *labeledMetricImp) DeepCopy() *labeledMetricImp {
	return &labeledMetricImp{
		BasicMetric:   l.BasicMetric.DeepCopy(),
		timestampSets: l.timestampSets,
		seriesMetric:  l.seriesMetric.DeepCopy().(*types.SeriesMetric),
	}
}

func (l *labeledMetricImp) gc(expiredTimestamp int64) {
	var ordered []*types.SeriesItem
	for _, m := range l.seriesMetric.Values {
		if m.Timestamp > expiredTimestamp {
			ordered = append(ordered, m)
		} else {
			delete(l.timestampSets, m.Timestamp)
		}
	}
	l.seriesMetric.Values = ordered
}

func (l *labeledMetricImp) len() int {
	return l.seriesMetric.Len()
}

// MetricImp is used as an internal version of metricItem.
type MetricImp struct {
	// those unified fields is only kept for one replica in MetricImp,
	// and we will try to copy into types.Metric when corresponding functions are called.
	types.MetricMetaImp
	types.ObjectMetaImp

	sync.RWMutex

	// Timestamp will be used as a unique key to avoid
	// duplicated metric to be written more than once
	expiredTime int64

	labeledMetricStore map[string]*labeledMetricImp
	aggregatedMetric   map[string]*types.AggregatedMetric
}

func NewInternalMetric(m types.MetricMetaImp, o types.ObjectMetaImp) *MetricImp {
	return &MetricImp{
		MetricMetaImp:      m,
		ObjectMetaImp:      o,
		labeledMetricStore: make(map[string]*labeledMetricImp),
		aggregatedMetric:   make(map[string]*types.AggregatedMetric),
	}
}

func (a *MetricImp) GetSeriesItems(metricSelector labels.Selector, latest bool) ([]*types.SeriesMetric, bool) {
	a.RLock()
	defer a.RUnlock()

	if len(a.labeledMetricStore) == 0 {
		return nil, false
	}

	var latestTimestamp int64 = 0
	result := make([]*types.SeriesMetric, 0, len(a.labeledMetricStore))
	for k := range a.labeledMetricStore {
		if metricSelector != nil && !metricSelector.Matches(labels.Set(a.labeledMetricStore[k].Labels)) {
			continue
		}

		res := a.labeledMetricStore[k].seriesMetric.DeepCopy().(*types.SeriesMetric)
		res.MetricMetaImp = a.MetricMetaImp.DeepCopy()
		res.ObjectMetaImp = a.ObjectMetaImp.DeepCopy()
		res.BasicMetric = a.labeledMetricStore[k].BasicMetric
		if latest {
			latestItem := res.Values[res.Len()-1]
			if latestItem.Timestamp <= latestTimestamp {
				continue
			}

			latestTimestamp = latestItem.Timestamp
			res.Values = []*types.SeriesItem{latestItem}
		}
		result = append(result, res)
	}

	// only one latest metric
	if latest && len(result) > 1 {
		latestMetric := result[0]
		for i := range result {
			if latestMetric.Values[0].Timestamp < result[i].Values[0].Timestamp {
				latestMetric = result[i]
			}
		}
		result = []*types.SeriesMetric{latestMetric}
	}

	return result, true
}

func (a *MetricImp) aggregateMatchedMetric(metricSelector labels.Selector, aggFunc aggregateFunc) (*types.AggregatedMetric, error) {
	var (
		aggregatedValue float64
		identity        types.AggregatedIdentity
		err             error
		matchedItems    = make([]*types.SeriesItem, 0)
	)

	for k := range a.labeledMetricStore {
		ms := a.labeledMetricStore[k]
		if metricSelector.Matches(labels.Set(ms.Labels)) {
			matchedItems = append(matchedItems, ms.seriesMetric.Values...)
		}
	}

	if aggregatedValue, err = aggFunc(matchedItems); err != nil {
		general.Errorf("aggregate for %v/%v metric %v with metric selector %v failed, err:%v",
			a.GetObjectNamespace(), a.GetObjectName(), a.MetricMetaImp.Name, metricSelector, err)
		return nil, err
	}

	if identity, err = buildAggregatedIdentity(matchedItems); err != nil {
		general.Errorf("get aggregated identity for %v/%v metric %v with metric selector %v failed, err:%v",
			a.GetObjectNamespace(), a.GetObjectName(), a.MetricMetaImp.Name, metricSelector, err)
		return nil, err
	}

	return types.NewAggregatedInternalMetric(aggregatedValue, identity), nil
}

func (a *MetricImp) GetAggregatedItems(metricSelector labels.Selector, agg string) (types.Metric, bool) {
	a.RLock()
	defer a.RUnlock()

	var v *types.AggregatedMetric
	if metricSelector == nil {
		var ok bool
		v, ok = a.aggregatedMetric[agg]
		if !ok {
			return nil, false
		}
	} else {
		// realtime aggregate for items selected by metricSelector
		aggFunc, ok := aggregateFuncMap[agg]
		if !ok {
			general.Errorf("unsupported aggregate function:%v", agg)
			return nil, false
		}

		aggregatedMetric, err := a.aggregateMatchedMetric(metricSelector, aggFunc)
		if err != nil {
			general.Errorf("aggregate metric failed, err:%v", err)
			return nil, false
		}
		v = aggregatedMetric
	}

	res := v.DeepCopy().(*types.AggregatedMetric)
	res.MetricMetaImp = types.AggregatorMetricMetaImp(a.MetricMetaImp, agg)
	res.ObjectMetaImp = a.ObjectMetaImp.DeepCopy()
	// don't set metric labels for aggregated metric
	return res, true
}

func (a *MetricImp) AddSeriesMetric(is *types.SeriesMetric) []*types.SeriesItem {
	a.Lock()
	defer a.Unlock()

	var res []*types.SeriesItem

	labelsString := is.BasicMetric.String()
	if _, ok := a.labeledMetricStore[labelsString]; !ok {
		a.labeledMetricStore[labelsString] = newLabeledMetricImp(is.BasicMetric)
	}

	ms := a.labeledMetricStore[labelsString]

	for _, v := range is.Values {
		// timestamp must be none-empty and valid
		if v.Timestamp == 0 || v.Timestamp < a.expiredTime {
			continue
		}

		// timestamp must be unique
		if _, ok := ms.timestampSets[v.Timestamp]; ok {
			continue
		}
		ms.timestampSets[v.Timestamp] = struct{}{}

		// always make the Value list as ordered
		i := sort.Search(len(ms.seriesMetric.Values), func(i int) bool {
			return v.Timestamp < ms.seriesMetric.Values[i].Timestamp
		})

		ms.seriesMetric.Values = append(ms.seriesMetric.Values, &types.SeriesItem{})
		copy(ms.seriesMetric.Values[i+1:], ms.seriesMetric.Values[i:])
		ms.seriesMetric.Values[i] = &types.SeriesItem{
			Value:     v.Value,
			Timestamp: v.Timestamp,
		}

		res = append(res, v)
	}

	return res
}

func (a *MetricImp) MergeAggregatedMetric(as *types.AggregatedMetric) {
	a.Lock()
	defer a.Unlock()

	_, aggName := types.ParseAggregator(as.GetName())
	if _, ok := a.aggregatedMetric[aggName]; !ok {
		a.aggregatedMetric[aggName] = as
	}
}

// aggregate calculate the aggregated metric based on snapshot of current store.
func (a *MetricImp) aggregate() {
	a.aggregatedMetric = make(map[string]*types.AggregatedMetric)
	if len(a.labeledMetricStore) <= 0 {
		return
	}

	allItems := make([]*types.SeriesItem, 0)
	for _, ms := range a.labeledMetricStore {
		allItems = append(allItems, ms.seriesMetric.Values...)
	}

	var err error
	var identity types.AggregatedIdentity
	var sum float64
	var statsData stats.Float64Data
	var max = allItems[0].Value
	var min = allItems[0].Value
	var latestTime = allItems[0].Timestamp
	var oldestTime = allItems[0].Timestamp
	var latestItem = allItems[0]

	if identity, err = buildAggregatedIdentity(allItems); err != nil {
		general.Errorf("failed to get aggregated identity,err:%v", err)
		return
	}

	for i := range allItems {
		item := allItems[i]
		sum += item.Value
		max = general.MaxFloat64(max, item.Value)
		min = general.MinFloat64(min, item.Value)
		statsData = append(statsData, item.Value)
		latestTime = general.MaxInt64(latestTime, item.Timestamp)
		oldestTime = general.MinInt64(oldestTime, item.Timestamp)
		if latestItem.Timestamp < item.Timestamp {
			latestItem = item
		}
	}

	a.aggregatedMetric[apimetric.AggregateFunctionMax] = types.NewAggregatedInternalMetric(max, identity)
	a.aggregatedMetric[apimetric.AggregateFunctionMin] = types.NewAggregatedInternalMetric(min, identity)
	a.aggregatedMetric[apimetric.AggregateFunctionAvg] = types.NewAggregatedInternalMetric(sum/float64(len(allItems)), identity)
	a.aggregatedMetric[apimetric.AggregateFunctionLatest] = types.NewAggregatedInternalMetric(latestItem.Value, identity)

	if p99, err := statsData.Percentile(99); err != nil {
		general.Errorf("failed to get stats p99: %v", err)
	} else {
		a.aggregatedMetric[apimetric.AggregateFunctionP99] = types.NewAggregatedInternalMetric(p99, identity)
	}

	if p90, err := statsData.Percentile(90); err != nil {
		general.Errorf("failed to get stats p90: %v", err)
	} else {
		a.aggregatedMetric[apimetric.AggregateFunctionP90] = types.NewAggregatedInternalMetric(p90, identity)
	}
}

// AggregateMetric calculate the aggregated metric based on snapshot of current store
func (a *MetricImp) AggregateMetric() {
	a.Lock()
	defer a.Unlock()

	a.aggregate()
}

func (a *MetricImp) GC(expiredTimestamp int64) {
	a.Lock()
	defer a.Unlock()

	a.expiredTime = expiredTimestamp

	for k, l := range a.labeledMetricStore {
		l.gc(expiredTimestamp)
		if l.len() == 0 {
			delete(a.labeledMetricStore, k)
		}
	}

	a.aggregate()
}

func (a *MetricImp) Empty() bool {
	a.RLock()
	defer a.RUnlock()

	return len(a.labeledMetricStore) == 0
}

func (a *MetricImp) Len() int {
	a.RLock()
	defer a.RUnlock()

	count := 0
	for k := range a.labeledMetricStore {
		count += a.labeledMetricStore[k].len()
	}
	return count
}

func (a *MetricImp) GenerateTags() []metrics.MetricTag {
	return []metrics.MetricTag{
		{Key: "metric_name", Val: a.GetName()},
		{Key: "object_name", Val: a.GetObjectName()},
	}
}

// GetLatestTimestamp returns the latest metric timestamp in milliseconds.
func (a *MetricImp) GetLatestTimestamp() int64 {
	a.RLock()
	defer a.RUnlock()

	var latestTimestamp int64 = -1

	for s := range a.labeledMetricStore {
		seriesMetric := a.labeledMetricStore[s].seriesMetric
		if latestTimestamp < seriesMetric.Values[seriesMetric.Len()-1].Timestamp {
			latestTimestamp = seriesMetric.Values[seriesMetric.Len()-1].Timestamp
		}
	}

	return latestTimestamp
}
