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

package cpu

import (
	"strconv"

	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
)

type CPUShareOptions struct {
	RestrictRefPolicy              map[string]string
	RestrictControlKnobMaxGap      map[string]string
	RestrictControlKnobMaxGapRatio map[string]string
}

// NewCPUShareOptions creates a new Options with a default config
func NewCPUShareOptions() *CPUShareOptions {
	return &CPUShareOptions{
		RestrictRefPolicy: map[string]string{
			string(types.CPUProvisionPolicyRama): string(types.CPUProvisionPolicyCanonical),
		},
		RestrictControlKnobMaxGap: map[string]string{
			string(types.ControlKnobNonReclaimedCPUSize): "20",
		},
		RestrictControlKnobMaxGapRatio: map[string]string{
			string(types.ControlKnobNonReclaimedCPUSize): "0.3",
		},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUShareOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringToStringVar(&o.RestrictRefPolicy, "share-restrict-ref-policy", o.RestrictRefPolicy,
		"the provision policy map is used to restrict the control knob of base policy by the one of reference for share region")
	fs.StringToStringVar(&o.RestrictControlKnobMaxGap, "share-restrict-control-knob-max-gap", o.RestrictControlKnobMaxGap,
		"the max gap between the reference policy's control knob and the base one")
	fs.StringToStringVar(&o.RestrictControlKnobMaxGapRatio, "share-restrict-control-knob-max-gap-ratio", o.RestrictControlKnobMaxGapRatio,
		"the max gap ratio between the reference policy's control knob and and the base one")
}

// ApplyTo fills up config with options
func (o *CPUShareOptions) ApplyTo(c *cpu.CPUShareConfiguration) error {
	restrictRefPolicy := make(map[types.CPUProvisionPolicyName]types.CPUProvisionPolicyName)
	for k, v := range o.RestrictRefPolicy {
		restrictRefPolicy[types.CPUProvisionPolicyName(k)] = types.CPUProvisionPolicyName(v)
	}
	c.RestrictRefPolicy = restrictRefPolicy

	restrictControlKnobMaxGap, err := stringMapToControlKnobMap(o.RestrictControlKnobMaxGap)
	if err != nil {
		return err
	}
	c.RestrictControlKnobMaxGap = restrictControlKnobMaxGap

	restrictControlKnobMaxGapRatio, err := stringMapToControlKnobMap(o.RestrictControlKnobMaxGapRatio)
	if err != nil {
		return err
	}
	c.RestrictControlKnobMaxGapRatio = restrictControlKnobMaxGapRatio

	return nil
}

func stringMapToControlKnobMap(origin map[string]string) (map[types.ControlKnobName]float64, error) {
	controlKnobMap := make(map[types.ControlKnobName]float64)
	for k, v := range origin {
		val, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, err
		}
		controlKnobMap[types.ControlKnobName(k)] = val
	}
	return controlKnobMap, nil
}
