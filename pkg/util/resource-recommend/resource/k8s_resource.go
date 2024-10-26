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

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
)

func ConvertAndGetResource(ctx context.Context, client dynamic.Interface,
	namespace string, targetRef v1alpha1.CrossVersionObjectReference,
	mapper *restmapper.DeferredDiscoveryRESTMapper,
) (*unstructured.Unstructured, error) {
	klog.V(5).Infof("get resource in targetRef: %v, namespace: %v", targetRef, namespace)
	gvk := schema.FromAPIVersionAndKind(targetRef.APIVersion, targetRef.Kind)
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}

	resource, err := client.Resource(schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: mapping.Resource.Resource,
	}).Namespace(namespace).Get(ctx, targetRef.Name, metav1.GetOptions{ResourceVersion: "0"})
	if err != nil {
		return nil, err
	}
	return resource, nil
}

func GetAllClaimedContainers(resource *unstructured.Unstructured) ([]string, error) {
	klog.V(5).InfoS("Get all controller claimed containers", "resource", resource)
	templateSpec, found, err := unstructured.NestedMap(resource.Object, "spec", "template", "spec")
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
