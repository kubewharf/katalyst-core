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
	DryRun                           bool
	DisablePowerCapping              bool
	DisablePowerPressureEvict        bool
	PowerCappingAdvisorSocketAbsPath string
	AnnotationKeyPrefix              string
	DVFSIndication                   string

	CPUHeadroomPowerDiscountP1 float64
	CPUHeadroomPowerDiscountP2 float64
	CPUHeadroomPowerDiscountP3 float64
}

func (p *PowerAwarePluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("power-aware-plugin")
	fs.BoolVar(&p.DryRun, "power-aware-dryrun", p.DryRun, "flag for dry run power aware advisor")
	fs.BoolVar(&p.DisablePowerPressureEvict, "power-pressure-evict-Disabled", p.DisablePowerPressureEvict, "flag for power aware plugin disabling power pressure eviction")
	fs.BoolVar(&p.DisablePowerCapping, "power-capping-Disabled", p.DisablePowerCapping, "flag for power aware plugin disabling power capping")
	fs.StringVar(&p.PowerCappingAdvisorSocketAbsPath, "power-capping-advisor-sock-abs-path", p.PowerCappingAdvisorSocketAbsPath, "absolute path of unix socket file for power capping advisor served in sys-advisor")
	fs.StringVar(&p.AnnotationKeyPrefix, "power-aware-annotation-key-prefix", p.AnnotationKeyPrefix, "prefix of node annotation keys used by power aware plugin")
	fs.StringVar(&p.DVFSIndication, "power-aware-dvfs-indication", p.DVFSIndication, "indication metric name of dvfs effect")
	fs.Float64Var(&p.CPUHeadroomPowerDiscountP1, "cpu-headroom-power-discount-p1", p.CPUHeadroomPowerDiscountP1, "discount rate of cpu headroom when power level is p1")
	fs.Float64Var(&p.CPUHeadroomPowerDiscountP2, "cpu-headroom-power-discount-p2", p.CPUHeadroomPowerDiscountP2, "discount rate of cpu headroom when power level is p2")
	fs.Float64Var(&p.CPUHeadroomPowerDiscountP3, "cpu-headroom-power-discount-p3", p.CPUHeadroomPowerDiscountP3, "discount rate of cpu headroom when power level is p3")
}

func (p *PowerAwarePluginOptions) ApplyTo(o *poweraware.PowerAwarePluginConfiguration) error {
	o.DryRun = p.DryRun
	o.DisablePowerPressureEvict = p.DisablePowerPressureEvict
	o.DisablePowerCapping = p.DisablePowerCapping
	o.PowerCappingAdvisorSocketAbsPath = p.PowerCappingAdvisorSocketAbsPath
	o.AnnotationKeyPrefix = p.AnnotationKeyPrefix
	o.DVFSIndication = p.DVFSIndication

	o.CPUHeadroomPowerDiscountP1 = p.CPUHeadroomPowerDiscountP1
	o.CPUHeadroomPowerDiscountP2 = p.CPUHeadroomPowerDiscountP2
	o.CPUHeadroomPowerDiscountP3 = p.CPUHeadroomPowerDiscountP3

	return nil
}

// NewPowerAwarePluginOptions creates a new Options with a default config.
func NewPowerAwarePluginOptions() *PowerAwarePluginOptions {
	return &PowerAwarePluginOptions{
		DVFSIndication:             poweraware.DVFSIndicationPower,
		CPUHeadroomPowerDiscountP1: 0.2,
		CPUHeadroomPowerDiscountP2: 0.4,
		CPUHeadroomPowerDiscountP3: 0.6,
	}
}
