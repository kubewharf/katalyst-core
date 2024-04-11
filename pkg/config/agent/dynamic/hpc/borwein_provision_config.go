package hpc

import (
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type BorweinProvisionConfig struct {
	BorweinProvisionParams []v1alpha1.BorweinProvisionParam
}

func NewBorweinProvisionConfig() *BorweinProvisionConfig {
	return &BorweinProvisionConfig{
		BorweinProvisionParams: make([]v1alpha1.BorweinProvisionParam, 0),
	}
}

func (bpc *BorweinProvisionConfig) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if hpcConfig := conf.HyperParameterConfiguration; hpcConfig != nil &&
		hpcConfig.Spec.Config.BorweinProvisionConfig != nil &&
		hpcConfig.Spec.Config.BorweinProvisionConfig.BorweinProvisionParams != nil {
		bpc.BorweinProvisionParams = hpcConfig.Spec.Config.BorweinProvisionConfig.BorweinProvisionParams
	}
}
