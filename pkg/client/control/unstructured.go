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

package control

import (
	"context"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// UnstructuredControl is used to update Unstructured obj
// todo: use patch instead of update to avoid conflict
type UnstructuredControl interface {
	// CreateUnstructured is used to create unstructured obj
	CreateUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
		obj *unstructured.Unstructured, opts metav1.CreateOptions) (*unstructured.Unstructured, error)

	// GetUnstructured is used to get unstructured obj
	GetUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
		namespace, name string, opts metav1.GetOptions) (*unstructured.Unstructured, error)

	// PatchUnstructured is to patch unstructured object spec and metadata
	PatchUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
		oldObj, newObj *unstructured.Unstructured) (*unstructured.Unstructured, error)

	// UpdateUnstructured is used to update unstructured obj spec and metadata
	UpdateUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
		obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error)

	// UpdateUnstructuredStatus is used to update unstructured obj status
	UpdateUnstructuredStatus(ctx context.Context, gvr metav1.GroupVersionResource,
		obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error)

	// PatchUnstructuredStatus is to patch unstructured object status
	PatchUnstructuredStatus(ctx context.Context, gvr metav1.GroupVersionResource,
		oldObj, newObj *unstructured.Unstructured) (*unstructured.Unstructured, error)

	// DeleteUnstructured is used to delete unstructured obj
	DeleteUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
		obj *unstructured.Unstructured, opts metav1.DeleteOptions) error
}

type DummyUnstructuredControl struct{}

func (d DummyUnstructuredControl) CreateUnstructured(_ context.Context, _ metav1.GroupVersionResource,
	obj *unstructured.Unstructured, _ metav1.CreateOptions,
) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (d DummyUnstructuredControl) GetUnstructured(_ context.Context, _ metav1.GroupVersionResource,
	_, _ string, _ metav1.GetOptions,
) (*unstructured.Unstructured, error) {
	return nil, nil
}

func (d DummyUnstructuredControl) UpdateUnstructured(_ context.Context, _ metav1.GroupVersionResource,
	obj *unstructured.Unstructured, _ metav1.UpdateOptions,
) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (d DummyUnstructuredControl) PatchUnstructured(_ context.Context, _ metav1.GroupVersionResource,
	_, newObj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	return newObj, nil
}

func (d DummyUnstructuredControl) UpdateUnstructuredStatus(_ context.Context, _ metav1.GroupVersionResource,
	obj *unstructured.Unstructured, _ metav1.UpdateOptions,
) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (d DummyUnstructuredControl) PatchUnstructuredStatus(_ context.Context, _ metav1.GroupVersionResource,
	_, newObj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	return newObj, nil
}

func (d DummyUnstructuredControl) DeleteUnstructured(_ context.Context, _ metav1.GroupVersionResource,
	_ *unstructured.Unstructured, _ metav1.DeleteOptions,
) error {
	return nil
}

type RealUnstructuredControl struct {
	client dynamic.Interface
}

func (r *RealUnstructuredControl) CreateUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
	obj *unstructured.Unstructured, opts metav1.CreateOptions,
) (*unstructured.Unstructured, error) {
	schemaGVR := schema.GroupVersionResource(gvr)

	if obj.GetNamespace() != "" {
		return r.client.Resource(schemaGVR).Namespace(obj.GetNamespace()).Create(ctx, obj, opts)
	}

	return r.client.Resource(schemaGVR).Create(ctx, obj, opts)
}

func (r *RealUnstructuredControl) GetUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
	namespace, name string, opts metav1.GetOptions,
) (*unstructured.Unstructured, error) {
	schemaGVR := schema.GroupVersionResource(gvr)

	if namespace != "" {
		return r.client.Resource(schemaGVR).Namespace(namespace).Get(ctx, name, opts)
	}

	return r.client.Resource(schemaGVR).Get(ctx, name, opts)
}

func (r *RealUnstructuredControl) UpdateUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
	obj *unstructured.Unstructured, opts metav1.UpdateOptions,
) (*unstructured.Unstructured, error) {
	if obj == nil {
		return nil, fmt.Errorf("can't update a nil Unstructured obj")
	}

	schemaGVR := schema.GroupVersionResource(gvr)
	if obj.GetNamespace() != "" {
		return r.client.Resource(schemaGVR).Namespace(obj.GetNamespace()).Update(ctx, obj, opts)
	}

	return r.client.Resource(schemaGVR).Update(ctx, obj, opts)
}

