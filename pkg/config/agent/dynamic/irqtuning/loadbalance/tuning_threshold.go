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

package loadbalance

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type IRQLoadBalanceTuningThresholds struct {
	// irq core cpu util threshold, which will trigger irq cores load balance, generally this value should greater-equal IrqCoresExpectedCpuUtil
	CPUUtilThreshold int
	// threshold of cpu util gap between source core and dest core of irq affinity changing
	CPUUtilGapThreshold int
}

func NewIRQLoadBalanceTuningThresholds() *IRQLoadBalanceTuningThresholds {
	return &IRQLoadBalanceTuningThresholds{
		CPUUtilThreshold:    65,
		CPUUtilGapThreshold: 20,
	}
}

func (c *IRQLoadBalanceTuningThresholds) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil &&
		itc.Spec.Config.LoadBalance != nil &&
		itc.Spec.Config.LoadBalance.Thresholds != nil {
		config := itc.Spec.Config.LoadBalance.Thresholds

		if config.CPUUtilThreshold != nil {
			c.CPUUtilThreshold = *config.CPUUtilThreshold
		}
		if config.CPUUtilGapThreshold != nil {
			c.CPUUtilGapThreshold = *config.CPUUtilGapThreshold
		}
	}
}
