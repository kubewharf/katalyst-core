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
	irqdynamicconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning"
)

type IRQTuningOptions struct {
	EnableTuner          bool
	TuningPolicy         string
	TuningInterval       int
	EnableRPS            bool
	NICAffinityPolicy    string
	ReniceKsoftirqd      bool
	KsoftirqdNice        int
	CoresExpectedCPUUtil int

	CoreNetOverLoadThresh *netoverload.IRQCoreNetOverloadThresholdOptions
	LoadBalanceOptions    *loadbalance.IRQLoadBalanceOptions
	CoresAdjustOptions    *coresadjust.IRQCoresAdjustOptions
	CoresExclusionOptions *coresexclusion.IRQCoresExclusionOptions
}

func NewIRQTuningOptions() *IRQTuningOptions {
	return &IRQTuningOptions{
		EnableTuner:          false,
		TuningPolicy:         string(v1alpha1.TuningPolicyBalance),
		TuningInterval:       5,
		EnableRPS:            false,
		NICAffinityPolicy:    string(v1alpha1.NICAffinityPolicyCompleteMap),
		ReniceKsoftirqd:      false,
		KsoftirqdNice:        -20,
		CoresExpectedCPUUtil: 50,

		CoreNetOverLoadThresh: netoverload.NewIRQCoreNetOverloadThresholdOptions(),
		LoadBalanceOptions:    loadbalance.NewIRQLoadBalanceOptions(),
		CoresAdjustOptions:    coresadjust.NewIRQCoresAdjustOptions(),
		CoresExclusionOptions: coresexclusion.NewIRQCoresExclusionOptions(),
	}
}

func (o *IRQTuningOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("irq-tuning")
	fs.BoolVar(&o.EnableTuner, "enable-tuner", o.EnableTuner, "enable irq tuner")
	fs.StringVar(&o.TuningPolicy, "tuning-policy", o.TuningPolicy, "irq tuning policy")
	fs.IntVar(&o.TuningInterval, "tuning-interval", o.TuningInterval, "irq tuning periodic interval")
	fs.BoolVar(&o.EnableRPS, "enable-rps", o.EnableRPS, "enable irq rps")
	fs.StringVar(&o.NICAffinityPolicy, "nic-affinity-policy", o.NICAffinityPolicy, "irq nic affinity policy")
	fs.BoolVar(&o.ReniceKsoftirqd, "renice-ksoftirqd", o.ReniceKsoftirqd, "renice ksoftirqd")
	fs.IntVar(&o.KsoftirqdNice, "ksoftirqd-nice", o.KsoftirqdNice, "ksoftirqd nice")
	fs.IntVar(&o.CoresExpectedCPUUtil, "cores-expected-cpu-util", o.CoresExpectedCPUUtil, "irq cores expected cpu util")

	o.CoreNetOverLoadThresh.AddFlags(fss)
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
	c.NICAffinityPolicy = v1alpha1.NICAffinityPolicy(o.NICAffinityPolicy)

	c.ReniceKsoftirqd = o.ReniceKsoftirqd
	c.KsoftirqdNice = o.KsoftirqdNice

	c.CoresExpectedCPUUtil = o.CoresExpectedCPUUtil

	errList = append(errList, o.CoreNetOverLoadThresh.ApplyTo(c.CoreNetOverLoadThresh))
	errList = append(errList, o.LoadBalanceOptions.ApplyTo(c.LoadBalanceConf))
	errList = append(errList, o.CoresAdjustOptions.ApplyTo(c.CoresAdjustConf))
	errList = append(errList, o.CoresExclusionOptions.ApplyTo(c.CoresExclusionConf))
	return errors.NewAggregate(errList)
}
