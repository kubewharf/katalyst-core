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
	"sort"
	"time"

	"github.com/montanaflynn/stats"

	apimetric "github.com/kubewharf/katalyst-api/pkg/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// internalMetricImp is used as an internal version of metricItem.
// notice: we will not add any locks for this structure, and it should be done in caller
type internalMetricImp struct {
	// those unified fields is only kept for one replica in internalMetricImp,
	// and we will try to copy into types.Metric when corresponding functions are called.
	types.MetricMetaImp
	types.ObjectMetaImp
	types.BasicMetric

	// Timestamp will be used as a unique key to avoid
	// duplicated metric to be written more than once
	expiredTime   int64
	timestampSets map[int64]interface{}
	seriesMetric  *types.SeriesMetric

	aggregatedMetric *types.AggregatedMetric
}

func newInternalMetric(m types.MetricMetaImp, o types.ObjectMetaImp, b types.BasicMetric) *internalMetricImp {
	return &internalMetricImp{
		MetricMetaImp:    m,
		ObjectMetaImp:    o,
		BasicMetric:      b,
		timestampSets:    make(map[int64]interface{}),
		seriesMetric:     types.NewSeriesMetric(),
		aggregatedMetric: types.NewAggregatedInternalMetric(),
	}
}

func (a *internalMetricImp) getSeriesItems(latest bool) (types.Metric, bool) {
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

func (a *internalMetricImp) getAggregatedItems(agg string) (types.Metric, bool) {
	v, ok := a.aggregatedMetric.Values[agg]
	if !ok {
		return nil, false
	}

	res := a.aggregatedMetric.DeepCopy().(*types.AggregatedMetric)
	res.MetricMetaImp = a.MetricMetaImp.DeepCopy()
	res.ObjectMetaImp = a.ObjectMetaImp.DeepCopy()
	res.BasicMetric = a.BasicMetric.DeepCopy()
	res.Values = map[string]float64{a.GetName() + agg: v}
	return res, true
}

func (a *internalMetricImp) addSeriesMetric(is *types.SeriesMetric) []*types.SeriesItem {
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

func (a *internalMetricImp) mergeAggregatedMetric(as *types.AggregatedMetric) {
	if len(a.aggregatedMetric.Values) == 0 {
		a.aggregatedMetric = as
		return
	}

	for k, v := range as.Values {
		if _, ok := a.aggregatedMetric.Values[k]; !ok {
			a.aggregatedMetric.Values[k] = v
		}
	}
}

// aggregateMetric calculate the aggregated metric based on snapshot of current store
func (a *internalMetricImp) aggregateMetric() {
	a.aggregatedMetric.Values = make(map[string]float64)
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

	a.aggregatedMetric.Count = int64(a.seriesMetric.Len())
	a.aggregatedMetric.Timestamp = a.seriesMetric.Values[0].Timestamp
	a.aggregatedMetric.WindowSeconds = (a.seriesMetric.Values[a.seriesMetric.Len()-1].Timestamp - a.seriesMetric.Values[0].Timestamp) / time.Second.Milliseconds()
	a.aggregatedMetric.Values[apimetric.AggregateFunctionMax] = max
	a.aggregatedMetric.Values[apimetric.AggregateFunctionMin] = min
	a.aggregatedMetric.Values[apimetric.AggregateFunctionAvg] = sum / float64(a.seriesMetric.Len())

	if p99, err := statsData.Percentile(99); err != nil {
		general.Errorf("failed to get stats p99: %v", err)
	} else {
		a.aggregatedMetric.Values[apimetric.AggregateFunctionP99] = p99
	}

	if p90, err := statsData.Percentile(90); err != nil {
		general.Errorf("failed to get stats p90: %v", err)
	} else {
		a.aggregatedMetric.Values[apimetric.AggregateFunctionP90] = p90
	}
}

func (a *internalMetricImp) gc(expiredTimestamp int64) {
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
	a.aggregateMetric()
}

func (a *internalMetricImp) empty() bool { return a.seriesMetric.Len() == 0 }

func (a *internalMetricImp) len() int { return a.seriesMetric.Len() }

func (a *internalMetricImp) generateTags() []metrics.MetricTag {
	return []metrics.MetricTag{
		{Key: "metric_name", Val: a.GetName()},
		{Key: "object_name", Val: a.GetObjectName()},
	}
}
