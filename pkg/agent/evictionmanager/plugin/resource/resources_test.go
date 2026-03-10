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

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func TestNewResourcesEvictionPlugin(t *testing.T) {
	t.Parallel()

	testNodeName := "test-node"
	testConf := generateTestConfiguration(t, testNodeName)
	pods := []*corev1.Pod{
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "test-pod-1",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationSoftEvictNotificationKey: "true",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "test-container-1",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("100"),
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
					consts.PodAnnotationQoSLevelKey:              consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationSoftEvictNotificationKey: "true",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "test-container-2",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								consts.ReclaimedResourceMilliCPU: resource.MustParse("50"),
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "test-pod-2",
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
						consts.ReclaimedResourceMilliCPU: resource.MustParse("90"),
					},
				},
			},
		},
	}, nil)
	assert.NoError(t, err)

	testMetaServer := generateTestMetaServer(ctx.Client, testConf, pods)

	reclaimedResourcesGetter := func(ctx context.Context) (corev1.ResourceList, error) {
		cnr, err := testMetaServer.GetCNR(ctx)
		if err != nil {
			return nil, err
		}

		allocatable := make(corev1.ResourceList)
		if cnr != nil && cnr.Status.Resources.Allocatable != nil {
			allocatable = *cnr.Status.Resources.Allocatable
		}
		return allocatable, nil
	}

	reclaimedThresholdGetter := func(resourceName corev1.ResourceName) *float64 {
		if threshold, ok := testConf.GetDynamicConfiguration().EvictionThreshold[resourceName]; !ok {
			return nil
		} else {
			return &threshold
		}
	}

	reclaimedSoftThresholdGetter := func(resourceName corev1.ResourceName) *float64 {
		if threshold, ok := testConf.GetDynamicConfiguration().SoftEvictionThreshold[resourceName]; !ok {
			return nil
		} else {
			return &threshold
		}
	}

	scoreGetter := func(pod *corev1.Pod) *float64 {
		score := 100.0
		return &score
	}

	deletionGracePeriodGetter := func() int64 {
		return testConf.GetDynamicConfiguration().ReclaimedResourcesEvictionConfiguration.DeletionGracePeriod
	}
	thresholdMetToleranceDurationGetter := func() int64 {
		return testConf.GetDynamicConfiguration().ThresholdMetToleranceDuration
	}

	p := NewResourcesEvictionPlugin(
		"test",
		testMetaServer,
		metrics.DummyMetrics{},
		reclaimedResourcesGetter,
		native.SumUpPodRequestResources,
		reclaimedThresholdGetter,
		reclaimedSoftThresholdGetter,
		scoreGetter,
		deletionGracePeriodGetter,
		thresholdMetToleranceDurationGetter,
		testConf.SkipZeroQuantityResourceNames,
		testConf.CheckReclaimedQoSForPod,
	)

	resp, err := p.ThresholdMet(context.Background(), nil)
	assert.NoError(t, err)

	_, err = p.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*corev1.Pod{},
		TopN:          0,
		EvictionScope: resp.EvictionScope,
	})
	assert.NoError(t, err)

	_, err = p.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    pods,
		TopN:          0,
		EvictionScope: resp.EvictionScope,
	})
	assert.NoError(t, err)

	_, err = p.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    pods,
		TopN:          1,
		EvictionScope: resp.EvictionScope,
	})
	assert.NoError(t, err)
}
