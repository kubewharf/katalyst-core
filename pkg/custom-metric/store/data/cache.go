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
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricsNameKCMASStoreDataLatencySet = "kcmas_store_data_latency_set"
	metricsNameKCMASStoreDataLatencyGet = "kcmas_store_data_latency_get"
)

type InternalValue struct {
	Value     float64 `json:"value,omitempty"`
	Timestamp int64   `json:"timestamp,omitempty"`
}

func NewInternalValue(value float64, timestamp int64) *InternalValue {
	return &InternalValue{
		Value:     value,
		Timestamp: timestamp,
	}
}

func (i *InternalValue) DeepCopy() *InternalValue {
	return &InternalValue{
		Value:     i.Value,
		Timestamp: i.Timestamp,
	}
}

func (i *InternalValue) GetQuantity() resource.Quantity {
	return resource.MustParse(big.NewFloat(i.Value).String())
}
func (i *InternalValue) GetTimestamp() int64 { return i.Timestamp }

type MetricMeta struct {
	Name       string `json:"name,omitempty"`
	Namespaced bool   `json:"namespaced,omitempty"`
	ObjectKind string `json:"objectKind,omitempty"`
}

func (m *MetricMeta) GetName() string       { return m.Name }
func (m *MetricMeta) GetNamespaced() bool   { return m.Namespaced }
func (m *MetricMeta) GetObjectKind() string { return m.ObjectKind }

func (m *MetricMeta) SetObjectKind(objectKind string) { m.ObjectKind = objectKind }
func (m *MetricMeta) SetNamespaced(namespaced bool)   { m.Namespaced = namespaced }
func (m *MetricMeta) SetName(name string)             { m.Name = name }

type ObjectMeta struct {
	ObjectNamespace string `json:"objectNamespace,omitempty"`
	ObjectName      string `json:"objectName,omitempty"`
}

func (m *ObjectMeta) GetObjectNamespace() string { return m.ObjectNamespace }
func (m *ObjectMeta) GetObjectName() string      { return m.ObjectName }

func (m *ObjectMeta) SetObjectNamespace(objectNamespace string) { m.ObjectNamespace = objectNamespace }
func (m *ObjectMeta) SetObjectName(objectName string)           { m.ObjectName = objectName }

// InternalMetric is used as an internal version of metricItem
type InternalMetric struct {
	MetricMeta `json:",inline"`
	ObjectMeta `json:",inline"`

	Labels        map[string]string `json:"labels,omitempty"`
	InternalValue []*InternalValue  `json:"internalValue,omitempty"`
}

func NewInternalMetric(name string) *InternalMetric {
	return &InternalMetric{
		MetricMeta: MetricMeta{
			Name: name,
		},
		Labels: make(map[string]string),
	}
}

func (a *InternalMetric) String() string {
	return fmt.Sprintf("{ObjectNamespace: %v, Name: %v, ObjectKind: %v, ObjectName: %v}",
		a.ObjectNamespace, a.Name, a.ObjectKind, a.ObjectName)
}

func (a *InternalMetric) DeepCopy() *InternalMetric {
	b := &InternalMetric{
		MetricMeta: a.MetricMeta,
		ObjectMeta: a.ObjectMeta,
		Labels:     general.DeepCopyMap(a.Labels),
	}

	for _, i := range a.InternalValue {
		b.InternalValue = append(b.InternalValue, i.DeepCopy())
	}
	return b
}

// todo delete this
func (a *InternalMetric) DeepCopyWithLimit(limit int) *InternalMetric {
	b := a.DeepCopy()

	total := len(b.InternalValue)
	if limit > 0 && total > limit {
		b.InternalValue = b.InternalValue[total-limit:]
	}

	return b
}

// todo implement this
func (a *InternalMetric) GetAggregatedMetricValue() *InternalMetric {
	return &InternalMetric{}
}