func (r *RealUnstructuredControl) PatchUnstructured(ctx context.Context, gvr metav1.GroupVersionResource,
	oldObj, newObj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	patchBytes, err := prepareUnstructuredPatchBytes(oldObj, newObj)
	if err != nil {
		return nil, err
	}

	schemaGVR := schema.GroupVersionResource(gvr)
	if newObj.GetNamespace() == "" {
		return r.client.Resource(schemaGVR).Patch(ctx, newObj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{})
	}

	return r.client.Resource(schemaGVR).Namespace(newObj.GetNamespace()).Patch(ctx, newObj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{})
}

func (r *RealUnstructuredControl) UpdateUnstructuredStatus(ctx context.Context, gvr metav1.GroupVersionResource,
	obj *unstructured.Unstructured, opts metav1.UpdateOptions,
) (*unstructured.Unstructured, error) {
	if obj == nil {
		return nil, fmt.Errorf("can't update a nil Unstructured obj's status")
	}

	schemaGVR := schema.GroupVersionResource(gvr)
	if obj.GetNamespace() != "" {
		return r.client.Resource(schemaGVR).Namespace(obj.GetNamespace()).UpdateStatus(ctx, obj, opts)
	}

	return r.client.Resource(schemaGVR).UpdateStatus(ctx, obj, opts)
}

func (r *RealUnstructuredControl) PatchUnstructuredStatus(ctx context.Context, gvr metav1.GroupVersionResource,
	oldObj, newObj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	patchBytes, err := prepareUnstructuredStatusPatchBytes(oldObj, newObj)
	if err != nil {
		return nil, err
	}

	schemaGVR := schema.GroupVersionResource(gvr)
	if newObj.GetNamespace() == "" {
		return r.client.Resource(schemaGVR).Patch(ctx, newObj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	}

	return r.client.Resource(schemaGVR).Namespace(newObj.GetNamespace()).Patch(ctx, newObj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
}

func (r *RealUnstructuredControl) DeleteUnstructured(ctx context.Context, gvr metav1.GroupVersionResource, obj *unstructured.Unstructured, opts metav1.DeleteOptions) error {
	if obj == nil {
		return fmt.Errorf("can't delete a nil Unstructured obj")
	}

	schemaGVR := native.ToSchemaGVR(gvr.Group, gvr.Version, gvr.Resource)
	if obj.GetNamespace() != "" {
		return r.client.Resource(schemaGVR).Namespace(obj.GetNamespace()).Delete(ctx, obj.GetName(), opts)
	}

	return r.client.Resource(schemaGVR).Delete(ctx, obj.GetName(), opts)
}

func NewRealUnstructuredControl(client dynamic.Interface) *RealUnstructuredControl {
	return &RealUnstructuredControl{
		client: client,
	}
}

func prepareUnstructuredPatchBytes(oldObj, newObj *unstructured.Unstructured) ([]byte, error) {
	oldData, err := json.Marshal(oldObj)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal oldData %q: %v", newObj.GetName(), err)
	}

	diff := oldObj.DeepCopy()
	metadata, _, err := unstructured.NestedFieldNoCopy(newObj.Object, "metadata")
	if err != nil {
		return nil, err
	}
	err = unstructured.SetNestedField(diff.Object, metadata, "metadata")
	if err != nil {
		return nil, err
	}

	spec, _, err := unstructured.NestedFieldNoCopy(newObj.Object, "spec")
	if err != nil {
		return nil, err
	}
	err = unstructured.SetNestedField(diff.Object, spec, "spec")
	if err != nil {
		return nil, err
	}

	newData, err := json.Marshal(diff)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal newData %q: %v", newObj.GetName(), err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to CreateTwoWayMergePatch %q: %v", newObj.GetName(), err)
	}

	return patchBytes, nil
}

func prepareUnstructuredStatusPatchBytes(oldObj, newObj *unstructured.Unstructured) ([]byte, error) {
	oldData, err := json.Marshal(oldObj)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal oldData %q: %v", newObj.GetName(), err)
	}

	diff := oldObj.DeepCopy()
	spec, _, err := unstructured.NestedFieldNoCopy(newObj.Object, "status")
	if err != nil {
		return nil, err
	}
	err = unstructured.SetNestedField(diff.Object, spec, "status")
	if err != nil {
		return nil, err
	}

	newData, err := json.Marshal(diff)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal newData %q: %v", newObj.GetName(), err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to CreateTwoWayMergePatch %q: %v", newObj.GetName(), err)
	}

	return patchBytes, nil
}
