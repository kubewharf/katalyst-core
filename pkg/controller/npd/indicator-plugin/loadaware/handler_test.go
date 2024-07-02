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

package loadaware

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestOnNodeAdd(t *testing.T) {
	t.Parallel()

	p := &Plugin{
		workers:         3,
		nodePoolMap:     map[int32]sets.String{},
		nodeStatDataMap: map[string]*NodeMetricData{},
	}

	testNode1 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "testNode1",
		},
		Status: v1.NodeStatus{
			Allocatable: map[v1.ResourceName]resource.Quantity{
				v1.ResourceCPU:    resource.MustParse("16"),
				v1.ResourceMemory: resource.MustParse("32Gi"),
			},
		},
	}

	p.OnNodeAdd(testNode1)
	assert.NotNil(t, p.nodeStatDataMap["testNode1"])
	assert.Equal(t, 2, len(p.nodeStatDataMap["testNode1"].TotalRes))

	p.OnNodeDelete(testNode1)
	assert.Nil(t, p.nodeStatDataMap["testNode1"])
}

func TestOnNodeUpdate(t *testing.T) {
	t.Parallel()

	p := &Plugin{
		nodeStatDataMap: map[string]*NodeMetricData{},
	}

	testNode1 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "testNode1",
		},
		Status: v1.NodeStatus{
			Allocatable: map[v1.ResourceName]resource.Quantity{
				v1.ResourceCPU:    resource.MustParse("16"),
				v1.ResourceMemory: resource.MustParse("32Gi"),
			},
		},
	}

	p.OnNodeUpdate(nil, testNode1)
	assert.NotNil(t, p.nodeStatDataMap["testNode1"])
	assert.Equal(t, 2, len(p.nodeStatDataMap["testNode1"].TotalRes))
}

func TestOnPodAdd(t *testing.T) {
	t.Parallel()

	p := &Plugin{
		nodeToPodsMap:             map[string]map[string]struct{}{},
		podUsageSelectorKey:       "app",
		podUsageSelectorVal:       "testPod",
		podUsageSelectorNamespace: "katalyst-system",
	}

	testPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "testPod1",
			Namespace: "katalyst-system",
			Labels: map[string]string{
				"app": "testPod",
			},
		},
		Spec: v1.PodSpec{
			NodeName: "testNode1",
		},
	}

	p.OnPodAdd(testPod1)
	assert.NotNil(t, p.nodeToPodsMap["testNode1"])
	assert.Equal(t, 1, len(p.nodeToPodsMap["testNode1"]))

	p.OnPodDelete(testPod1)
	assert.Equal(t, 0, len(p.nodeToPodsMap["testNode1"]))

	p.OnPodDelete("")
}

func TestOnPodUpdate(t *testing.T) {
	t.Parallel()

	p := &Plugin{
		nodeToPodsMap:             map[string]map[string]struct{}{},
		podUsageSelectorKey:       "app",
		podUsageSelectorVal:       "testPod",
		podUsageSelectorNamespace: "katalyst-system",
	}

	testPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "testPod1",
			Namespace: "katalyst-system",
			Labels: map[string]string{
				"app": "testPod",
			},
		},
		Spec: v1.PodSpec{
			NodeName: "testNode1",
		},
	}

	p.OnPodUpdate(nil, testPod1)
	assert.NotNil(t, p.nodeToPodsMap["testNode1"])
	assert.Equal(t, 1, len(p.nodeToPodsMap["testNode1"]))

	p.OnPodUpdate(nil, "")
}
