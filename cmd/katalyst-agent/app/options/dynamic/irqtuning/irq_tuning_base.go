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
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/coresadjust"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/coresexclusion"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/loadbalance"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/netoverload"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/rpsexcludeirqcore"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/throughputclassswitch"
	irqdynamicconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning"
)

type IRQTuningOptions struct {
	// EnableTuner indicates whether to enable the interrupt tuning function.
	EnableTuner bool
	// TuningPolicy represents the interrupt tuning strategy. One of Balance, Exclusive, Auto.
	TuningPolicy string
	// TuningInterval is the interval of interrupt tuning.
	TuningInterval int
	// EnableRPS indicates whether to enable the RPS function.
	EnableRPS bool
	// EnableRPSCPUVSNicsQueue enable rps when (cpus count)/(nics queue count) greater than this config.
	EnableRPSCPUVSNicsQueue float64
	// NICAffinityPolicy represents the NICs's irqs affinity sockets policy.
	NICAffinityPolicy string
	// ReniceKsoftirqd indicates whether to renice ksoftirqd process.
	ReniceKsoftirqd bool
	// KsoftirqdNice is the nice value of ksoftirqd process.
	KsoftirqdNice int
	// CoresExpectedCPUUtil is the expected CPU utilization of cores.
	CoresExpectedCPUUtil int

	// RPSExcludeIRQCoresThreshold describes the threshold of excluding irq cores for rps.
	RPSExcludeIRQCoresThreshold *rpsexcludeirqcore.RPSExcludeIRQCoresThreshold
	// ThroughputClassSwitchOptions describes the switch configuration for a throughput class.
	ThroughputClassSwitchOptions *throughputclassswitch.ThroughputClassSwitchOptions
	// Threshold description for interrupting core network overLoad.
	CoreNetOverLoadThreshold *netoverload.IRQCoreNetOverloadThresholdOptions
	// Describes the constraints of the balanced configuration.
	LoadBalanceOptions *loadbalance.IRQLoadBalanceOptions
	// Configuration that requires interrupt core adjustment.
	CoresAdjustOptions *coresadjust.IRQCoresAdjustOptions
	// Need to adjust to interrupt exclusive core requirements.
	CoresExclusionOptions *coresexclusion.IRQCoresExclusionOptions
}

func NewIRQTuningOptions() *IRQTuningOptions {
	return &IRQTuningOptions{
		EnableTuner:             false,
		TuningPolicy:            string(v1alpha1.TuningPolicyBalance),
		TuningInterval:          5,
		EnableRPS:               false,
		EnableRPSCPUVSNicsQueue: 0,
		NICAffinityPolicy:       string(v1alpha1.NICAffinityPolicyCompleteMap),
		ReniceKsoftirqd:         false,
		KsoftirqdNice:           -20,
		CoresExpectedCPUUtil:    50,

		RPSExcludeIRQCoresThreshold:  rpsexcludeirqcore.NewRPSExcludeIRQCoresThreshold(),
		ThroughputClassSwitchOptions: throughputclassswitch.NewThroughputClassSwitchOptions(),
		CoreNetOverLoadThreshold:     netoverload.NewIRQCoreNetOverloadThresholdOptions(),
		LoadBalanceOptions:           loadbalance.NewIRQLoadBalanceOptions(),
		CoresAdjustOptions:           coresadjust.NewIRQCoresAdjustOptions(),
		CoresExclusionOptions:        coresexclusion.NewIRQCoresExclusionOptions(),
	}
}

func (o *IRQTuningOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("irq-tuning")
	fs.BoolVar(&o.EnableTuner, "enable-tuner", o.EnableTuner, "enable irq tuner")
	fs.StringVar(&o.TuningPolicy, "tuning-policy", o.TuningPolicy, "irq tuning policy")
	fs.IntVar(&o.TuningInterval, "tuning-interval", o.TuningInterval, "irq tuning periodic interval")
	fs.BoolVar(&o.EnableRPS, "enable-rps", o.EnableRPS, "enable irq rps")
	fs.Float64Var(&o.EnableRPSCPUVSNicsQueue, "enable-rps-cpu-vs-nics-queue", o.EnableRPSCPUVSNicsQueue, "enable rps cpu vs nics queue")
	fs.StringVar(&o.NICAffinityPolicy, "nic-affinity-policy", o.NICAffinityPolicy, "irq nic affinity policy")
	fs.BoolVar(&o.ReniceKsoftirqd, "renice-ksoftirqd", o.ReniceKsoftirqd, "renice ksoftirqd")
	fs.IntVar(&o.KsoftirqdNice, "ksoftirqd-nice", o.KsoftirqdNice, "ksoftirqd nice")
	fs.IntVar(&o.CoresExpectedCPUUtil, "cores-expected-cpu-util", o.CoresExpectedCPUUtil, "irq cores expected cpu util")

	o.RPSExcludeIRQCoresThreshold.AddFlags(fss)
	o.ThroughputClassSwitchOptions.AddFlags(fss)
	o.CoreNetOverLoadThreshold.AddFlags(fss)
	o.LoadBalanceOptions.AddFlags(fss)
	o.CoresAdjustOptions.AddFlags(fss)
	o.CoresExclusionOptions.AddFlags(fss)
}

func (o *IRQTuningOptions) ApplyTo(c *irqdynamicconf.IRQTuningConfiguration) error {
	var errList []error
	c.EnableTuner = o.EnableTuner
	c.TuningPolicy = v1alpha1.TuningPolicy(o.TuningPolicy)
	c.TuningInterval = o.TuningInterval

	c.EnableRPS = o.EnableRPS
	c.EnableRPSCPUVSNicsQueue = o.EnableRPSCPUVSNicsQueue
	c.NICAffinityPolicy = v1alpha1.NICAffinityPolicy(o.NICAffinityPolicy)

	c.ReniceKsoftirqd = o.ReniceKsoftirqd
	c.KsoftirqdNice = o.KsoftirqdNice

	c.CoresExpectedCPUUtil = o.CoresExpectedCPUUtil

	errList = append(errList, o.RPSExcludeIRQCoresThreshold.ApplyTo(c.RPSExcludeIRQCoresThreshold))
	errList = append(errList, o.ThroughputClassSwitchOptions.ApplyTo(c.ThroughputClassSwitchConf))
	errList = append(errList, o.CoreNetOverLoadThreshold.ApplyTo(c.CoreNetOverLoadThreshold))
	errList = append(errList, o.LoadBalanceOptions.ApplyTo(c.LoadBalanceConf))
	errList = append(errList, o.CoresAdjustOptions.ApplyTo(c.CoresAdjustConf))
	errList = append(errList, o.CoresExclusionOptions.ApplyTo(c.CoresExclusionConf))
	return errors.NewAggregate(errList)
}
