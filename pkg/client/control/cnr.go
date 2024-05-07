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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
)

// CNRControl is used to update CNR
type CNRControl interface {
	// CreateCNR creates CNR from APIServer
	CreateCNR(ctx context.Context, cnr *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error)

	// DeleteCNR deletes CNR from APIServer
	DeleteCNR(ctx context.Context, cnrName string) error

	// PatchCNRSpecAndMetadata is used to update the changes for CNR Spec contents
	PatchCNRSpecAndMetadata(ctx context.Context, cnrName string, oldCNR, newCNR *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error)

	// PatchCNRStatus is used to update the changes for CNR Status contents
	PatchCNRStatus(ctx context.Context, cnrName string, oldCNR, newCNR *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error)
}

type DummyCNRControl struct{}

func (d DummyCNRControl) CreateCNR(_ context.Context, _ *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error) {
	return nil, nil
}

func (d DummyCNRControl) DeleteCNR(_ context.Context, _ string) error {
	return nil
}

func (d DummyCNRControl) PatchCNRSpecAndMetadata(_ context.Context, _ string, _, _ *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error) {
	return nil, nil
}

func (d DummyCNRControl) PatchCNRStatus(_ context.Context, _ string, _, _ *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error) {
	return nil, nil
}

var _ CNRControl = DummyCNRControl{}

type CNRControlImpl struct {
	client clientset.Interface
}

func NewCNRControlImpl(client clientset.Interface) *CNRControlImpl {
	return &CNRControlImpl{
		client: client,
	}
}

func (c *CNRControlImpl) CreateCNR(ctx context.Context, cnr *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error) {
	if cnr == nil {
		return nil, fmt.Errorf("can't create a nil cnr")
	}

	return c.client.NodeV1alpha1().CustomNodeResources().Create(ctx, cnr, metav1.CreateOptions{})
}

func (c *CNRControlImpl) DeleteCNR(ctx context.Context, cnrName string) error {
	return c.client.NodeV1alpha1().CustomNodeResources().Delete(ctx, cnrName, metav1.DeleteOptions{})
}

func (c *CNRControlImpl) PatchCNRSpecAndMetadata(ctx context.Context, cnrName string, oldCNR, newCNR *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error) {
	patchBytes, err := preparePatchBytesForCNRSpecAndMetadata(cnrName, oldCNR, newCNR)
	if err != nil {
		klog.Errorf("prepare patch bytes for spec and metadata for cnr %q: %v", cnrName, err)
		return nil, err
	}

	updatedCNR, err := c.client.NodeV1alpha1().CustomNodeResources().Patch(ctx, cnrName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		klog.Errorf("failed to patch spec and metadata %q for cnr %q: %v", patchBytes, cnrName, err)
		return nil, err
	}

	return updatedCNR, nil
}

func (c *CNRControlImpl) PatchCNRStatus(ctx context.Context, cnrName string, oldCNR, newCNR *v1alpha1.CustomNodeResource) (*v1alpha1.CustomNodeResource, error) {
	patchBytes, err := preparePatchBytesForCNRStatus(cnrName, oldCNR, newCNR)
	if err != nil {
		klog.Errorf("prepare patch bytes for status for cnr %q: %v", cnrName, err)
		return nil, err
	}

	updatedCNR, err := c.client.NodeV1alpha1().CustomNodeResources().Patch(ctx, cnrName, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if err != nil {
		klog.Errorf("failed to patch status %q for cnr %q: %v", patchBytes, cnrName, err)
		return nil, err
	}

	return updatedCNR, nil
}

// preparePatchBytesForCNRStatus generate those json patch bytes for comparing new and old CNR Status
// while keep the Spec remains the same
func preparePatchBytesForCNRStatus(cnrName string, oldCNR, newCNR *v1alpha1.CustomNodeResource) ([]byte, error) {
	if oldCNR == nil || newCNR == nil {
		return nil, fmt.Errorf("neither old nor new object can be nil")
	}

	oldData, err := json.Marshal(oldCNR)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal oldData for cnr %q: %v", cnrName, err)
	}

	diffCNR := oldCNR.DeepCopy()
	diffCNR.Status = newCNR.Status
	newData, err := json.Marshal(diffCNR)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal newData for cnr %q: %v", cnrName, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to CreateTwoWayMergePatch for cnr %q: %v", cnrName, err)
	}

	return patchBytes, nil
}

// preparePatchBytesForCNRStatus generate those json patch bytes for comparing new and old CNR Spec
// while keep the Status remains the same
func preparePatchBytesForCNRSpecAndMetadata(cnrName string, oldCNR, newCNR *v1alpha1.CustomNodeResource) ([]byte, error) {
	if oldCNR == nil || newCNR == nil {
		return nil, fmt.Errorf("neither old nor new object can be nil")
	}

	oldData, err := json.Marshal(oldCNR)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal oldData for cnr %q: %v", cnrName, err)
	}

	diffCNR := oldCNR.DeepCopy()
	diffCNR.Spec = newCNR.Spec
	diffCNR.ObjectMeta = newCNR.ObjectMeta
	newData, err := json.Marshal(diffCNR)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal newData for cnr %q: %v", cnrName, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to CreateTwoWayMergePatch for cnr %q: %v", cnrName, err)
	}

	return patchBytes, nil
}
