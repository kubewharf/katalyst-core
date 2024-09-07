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

package poweraware

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/poweraware"
)

type PowerAwarePluginOptions struct {
	disabled                  bool
	dryRun                    bool
	disablePowerCapping       bool
	disablePowerPressureEvict bool
}

func (p *PowerAwarePluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("power-aware-plugin")
	fs.BoolVar(&p.disabled, "power-aware-disabled", p.disabled, "flag for disabling power aware advisor")
	fs.BoolVar(&p.dryRun, "power-aware-dryrun", p.dryRun, "flag for dry run power aware advisor")
	fs.BoolVar(&p.disablePowerPressureEvict, "power-pressure-evict-disabled", p.disablePowerPressureEvict, "flag for power aware plugin disabling power pressure eviction")
	fs.BoolVar(&p.disablePowerCapping, "power-capping-disabled", p.disablePowerCapping, "flag for power aware plugin disabling power capping")
}

func (p *PowerAwarePluginOptions) ApplyTo(o *poweraware.PowerAwarePluginOptions) error {
	o.DryRun = p.dryRun
	o.Disabled = p.disabled
	o.DisablePowerPressureEvict = p.disablePowerPressureEvict
	o.DisablePowerCapping = p.disablePowerCapping
	return nil
}

// NewPowerAwarePluginOptions creates a new Options with a default config.
func NewPowerAwarePluginOptions() *PowerAwarePluginOptions {
	return &PowerAwarePluginOptions{}
}
