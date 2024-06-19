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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
)

type NodeProfileControl interface {
	CreateNPD(ctx context.Context, npd *v1alpha1.NodeProfileDescriptor, opts metav1.CreateOptions) (*v1alpha1.NodeProfileDescriptor, error)
	UpdateNPDStatus(ctx context.Context, npd *v1alpha1.NodeProfileDescriptor, opts metav1.UpdateOptions) (*v1alpha1.NodeProfileDescriptor, error)
	DeleteNPD(ctx context.Context, npdName string, opts metav1.DeleteOptions) error
}

type DummyNPDControl struct{}

func (d *DummyNPDControl) CreateNPD(ctx context.Context, npd *v1alpha1.NodeProfileDescriptor, opts metav1.CreateOptions) (*v1alpha1.NodeProfileDescriptor, error) {
	return nil, nil
}

func (d *DummyNPDControl) UpdateNPDStatus(ctx context.Context, npd *v1alpha1.NodeProfileDescriptor, opts metav1.UpdateOptions) (*v1alpha1.NodeProfileDescriptor, error) {
	return nil, nil
}

func (d *DummyNPDControl) DeleteNPD(ctx context.Context, npdName string, opts metav1.DeleteOptions) error {
	return nil
}

type NPDControlImp struct {
	client clientset.Interface
}

func NewNPDControlImp(client clientset.Interface) *NPDControlImp {
	return &NPDControlImp{
		client: client,
	}
}

func (n *NPDControlImp) CreateNPD(ctx context.Context, npd *v1alpha1.NodeProfileDescriptor, opts metav1.CreateOptions) (*v1alpha1.NodeProfileDescriptor, error) {
	if npd == nil {
		return nil, fmt.Errorf("npd is nil")
	}

	return n.client.NodeV1alpha1().NodeProfileDescriptors().Create(ctx, npd, opts)
}

func (n *NPDControlImp) UpdateNPDStatus(ctx context.Context, npd *v1alpha1.NodeProfileDescriptor, opts metav1.UpdateOptions) (*v1alpha1.NodeProfileDescriptor, error) {
	if npd == nil {
		return nil, fmt.Errorf("npd is nil")
	}

	return n.client.NodeV1alpha1().NodeProfileDescriptors().UpdateStatus(ctx, npd, opts)
}

func (n *NPDControlImp) DeleteNPD(ctx context.Context, npdName string, opts metav1.DeleteOptions) error {
	return n.client.NodeV1alpha1().NodeProfileDescriptors().Delete(ctx, npdName, opts)
}
