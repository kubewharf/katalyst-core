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
	"sort"
	"sync"
	"time"

	"github.com/montanaflynn/stats"

	apimetric "github.com/kubewharf/katalyst-api/pkg/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// MetricImp is used as an internal version of metricItem.
// notice: we will not add any locks for this structure, and it should be done in caller
type MetricImp struct {
	// those unified fields is only kept for one replica in MetricImp,
	// and we will try to copy into types.Metric when corresponding functions are called.
	types.MetricMetaImp
	types.ObjectMetaImp
	types.BasicMetric

	sync.RWMutex

	// Timestamp will be used as a unique key to avoid
	// duplicated metric to be written more than once
	expiredTime   int64
	timestampSets map[int64]interface{}
	seriesMetric  *types.SeriesMetric

	aggregatedMetric map[string]*types.AggregatedMetric
}

func NewInternalMetric(m types.MetricMetaImp, o types.ObjectMetaImp, b types.BasicMetric) *MetricImp {
	return &MetricImp{
		MetricMetaImp:    m,
		ObjectMetaImp:    o,
		BasicMetric:      b,
		timestampSets:    make(map[int64]interface{}),
		seriesMetric:     types.NewSeriesMetric(),
		aggregatedMetric: make(map[string]*types.AggregatedMetric),
	}
}

func (a *MetricImp) GetSeriesItems(latest bool) (types.Metric, bool) {
	a.RLock()
	defer a.RUnlock()

	if a.seriesMetric.Len() == 0 {
		return nil, false
	}

	res := a.seriesMetric.DeepCopy().(*types.SeriesMetric)
	res.MetricMetaImp = a.MetricMetaImp.DeepCopy()
	res.ObjectMetaImp = a.ObjectMetaImp.DeepCopy()
	res.BasicMetric = a.BasicMetric.DeepCopy()
	if latest {
		res.Values = []*types.SeriesItem{a.seriesMetric.Values[a.seriesMetric.Len()-1]}
	}
	return res, true
}

func (a *MetricImp) GetAggregatedItems(agg string) (types.Metric, bool) {
	a.RLock()
	defer a.RUnlock()

	v, ok := a.aggregatedMetric[agg]
	if !ok {
		return nil, false
	}

	res := v.DeepCopy().(*types.AggregatedMetric)
	res.MetricMetaImp = types.AggregatorMetricMetaImp(a.MetricMetaImp, agg)
	res.ObjectMetaImp = a.ObjectMetaImp.DeepCopy()
	res.BasicMetric = a.BasicMetric.DeepCopy()
	return res, true
}

func (a *MetricImp) AddSeriesMetric(is *types.SeriesMetric) []*types.SeriesItem {
	a.Lock()
	defer a.Unlock()

	var res []*types.SeriesItem
	for _, v := range is.Values {
		// timestamp must be none-empty and valid
		if v.Timestamp == 0 || v.Timestamp < a.expiredTime {
			continue
		}

		// timestamp must be unique
		if _, ok := a.timestampSets[v.Timestamp]; ok {
			continue
		}
		a.timestampSets[v.Timestamp] = struct{}{}

		// always make the Value list as ordered
		i := sort.Search(len(a.seriesMetric.Values), func(i int) bool {
			return v.Timestamp < a.seriesMetric.Values[i].Timestamp
		})

		a.seriesMetric.Values = append(a.seriesMetric.Values, &types.SeriesItem{})
		copy(a.seriesMetric.Values[i+1:], a.seriesMetric.Values[i:])
		a.seriesMetric.Values[i] = &types.SeriesItem{
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
	if len(a.seriesMetric.Values) <= 0 {
		return
	}

	var sum float64
	var statsData stats.Float64Data
	var max = a.seriesMetric.Values[0].Value
	var min = a.seriesMetric.Values[0].Value
	for _, item := range a.seriesMetric.Values {
		sum += item.Value
		max = general.MaxFloat64(max, item.Value)
		min = general.MinFloat64(min, item.Value)
		statsData = append(statsData, item.Value)
	}

	identity := types.AggregatedIdentity{
		Count:         int64(a.seriesMetric.Len()),
		Timestamp:     a.seriesMetric.Values[len(a.seriesMetric.Values)-1].Timestamp,
		WindowSeconds: (a.seriesMetric.Values[a.seriesMetric.Len()-1].Timestamp - a.seriesMetric.Values[0].Timestamp) / time.Second.Milliseconds(),
	}
	a.aggregatedMetric[apimetric.AggregateFunctionMax] = types.NewAggregatedInternalMetric(max, identity)
	a.aggregatedMetric[apimetric.AggregateFunctionMin] = types.NewAggregatedInternalMetric(min, identity)
	a.aggregatedMetric[apimetric.AggregateFunctionAvg] = types.NewAggregatedInternalMetric(sum/float64(a.seriesMetric.Len()), identity)

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

	var ordered []*types.SeriesItem
	for _, m := range a.seriesMetric.Values {
		if m.Timestamp > expiredTimestamp {
			ordered = append(ordered, m)
		} else {
			delete(a.timestampSets, m.Timestamp)
		}
	}
	a.seriesMetric.Values = ordered
	a.aggregate()
}

func (a *MetricImp) Empty() bool {
	a.RLock()
	defer a.RUnlock()

	return a.seriesMetric.Len() == 0
}

func (a *MetricImp) Len() int {
	a.RLock()
	defer a.RUnlock()

	return a.seriesMetric.Len()
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

	if a.seriesMetric.Len() != 0 {
		return a.seriesMetric.Values[a.seriesMetric.Len()-1].Timestamp
	}

	return -1
}

// GetOldestTimestamp returns the latest metric timestamp in milliseconds.
func (a *MetricImp) GetOldestTimestamp() int64 {
	a.RLock()
	defer a.RUnlock()

	if a.seriesMetric.Len() != 0 {
		return a.seriesMetric.Values[0].Timestamp
	}

	return -1
}
