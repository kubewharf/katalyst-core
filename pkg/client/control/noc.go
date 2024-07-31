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
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"

	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type NocUpdater interface {
	PatchNoc(ctx context.Context, oldNoc, newNoc *v1alpha1.NodeOvercommitConfig) (*v1alpha1.NodeOvercommitConfig, error)
	PatchNocStatus(ctx context.Context, oldNoc, newNoc *v1alpha1.NodeOvercommitConfig) (*v1alpha1.NodeOvercommitConfig, error)
}

type DummyNocUpdater struct{}

func (d *DummyNocUpdater) PatchNocStatus(_ context.Context, _, newNoc *v1alpha1.NodeOvercommitConfig) (*v1alpha1.NodeOvercommitConfig, error) {
	return newNoc, nil
}

func (d *DummyNocUpdater) PatchNoc(_ context.Context, _, newNoc *v1alpha1.NodeOvercommitConfig) (*v1alpha1.NodeOvercommitConfig, error) {
	return newNoc, nil
}

func NewRealNocUpdater(client clientset.Interface) NocUpdater {
	return &RealNocUpdater{
		client: client,
	}
}

type RealNocUpdater struct {
	client clientset.Interface
}

func (r *RealNocUpdater) PatchNocStatus(ctx context.Context, oldNoc, newNoc *v1alpha1.NodeOvercommitConfig) (*v1alpha1.NodeOvercommitConfig, error) {
	if oldNoc == nil || newNoc == nil {
		return nil, fmt.Errorf("can't patch a nil noc")
	}

	oldData, err := json.Marshal(v1alpha1.NodeOvercommitConfig{Status: oldNoc.Status})
	if err != nil {
		return nil, err
	}
	newData, err := json.Marshal(v1alpha1.NodeOvercommitConfig{Status: newNoc.Status})
	if err != nil {
		return nil, err
	}

	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldData, newData, oldData)
	if err != nil {
		return nil, fmt.Errorf("failed to create merge patch for nodeOvercommitConfig %s: %v",
			oldNoc.Name, err)
	}
	if general.JsonPathEmpty(patchBytes) {
		return newNoc, nil
	}

	return r.client.OvercommitV1alpha1().NodeOvercommitConfigs().Patch(ctx, oldNoc.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
}

func (r *RealNocUpdater) PatchNoc(ctx context.Context, oldNoc, newNoc *v1alpha1.NodeOvercommitConfig) (*v1alpha1.NodeOvercommitConfig, error) {
	if oldNoc == nil || newNoc == nil {
		return nil, fmt.Errorf("can't patch a nil noc")
	}

	oldData, err := json.Marshal(oldNoc)
	if err != nil {
		return nil, err
	}

	newData, err := json.Marshal(newNoc)
	if err != nil {
		return nil, err
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, err
	}
	if general.JsonPathEmpty(patchBytes) {
		return oldNoc, nil
	}

	return r.client.OvercommitV1alpha1().NodeOvercommitConfigs().Patch(ctx, oldNoc.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
}
