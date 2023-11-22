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

package native

import (
	"fmt"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

var (
	objectFieldsForLabelSelector       = []string{"spec", "selector"}
	objectFieldsForTemplateAnnotations = []string{"spec", "template", "metadata", "annotations"}
)

// GenerateUniqObjectUIDKey generate a uniq key (including UID) for the given object.
func GenerateUniqObjectUIDKey(obj metav1.Object) string {
	return fmt.Sprintf("%s/%s/%s", obj.GetNamespace(), obj.GetName(), obj.GetUID())
}

// ParseUniqObjectUIDKey parse the given key into namespace, name and uid
func ParseUniqObjectUIDKey(key string) (namespace string, name string, uid string, err error) {
	names := strings.Split(key, "/")
	if len(names) != 3 {
		return "", "", "", fmt.Errorf("workload key %s split error", key)
	}

	return names[0], names[1], names[2], nil
}

// GenerateUniqObjectNameKey generate a uniq key (without UID) for the given object.
func GenerateUniqObjectNameKey(obj metav1.Object) string {
	return GenerateNamespaceNameKey(obj.GetNamespace(), obj.GetName())
}

// GenerateNamespaceNameKey generate uniq key by concatenating namespace and name.
func GenerateNamespaceNameKey(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// GenerateUniqGVRNameKey generate a uniq key (without UID) for the GVR and its corresponding object.
func GenerateUniqGVRNameKey(gvr string, workload metav1.Object) (string, error) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(workload)
	if err != nil {
		return "", err
	}
	return gvr + "/" + key, nil
}

// ParseUniqGVRNameKey parse the given key into GVR and namespace/name
func ParseUniqGVRNameKey(key string) (gvr string, namespace string, name string, err error) {
	names := strings.Split(key, "/")
	if len(names) != 3 {
		return "", "", "", fmt.Errorf("workload key %s split error", key)
	}

	return names[0], names[1], names[2], nil
}

// GenerateDynamicResourceByGVR generates dynamic resource by given gvr, the format is such as `resource.version.group`,
// which can be input of ParseResourceArg
func GenerateDynamicResourceByGVR(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Version, gvr.Group)
}

// ObjectOwnerReferenceIndex is used by informer to index a resource by owner
func ObjectOwnerReferenceIndex(o interface{}) ([]string, error) {
	obj, ok := o.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("failed to reflect a obj to pod")
	}

	var keys []string
	for _, ow := range obj.GetOwnerReferences() {
		keys = append(keys, GenerateObjectOwnerReferenceKey(ow))
	}
	return keys, nil
}

// GenerateObjectOwnerReferenceKey is to generate a unique key by owner reference
func GenerateObjectOwnerReferenceKey(reference metav1.OwnerReference) string {
	return fmt.Sprintf("%s,%s,%s", reference.APIVersion, reference.Kind, reference.Name)
}

func ToSchemaGVR(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}

func ToUnstructured(obj interface{}) (*unstructured.Unstructured, error) {
	ret, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: ret}, nil
}

func FilterOutDeletingUnstructured(objList []*unstructured.Unstructured) []*unstructured.Unstructured {
	aliveTargets := make([]*unstructured.Unstructured, 0, len(objList))
	for _, obj := range objList {
		if obj.GetDeletionTimestamp() != nil {
			continue
		}
		aliveTargets = append(aliveTargets, obj)
	}
	return aliveTargets
}

// CheckObjectEqual returns true if uid equals or the namespace/name pair equal
func CheckObjectEqual(obj1, obj2 metav1.Object) bool {
	return apiequality.Semantic.DeepEqual(obj1.GetUID(), obj2.GetUID()) ||
		(obj1.GetName() == obj2.GetName() && obj1.GetNamespace() == obj2.GetNamespace())
}

// GetUnstructuredSelector parse a unstructured object and return its labelSelector (for pods)
func GetUnstructuredSelector(object *unstructured.Unstructured) (labels.Selector, error) {
	anno := object.GetAnnotations()
	if selectorStr, ok := anno[consts.WorkloadAnnotationVPASelectorKey]; ok {
		selector, err := labels.Parse(selectorStr)
		if err != nil {
			return nil, err
		}
		return selector, nil
	}

	val, ok, err := unstructured.NestedFieldCopy(object.UnstructuredContent(), objectFieldsForLabelSelector...)
	if err != nil {
		return nil, err
	} else if !ok || val == nil {
		return nil, fmt.Errorf("%v doesn't exist", objectFieldsForLabelSelector)
	}

	selector := &metav1.LabelSelector{}
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(val.(map[string]interface{}), selector)

	return metav1.LabelSelectorAsSelector(selector)
}

// GetUnstructuredTemplateAnnotations parse a unstructured object and return its template's annotations (for workload like deployments, statefulsets)
func GetUnstructuredTemplateAnnotations(object *unstructured.Unstructured) (map[string]string, error) {
	val, ok, err := unstructured.NestedFieldCopy(object.UnstructuredContent(), objectFieldsForTemplateAnnotations...)
	if err != nil {
		return nil, err
	} else if !ok || val == nil {
		return nil, fmt.Errorf("%v doesn't exist", objectFieldsForTemplateAnnotations)
	}

	annotations := make(map[string]string)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(val.(map[string]interface{}), &annotations); err != nil {
		return nil, err
	}

	return annotations, nil
}

// VisitUnstructuredAncestors is to walk through all the ancestors of the given object,
// during this process, we will try to handle each ancestor with the given util function.
// if the handleFunc returns true, it means that we should continue the walking process
// for other ancestors, otherwise, we break the process and return.
func VisitUnstructuredAncestors(object *unstructured.Unstructured, unstructuredMap map[schema.GroupVersionKind]cache.GenericLister,
	handleFunc func(owner *unstructured.Unstructured) bool) bool {
	if !handleFunc(object) {
		return false
	}

	for _, owner := range object.GetOwnerReferences() {
		gvk := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind)
		if _, ok := unstructuredMap[gvk]; ok {
			ownerObj, err := unstructuredMap[gvk].ByNamespace(object.GetNamespace()).Get(owner.Name)
			if err != nil || ownerObj == nil {
				klog.Errorf("get object %s/%s owner %s failed: %s", object.GetNamespace(), object.GetName(), owner.Name, err)
				continue
			}

			if toContinue := VisitUnstructuredAncestors(ownerObj.(*unstructured.Unstructured), unstructuredMap, handleFunc); !toContinue {
				return false
			}
		}
	}

	return true
}