func (a *InternalMetric) GetLabels() map[string]string { return a.Labels }
func (a *InternalMetric) GetValues() []*InternalValue  { return a.InternalValue }

func (a *InternalMetric) SetObjectNamespace(objectNamespace string) {
	a.ObjectMeta.SetObjectNamespace(objectNamespace)
	a.SetNamespaced(objectNamespace != "")
}

func (a *InternalMetric) SetLabel(key, value string)    { a.Labels[key] = value }
func (a *InternalMetric) AppendMetric(i *InternalValue) { a.InternalValue = append(a.InternalValue, i) }

func (a *InternalMetric) Len() int { return len(a.InternalValue) }
func (a *InternalMetric) Less(i, j int) bool {
	return a.InternalValue[i].Timestamp < a.InternalValue[j].Timestamp
}
func (a *InternalMetric) Swap(i, j int) {
	a.InternalValue[i], a.InternalValue[j] = a.InternalValue[j], a.InternalValue[i]
}

func (a *InternalMetric) GenerateTags() []metrics.MetricTag {
	return []metrics.MetricTag{
		{Key: "metric_name", Val: a.Name},
		{Key: "object_name", Val: a.ObjectName},
	}
}

func Marshal(internalList []*InternalMetric) ([]byte, error) {
	return json.Marshal(internalList)
}

func Unmarshal(bytes []byte) ([]*InternalMetric, error) {
	var res []*InternalMetric
	err := json.Unmarshal(bytes, &res)
	return res, err
}

// PackInternalMetricList merges internal metric lists and sort them, if the same
// timestamp appears in different list (which should happen actually), we will
// randomly choose one item.
func PackInternalMetricList(internalLists ...[]*InternalMetric) []*InternalMetric {
	if len(internalLists) == 0 {
		return []*InternalMetric{}
	} else if len(internalLists) == 1 {
		return internalLists[0]
	}

	c := NewCachedMetric(metrics.DummyMetrics{})
	for _, internalList := range internalLists {
		c.Add(internalList...)
	}
	return c.ListAllMetric()
}

// PackMetricMetaList merges MetricMeta lists and removes duplicates
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
func GenerateMetaTags(m *MetricMeta, d *ObjectMeta) []metrics.MetricTag {
	return []metrics.MetricTag{
		{Key: "metric_name", Val: m.Name},
		{Key: "object_name", Val: d.ObjectName},
	}
}

// CachedMetric stores all metricItems in an organized way;
type CachedMetric struct {
	sync.RWMutex
	metricsEmitter metrics.MetricEmitter

	// namespaced is used as a normal storage format, while
	// metricMap flatten the metrics as a reversed index to make refer faster
	namespaced map[string]*namespacedMetric
	metricMap  map[MetricMeta]map[ObjectMeta]*InternalMetric
}

func NewCachedMetric(metricsEmitter metrics.MetricEmitter) *CachedMetric {
	return &CachedMetric{
		metricsEmitter: metricsEmitter,
		namespaced:     make(map[string]*namespacedMetric),
		metricMap:      make(map[MetricMeta]map[ObjectMeta]*InternalMetric),
	}
}

