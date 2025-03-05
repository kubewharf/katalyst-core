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

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/loadbalance"
)

type IRQLoadBalanceTuningThresholdOptions struct {
	// irq core cpu util threshold, which will trigger irq cores load balance, generally this value should greater-equal IrqCoresExpectedCpuUtil
	CPUUtilThresh int
	// threshold of cpu util gap between source core and dest core of irq affinity changing
	CPUUtilGapThresh int
}

func NewIRQLoadBalanceTuningThresholdOptions() *IRQLoadBalanceTuningThresholdOptions {
	return &IRQLoadBalanceTuningThresholdOptions{
		CPUUtilThresh:    65,
		CPUUtilGapThresh: 20,
	}
}

func (o *IRQLoadBalanceTuningThresholdOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("load-balance-thresholds")
	fs.IntVar(&o.CPUUtilThresh, "cpu-util-thresh", o.CPUUtilThresh, "irq core cpu util threshold, which will trigger irq cores load balance, generally this value should greater-equal IrqCoresExpectedCpuUtil")
	fs.IntVar(&o.CPUUtilGapThresh, "cpu-util-gap-thresh", o.CPUUtilGapThresh, "threshold of cpu util gap between source core and dest core of irq affinity changing")
}

func (o *IRQLoadBalanceTuningThresholdOptions) ApplyTo(c *loadbalance.IRQLoadBalanceTuningThresholds) error {
	c.CPUUtilThresh = o.CPUUtilThresh
	c.CPUUtilGapThresh = o.CPUUtilGapThresh

	return nil
}
