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

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic/dynamicinformer"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func TestGetSPDForPod(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(v1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)
	dynamicFactory := dynamicinformer.NewDynamicSharedInformerFactory(fakeDynamicClient, 0)
	dpInformer := dynamicFactory.ForResource(schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
	})
	rsInformer := dynamicFactory.ForResource(schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "replicasets",
	})

	workloadInformers := map[schema.GroupVersionKind]cache.GenericLister{
		{Group: "apps", Version: "v1", Kind: "Deployment"}: dpInformer.Lister(),
		{Group: "apps", Version: "v1", Kind: "ReplicaSet"}: rsInformer.Lister(),
	}

	dp := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dp1",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.WorkloadAnnotationSPDEnableKey: apiconsts.WorkloadAnnotationSPDEnabled,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": "dp1",
				},
			},
		},
	}

	u, err := native.ToUnstructured(dp)
	assert.NoError(t, err)
	_ = dpInformer.Informer().GetStore().Add(u)

	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rs1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "dp1",
				},
			},
		},
	}

	u, err = native.ToUnstructured(rs)
	assert.NoError(t, err)
	_ = rsInformer.Informer().GetStore().Add(u)

	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "rs1",
				},
			},
			Labels: map[string]string{
				"name": "dp1",
			},
		},
	}

	internalClient := externalfake.NewSimpleClientset()
	internalFactory := externalversions.NewSharedInformerFactory(internalClient, 0)
	spdInformer := internalFactory.Workload().V1alpha1().ServiceProfileDescriptors()
	_ = spdInformer.Informer().GetIndexer().AddIndexers(cache.Indexers{
		consts.TargetReferenceIndex: SPDTargetReferenceIndex,
	})
	spd := &apiworkload.ServiceProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{Name: "spa1", Namespace: "default"},
		Spec: apiworkload.ServiceProfileDescriptorSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
				Name:       "dp1",
			},
		},
	}
	_ = spdInformer.Informer().GetStore().Add(spd)

	s, err := GetSPDForPod(pod1, spdInformer.Informer().GetIndexer(), workloadInformers, spdInformer.Lister())
	assert.NoError(t, err)
	assert.Equal(t, spd, s)

	pod1.Annotations = map[string]string{
		apiconsts.PodAnnotationSPDNameKey: spd.Name,
	}

	s, err = GetSPDForPod(pod1, spdInformer.Informer().GetIndexer(), workloadInformers, spdInformer.Lister())
	assert.NoError(t, err)
	assert.Equal(t, spd, s)

	pod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "rs2",
				},
			},
		},
	}

	s, err = GetSPDForPod(pod2, spdInformer.Informer().GetIndexer(), workloadInformers, spdInformer.Lister())
	assert.Nil(t, s)
	assert.Error(t, err)
}
