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

	"sort"
	"sync"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type InternalValue struct {
	Value     int64 `json:"value,omitempty"`
	Timestamp int64 `json:"timestamp,omitempty"`
}

func NewInternalValue(value int64, timestamp int64) *InternalValue {
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

func (i *InternalValue) GetValue() int64     { return i.Value }
func (i *InternalValue) GetTimestamp() int64 { return i.Timestamp }

// InternalMetric is used as an internal version of metricItem
type InternalMetric struct {
	Namespace     string            `json:"namespace,omitempty"`
	Name          string            `json:"name,omitempty"`
	Object        string            `json:"object,omitempty"`
	ObjectName    string            `json:"objectName,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	InternalValue []*InternalValue  `json:"internalValue,omitempty"`
}

func NewInternalMetric(name string) *InternalMetric {
	return &InternalMetric{
		Name:   name,
		Labels: make(map[string]string),
	}
}

func (a *InternalMetric) String() string {
	return fmt.Sprintf("{Namespace: %v, Name: %v, Object: %v, ObjectName: %v}",
		a.Namespace, a.Name, a.Object, a.ObjectName)
}

func (a *InternalMetric) DeepCopy() *InternalMetric {
	b := &InternalMetric{
		Namespace:  a.Namespace,
		Name:       a.Name,
		Object:     a.Object,
		ObjectName: a.ObjectName,
		Labels:     general.DeepCopyMap(a.Labels),
	}

	for _, i := range a.InternalValue {
		b.InternalValue = append(b.InternalValue, i.DeepCopy())
	}
	return b
}

func (a *InternalMetric) DeepCopyWithLimit(limit int) *InternalMetric {
	b := a.DeepCopy()

	total := len(b.InternalValue)
	if limit > 0 && total > limit {
		b.InternalValue = b.InternalValue[total-limit:]
	}

	return b
}

func (a *InternalMetric) GetNamespace() string         { return a.Namespace }
func (a *InternalMetric) GetName() string              { return a.Name }
func (a *InternalMetric) GetObject() string            { return a.Object }
func (a *InternalMetric) GetObjectName() string        { return a.ObjectName }
func (a *InternalMetric) GetLabels() map[string]string { return a.Labels }
func (a *InternalMetric) GetValues() []*InternalValue  { return a.InternalValue }

func (a *InternalMetric) SetNamespace(namespace string)   { a.Namespace = namespace }
func (a *InternalMetric) SetObject(object string)         { a.Object = object }
func (a *InternalMetric) SetObjectName(objectName string) { a.ObjectName = objectName }
func (a *InternalMetric) SetLabel(key, value string)      { a.Labels[key] = value }
func (a *InternalMetric) AppendMetric(i *InternalValue)   { a.InternalValue = append(a.InternalValue, i) }

func Marshal(internalList []*InternalMetric) ([]byte, error) {
	return json.Marshal(internalList)
}

func Unmarshal(bytes []byte) ([]*InternalMetric, error) {
	var res []*InternalMetric
	err := json.Unmarshal(bytes, &res)
	return res, err
}

// CachedMetric stores all metricItems in an organized way;
type CachedMetric struct {
	sync.RWMutex

	// namespaced is used as a normal storage format, while
	// metricMap flatten the metrics as a reversed index to make refer faster
	namespaced map[string]*namespacedMetric
	metricMap  map[string]map[string]*InternalMetric
}

func NewCachedMetric() *CachedMetric {
	return &CachedMetric{
		namespaced: make(map[string]*namespacedMetric),
		metricMap:  make(map[string]map[string]*InternalMetric),
	}
}

func (c *CachedMetric) Add(dList ...*InternalMetric) {
	c.Lock()
	defer c.Unlock()

	for _, d := range dList {
		if d == nil || len(d.InternalValue) == 0 || d.Name == "" {
			continue
		}

		namespace := d.Namespace

		if _, ok := c.namespaced[namespace]; !ok {
			c.namespaced[namespace] = newNamespacedMetric()
		}
		addedValues := c.namespaced[namespace].add(d)
		if len(addedValues) == 0 {
			continue
		}

		dStr := d.String()
		if _, ok := c.metricMap[d.Name]; !ok {
			c.metricMap[d.Name] = make(map[string]*InternalMetric)
		}
		if _, ok := c.metricMap[d.Name][dStr]; !ok {
			c.metricMap[d.Name][dStr] = &InternalMetric{
				Namespace:  d.Namespace,
				Name:       d.Name,
				Object:     d.Object,
				ObjectName: d.ObjectName,
				Labels:     d.Labels,
			}
		}

		c.metricMap[d.Name][dStr].Labels = d.Labels
		c.metricMap[d.Name][dStr].InternalValue = append(c.metricMap[d.Name][dStr].InternalValue, addedValues...)
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

// ListAllMetricNames returns all metric with a flattened slice, but only contain names
func (c *CachedMetric) ListAllMetricNames() []string {
	c.RLock()
	defer c.RUnlock()

	var res []string
	for name, internalMap := range c.metricMap {
		if len(internalMap) == 0 {
			continue
		}
		res = append(res, name)
	}
	return res
}

// GetMetric returns the metric matched with the given key
func (c *CachedMetric) GetMetric(metricName string) ([]*InternalMetric, bool) {
	return c.GetMetricWithLimit(metricName, -1)
}

func (c *CachedMetric) GetMetricWithLimit(metricName string, limit int) ([]*InternalMetric, bool) {
	c.RLock()
	defer c.RUnlock()

	var res []*InternalMetric
	if internalMap, ok := c.metricMap[metricName]; ok {
		for _, internal := range internalMap {
			res = append(res, internal.DeepCopyWithLimit(limit))
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
	c.RLock()
	defer c.RUnlock()

	var res []*InternalMetric
	if _, ok := c.namespaced[namespace]; !ok {
		return res
	}

	for _, internal := range c.namespaced[namespace].listAllMetric() {
		internal.Namespace = namespace
		res = append(res, internal.DeepCopyWithLimit(limit))
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

	for name, internalMap := range c.metricMap {
		for dStr, internal := range internalMap {
			var valueList []*InternalValue
			for _, value := range internal.InternalValue {
				if value.Timestamp > expiredTimestamp {
					valueList = append(valueList, value)
				}
			}

			internal.InternalValue = valueList
			if len(internal.InternalValue) == 0 {
				delete(internalMap, dStr)
			}
		}

		if len(internalMap) == 0 {
			delete(c.metricMap, name)
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
	for object, resourced := range n.resourced {
		for _, internal := range resourced.listAllMetric() {
			internal.Object = object
			res = append(res, internal)
		}
	}
	return res
}

func (n *namespacedMetric) add(d *InternalMetric) []*InternalValue {
	object := d.Object

	n.Lock()
	defer n.Unlock()

	if _, ok := n.resourced[object]; !ok {
		n.resourced[object] = newResourcedMetric()
	}
	return n.resourced[object].add(d)
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
			internal.ObjectName = objectName
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
		internal.Name = name

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
	data      int64
	timestamp int64
}
