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

	apis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
)

// ServiceProfileControl is used to update ServiceProfileDescriptor CR
// todo: use patch instead of update to avoid conflict
type ServiceProfileControl interface {
	CreateSPD(ctx context.Context, spd *apis.ServiceProfileDescriptor, opts metav1.CreateOptions) (*apis.ServiceProfileDescriptor, error)
	UpdateSPD(ctx context.Context, spd *apis.ServiceProfileDescriptor, opts metav1.UpdateOptions) (*apis.ServiceProfileDescriptor, error)
	UpdateSPDStatus(ctx context.Context, spd *apis.ServiceProfileDescriptor, opts metav1.UpdateOptions) (*apis.ServiceProfileDescriptor, error)
	DeleteSPD(ctx context.Context, spd *apis.ServiceProfileDescriptor, opts metav1.DeleteOptions) error
}

type DummySPDControl struct{}

func (d *DummySPDControl) CreateSPD(ctx context.Context, _ *apis.ServiceProfileDescriptor, _ metav1.CreateOptions) (*apis.ServiceProfileDescriptor, error) {
	return nil, nil
}

func (d *DummySPDControl) UpdateSPD(_ context.Context, _ *apis.ServiceProfileDescriptor, _ metav1.UpdateOptions) (*apis.ServiceProfileDescriptor, error) {
	return nil, nil
}

func (d *DummySPDControl) UpdateSPDStatus(_ context.Context, _ *apis.ServiceProfileDescriptor, _ metav1.UpdateOptions) (*apis.ServiceProfileDescriptor, error) {
	return nil, nil
}

func (d *DummySPDControl) DeleteSPD(_ context.Context, _ *apis.ServiceProfileDescriptor, _ metav1.DeleteOptions) error {
	return nil
}

type SPDControlImp struct {
	client clientset.Interface
}

func NewSPDControlImp(client clientset.Interface) *SPDControlImp {
	return &SPDControlImp{
		client: client,
	}
}

func (r *SPDControlImp) CreateSPD(ctx context.Context, spd *apis.ServiceProfileDescriptor, opts metav1.CreateOptions) (*apis.ServiceProfileDescriptor, error) {
	if spd == nil {
		return nil, fmt.Errorf("can't update a nil spd")
	}

	return r.client.WorkloadV1alpha1().ServiceProfileDescriptors(spd.Namespace).Create(ctx, spd, opts)
}

func (r *SPDControlImp) UpdateSPD(ctx context.Context, spd *apis.ServiceProfileDescriptor, opts metav1.UpdateOptions) (*apis.ServiceProfileDescriptor, error) {
	if spd == nil {
		return nil, fmt.Errorf("can't update a nil spd")
	}

	return r.client.WorkloadV1alpha1().ServiceProfileDescriptors(spd.Namespace).Update(ctx, spd, opts)
}

func (r *SPDControlImp) UpdateSPDStatus(ctx context.Context, spd *apis.ServiceProfileDescriptor, opts metav1.UpdateOptions) (*apis.ServiceProfileDescriptor, error) {
	if spd == nil {
		return nil, fmt.Errorf("can't update a nil spd's status")
	}

	return r.client.WorkloadV1alpha1().ServiceProfileDescriptors(spd.Namespace).UpdateStatus(ctx, spd, opts)
}

func (r *SPDControlImp) DeleteSPD(ctx context.Context, spd *apis.ServiceProfileDescriptor, opts metav1.DeleteOptions) error {
	if spd == nil {
		return fmt.Errorf("can't delete a nil spd ")
	}

	return r.client.WorkloadV1alpha1().ServiceProfileDescriptors(spd.Namespace).Delete(ctx, spd.Name, opts)
}
