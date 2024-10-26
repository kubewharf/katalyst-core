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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	memory "k8s.io/client-go/discovery/cached"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/restmapper"
	clientTesting "k8s.io/client-go/testing"
)

func CreateMockPod(labels, annotations map[string]string, name, namespace, nodeName string, containers []v1.Container, client corev1.CoreV1Interface) error {
	pod := &v1.Pod{
		Spec: v1.PodSpec{
			NodeName:   nodeName,
			Containers: containers,
		},
	}
	pod.SetLabels(labels)
	pod.SetAnnotations(annotations)
	pod.SetName(name)
	pod.SetNamespace(namespace)

	_, err := client.Pods(namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
	return err
}

func CreateMockUnstructured(matchLabels, unstructuredTemplateSpec map[string]interface{}, name, namespace, apiVersion, kind string, client dynamic.Interface) error {
	collectorObject := &unstructured.Unstructured{}
	collectorObject.SetName(name)
	collectorObject.SetNamespace(namespace)
	collectorObject.SetKind(kind)
	collectorObject.SetAPIVersion(apiVersion)
	unstructured.SetNestedMap(collectorObject.Object, matchLabels, "spec", "selector", "matchLabels")
	unstructured.SetNestedMap(collectorObject.Object, unstructuredTemplateSpec, "spec", "template", "spec")

	gvk := schema.FromAPIVersionAndKind(apiVersion, kind)
	gvr, _ := meta.UnsafeGuessKindToResource(gvk)

	_, err := client.Resource(gvr).Namespace(namespace).Create(context.TODO(), collectorObject, metav1.CreateOptions{})
	return err
}

func CreateMockRESTMapper() *restmapper.DeferredDiscoveryRESTMapper {
	fakeDiscoveryClient := &discoveryfake.FakeDiscovery{Fake: &clientTesting.Fake{}}
	fakeDiscoveryClient.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "pods", SingularName: "pod", Namespaced: true, Kind: "Pod"},
				{Name: "deployments", SingularName: "deployment", Namespaced: true, Kind: "Deployment"},
				{Name: "kinds", SingularName: "kind", Namespaced: true, Kind: "Kind"},
			},
		},
	}
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(fakeDiscoveryClient))
	return restMapper
}
