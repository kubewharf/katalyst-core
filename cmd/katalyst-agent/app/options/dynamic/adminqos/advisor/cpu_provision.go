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

package advisor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
)

type PolicyRamaOptions struct {
	EnableBorwein                   bool
	EnableBorweinModelResultFetcher bool
}

func NewPolicyRamaOptions() *PolicyRamaOptions {
	return &PolicyRamaOptions{}
}

// AddFlags adds flags to the specified FlagSet.
func (o *PolicyRamaOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.EnableBorwein, "enable-borwein-in-rama", o.EnableBorwein,
		"if set as true, enable borwein model to adjust target indicator offset in rama policy")
	fs.BoolVar(&o.EnableBorweinModelResultFetcher, "enable-borwein-model-result-fetcher", o.EnableBorweinModelResultFetcher,
		"if set as true, enable borwein model result fetcher to call borwein-inference-server and get results")
}

// ApplyTo fills up config with options
func (o *PolicyRamaOptions) ApplyTo(c *v1alpha1.PolicyRamaConfiguration) error {
	c.EnableBorwein = o.EnableBorwein
	c.EnableBorweinModelResultFetcher = o.EnableBorweinModelResultFetcher
	return nil
}

type CPUProvisionOptions struct {
	PolicyRama                   *PolicyRamaOptions
	RegionIndicatorTargetOptions map[string]string
}

func NewCPUProvisionOptions() *CPUProvisionOptions {
	return &CPUProvisionOptions{
		PolicyRama:                   NewPolicyRamaOptions(),
		RegionIndicatorTargetOptions: map[string]string{},
	}
}

// ApplyTo fills up config with options
func (o *CPUProvisionOptions) ApplyTo(c *advisor.CPUProvisionConfiguration) error {
	var errList []error
	errList = append(errList, o.PolicyRama.ApplyTo(c.PolicyRama))

	for regionType, targets := range o.RegionIndicatorTargetOptions {
		regionIndicatorTarget := make([]v1alpha1.IndicatorTargetConfiguration, 0)
		indicatorTargets := strings.Split(targets, "/")
		for _, indicatorTarget := range indicatorTargets {
			tmp := strings.Split(indicatorTarget, "=")
			if len(tmp) != 2 {
				errList = append(errList, fmt.Errorf("indicatorTarget %v is invalid", indicatorTarget))
				continue
			}
			target, err := strconv.ParseFloat(tmp[1], 64)
			if err != nil {
				errList = append(errList, err)
				continue
			}
			regionIndicatorTarget = append(regionIndicatorTarget, v1alpha1.IndicatorTargetConfiguration{Name: tmp[0], Target: target})
		}
		c.RegionIndicatorTargetConfiguration[regionType] = regionIndicatorTarget
	}

	return errors.NewAggregate(errList)
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUProvisionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("cpu-provision")
	o.PolicyRama.AddFlags(fs)
	fs.StringToStringVar(&o.RegionIndicatorTargetOptions, "region-indicator-targets", o.RegionIndicatorTargetOptions,
		"indicators targets for each region, in format like cpu_sched_wait=400/cpu_iowait_ratio=0.8")
}
