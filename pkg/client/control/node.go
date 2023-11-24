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

	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// NodeUpdater is used to update Node
type NodeUpdater interface {
	PatchNode(ctx context.Context, oldNode, newNode *core.Node) error
	PatchNodeStatus(ctx context.Context, oldNode, newNode *core.Node) error
	UpdateNode(ctx context.Context, node *core.Node, opts metav1.UpdateOptions) (*core.Node, error)
}

type DummyNodeUpdater struct{}

func (d *DummyNodeUpdater) UpdateNode(_ context.Context, _ *core.Node, _ metav1.UpdateOptions) (*core.Node, error) {
	return nil, nil
}

func (d *DummyNodeUpdater) PatchNode(_ context.Context, _, _ *core.Node) error {
	return nil
}

func (d *DummyNodeUpdater) PatchNodeStatus(_ context.Context, _, _ *core.Node) error {
	return nil
}

type RealNodeUpdater struct {
	client kubernetes.Interface
}

func NewRealNodeUpdater(client kubernetes.Interface) *RealNodeUpdater {
	return &RealNodeUpdater{
		client: client,
	}
}

func (r *RealNodeUpdater) UpdateNode(ctx context.Context, node *core.Node, opts metav1.UpdateOptions) (*core.Node, error) {
	if node == nil {
		return nil, fmt.Errorf("can't update a nil node")
	}

	return r.client.CoreV1().Nodes().Update(ctx, node, opts)
}

func (r *RealNodeUpdater) PatchNode(ctx context.Context, oldNode, newNode *core.Node) error {
	if oldNode == nil || newNode == nil {
		return fmt.Errorf("can't update a nil node")
	}

	oldData, err := json.Marshal(oldNode)
	if err != nil {
		return err
	}

	newData, err := json.Marshal(newNode)
	if err != nil {
		return err
	}

	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, &core.Node{})
	if err != nil {
		return fmt.Errorf("failed to create merge patch for node%q: %v", oldNode.Name, err)
	}
	if general.JsonPathEmpty(patchBytes) {
		return nil
	}

	_, err = r.client.CoreV1().Nodes().Patch(ctx, oldNode.Name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

func (r *RealNodeUpdater) PatchNodeStatus(ctx context.Context, oldNode, newNode *core.Node) error {
	if oldNode == nil || newNode == nil {
		return fmt.Errorf("can't update a nil node")
	}

	oldData, err := json.Marshal(oldNode)
	if err != nil {
		return err
	}

	newData, err := json.Marshal(newNode)
	if err != nil {
		return err
	}

	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, &core.Node{})
	if err != nil {
		return fmt.Errorf("failed to create merge patch for node%q: %v", oldNode.Name, err)
	}
	if general.JsonPathEmpty(patchBytes) {
		return nil
	}

	_, err = r.client.CoreV1().Nodes().Patch(ctx, oldNode.Name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return err
}
