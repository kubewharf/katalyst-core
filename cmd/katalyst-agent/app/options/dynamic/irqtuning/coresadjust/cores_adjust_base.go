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
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/coresadjust"
)

// IRQCoresAdjustOptions is the configuration for IRQCoresAdjustConfig.
type IRQCoresAdjustOptions struct {
	// minimum percent of (100 * irq cores/total(or socket) cores), valid value [0,100], default 2
	PercentMin int

	// maximum percent of (100 * irq cores/total(or socket) cores), valid value [0,100], default 30
	PercentMax int

	IncOptions *IRQCoresIncOptions
	DecOptions *IRQCoresDecOptions
}

func NewIRQCoresAdjustOptions() *IRQCoresAdjustOptions {
	return &IRQCoresAdjustOptions{
		PercentMin: 2,
		PercentMax: 30,
		IncOptions: NewIRQCoresIncOptions(),
		DecOptions: NewIRQCoresDecOptions(),
	}
}

func (o *IRQCoresAdjustOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("irq-cores-adjust")
	fs.IntVar(&o.PercentMin, "percent-min", o.PercentMin, "minimum percent of (100 * irq cores/total(or socket) cores), valid value [0,100], default 2")
	fs.IntVar(&o.PercentMax, "percent-max", o.PercentMax, "maximum percent of (100 * irq cores/total(or socket) cores), valid value [0,100], default 30")

	o.IncOptions.AddFlags(fss)
	o.DecOptions.AddFlags(fss)
}

func (o *IRQCoresAdjustOptions) ApplyTo(c *coresadjust.IRQCoresAdjustConfig) error {
	var errList []error
	c.PercentMin = o.PercentMin
	c.PercentMax = o.PercentMax

	errList = append(errList, o.IncOptions.ApplyTo(c.IncConf))
	errList = append(errList, o.DecOptions.ApplyTo(c.DecConf))

	return errors.NewAggregate(errList)
}
