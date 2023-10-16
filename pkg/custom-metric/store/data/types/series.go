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

	"k8s.io/apimachinery/pkg/api/resource"
)

type SeriesItem struct {
	Value     float64 `json:"value,omitempty"`
	Timestamp int64   `json:"timestamp,omitempty"`
}

var _ Item = &SeriesItem{}

func NewInternalItem(value float64, timestamp int64) *SeriesItem {
	return &SeriesItem{
		Value:     value,
		Timestamp: timestamp,
	}
}

func (i *SeriesItem) DeepCopy() Item {
	return &SeriesItem{
		Value:     i.Value,
		Timestamp: i.Timestamp,
	}
}

func (i *SeriesItem) GetQuantity() resource.Quantity {
	return resource.MustParse(big.NewFloat(i.Value).String())
}

func (i *SeriesItem) GetTimestamp() int64 { return i.Timestamp }

func (i *SeriesItem) GetCount() *int64         { return nil }
func (i *SeriesItem) GetWindowSeconds() *int64 { return nil }

type SeriesMetric struct {
	MetricMetaImp `json:",inline"`
	ObjectMetaImp `json:",inline"`
	BasicMetric   `json:",inline"`

	Values []*SeriesItem `json:"values,omitempty"`
}

var _ Metric = &SeriesMetric{}

func NewSeriesMetric() *SeriesMetric {
	return &SeriesMetric{}
}

func (is *SeriesMetric) DeepCopy() Metric {
	res := &SeriesMetric{
		MetricMetaImp: is.MetricMetaImp.DeepCopy(),
		ObjectMetaImp: is.ObjectMetaImp.DeepCopy(),
		BasicMetric:   is.BasicMetric.DeepCopy(),
	}
	for _, i := range is.Values {
		res.Values = append(res.Values, i.DeepCopy().(*SeriesItem))
	}
	return res
}

func (is *SeriesMetric) GetItemList() []Item {
	var res []Item
	for _, i := range is.Values {
		res = append(res, i)
	}
	return res
}

func (is *SeriesMetric) Len() int {
	return len(is.Values)
}

func (is *SeriesMetric) String() string {
	return fmt.Sprintf("{ObjectNamespace: %v, Name: %v, ObjectKind: %v, ObjectName: %v}",
		is.GetObjectNamespace(), is.GetName(), is.GetObjectKind(), is.GetObjectName())
}

func (is *SeriesMetric) AddMetric(item *SeriesItem) {
	is.Values = append(is.Values, item)
}
