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

package coresadjust

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/coresadjust"
)

type IRQCoresDecOptions struct {
	// interval of two successive irq cores decrease MUST greater-equal this interval
	SuccessiveDecInterval    int
	PingPongAdjustInterval   int
	SinceLastBalanceInterval int
	// max cores to decrease each time, deault 1
	DecCoresMaxEachTime int
	Thresholds          *IRQCoresDecThresholds
}

type IRQCoresDecThresholds struct {
	// threshold of decreasing irq cores, generally this threshold should be less-than IrqCoresExpectedCpuUtil
	AvgCPUUtilThreshold int
}

func NewIRQCoresDecOptions() *IRQCoresDecOptions {
	return &IRQCoresDecOptions{
		SuccessiveDecInterval:    30,
		PingPongAdjustInterval:   300,
		SinceLastBalanceInterval: 60,
		DecCoresMaxEachTime:      1,
		Thresholds: &IRQCoresDecThresholds{
			AvgCPUUtilThreshold: 40,
		},
	}
}

func (o *IRQCoresDecOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("irq-cores-decrease")
	fs.IntVar(&o.SuccessiveDecInterval, "successive-dec-interval", o.SuccessiveDecInterval, "interval of two successive irq cores decrease MUST greater-equal this interval")
	fs.IntVar(&o.PingPongAdjustInterval, "ping-pong-adjust-interval", o.PingPongAdjustInterval, "interval of ping-pong adjust")
	fs.IntVar(&o.SinceLastBalanceInterval, "since-last-balance-interval", o.SinceLastBalanceInterval, "interval of since last balance")
	fs.IntVar(&o.DecCoresMaxEachTime, "dec-cores-max-each-time", o.DecCoresMaxEachTime, "max cores to decrease each time, deault 1")
	fs.IntVar(&o.Thresholds.AvgCPUUtilThreshold, "avg-cpu-util-threshold", o.Thresholds.AvgCPUUtilThreshold, "threshold of decreasing irq cores, generally this threshold should be less-than IrqCoresExpectedCpuUtil")
}

func (o *IRQCoresDecOptions) ApplyTo(c *coresadjust.IRQCoresDecConfig) error {
	c.SuccessiveDecInterval = o.SuccessiveDecInterval
	c.PingPongAdjustInterval = o.PingPongAdjustInterval
	c.SinceLastBalanceInterval = o.SinceLastBalanceInterval
	c.DecCoresMaxEachTime = o.DecCoresMaxEachTime
	c.Thresholds.AvgCPUUtilThreshold = o.Thresholds.AvgCPUUtilThreshold

	return nil
}
