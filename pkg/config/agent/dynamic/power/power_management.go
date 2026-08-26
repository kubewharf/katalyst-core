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

package power

import (
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PowerManagementConfiguration struct {
	DisablePowerAdvisor bool
	DisablePowerCapping bool
	PowerReductionRatio int
}

func NewPowerManagementConfiguration() *PowerManagementConfiguration {
	return &PowerManagementConfiguration{
		PowerReductionRatio: 10,
	}
}

func (c *PowerManagementConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if conf == nil {
		return
	}

	pmc := conf.PowerManagementConfiguration
	if pmc == nil {
		return
	}

	if disablePowerAdvisor := pmc.Spec.Config.DisablePowerAdvisor; disablePowerAdvisor != nil {
		c.DisablePowerAdvisor = *disablePowerAdvisor
	}

	if powerReductionRatio := pmc.Spec.Config.PowerReductionRatio; powerReductionRatio != nil {
		c.PowerReductionRatio = int(*powerReductionRatio)
	}

	if disablePowerCapping := pmc.Spec.Config.DisablePowerCapping; disablePowerCapping != nil {
		c.DisablePowerCapping = *disablePowerCapping
	}

	if klog.V(6).Enabled() {
		general.Infof("pap: kcc delivered pmc = %v", c)
	}
}