func (c *CachedMetric) Add(dList ...*InternalMetric) {
	now := time.Now()

	c.Lock()
	defer c.Unlock()

	for _, d := range dList {
		if d == nil || len(d.InternalValue) == 0 || d.Name == "" {
			continue
		}

		objectNamespace := d.ObjectNamespace
		if _, ok := c.namespaced[objectNamespace]; !ok {
			c.namespaced[objectNamespace] = newNamespacedMetric()
		}
		addedValues := c.namespaced[objectNamespace].add(d)
		if len(addedValues) == 0 {
			continue
		}

		if _, ok := c.metricMap[d.MetricMeta]; !ok {
			c.metricMap[d.MetricMeta] = make(map[ObjectMeta]*InternalMetric)
		}
		if _, ok := c.metricMap[d.MetricMeta][d.ObjectMeta]; !ok {
			c.metricMap[d.MetricMeta][d.ObjectMeta] = &InternalMetric{
				MetricMeta: d.MetricMeta,
				ObjectMeta: d.ObjectMeta,
			}
		}

		c.metricMap[d.MetricMeta][d.ObjectMeta].Labels = d.Labels
		c.metricMap[d.MetricMeta][d.ObjectMeta].InternalValue = append(c.metricMap[d.MetricMeta][d.ObjectMeta].InternalValue, addedValues...)

		if len(addedValues) > 0 {
			sort.Sort(c.metricMap[d.MetricMeta][d.ObjectMeta])

			// todo, support to emit metrics only when the functionality switched on
			index := c.metricMap[d.MetricMeta][d.ObjectMeta].Len() - 1
			costs := now.Sub(time.UnixMilli(c.metricMap[d.MetricMeta][d.ObjectMeta].InternalValue[index].Timestamp)).Microseconds()
			_ = c.metricsEmitter.StoreInt64(metricsNameKCMASStoreDataLatencySet, costs, metrics.MetricTypeNameRaw,
				GenerateMetaTags(&d.MetricMeta, &d.ObjectMeta)...)
		}
	}
}

// ListAllMetric returns all metric with a flattened slice
func (c *CachedMetric) ListAllMetric() []*InternalMetric {
	c.RLock()
	defer c.RUnlock()

	var res []*InternalMetric
	for _, internalMap := range c.metricMap {
		for _, internal := range internalMap {
			res = append(res, internal.DeepCopy())
		}
	}
	return res
}

// ListAllMetricMeta returns all metric meta with a flattened slice
func (c *CachedMetric) ListAllMetricMeta(withObject bool) []MetricMeta {
	c.RLock()
	defer c.RUnlock()

	var res []MetricMeta
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
		res = append(res, metricMeta.Name)
	}
	return res
}

// GetMetric returns the metric matched with the given key
func (c *CachedMetric) GetMetric(namespace, metricName string, gr *schema.GroupResource) ([]*InternalMetric, bool) {
	return c.GetMetricWithLimit(namespace, metricName, gr, -1)
}

func (c *CachedMetric) GetMetricWithLimit(namespace, metricName string, gr *schema.GroupResource, limit int) ([]*InternalMetric, bool) {
	now := time.Now()

	c.RLock()
	defer c.RUnlock()

	var res []*InternalMetric
	metricMeta := MetricMeta{
		Name:       metricName,
		Namespaced: namespace != "",
	}
	if gr != nil {
		metricMeta.SetObjectKind(gr.String())
	}

	if internalMap, ok := c.metricMap[metricMeta]; ok {
		for _, internal := range internalMap {
			if internal.ObjectNamespace != namespace {
				continue
			}

			res = append(res, internal.DeepCopyWithLimit(limit))

			if internal.Len() > 0 {
				costs := now.Sub(time.UnixMilli(internal.InternalValue[internal.Len()-1].Timestamp)).Microseconds()
				_ = c.metricsEmitter.StoreInt64(metricsNameKCMASStoreDataLatencyGet, costs, metrics.MetricTypeNameRaw, internal.GenerateTags()...)
			}
		}
		return res, true
	}

	return nil, false
}

// GetMetricInNamespace returns the metric matched with the given Namespace
func (c *CachedMetric) GetMetricInNamespace(namespace string) []*InternalMetric {
	return c.GetMetricInNamespaceWithLimit(namespace, -1)
}

