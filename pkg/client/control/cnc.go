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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	jsonpatch "github.com/evanphx/json-patch"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
)

// CNCControl is used to update CustomNodeConfig
// todo: use patch instead of update to avoid conflict
type CNCControl interface {
	// CreateCNC is used to create new CNC obj
	CreateCNC(ctx context.Context, cnc *v1alpha1.CustomNodeConfig,
		opts metav1.CreateOptions) (*v1alpha1.CustomNodeConfig, error)

	// DeleteCNC is used to delete CNC obj
	DeleteCNC(ctx context.Context, cncName string,
		opts metav1.DeleteOptions) error

	// PatchCNC is used to update the changes for CNC spec and metadata contents
	PatchCNC(ctx context.Context, cncName string, oldCNC,
		newCNC *v1alpha1.CustomNodeConfig) (*v1alpha1.CustomNodeConfig, error)

	// PatchCNCStatus is used to update the changes for CNC status contents
	PatchCNCStatus(ctx context.Context, cncName string, oldCNC,
		newCNC *v1alpha1.CustomNodeConfig) (*v1alpha1.CustomNodeConfig, error)
}

type DummyCNCControl struct{}

func (d DummyCNCControl) CreateCNC(_ context.Context, cnc *v1alpha1.CustomNodeConfig,
	_ metav1.CreateOptions,
) (*v1alpha1.CustomNodeConfig, error) {
	return cnc, nil
}

func (d DummyCNCControl) DeleteCNC(_ context.Context, _ string,
	_ metav1.DeleteOptions,
) error {
	return nil
}

func (d DummyCNCControl) PatchCNC(_ context.Context, _ string,
	_, newCNC *v1alpha1.CustomNodeConfig,
) (*v1alpha1.CustomNodeConfig, error) {
	return newCNC, nil
}

func (d DummyCNCControl) PatchCNCStatus(_ context.Context, _ string,
	_, newCNC *v1alpha1.CustomNodeConfig,
) (*v1alpha1.CustomNodeConfig, error) {
	return newCNC, nil
}

type RealCNCControl struct {
	client clientset.Interface
}

func (r *RealCNCControl) CreateCNC(ctx context.Context, cnc *v1alpha1.CustomNodeConfig, opts metav1.CreateOptions) (*v1alpha1.CustomNodeConfig, error) {
	if cnc == nil {
		return nil, fmt.Errorf("can't create a nil cnc")
	}

	return r.client.ConfigV1alpha1().CustomNodeConfigs().Create(ctx, cnc, opts)
}

func (r *RealCNCControl) DeleteCNC(ctx context.Context, cncName string, opts metav1.DeleteOptions) error {
	return r.client.ConfigV1alpha1().CustomNodeConfigs().Delete(ctx, cncName, opts)
}

func (r *RealCNCControl) PatchCNC(ctx context.Context, cncName string, oldCNC, newCNC *v1alpha1.CustomNodeConfig) (*v1alpha1.CustomNodeConfig, error) {
	patchBytes, err := preparePatchBytesForCNC(cncName, oldCNC, newCNC)
	if err != nil {
		return nil, err
	}

	updatedCNC, err := r.client.ConfigV1alpha1().CustomNodeConfigs().Patch(ctx, cncName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to patch spec and metadata %q for cnc %q: %v", patchBytes, cncName, err)
	}

	return updatedCNC, nil
}

func (r *RealCNCControl) PatchCNCStatus(ctx context.Context, cncName string, oldCNC, newCNC *v1alpha1.CustomNodeConfig) (*v1alpha1.CustomNodeConfig, error) {
	patchBytes, err := preparePatchBytesForCNCStatus(cncName, oldCNC, newCNC)
	if err != nil {
		return nil, err
	}

	updatedCNC, err := r.client.ConfigV1alpha1().CustomNodeConfigs().Patch(ctx, cncName, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if err != nil {
		return nil, fmt.Errorf("failed to patch status %q for cnc %q: %v", patchBytes, cncName, err)
	}

	return updatedCNC, nil
}

// preparePatchBytesForCNCStatus generate those json patch bytes for comparing new and old CNC status
// while keep the spec remains the same
func preparePatchBytesForCNCStatus(cncName string, oldCNC, newCNC *v1alpha1.CustomNodeConfig) ([]byte, error) {
	if oldCNC == nil || newCNC == nil {
		return nil, fmt.Errorf("neither old nor new object can be nil")
	}

	oldData, err := json.Marshal(oldCNC)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal oldData for cnc %q: %v", cncName, err)
	}

	diffCNC := oldCNC.DeepCopy()
	diffCNC.Status = newCNC.Status
	newData, err := json.Marshal(diffCNC)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal newData for cnc %q: %v", cncName, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to CreateTwoWayMergePatch for cnc %q: %v", cncName, err)
	}

	return patchBytes, nil
}

// preparePatchBytesForCNC generate those json patch bytes for comparing new and old CNC spec and metadata
// while keep the status remains the same
func preparePatchBytesForCNC(cncName string, oldCNC, newCNC *v1alpha1.CustomNodeConfig) ([]byte, error) {
	if oldCNC == nil || newCNC == nil {
		return nil, fmt.Errorf("neither old nor new object can be nil")
	}

	oldData, err := json.Marshal(oldCNC)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal oldData for cnc %q: %v", cncName, err)
	}

	diffCNC := oldCNC.DeepCopy()
	diffCNC.Spec = newCNC.Spec
	diffCNC.ObjectMeta = newCNC.ObjectMeta
	newData, err := json.Marshal(diffCNC)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal newData for cnc %q: %v", cncName, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to CreateTwoWayMergePatch for cnc %q: %v", cncName, err)
	}

	return patchBytes, nil
}

func NewRealCNCControl(client clientset.Interface) *RealCNCControl {
	return &RealCNCControl{
		client: client,
	}
}
