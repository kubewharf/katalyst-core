package control

import (
	"context"
	"fmt"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type HPCControl interface {
	// CreateHPC is used to create new HPC obj
	CreateHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.CreateOptions) (*v1alpha1.HyperParameterConfiguration, error)

	// DeleteHPC is used to delete HPC obj
	DeleteHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.DeleteOptions) error

	// UpdateHPC is used to update the changes for HPC spec and metadata contents
	UpdateHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.UpdateOptions) (*v1alpha1.HyperParameterConfiguration, error)
}

type hpcControlImp struct {
	client clientset.Interface
}

func NewHPCControlImp(client clientset.Interface) HPCControl {
	return &hpcControlImp{client: client}
}

func (h *hpcControlImp) CreateHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.CreateOptions) (*v1alpha1.HyperParameterConfiguration, error) {
	if hpc == nil {
		return nil, fmt.Errorf("failed to update a nil hpc")
	}

	return h.client.ConfigV1alpha1().HyperParameterConfigurations(hpc.Namespace).Create(ctx, hpc, opts)
}

func (h *hpcControlImp) DeleteHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.DeleteOptions) error {
	if hpc == nil {
		return fmt.Errorf("failed to delete a nil hpc")
	}

	return h.client.ConfigV1alpha1().HyperParameterConfigurations(hpc.Namespace).Delete(ctx, hpc.Name, opts)
}

func (h *hpcControlImp) UpdateHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.UpdateOptions) (*v1alpha1.HyperParameterConfiguration, error) {
	if hpc == nil {
		return nil, fmt.Errorf("failed to update a nil hpc")
	}

	return h.client.ConfigV1alpha1().HyperParameterConfigurations(hpc.Namespace).Update(ctx, hpc, opts)
}

type DummyHPCControl struct{}

func (h *DummyHPCControl) CreateHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.CreateOptions) (*v1alpha1.HyperParameterConfiguration, error) {
	return nil, nil
}

func (h *DummyHPCControl) DeleteHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.DeleteOptions) error {
	return nil
}

func (h *DummyHPCControl) UpdateHPC(ctx context.Context, hpc *v1alpha1.HyperParameterConfiguration, opts metav1.UpdateOptions) (*v1alpha1.HyperParameterConfiguration, error) {
	return nil, nil
}
