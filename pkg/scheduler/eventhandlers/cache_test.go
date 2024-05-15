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

package eventhandlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	schedulercache "github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
)

var makeCachedPod = func(uid types.UID, name string, res v1.ResourceList, node string) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "n1",
			Name:      name,
			UID:       uid,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits:   res,
						Requests: res,
					},
				},
			},
			NodeName: node,
		},
	}
	return pod
}

var makeCachedCNR = func(name string, res v1.ResourceList) *apis.CustomNodeResource {
	cnr := &apis.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: apis.CustomNodeResourceStatus{
			Resources: apis.Resources{
				Allocatable: &res,
			},
		},
	}
	return cnr
}

func Test_CalculateQoSResource(t *testing.T) {
	t.Parallel()

	cache := schedulercache.GetCache()

	_, err := cache.GetNodeInfo("c1")
	assert.NotNil(t, err)

	t.Log("####1: add pod with not-exist cnr")
	p1 := makeCachedPod("p1", "p1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(2000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(2*1024*0o124*1024, resource.DecimalSI),
	}, "c1")
	addPodToCache(p1)

	node, err := cache.GetNodeInfo("c1")
	assert.Nil(t, err)
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMilliCPU, int64(0))
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMemory, int64(0))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMilliCPU, int64(2000))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMemory, int64(2*1024*0o124*1024))

	t.Log("### 2: update pod")
	p1 = makeCachedPod("p1", "p1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(3000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(3*1024*0o124*1024, resource.DecimalSI),
	}, "c1")
	updatePodInCache(p1, p1)

	node, err = cache.GetNodeInfo("c1")
	assert.Nil(t, err)
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMilliCPU, int64(0))
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMemory, int64(0))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMilliCPU, int64(3000))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMemory, int64(3*1024*0o124*1024))

	t.Log("### 3: add new pod")
	p2 := makeCachedPod("p2", "p2", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(2000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(2*1024*0o124*1024, resource.DecimalSI),
	}, "c1")
	addPodToCache(p2)

	node, err = cache.GetNodeInfo("c1")
	assert.Nil(t, err)
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMilliCPU, int64(0))
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMemory, int64(0))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMilliCPU, int64(5000))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMemory, int64(5*1024*0o124*1024))

	t.Log("### 4: add cnr")
	c1 := makeCachedCNR("c1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(8000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(8*1024*0o124*1024, resource.DecimalSI),
	})
	addCNRToCache(c1)

	node, err = cache.GetNodeInfo("c1")
	assert.Nil(t, err)
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMilliCPU, int64(8000))
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMemory, int64(8*1024*0o124*1024))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMilliCPU, int64(5000))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMemory, int64(5*1024*0o124*1024))

	t.Log("### 5: remove pod")
	deletePodFromCache(p1)

	node, err = cache.GetNodeInfo("c1")
	assert.Nil(t, err)
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMilliCPU, int64(8000))
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMemory, int64(8*1024*0o124*1024))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMilliCPU, int64(2000))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMemory, int64(2*1024*0o124*1024))

	t.Log("### 6: update cnr")
	c1 = makeCachedCNR("c1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(9000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(9*1024*0o124*1024, resource.DecimalSI),
	})
	updateNodeInCache(c1, c1)

	node, err = cache.GetNodeInfo("c1")
	assert.Nil(t, err)
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMilliCPU, int64(9000))
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMemory, int64(9*1024*0o124*1024))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMilliCPU, int64(2000))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMemory, int64(2*1024*0o124*1024))

	t.Log("### 7: delete cnr")
	deleteCNRFromCache(c1)

	_, err = cache.GetNodeInfo("c1")
	assert.NotNil(t, err)
}
