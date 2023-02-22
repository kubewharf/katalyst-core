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

package katalyst_base

import (
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	fakedisco "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metaFake "k8s.io/client-go/metadata/fake"
	coretesting "k8s.io/client-go/testing"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func nilObjectFilter(object []runtime.Object) []runtime.Object {
	objects := make([]runtime.Object, 0)
	for _, o := range object {
		if o.DeepCopyObject() == nil {
			continue
		}
		objects = append(objects, o)
	}
	return objects
}

var fakeDiscoveryClient = &fakedisco.FakeDiscovery{Fake: &coretesting.Fake{
	Resources: []*metav1.APIResourceList{
		{
			GroupVersion: appsv1.SchemeGroupVersion.String(),
			APIResources: []metav1.APIResource{
				{Name: "deployments", Namespaced: true, Kind: "Deployment"},
				{Name: "replicasets", Namespaced: true, Kind: "Replica"},
				{Name: "statefulsets", Namespaced: true, Kind: "StatefulSet"},
			},
		},
		{
			GroupVersion: v1.SchemeGroupVersion.String(),
			APIResources: []metav1.APIResource{
				{Name: "pods", Namespaced: true, Kind: "Pod"},
			},
		},
		{
			GroupVersion: v1alpha1.SchemeGroupVersion.String(),
			APIResources: []metav1.APIResource{
				{Name: v1alpha1.ResourceNameEvictionConfigurations, Namespaced: true, Kind: "EvictionConfiguration"},
			},
		},
	},
}}

func GenerateFakeGenericContext(objects ...[]runtime.Object) (*GenericContext, error) {
	var kubeObjects, internalObjects, dynamicObjects, metaObjects []runtime.Object
	if len(objects) > 0 {
		kubeObjects = objects[0]
	}
	if len(objects) > 1 {
		internalObjects = objects[1]
	}
	if len(objects) > 2 {
		dynamicObjects = objects[2]
	}
	if len(objects) > 3 {
		metaObjects = objects[3]
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(metav1.AddMetaToScheme(scheme))
	utilruntime.Must(v1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(apis.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	clientSet := client.GenericClientSet{
		MetaClient:      metaFake.NewSimpleMetadataClient(scheme, nilObjectFilter(metaObjects)...),
		KubeClient:      fake.NewSimpleClientset(nilObjectFilter(kubeObjects)...),
		InternalClient:  externalfake.NewSimpleClientset(nilObjectFilter(internalObjects)...),
		DynamicClient:   dynamicfake.NewSimpleDynamicClient(scheme, nilObjectFilter(dynamicObjects)...),
		DiscoveryClient: fakeDiscoveryClient,
	}

	controlCtx, err := NewGenericContext(&clientSet, "", sets.NewString(), &generic.GenericConfiguration{}, "")
	return controlCtx, err
}
