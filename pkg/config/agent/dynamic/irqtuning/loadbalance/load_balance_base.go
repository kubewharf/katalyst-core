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

// IRQLoadBalanceConfig is the configuration for IRQLoadBalanceConfig.
type IRQLoadBalanceConfig struct {
	// interval of two successive irq load balance MUST greater-equal this interval
	SuccessiveTuningInterval int
	Thresholds               *IRQLoadBalanceTuningThresholds
	// two successive tunes whose interval is less-equal this threshold will be considered as pingpong tunings
	PingPongIntervalThreshold int
	// ping pong count greater-equal this threshold will trigger increasing irq cores
	PingPongCountThreshold int
	// max number of irqs are permitted to be tuned from some irq cores to other cores in each time, allowed value {1, 2}
	IRQsTunedNumMaxEachTime int
	// max number of irq cores whose affinitied irqs are permitted to tuned to other cores in each time, allowed value {1,2}
	IRQCoresTunedNumMaxEachTime int
}

func NewIRQLoadBalanceConfig() *IRQLoadBalanceConfig {
	return &IRQLoadBalanceConfig{
		SuccessiveTuningInterval:    10,
		Thresholds:                  NewIRQLoadBalanceTuningThresholds(),
		PingPongIntervalThreshold:   180,
		PingPongCountThreshold:      1,
		IRQsTunedNumMaxEachTime:     2,
		IRQCoresTunedNumMaxEachTime: 1,
	}
}

func (c *IRQLoadBalanceConfig) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil &&
		itc.Spec.Config.LoadBalance != nil {
		config := itc.Spec.Config.LoadBalance

		if config.SuccessiveTuningInterval != nil {
			c.SuccessiveTuningInterval = *config.SuccessiveTuningInterval
		}
		if config.Thresholds != nil {
			c.Thresholds.ApplyConfiguration(conf)
		}
		if config.PingPongIntervalThreshold != nil {
			c.PingPongIntervalThreshold = *config.PingPongIntervalThreshold
		}
		if config.PingPongCountThreshold != nil {
			c.PingPongCountThreshold = *config.PingPongCountThreshold
		}
		if config.IRQTunedNumMaxEachTime != nil {
			c.IRQsTunedNumMaxEachTime = *config.IRQTunedNumMaxEachTime
		}
		if config.IRQCoresTunedNumMaxEachTime != nil {
			c.IRQCoresTunedNumMaxEachTime = *config.IRQCoresTunedNumMaxEachTime
		}
	}
}
