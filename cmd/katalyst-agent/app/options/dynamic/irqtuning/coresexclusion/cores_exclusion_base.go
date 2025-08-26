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

package coresexclusion

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/coresexclusion"
)

type IRQCoresExclusionOptions struct {
	Thresholds *IRQCoresExclusionThresholdOptions
	// Interval of successive enable/disable irq cores exclusion MUST >= SuccessiveSwitchInterval
	SuccessiveSwitchInterval float64
}

func NewIRQCoresExclusionOptions() *IRQCoresExclusionOptions {
	return &IRQCoresExclusionOptions{
		Thresholds:               NewIRQCoresExclusionThresholdOptions(),
		SuccessiveSwitchInterval: 600,
	}
}

func (o *IRQCoresExclusionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("irq-cores-exclusion")
	fs.Float64Var(&o.SuccessiveSwitchInterval, "successive-switch-interval", o.SuccessiveSwitchInterval, "interval of successive enable/disable irq cores exclusion MUST >= SuccessiveSwitchInterval")

	o.Thresholds.AddFlags(fss)
}

func (o *IRQCoresExclusionOptions) ApplyTo(c *coresexclusion.IRQCoresExclusionConfig) error {
	c.SuccessiveSwitchInterval = o.SuccessiveSwitchInterval

	err := o.Thresholds.ApplyTo(c.Thresholds)
	return err
}
