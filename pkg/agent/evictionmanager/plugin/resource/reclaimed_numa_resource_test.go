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

package resource

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestNewReclaimedNumaResourcesEvictionPlugin(t *testing.T) {
	t.Parallel()

	testNodeName := "test-node"
	testConf := generateTestConfiguration(t, testNodeName)
	pods := []*corev1.Pod{
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "test-pod-1",
				Annotations: map[string]string{
					consts.PodAnnotationNUMABindResultKey: "0",
					consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "test-container-1",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("5000"),
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "test-pod-2",
				Annotations: map[string]string{
					consts.PodAnnotationNUMABindResultKey: "1",
					consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "test-container-2",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("5100"),
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "test-pod-3",
			},
		},
	}
	ctx, err := katalyst_base.GenerateFakeGenericContext(nil, []runtime.Object{
		&v1alpha1.CustomNodeResource{
			ObjectMeta: v1.ObjectMeta{
				Name: testNodeName,
			},
			Status: v1alpha1.CustomNodeResourceStatus{
				Resources: v1alpha1.Resources{
					Allocatable: &corev1.ResourceList{
						consts.ReclaimedResourceMilliCPU: resource.MustParse("1000"),
					},
				},
				TopologyZone: []*v1alpha1.TopologyZone{
					{
						Name: "0",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "0",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Allocatable: &corev1.ResourceList{
										consts.ReclaimedResourceMilliCPU: resource.MustParse("1000"),
										consts.ReclaimedResourceMemory:   resource.MustParse("1000"),
									},
								},
							},
						},
					},
					{
						Name: "1",
						Type: v1alpha1.TopologyTypeSocket,
						Children: []*v1alpha1.TopologyZone{
							{
								Name: "1",
								Type: v1alpha1.TopologyTypeNuma,
								Resources: v1alpha1.Resources{
									Allocatable: &corev1.ResourceList{
										consts.ReclaimedResourceMilliCPU: resource.MustParse("1000"),
										consts.ReclaimedResourceMemory:   resource.MustParse("1000"),
									},
								},
							},
						},
					},
				},
			},
		},
	}, nil)
	assert.NoError(t, err)

	testMetaServer := generateTestMetaServer(ctx.Client, testConf, pods)

	plugin := NewReclaimedNumaResourcesEvictionPlugin(ctx.Client, &events.FakeRecorder{}, testMetaServer,
		metrics.DummyMetrics{}, testConf)
	assert.NoError(t, err)

	met, err := plugin.ThresholdMet(context.TODO())
	assert.NoError(t, err)
	assert.NotNil(t, met)

	evictionPods, err := plugin.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    pods,
		TopN:          1,
		EvictionScope: met.EvictionScope,
	})
	assert.NoError(t, err)
	assert.NotNil(t, evictionPods)
	assert.NotEqual(t, 0, len(evictionPods.GetTargetPods()))

	evictPods, err := plugin.GetEvictPods(context.TODO(), &pluginapi.GetEvictPodsRequest{
		ActivePods: pods,
	})
	assert.NoError(t, err)
	assert.NotNil(t, evictPods)
}
