package hpc

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type HyperParameterConfiguration struct {
	*BorweinProvisionConfig
}

func NewHyperParameterConfiguration() *HyperParameterConfiguration {
	return &HyperParameterConfiguration{
		BorweinProvisionConfig: NewBorweinProvisionConfig(),
	}
}

func (hpc *HyperParameterConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	hpc.BorweinProvisionConfig.ApplyConfiguration(conf)
}
