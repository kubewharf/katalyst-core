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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
)

func Test_bucketObjectMetricStore_getIndex(t *testing.T) {
	t.Parallel()

	store := NewBucketObjectMetricStore(100, types.MetricMetaImp{})

	bucketStore := store.(*bucketObjectMetricStore)
	objectMeta1 := types.ObjectMetaImp{ObjectNamespace: "ns-0", ObjectName: "pod-1"}
	objectMeta2 := types.ObjectMetaImp{ObjectNamespace: "ns-1", ObjectName: "pod-2"}
	indexForObject1, err := bucketStore.getIndex(objectMeta1)
	assert.NoError(t, err)
	indexForObject1SecondResult, err1 := bucketStore.getIndex(objectMeta1)
	assert.NoError(t, err1)
	assert.Equal(t, indexForObject1SecondResult, indexForObject1)
	indexForObject2, err2 := bucketStore.getIndex(objectMeta2)
	assert.NoError(t, err2)
	assert.NotEqual(t, indexForObject1, indexForObject2)
}
