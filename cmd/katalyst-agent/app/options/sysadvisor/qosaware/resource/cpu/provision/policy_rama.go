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

package provision

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	provisionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/provision"
)

type PolicyRamaOptions struct {
	RestrictedByRefPolicyName        string
	RestrictedByRefPolicyMaxGap      float64
	RestrictedByRefPolicyMaxGapRatio float64

	EnableBorwein                   bool
	EnableBorweinModelResultFetcher bool
}

func NewPolicyRamaOptions() *PolicyRamaOptions {
	return &PolicyRamaOptions{
		RestrictedByRefPolicyName:        "",
		RestrictedByRefPolicyMaxGap:      20,
		RestrictedByRefPolicyMaxGapRatio: 0.3,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *PolicyRamaOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.RestrictedByRefPolicyName, "restricted-by-ref-policy-name", o.RestrictedByRefPolicyName,
		"if set, it will restrict the non-reclaimed cpu size by the reference policy")
	fs.Float64Var(&o.RestrictedByRefPolicyMaxGap, "restricted-by-ref-policy-max-gap", o.RestrictedByRefPolicyMaxGap,
		"the max gap between the reference policy's result and the rama's result")
	fs.Float64Var(&o.RestrictedByRefPolicyMaxGapRatio, "restricted-by-ref-policy-max-gap-ratio", o.RestrictedByRefPolicyMaxGapRatio,
		"the max gap ratio between the reference policy's result and the rama's result")
	fs.BoolVar(&o.EnableBorwein, "enable-borwein-in-rama", o.EnableBorwein,
		"if set as true, enable borwein model to adjust target indicator offset in rama policy")
	fs.BoolVar(&o.EnableBorweinModelResultFetcher, "enable-borwein-model-result-fetcher", o.EnableBorweinModelResultFetcher,
		"if set as true, enable borwein model result fetcher to call borwein-inference-server and get results")
}

// ApplyTo fills up config with options
func (o *PolicyRamaOptions) ApplyTo(c *provisionconfig.PolicyRamaConfiguration) error {
	c.RestrictedByRefPolicyName = types.CPUProvisionPolicyName(o.RestrictedByRefPolicyName)
	c.RestrictedByRefPolicyMaxGap = o.RestrictedByRefPolicyMaxGap
	c.RestrictedByRefPolicyMaxGapRatio = o.RestrictedByRefPolicyMaxGapRatio
	c.EnableBorwein = o.EnableBorwein
	c.EnableBorweinModelResultFetcher = o.EnableBorweinModelResultFetcher
	return nil
}
