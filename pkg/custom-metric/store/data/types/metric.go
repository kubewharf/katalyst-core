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
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type MetricMeta interface {
	GetName() string
	GetNamespaced() bool
	GetObjectKind() string
}

type MetricMetaImp struct {
	Name       string `json:"name,omitempty"`
	Namespaced bool   `json:"namespaced,omitempty"`
	ObjectKind string `json:"objectKind,omitempty"`
}

var _ MetricMeta = MetricMetaImp{}

func (m MetricMetaImp) GetName() string         { return m.Name }
func (m MetricMetaImp) GetNamespaced() bool     { return m.Namespaced }
func (m MetricMetaImp) GetObjectKind() string   { return m.ObjectKind }
func (m MetricMetaImp) DeepCopy() MetricMetaImp { return m }

type ObjectMeta interface {
	GetObjectName() string
	GetObjectNamespace() string
}

type ObjectMetaImp struct {
	ObjectNamespace string `json:"objectNamespace,omitempty"`
	ObjectName      string `json:"objectName,omitempty"`
}

var _ ObjectMeta = ObjectMetaImp{}

func (m ObjectMetaImp) GetObjectNamespace() string { return m.ObjectNamespace }
func (m ObjectMetaImp) GetObjectName() string      { return m.ObjectName }
func (m ObjectMetaImp) DeepCopy() ObjectMetaImp    { return m }

type BasicMetric struct {
	Labels map[string]string `json:"labels,omitempty"`
}

func (m *BasicMetric) GetLabels() map[string]string { return m.Labels }
func (m *BasicMetric) DeepCopy() BasicMetric {
	return BasicMetric{Labels: general.DeepCopyMap(m.Labels)}
}
func (m *BasicMetric) String() string {
	if len(m.Labels) == 0 {
		return ""
	}

	keySlice := make([]string, 0, len(m.Labels))
	for k := range m.Labels {
		key := k
		keySlice = append(keySlice, key)
	}
	sort.Strings(keySlice)
	builder := strings.Builder{}
	for i := range keySlice {
		builder.WriteString(keySlice[i])
		builder.WriteString("=")
		builder.WriteString(m.Labels[keySlice[i]])
		builder.WriteString(",")
	}

	return builder.String()
}

// PackMetricMetaList merges MetricMetaImp lists and removes duplicates
func PackMetricMetaList(metricMetaLists ...[]MetricMeta) []MetricMeta {
	metricTypeMap := make(map[MetricMeta]interface{})
	for _, metricsTypeList := range metricMetaLists {
		for _, metricsType := range metricsTypeList {
			if _, ok := metricTypeMap[metricsType]; !ok {
				metricTypeMap[metricsType] = struct{}{}
			}
		}
	}

	var res []MetricMeta
	for metricType := range metricTypeMap {
		res = append(res, metricType)
	}
	return res
}

// GenerateMetaTags returns tag based on the given meta-info
func GenerateMetaTags(m MetricMeta, d ObjectMeta) []metrics.MetricTag {
	return []metrics.MetricTag{
		{Key: "metric_name", Val: m.GetName()},
		{Key: "object_name", Val: d.GetObjectName()},
	}
}

// Item represents the standard format of metric-item in local cache
type Item interface {
	DeepCopy() Item
	// GetQuantity returns the value for this metric
	GetQuantity() resource.Quantity
	// GetTimestamp returns the timestamp that this metric was generated
	GetTimestamp() int64
	// GetCount returns the total amount of raw metrics that generates this
	GetCount() *int64
	// GetWindowSeconds returns the time-window that this the raw metrics represent for
	GetWindowSeconds() *int64
}

type Metric interface {
	MetricMeta
	ObjectMeta

	DeepCopy() Metric

	// String returns a standard format for this metricItem
	// GetLabels/SetLabels maintains the labels in Metric
	String() string
	GetLabels() map[string]string

	// Len returns length for Item
	// GetItemList returns a list for Item
	Len() int
	GetItemList() []Item
}
