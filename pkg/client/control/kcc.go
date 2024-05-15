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

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
)

// KCCControl is used to update KatalystCustomConfig
// todo: use patch instead of update to avoid conflict
type KCCControl interface {
	// UpdateKCC is used to update the changes for KCC spec and metadata contents
	UpdateKCC(ctx context.Context, kcc *v1alpha1.KatalystCustomConfig,
		opts metav1.UpdateOptions) (*v1alpha1.KatalystCustomConfig, error)

	// UpdateKCCStatus is used to update the change for KCC status
	UpdateKCCStatus(ctx context.Context, kcc *v1alpha1.KatalystCustomConfig,
		opts metav1.UpdateOptions) (*v1alpha1.KatalystCustomConfig, error)
}

type DummyKCCControl struct{}

func (d DummyKCCControl) UpdateKCC(_ context.Context, kcc *v1alpha1.KatalystCustomConfig,
	_ metav1.UpdateOptions,
) (*v1alpha1.KatalystCustomConfig, error) {
	return kcc, nil
}

func (d DummyKCCControl) UpdateKCCStatus(_ context.Context, kcc *v1alpha1.KatalystCustomConfig,
	_ metav1.UpdateOptions,
) (*v1alpha1.KatalystCustomConfig, error) {
	return kcc, nil
}

type RealKCCControl struct {
	client clientset.Interface
}

func (r *RealKCCControl) UpdateKCC(ctx context.Context, kcc *v1alpha1.KatalystCustomConfig,
	opts metav1.UpdateOptions,
) (*v1alpha1.KatalystCustomConfig, error) {
	if kcc == nil {
		return nil, fmt.Errorf("can't update a nil KCC")
	}

	return r.client.ConfigV1alpha1().KatalystCustomConfigs(kcc.Namespace).Update(ctx, kcc, opts)
}

func (r *RealKCCControl) UpdateKCCStatus(ctx context.Context, kcc *v1alpha1.KatalystCustomConfig,
	opts metav1.UpdateOptions,
) (*v1alpha1.KatalystCustomConfig, error) {
	if kcc == nil {
		return nil, fmt.Errorf("can't update a nil KCC's status")
	}

	return r.client.ConfigV1alpha1().KatalystCustomConfigs(kcc.Namespace).UpdateStatus(ctx, kcc, opts)
}

func NewRealKCCControl(client clientset.Interface) *RealKCCControl {
	return &RealKCCControl{
		client: client,
	}
}
