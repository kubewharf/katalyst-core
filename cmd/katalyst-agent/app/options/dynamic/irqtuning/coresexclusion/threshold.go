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

type IRQCoresExclusionThresholdOptions struct {
	EnableThresholds  *EnableIRQCoresExclusionThresholds
	DisableThresholds *DisableIRQCoresExclusionThresholds
}

type EnableIRQCoresExclusionThresholds struct {
	RxPPSThresh     uint64
	SuccessiveCount int
}

type DisableIRQCoresExclusionThresholds struct {
	RxPPSThresh     uint64
	SuccessiveCount int
}

func NewIRQCoresExclusionThresholdOptions() *IRQCoresExclusionThresholdOptions {
	return &IRQCoresExclusionThresholdOptions{
		EnableThresholds: &EnableIRQCoresExclusionThresholds{
			RxPPSThresh:     60000,
			SuccessiveCount: 30,
		},
		DisableThresholds: &DisableIRQCoresExclusionThresholds{
			RxPPSThresh:     30000,
			SuccessiveCount: 30,
		},
	}
}

func (o *IRQCoresExclusionThresholdOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("exclusion-thresholds")
	fs.Uint64Var(&o.EnableThresholds.RxPPSThresh, "enable-thresholds-rxpps-thresh", o.EnableThresholds.RxPPSThresh, "irq cores exclusion enable thresholds rxpps thresh")
	fs.IntVar(&o.EnableThresholds.SuccessiveCount, "enable-thresholds-successive-count", o.EnableThresholds.SuccessiveCount, "irq cores exclusion enable thresholds successive count")
	fs.Uint64Var(&o.DisableThresholds.RxPPSThresh, "disable-thresholds-rxpps-thresh", o.DisableThresholds.RxPPSThresh, "irq cores exclusion disable thresholds rxpps thresh")
	fs.IntVar(&o.DisableThresholds.SuccessiveCount, "disable-thresholds-successive-count", o.DisableThresholds.SuccessiveCount, "irq cores exclusion disable thresholds successive count")
}

func (o *IRQCoresExclusionThresholdOptions) ApplyTo(c *coresexclusion.IRQCoresExclusionThresholds) error {
	c.EnableThresholds.RxPPSThresh = o.EnableThresholds.RxPPSThresh
	c.EnableThresholds.SuccessiveCount = o.EnableThresholds.SuccessiveCount
	c.DisableThresholds.RxPPSThresh = o.DisableThresholds.RxPPSThresh
	c.DisableThresholds.SuccessiveCount = o.DisableThresholds.SuccessiveCount
	return nil
}