func (c *CachedMetric) GetMetricInNamespaceWithLimit(namespace string, limit int) []*InternalMetric {
	now := time.Now()

	c.RLock()
	defer c.RUnlock()

	var res []*InternalMetric
	if _, ok := c.namespaced[namespace]; !ok {
		return res
	}

	for _, internal := range c.namespaced[namespace].listAllMetric() {
		internal.SetObjectNamespace(namespace)
		res = append(res, internal.DeepCopyWithLimit(limit))

		if internal.Len() > 0 {
			costs := now.Sub(time.UnixMilli(internal.InternalValue[internal.Len()-1].Timestamp)).Microseconds()
			_ = c.metricsEmitter.StoreInt64(metricsNameKCMASStoreDataLatencyGet, costs, metrics.MetricTypeNameRaw, internal.GenerateTags()...)
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

	for name, n := range c.namespaced {
		n.gc(expiredTimestamp)
		if n.empty() {
			delete(c.namespaced, name)
		}
	}

	for metricMeta, internalMap := range c.metricMap {
		for objectMeta, internal := range internalMap {
			var valueList []*InternalValue
			for _, value := range internal.InternalValue {
				if value.Timestamp > expiredTimestamp {
					valueList = append(valueList, value)
				}
			}

			internal.InternalValue = valueList
			if len(internal.InternalValue) == 0 {
				delete(internalMap, objectMeta)
			}
		}

		if len(internalMap) == 0 {
			delete(c.metricMap, metricMeta)
		}
	}
}

// namespacedMetric stores all metricItems in a certain Namespace in an organized way
type namespacedMetric struct {
	sync.RWMutex

	resourced map[string]*resourcedMetric
}

func newNamespacedMetric() *namespacedMetric {
	return &namespacedMetric{
		resourced: make(map[string]*resourcedMetric),
	}
}

func (n *namespacedMetric) listAllMetric() []*InternalMetric {
	n.RLock()
	defer n.RUnlock()

	var res []*InternalMetric
	for objectKind, resourced := range n.resourced {
		for _, internal := range resourced.listAllMetric() {
			internal.SetObjectKind(objectKind)
			res = append(res, internal)
		}
	}
	return res
}

func (n *namespacedMetric) add(d *InternalMetric) []*InternalValue {
	objectKind := d.ObjectKind

	n.Lock()
	defer n.Unlock()

	if _, ok := n.resourced[objectKind]; !ok {
		n.resourced[objectKind] = newResourcedMetric()
	}
	return n.resourced[objectKind].add(d)
}

func (n *namespacedMetric) gc(expiredTimestamp int64) {
	n.Lock()
	defer n.Unlock()

	for name, r := range n.resourced {
		r.gc(expiredTimestamp)
		if r.empty() {
			delete(n.resourced, name)
		}
	}
}

func (n *namespacedMetric) empty() bool {
	return len(n.resourced) == 0
}

// resourcedMetric stores all metricItems for a certain Object kind in an organized way
type resourcedMetric struct {
	sync.RWMutex

	objected map[string]*objectedMetric
}

func newResourcedMetric() *resourcedMetric {
	return &resourcedMetric{
		objected: make(map[string]*objectedMetric),
	}
}

func (r *resourcedMetric) listAllMetric() []*InternalMetric {
	r.RLock()
	defer r.RUnlock()

	var res []*InternalMetric
	for objectName, objected := range r.objected {
		for _, internal := range objected.listAllMetric() {
			internal.SetObjectName(objectName)
			res = append(res, internal)
		}
	}
	return res
}

func (r *resourcedMetric) add(d *InternalMetric) []*InternalValue {
	objName := d.ObjectName

	r.Lock()
	defer r.Unlock()

	if _, ok := r.objected[objName]; !ok {
		r.objected[objName] = newObjectedMetric()
	}
	return r.objected[objName].add(d)
}

func (r *resourcedMetric) gc(expiredTimestamp int64) {
	r.Lock()
	defer r.Unlock()

	for name, o := range r.objected {
		o.gc(expiredTimestamp)
		if o.empty() {
			delete(r.objected, name)
		}
	}
}

func (r *resourcedMetric) empty() bool {
	return len(r.objected) == 0
}

// objectedMetric stores all metricItems for a certain Object in an organized way
type objectedMetric struct {
	sync.RWMutex

	series map[string]*seriesMetric
}

func newObjectedMetric() *objectedMetric {
	return &objectedMetric{
		series: make(map[string]*seriesMetric),
	}
}

func (o *objectedMetric) listAllMetric() []*InternalMetric {
	o.RLock()
	defer o.RUnlock()

	var res []*InternalMetric
	for name, series := range o.series {
		internal := series.listAllMetric()
		internal.SetName(name)

		res = append(res, internal)
	}
	return res
}

func (o *objectedMetric) add(d *InternalMetric) []*InternalValue {
	// not support for empty metric Name
	if d.Name == "" {
		return []*InternalValue{}
	}

	o.Lock()
	defer o.Unlock()

	if _, ok := o.series[d.Name]; !ok {
		o.series[d.Name] = newSeriesMetric(d.Labels)
	}

	// always overwrite Labels if we have changed it
	o.series[d.Name].labels = d.Labels
	return o.series[d.Name].add(d.InternalValue)
}

func (o *objectedMetric) gc(expiredTimestamp int64) {
	o.Lock()
	defer o.Unlock()

	for name, s := range o.series {
		s.gc(expiredTimestamp)
		if s.empty() {
			delete(o.series, name)
		}
	}
}

func (o *objectedMetric) empty() bool {
	return len(o.series) == 0
}

// seriesMetric stores all metricItems for a certain Object in an organized way
type seriesMetric struct {
	sync.RWMutex

	// Timestamp will be used as a unique key to avoid
	// duplicated metric to be written more than once
	timestampSets map[int64]interface{}
	expiredTime   int64
	labels        map[string]string

	ordered []metric
}

func newSeriesMetric(labels map[string]string) *seriesMetric {
	return &seriesMetric{
		labels:        labels,
		timestampSets: make(map[int64]interface{}),
	}
}

func (s *seriesMetric) listAllMetric() *InternalMetric {
	s.RLock()
	defer s.RUnlock()

	res := &InternalMetric{
		Labels: s.labels,
	}
	for _, ordered := range s.ordered {
		res.InternalValue = append(res.InternalValue, &InternalValue{
			Value:     ordered.data,
			Timestamp: ordered.timestamp,
		})
	}
	return res
}

// add returns those valid InternalMetric slice
func (s *seriesMetric) add(valueList []*InternalValue) []*InternalValue {
	s.Lock()
	defer s.Unlock()

	var res []*InternalValue
	for _, v := range valueList {
		// Timestamp must be none-empty and valid
		if v.Timestamp == 0 || v.Timestamp < s.expiredTime {
			continue
		}
		// Timestamp must be unique
		if _, ok := s.timestampSets[v.Timestamp]; ok {
			continue
		}
		s.timestampSets[v.Timestamp] = struct{}{}

		// always make the Value list as ordered
		i := sort.Search(len(s.ordered), func(i int) bool {
			return v.Timestamp < s.ordered[i].timestamp
		})

		s.ordered = append(s.ordered, metric{})
		copy(s.ordered[i+1:], s.ordered[i:])
		s.ordered[i] = metric{
			data:      v.Value,
			timestamp: v.Timestamp,
		}

		res = append(res, v)
	}

	return res
}

func (s *seriesMetric) gc(expiredTimestamp int64) {
	s.Lock()
	defer s.Unlock()

	s.expiredTime = expiredTimestamp

	var ordered []metric
	for _, m := range s.ordered {
		if m.timestamp > expiredTimestamp {
			ordered = append(ordered, m)
		} else {
			delete(s.timestampSets, m.timestamp)
		}
	}
	s.ordered = ordered
}

func (s *seriesMetric) empty() bool {
	return len(s.ordered) == 0
}

type metric struct {
	data      float64
	timestamp int64
}
