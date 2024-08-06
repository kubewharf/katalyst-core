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
	"encoding/json"
	"fmt"
	v1 "k8s.io/api/apps/v1"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"

	"k8s.io/client-go/kubernetes"
)

func DevConvertAndGetResource(ctx context.Context, client kubernetes.Interface, namespace string, targetRef v1alpha1.CrossVersionObjectReference) (*v1.Deployment, error) {
	klog.V(5).Infof("get resource in targetRef: %v, namespace: %v", targetRef, namespace)
	deployment, err := client.AppsV1().Deployments(namespace).Get(ctx, targetRef.Name, metav1.GetOptions{
		TypeMeta: metav1.TypeMeta{
			Kind:       targetRef.Kind,
			APIVersion: targetRef.APIVersion,
		},
	})
	if err != nil {
		return nil, err
	}
	return deployment, nil
}

func DevGetAllClaimedContainers(deployment *v1.Deployment) ([]string, error) {
	if deployment == nil {
		return nil, fmt.Errorf("get containers failed, pod is nil object")
	}
	containerName := make([]string, 0, len(deployment.Spec.Template.Spec.Containers))
	for _, v := range deployment.Spec.Template.Spec.Containers {
		containerName = append(containerName, v.Name)
	}
	return containerName, nil
}

func ConvertAndGetResource(ctx context.Context, client k8sclient.Client, namespace string, targetRef v1alpha1.CrossVersionObjectReference) (*unstructured.Unstructured, error) {
	klog.V(5).Infof("Get resource in", "targetRef", targetRef, "namespace", namespace)
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(targetRef.APIVersion)
	obj.SetKind(targetRef.Kind)
	if err := client.Get(ctx, k8stypes.NamespacedName{Namespace: namespace, Name: targetRef.Name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func GetAllClaimedContainers(controller *unstructured.Unstructured) ([]string, error) {
	klog.V(5).InfoS("Get all controller claimed containers", "controller", controller)
	templateSpec, found, err := unstructured.NestedMap(controller.Object, "spec", "template", "spec")
	if err != nil {
		return nil, errors.Wrapf(err, "unstructured.NestedMap err")
	}
	if !found {
		return nil, errors.Errorf("spec.template.spec not found in the controller")
	}
	containersList, found, err := unstructured.NestedSlice(templateSpec, "containers")
	if err != nil {
		return nil, errors.Wrapf(err, "unstructured.NestedSlice err")
	}
	if !found {
		return nil, errors.Errorf("failure to find containers in the controller")
	}

	containerNames := make([]string, 0, len(containersList))
	for _, container := range containersList {
		containerMap, ok := container.(map[string]interface{})
		if !ok {
			klog.Errorf("Unable to convert container:%v to map[string]interface{}", container)
			continue
		}
		name, found, err := unstructured.NestedString(containerMap, "name")
		if err != nil || !found {
			klog.Errorf("Container name not found or get container name err:%v in the containerMap %v", err, containerMap)
			continue
		}
		containerNames = append(containerNames, name)
	}
	return containerNames, nil
}

type patchRecord struct {
	Op    string      `json:"op,inline"`
	Path  string      `json:"path,inline"`
	Value interface{} `json:"value"`
}

func PatchUpdateResourceRecommend(client k8sclient.Client, namespaceName k8stypes.NamespacedName,
	resourceRecommend *v1alpha1.ResourceRecommend,
) error {
	obj := &v1alpha1.ResourceRecommend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namespaceName.Name,
			Namespace: namespaceName.Namespace,
		},
	}
	patches := []patchRecord{{
		Op:    "replace",
		Path:  "/status",
		Value: resourceRecommend.Status,
	}}

	patch, err := json.Marshal(patches)
	if err != nil {
		return errors.Wrapf(err, "failed to Marshal resourceRecommend: %+v", resourceRecommend)
	}
	patchDate := k8sclient.RawPatch(k8stypes.JSONPatchType, patch)
	err = client.Status().Patch(context.TODO(), obj, patchDate)
	if err != nil {
		return errors.Wrapf(err, "failed to patch resource")
	}
	return nil
}
