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

package irqtuning

import (
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/coresadjust"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/coresexclusion"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/loadbalance"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/netoverload"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/rpsexcludeirqcore"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/throughputclassswitch"
)

// IRQTuningConfiguration is the configuration for irq tuner.
type IRQTuningConfiguration struct {
	EnableTuner             bool
	TuningPolicy            v1alpha1.TuningPolicy
	TuningInterval          int
	EnableRPS               bool
	EnableRPSCPUVSNicsQueue float64
	NICAffinityPolicy       v1alpha1.NICAffinityPolicy
	ReniceKsoftirqd         bool
	KsoftirqdNice           int
	CoresExpectedCPUUtil    int

	RPSExcludeIRQCoresThreshold *rpsexcludeirqcore.RPSExcludeIRQCoresThreshold
	ThroughputClassSwitchConf   *throughputclassswitch.ThroughputClassSwitchConfig
	CoreNetOverLoadThreshold    *netoverload.IRQCoreNetOverloadThresholds
	LoadBalanceConf             *loadbalance.IRQLoadBalanceConfig
	CoresAdjustConf             *coresadjust.IRQCoresAdjustConfig
	CoresExclusionConf          *coresexclusion.IRQCoresExclusionConfig
}

func NewIRQTuningConfiguration() *IRQTuningConfiguration {
	return &IRQTuningConfiguration{
		EnableTuner:             false,
		TuningPolicy:            v1alpha1.TuningPolicyBalance,
		TuningInterval:          5,
		EnableRPS:               false,
		EnableRPSCPUVSNicsQueue: 0,
		NICAffinityPolicy:       v1alpha1.NICAffinityPolicyCompleteMap,
		ReniceKsoftirqd:         false,
		KsoftirqdNice:           -20,
		CoresExpectedCPUUtil:    50,

		RPSExcludeIRQCoresThreshold: rpsexcludeirqcore.NewRPSExcludeIRQCoresThreshold(),
		ThroughputClassSwitchConf:   throughputclassswitch.NewThroughputClassSwitchConfig(),
		CoreNetOverLoadThreshold:    netoverload.NewIRQCoreNetOverloadThresholds(),
		LoadBalanceConf:             loadbalance.NewIRQLoadBalanceConfig(),
		CoresAdjustConf:             coresadjust.NewIRQCoresAdjustConfig(),
		CoresExclusionConf:          coresexclusion.NewIRQCoresExclusionConfig(),
	}
}

func (c *IRQTuningConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil {
		if itc.Spec.Config.EnableTuner != nil {
			c.EnableTuner = *itc.Spec.Config.EnableTuner
		}
		c.TuningPolicy = itc.Spec.Config.TuningPolicy
		if itc.Spec.Config.TuningInterval != nil {
			c.TuningInterval = *itc.Spec.Config.TuningInterval
		}
		if itc.Spec.Config.EnableRPS != nil {
			c.EnableRPS = *itc.Spec.Config.EnableRPS
		}
		if itc.Spec.Config.EnableRPSCPUVSNicsQueue != nil {
			c.EnableRPSCPUVSNicsQueue = *itc.Spec.Config.EnableRPSCPUVSNicsQueue
		}
		c.NICAffinityPolicy = itc.Spec.Config.NICAffinityPolicy
		if itc.Spec.Config.ReniceKsoftirqd != nil {
			c.ReniceKsoftirqd = *itc.Spec.Config.ReniceKsoftirqd
		}
		if itc.Spec.Config.KsoftirqdNice != nil {
			c.KsoftirqdNice = *itc.Spec.Config.KsoftirqdNice
		}
		if itc.Spec.Config.CoresExpectedCPUUtil != nil {
			c.CoresExpectedCPUUtil = *itc.Spec.Config.CoresExpectedCPUUtil
		}

		c.RPSExcludeIRQCoresThreshold.ApplyConfiguration(conf)
		c.ThroughputClassSwitchConf.ApplyConfiguration(conf)
		c.CoreNetOverLoadThreshold.ApplyConfiguration(conf)
		c.LoadBalanceConf.ApplyConfiguration(conf)
		c.CoresAdjustConf.ApplyConfiguration(conf)
		c.CoresExclusionConf.ApplyConfiguration(conf)
	}
}
