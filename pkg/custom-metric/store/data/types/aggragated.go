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

package types

import (
	"fmt"
	"math/big"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var validAggregatorSuffixList = []string{
	metric.AggregateFunctionAvg,
	metric.AggregateFunctionMax,
	metric.AggregateFunctionMin,
	metric.AggregateFunctionP99,
	metric.AggregateFunctionP90,
}

type AggregatedIdentity struct {
	Count         int64 `json:"count,omitempty"`
	Timestamp     int64 `json:"timestamp,omitempty"`
	WindowSeconds int64 `json:"windowSeconds,omitempty"`
}

type AggregatedItem struct {
	AggregatedIdentity `json:",inline"`

	Value float64 `json:"value,omitempty"`
}

var _ Item = &AggregatedItem{}

func (a *AggregatedItem) DeepCopy() Item {
	return &AggregatedItem{
		AggregatedIdentity: a.AggregatedIdentity,
		Value:              a.Value,
	}
}

func (a *AggregatedItem) GetQuantity() resource.Quantity {
	return resource.MustParse(big.NewFloat(a.Value).String())
}

func (a *AggregatedItem) GetCount() *int64 { return &a.Count }

func (a *AggregatedItem) GetTimestamp() int64 { return a.Timestamp }

func (a *AggregatedItem) GetWindowSeconds() *int64 { return &a.Count }

type AggregatedMetric struct {
	MetricMetaImp `json:",inline"`
	ObjectMetaImp `json:",inline"`
	BasicMetric   `json:",inline"`

	AggregatedIdentity `json:",inline"`
	Values             map[string]float64 `json:"values,omitempty"`
}

var _ Metric = &AggregatedMetric{}

func NewAggregatedInternalMetric() *AggregatedMetric {
	return &AggregatedMetric{
		Values: make(map[string]float64),
	}
}

func (as *AggregatedMetric) DeepCopy() Metric {
	return &AggregatedMetric{
		MetricMetaImp: as.MetricMetaImp.DeepCopy(),
		ObjectMetaImp: as.ObjectMetaImp.DeepCopy(),
		BasicMetric:   as.BasicMetric.DeepCopy(),

		AggregatedIdentity: as.AggregatedIdentity,
		Values:             general.DeepCopyFload64Map(as.Values),
	}
}

func (as *AggregatedMetric) Len() int {
	return len(as.Values)
}

func (as *AggregatedMetric) String() string {
	return fmt.Sprintf("{ObjectNamespace: %v, Name: %v, ObjectKind: %v, ObjectName: %v}",
		as.GetObjectNamespace(), as.GetName(), as.GetObjectKind(), as.GetObjectName())
}

func (as *AggregatedMetric) GetItemList() []Item {
	var res []Item
	for k, v := range as.Values {
		if len(k) > 0 {
			res = append(res, &AggregatedItem{
				AggregatedIdentity: as.AggregatedIdentity,
				Value:              v,
			})
		}
	}
	return res
}

// ParseAggregator parses the given metricName into aggregator-suffix and origin-metric,
// and the returned value represents `metric, aggregator`
func ParseAggregator(metricName string) (string, string) {
	for _, agg := range validAggregatorSuffixList {
		if strings.HasSuffix(metricName, agg) {
			return strings.TrimSuffix(metricName, agg), agg
		}
	}
	return metricName, ""
}
