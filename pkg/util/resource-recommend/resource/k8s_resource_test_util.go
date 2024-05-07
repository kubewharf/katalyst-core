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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func CreateMockPod(labels, annotations map[string]string, name, namespace, nodeName string, containers []v1.Container, client client.Client) {
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

	client.Create(context.TODO(), pod)
}

func CreateMockUnstructured(matchLabels, unstructuredTemplateSpec map[string]interface{}, name, namespace, apiVersion, kind string, client client.Client) {
	collectorObject := &unstructured.Unstructured{}
	collectorObject.SetName(name)
	collectorObject.SetNamespace(namespace)
	collectorObject.SetKind(kind)
	collectorObject.SetAPIVersion(apiVersion)
	unstructured.SetNestedMap(collectorObject.Object, matchLabels, "spec", "selector", "matchLabels")
	unstructured.SetNestedMap(collectorObject.Object, unstructuredTemplateSpec, "spec", "template", "spec")
	client.Create(context.TODO(), collectorObject)
}
