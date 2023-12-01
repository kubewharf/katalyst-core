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
	"bytes"
	"hash/fnv"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/internal"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
)

type ObjectMetricStoreType string

const (
	ObjectMetricStoreTypeSimple ObjectMetricStoreType = "simple"
	ObjectMetricStoreTypeBucket ObjectMetricStoreType = "bucket"
)

type ObjectMetricStore interface {
	Add(objectMeta types.ObjectMetaImp) error
	ObjectExists(objectMeta types.ObjectMetaImp) (bool, error)
	GetInternalMetricImp(objectMeta types.ObjectMetaImp) (*internal.MetricImp, error)

	// Iterate is read only, please do not perform any write operation like add/delete to this object
	Iterate(f func(internalMetric *internal.MetricImp))
	Purge()
	Len() int
}

type simpleObjectMetricStore struct {
	metricMeta types.MetricMetaImp
	objectMap  map[types.ObjectMeta]*internal.MetricImp
	sync.RWMutex
}

func NewSimpleObjectMetricStore(metricMeta types.MetricMetaImp) ObjectMetricStore {
	return &simpleObjectMetricStore{
		metricMeta: metricMeta,
		objectMap:  make(map[types.ObjectMeta]*internal.MetricImp),
	}
}

func (s *simpleObjectMetricStore) Add(objectMeta types.ObjectMetaImp) error {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.objectMap[objectMeta]; !ok {
		s.objectMap[objectMeta] = internal.NewInternalMetric(s.metricMeta, objectMeta)
	}

	return nil
}

func (s *simpleObjectMetricStore) ObjectExists(objectMeta types.ObjectMetaImp) (bool, error) {
	s.RLock()
	defer s.RUnlock()

	_, exist := s.objectMap[objectMeta]
	return exist, nil
}

func (s *simpleObjectMetricStore) GetInternalMetricImp(objectMeta types.ObjectMetaImp) (*internal.MetricImp, error) {
	s.RLock()
	defer s.RUnlock()

	return s.objectMap[objectMeta], nil
}

func (s *simpleObjectMetricStore) Iterate(f func(internalMetric *internal.MetricImp)) {
	s.RLock()
	defer s.RUnlock()

	for _, InternalMetricImp := range s.objectMap {
		f(InternalMetricImp)
	}
}

func (s *simpleObjectMetricStore) Purge() {
	s.Lock()
	defer s.Unlock()

	for _, internalMetric := range s.objectMap {
		if internalMetric.Empty() {
			delete(s.objectMap, internalMetric.ObjectMetaImp)
		}
	}
}

func (s *simpleObjectMetricStore) Len() int {
	s.RLock()
	defer s.RUnlock()
	return len(s.objectMap)
}

type bucketObjectMetricStore struct {
	bucketSize int
	buckets    map[int]ObjectMetricStore
}

func NewBucketObjectMetricStore(bucketSize int, metricMeta types.MetricMetaImp) ObjectMetricStore {
	buckets := make(map[int]ObjectMetricStore, bucketSize)
	for i := 0; i < bucketSize; i++ {
		index := i
		buckets[index] = NewSimpleObjectMetricStore(metricMeta)
	}

	return &bucketObjectMetricStore{
		bucketSize: bucketSize,
		buckets:    buckets,
	}
}

func (b *bucketObjectMetricStore) getIndex(objectMeta types.ObjectMetaImp) (int, error) {
	buffer := bytes.Buffer{}
	buffer.WriteString(objectMeta.GetObjectNamespace())
	buffer.WriteString(":")
	buffer.WriteString(objectMeta.GetObjectName())
	namespacedName := make([]byte, buffer.Len())
	_, err := buffer.Read(namespacedName)
	if err != nil {
		return -1, err
	}

	h := fnv.New64()
	_, err = h.Write(namespacedName)
	if err != nil {
		return -1, err
	}
	return int(h.Sum64() % uint64(b.bucketSize)), nil
}

func (b *bucketObjectMetricStore) Add(objectMeta types.ObjectMetaImp) error {
	index, err := b.getIndex(objectMeta)
	if err != nil {
		return err
	}
	return b.buckets[index].Add(objectMeta)
}

func (b *bucketObjectMetricStore) ObjectExists(objectMeta types.ObjectMetaImp) (bool, error) {
	index, err := b.getIndex(objectMeta)
	if err != nil {
		return false, err
	}
	return b.buckets[index].ObjectExists(objectMeta)
}

func (b *bucketObjectMetricStore) GetInternalMetricImp(objectMeta types.ObjectMetaImp) (*internal.MetricImp, error) {
	index, err := b.getIndex(objectMeta)
	if err != nil {
		return nil, err
	}

	return b.buckets[index].GetInternalMetricImp(objectMeta)
}

func (b *bucketObjectMetricStore) Iterate(f func(internalMetric *internal.MetricImp)) {
	for i := range b.buckets {
		b.buckets[i].Iterate(f)
	}
}

func (b *bucketObjectMetricStore) Purge() {
	for i := range b.buckets {
		b.buckets[i].Purge()
	}
}

func (b *bucketObjectMetricStore) Len() int {
	var length int
	for i := range b.buckets {
		length += b.buckets[i].Len()
	}
	return length
}
