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
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	apiappsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
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

func CreateMockDeployment(matchLabels map[string]string, containers []v1.Container, name, namespace, apiVersion, kind string, client appsv1.AppsV1Interface) error {
	deployment := &apiappsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: apiappsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: matchLabels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: matchLabels,
				},
				Spec: v1.PodSpec{
					Containers: containers,
				},
			},
		},
	}

	groupVersion, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return fmt.Errorf("failed to parse apiVersion: %v", err)
	}
	deployment.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   groupVersion.Group,
		Version: groupVersion.Version,
		Kind:    kind,
	})

	_, err = client.Deployments(namespace).Create(context.TODO(), deployment, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create deployment: %v", err)
	}
	return nil
}
