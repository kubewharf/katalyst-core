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

package power

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/power"
)

type PowerOptions struct {
	DisablePowerAdvisor bool
	DisablePowerCapping bool

	// PowerReductionRatio is the percentage ratio the power usage is allowed to reduce
	PowerReductionRatio int
}

func NewPowerOptions() *PowerOptions {
	return &PowerOptions{
		DisablePowerAdvisor: true,
		DisablePowerCapping: true,
		PowerReductionRatio: 10,
	}
}

func (o *PowerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("power-management")
	fs.BoolVar(&o.DisablePowerCapping, "disable-power-capping", o.DisablePowerCapping, "disable power capping")
	fs.BoolVar(&o.DisablePowerAdvisor, "disable-power-advisor", o.DisablePowerAdvisor, "disable power advisor")
	fs.IntVar(&o.PowerReductionRatio, "power-reduction-ratio", o.PowerReductionRatio, "allowed power reduction percentage")
}

func (o *PowerOptions) ApplyTo(c *power.PowerManagementConfiguration) error {
	c.DisablePowerCapping = o.DisablePowerCapping
	c.DisablePowerAdvisor = o.DisablePowerAdvisor
	c.PowerReductionRatio = o.PowerReductionRatio
	return nil
}
