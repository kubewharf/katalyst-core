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

package qosawarenoderesources

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeschedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var makeFit = func(scoringStrategyType kubeschedulerconfig.ScoringStrategyType) (framework.Plugin, error) {
	return NewFit(&config.QoSAwareNodeResourcesFitArgs{
		ScoringStrategy: &config.ScoringStrategy{
			Type: scoringStrategyType,
			Resources: []kubeschedulerconfig.ResourceSpec{
				{
					Name:   fmt.Sprintf("%s", v1.ResourceCPU),
					Weight: 30,
				},
				{
					Name:   fmt.Sprintf("%s", v1.ResourceMemory),
					Weight: 70,
				},
			},
			ReclaimedResources: []kubeschedulerconfig.ResourceSpec{
				{
					Name:   fmt.Sprintf("%s", consts.ReclaimedResourceMilliCPU),
					Weight: 30,
				},
				{
					Name:   fmt.Sprintf("%s", consts.ReclaimedResourceMemory),
					Weight: 70,
				},
			},
			RequestedToCapacityRatio: &kubeschedulerconfig.RequestedToCapacityRatioParam{
				Shape: []kubeschedulerconfig.UtilizationShapePoint{
					{
						Utilization: 10,
						Score:       3,
					},
					{
						Utilization: 20,
						Score:       4,
					},
					{
						Utilization: 50,
						Score:       7,
					},
				},
			},
			ReclaimedRequestedToCapacityRatio: &kubeschedulerconfig.RequestedToCapacityRatioParam{
				Shape: []kubeschedulerconfig.UtilizationShapePoint{
					{
						Utilization: 10,
						Score:       3,
					},
					{
						Utilization: 20,
						Score:       4,
					},
					{
						Utilization: 50,
						Score:       7,
					},
				},
			},
		},
	}, nil)
}

var makeFitPod = func(uid types.UID, name string, res v1.ResourceList, node string) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "n1",
			Name:      name,
			UID:       uid,
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
			},
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

var makeFitNode = func(name string, pods []*v1.Pod, res v1.ResourceList) *framework.NodeInfo {
	ni := framework.NewNodeInfo(pods...)
	ni.SetNode(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: v1.NodeStatus{
			Allocatable: res,
		},
	})
	return ni
}

var makeFitCNR = func(name string, res v1.ResourceList) *apis.CustomNodeResource {
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

func Test_Fit(t *testing.T) {
	t.Parallel()
	util.SetQoSConfig(generic.NewQoSConfiguration())

	f, err := makeFit(kubeschedulerconfig.LeastAllocated)
	assert.Nil(t, err)
	fit := f.(*Fit)

	state := framework.NewCycleState()
	p1 := makeFitPod("p1", "p1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(2000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(2*1024*0o124*1024, resource.DecimalSI),
	}, "c1")

	fit.PreFilter(context.Background(), state, p1)
	data, err := state.Read(preFilterStateKey)
	assert.Nil(t, err)
	assert.Equal(t, data, &preFilterState{
		QoSResource: native.QoSResource{
			ReclaimedMilliCPU: int64(2000),
			ReclaimedMemory:   int64(2 * 1024 * 0o124 * 1024),
		},
	})

	assert.Nil(t, fit.PreFilterExtensions())

	e := []framework.ClusterEvent{
		{Resource: framework.Pod, ActionType: framework.Delete},
		{Resource: framework.Node, ActionType: framework.Add},
	}
	assert.Equal(t, fit.EventsToRegister(), e)
	assert.Nil(t, fit.ScoreExtensions())

	fit.Reserve(context.Background(), nil, p1, "c1")
	node, err := cache.GetCache().GetNodeInfo("c1")
	assert.Nil(t, err)
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMilliCPU, int64(0))
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMemory, int64(0))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMilliCPU, int64(2000))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMemory, int64(2*1024*0o124*1024))

	fit.Unreserve(context.Background(), nil, p1, "c1")
	node, err = cache.GetCache().GetNodeInfo("c1")
	assert.Nil(t, err)
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMilliCPU, int64(0))
	assert.Equal(t, node.QoSResourcesAllocatable.ReclaimedMemory, int64(0))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMilliCPU, int64(0))
	assert.Equal(t, node.QoSResourcesRequested.ReclaimedMemory, int64(0))
}

