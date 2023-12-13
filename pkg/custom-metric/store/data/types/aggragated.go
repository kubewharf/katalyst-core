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
	"bytes"
	"math/big"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/metric"
)

var validAggregatorSuffixList = []string{
	metric.AggregateFunctionAvg,
	metric.AggregateFunctionMax,
	metric.AggregateFunctionMin,
	metric.AggregateFunctionP99,
	metric.AggregateFunctionP90,
	metric.AggregateFunctionLatest,
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

func (a *AggregatedItem) GetWindowSeconds() *int64 { return &a.WindowSeconds }

type AggregatedMetric struct {
	MetricMetaImp `json:",inline"`
	ObjectMetaImp `json:",inline"`
	BasicMetric   `json:",inline"`

	AggregatedIdentity `json:",inline"`
	Value              float64 `json:"values,omitempty"`
}

var _ Metric = &AggregatedMetric{}

func NewAggregatedInternalMetric(value float64, identity AggregatedIdentity) *AggregatedMetric {
	return &AggregatedMetric{
		Value:              value,
		AggregatedIdentity: identity,
	}
}

func (as *AggregatedMetric) DeepCopy() Metric {
	return &AggregatedMetric{
		MetricMetaImp: as.MetricMetaImp.DeepCopy(),
		ObjectMetaImp: as.ObjectMetaImp.DeepCopy(),
		BasicMetric:   as.BasicMetric.DeepCopy(),

		AggregatedIdentity: as.AggregatedIdentity,
		Value:              as.Value,
	}
}

func (as *AggregatedMetric) GetBaseMetricMetaImp() MetricMetaImp {
	origin, _ := ParseAggregator(as.GetName())
	return MetricMetaImp{
		Name:       origin,
		Namespaced: as.Namespaced,
		ObjectKind: as.ObjectKind,
	}
}

func (as *AggregatedMetric) Len() int {
	return 1
}

func (as *AggregatedMetric) String() string {
	b := bytes.Buffer{}
	b.WriteString("{ObjectNamespace: ")
	b.WriteString(as.GetObjectNamespace())
	b.WriteString(", Name: ")
	b.WriteString(as.GetName())
	b.WriteString(", ObjectKind: ")
	b.WriteString(as.GetObjectKind())
	b.WriteString(", ObjectName: ")
	b.WriteString(as.GetObjectName())
	b.WriteString("}")
	return b.String()
}

func (as *AggregatedMetric) GetItemList() []Item {
	return []Item{
		&AggregatedItem{
			AggregatedIdentity: as.AggregatedIdentity,
			Value:              as.Value,
		},
	}
}

func AggregatorMetricMetaImp(origin MetricMetaImp, aggName string) MetricMetaImp {
	return MetricMetaImp{
		Name:       origin.GetName() + aggName,
		Namespaced: origin.GetNamespaced(),
		ObjectKind: origin.GetObjectKind(),
	}
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

// IsAggregatorMetric return whether this metric is aggregator metric
func IsAggregatorMetric(metricName string) bool {
	_, agg := ParseAggregator(metricName)
	if len(agg) > 0 {
		return true
	}
	return false
}
