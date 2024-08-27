/*
Copyright 2024 The Katalyst Authors.

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

package controller

import (
	"context"
	apis "github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	cliflag "k8s.io/component-base/cli/flag"
	"testing"
)

// todo: 240825 still only this file not complete
const BufferPercent = int32(10)

var makePod = func(name string, annotations, labels map[string]string, owners []metav1.OwnerReference) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       "default",
			Annotations:     annotations,
			Labels:          labels,
			OwnerReferences: owners,
		},
	}
	return pod
}

var transformParse = func(str string) *resource.Quantity {
	quantity := resource.MustParse(str)
	return &quantity
}

var bufferPercent = int32(10)

func TestResourceRecommendControllerSyncWork(t *testing.T) {
	t.Parallel()

	pod1 := makePod("pod1",
		map[string]string{},
		map[string]string{"workload": "sts1"},
		[]metav1.OwnerReference{
			{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
		})
	pod1.Spec.Containers = []v1.Container{
		{
			Name: "c1",
			Resources: v1.ResourceRequirements{
				Limits: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    resource.MustParse("4"),
					v1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    resource.MustParse("2"),
					v1.ResourceMemory: resource.MustParse("2Gi"),
				},
			},
		},
	}
	pod1.Status.QOSClass = v1.PodQOSBurstable

	recommendation1 := &apis.ResourceRecommend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "recommendation1",
			Namespace: "default",
		},
		Spec: apis.ResourceRecommendSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
				Name:       "sts1",
			},
			ResourcePolicy: apis.ResourcePolicy{
				AlgorithmPolicy: apis.AlgorithmPolicy{
					Algorithm:   apis.AlgorithmPercentile,
					Recommender: "default",
				},
				ContainerPolicies: []apis.ContainerResourcePolicy{
					{
						ContainerName: "c1",
						ControlledResourcesPolicies: []apis.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: &bufferPercent,
								MinAllowed:    transformParse("1"),
								MaxAllowed:    transformParse("3"),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: &bufferPercent,
								MinAllowed:    transformParse("500Mi"),
								MaxAllowed:    transformParse("2Gi"),
							},
						},
					},
				},
			},
		},
		// todo: 添加 Status 部分内容
	}

	recommendationNew1 := recommendation1.DeepCopy()
	recommendationNew1.OwnerReferences = nil
	for _, tc := range []struct {
		name              string
		object            runtime.Object
		recommendation    *apis.ResourceRecommend
		recommendationNew *apis.ResourceRecommend
	}{
		{
			name:              "delete owner reference",
			recommendation:    recommendation1,
			recommendationNew: recommendationNew1,
			object: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sts1",
					Namespace: "default",
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"workload": "sts1",
						},
					},
				},
			},
		},
		{},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.TODO()
			genericConf := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: []string{"deployment.v1.apps"},
			}

			fss := &cliflag.NamedFlagSets{}
			resourceRecommenderOptions := options.NewResourceRecommenderOptions()
			resourceRecommenderOptions.AddFlags(fss)
			resourceRecommenderConf := controller.NewResourceRecommenderConfig()
			_ = resourceRecommenderOptions.ApplyTo(resourceRecommenderConf)

			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil,
				[]runtime.Object{tc.object})
			assert.NoError(t, err)

			oc, err := NewPodOOMRecorderController(ctx, controlCtx, genericConf, controllerConf, resourceRecommenderConf)
			assert.NoError(t, err)

			rrc, err := NewResourceRecommendController(ctx, controlCtx, genericConf, controllerConf, resourceRecommenderConf, oc.Recorder)
			assert.NoError(t, err)

			_, err = controlCtx.Client.InternalClient.RecommendationV1alpha1().
				ResourceRecommends(tc.recommendation.Namespace).
				Create(ctx, tc.recommendation, metav1.CreateOptions{})
			assert.NoError(t, err)

			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(tc.recommendation)
			assert.NoError(t, err)

			controlCtx.StartInformer(rrc.ctx)
			synced := cache.WaitForCacheSync(rrc.ctx.Done(), rrc.syncedFunc...)
			assert.True(t, synced)

			err = rrc.syncRec(key)
			assert.NoError(t, err)
		})
	}
}