func Test_Allocated(t *testing.T) {
	t.Parallel()
	util.SetQoSConfig(generic.NewQoSConfiguration())

	f, _ := makeFit(kubeschedulerconfig.LeastAllocated)
	fit := f.(*Fit)

	state := framework.NewCycleState()
	p1 := makeFitPod("p1", "p1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(2000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(2*1024*0o124*1024, resource.DecimalSI),
	}, "Test_Allocated_n1")
	p2 := makeFitPod("p2", "p2", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(3000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(3*1024*0o124*1024, resource.DecimalSI),
	}, "Test_Allocated_n1")
	n1 := makeFitNode("Test_Allocated_n1", []*v1.Pod{p1, p2}, map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(9000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(12*1024*0o124*1024, resource.DecimalSI),
	})

	p3 := makeFitPod("p3", "p3", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(3000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(3*1024*0o124*1024, resource.DecimalSI),
	}, "")
	fit.PreFilter(context.Background(), state, p3)

	status := fit.Filter(context.Background(), state, p3, n1)
	t.Logf("%v", status.Reasons())
	assert.Equal(t, status.IsSuccess(), false)

	c1 := makeFitCNR("Test_Allocated_n1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(9000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(12*1024*0o124*1024, resource.DecimalSI),
	})
	cache.GetCache().AddOrUpdateCNR(c1)
	status = fit.Filter(context.Background(), state, p3, n1)
	assert.Equal(t, status.IsSuccess(), true)

	p4 := makeFitPod("p4", "p4", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(3000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(30*1024*0o124*1024, resource.DecimalSI),
	}, "")
	fit.PreFilter(context.Background(), state, p4)

	status = fit.Filter(context.Background(), state, p4, n1)
	t.Logf("%v", status)
	assert.Equal(t, status.IsSuccess(), false)
}

func Test_FitScore(t *testing.T) {
	t.Parallel()
	util.SetQoSConfig(generic.NewQoSConfiguration())

	state := framework.NewCycleState()
	p1 := makeFitPod("p1", "p1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(2000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(2*1024*0o124*1024, resource.DecimalSI),
	}, "Test_FitScore_n1")
	_ = cache.GetCache().AddPod(p1)

	p2 := makeFitPod("p2", "p2", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(3000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(3*1024*0o124*1024, resource.DecimalSI),
	}, "Test_FitScore_n1")
	_ = cache.GetCache().AddPod(p2)

	c1 := makeFitCNR("Test_FitScore_n1", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(9000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(12*1024*0o124*1024, resource.DecimalSI),
	})
	cache.GetCache().AddOrUpdateCNR(c1)

	p3 := makeFitPod("p3", "p3", map[v1.ResourceName]resource.Quantity{
		consts.ReclaimedResourceMilliCPU: *resource.NewQuantity(3000, resource.DecimalSI),
		consts.ReclaimedResourceMemory:   *resource.NewQuantity(3*1024*0o124*1024, resource.DecimalSI),
	}, "")

	leastF, _ := makeFit(kubeschedulerconfig.LeastAllocated)
	leastFit := leastF.(*Fit)
	score, _ := leastFit.Score(context.Background(), state, p3, "Test_FitScore_n1")
	assert.Equal(t, score, int64(26))

	mostF, _ := makeFit(kubeschedulerconfig.MostAllocated)
	mostFit := mostF.(*Fit)
	score, _ = mostFit.Score(context.Background(), state, p3, "Test_FitScore_n1")
	assert.Equal(t, score, int64(72))

	ratioF, _ := makeFit(kubeschedulerconfig.RequestedToCapacityRatio)
	ratioFit := ratioF.(*Fit)
	score, _ = ratioFit.Score(context.Background(), state, p3, "Test_FitScore_n1")
	assert.Equal(t, score, int64(70))
}
