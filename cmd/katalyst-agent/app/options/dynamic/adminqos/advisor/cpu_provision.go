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
	workloadapi "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
)

func stringMapToControlKnobMap(origin map[string]string) (map[string]float64, error) {
	controlKnobMap := make(map[string]float64)
	for k, v := range origin {
		val, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, err
		}
		controlKnobMap[k] = val
	}
	return controlKnobMap, nil
}

type ControlKnobConstrains struct {
	RestrictControlKnobMaxUpperGap      map[string]string
	RestrictControlKnobMaxLowerGap      map[string]string
	RestrictControlKnobMaxUpperGapRatio map[string]string
	RestrictControlKnobMaxLowerGapRatio map[string]string
}

func NewControlKnobConstrains() *ControlKnobConstrains {
	return &ControlKnobConstrains{
		RestrictControlKnobMaxUpperGap: map[string]string{
			string(v1alpha1.ControlKnobNonReclaimedCPURequirement): "20",
		},
		RestrictControlKnobMaxLowerGap: map[string]string{
			string(v1alpha1.ControlKnobNonReclaimedCPURequirement): "20",
		},
		RestrictControlKnobMaxUpperGapRatio: map[string]string{
			string(v1alpha1.ControlKnobNonReclaimedCPURequirement): "0.3",
		},
		RestrictControlKnobMaxLowerGapRatio: map[string]string{
			string(v1alpha1.ControlKnobNonReclaimedCPURequirement): "0.3",
		},
	}
}

func (o *ControlKnobConstrains) AddFlags(fs *pflag.FlagSet) {
	fs.StringToStringVar(&o.RestrictControlKnobMaxUpperGap, "share-restrict-control-knob-max-upper-gap", o.RestrictControlKnobMaxUpperGap,
		"the max upper gap between the reference policy's control knob and the base one")
	fs.StringToStringVar(&o.RestrictControlKnobMaxLowerGap, "share-restrict-control-knob-max-lower-gap", o.RestrictControlKnobMaxLowerGap,
		"the max lower gap between the reference policy's control knob and the base one")
	fs.StringToStringVar(&o.RestrictControlKnobMaxUpperGapRatio, "share-restrict-control-knob-max-upper-gap-ratio", o.RestrictControlKnobMaxUpperGapRatio,
		"the max upper gap ratio between the reference policy's control knob and and the base one")
	fs.StringToStringVar(&o.RestrictControlKnobMaxLowerGapRatio, "share-restrict-control-knob-max-lower-gap-ratio", o.RestrictControlKnobMaxLowerGapRatio,
		"the max lower gap ratio between the reference policy's control knob and and the base one")
}

func (o *ControlKnobConstrains) ApplyTo(c map[v1alpha1.ControlKnobName]v1alpha1.RestrictConstraints) error {
	restrictControlKnobMaxUpperGap, err := stringMapToControlKnobMap(o.RestrictControlKnobMaxUpperGap)
	if err != nil {
		return err
	}
	for name, maxUpperGap := range restrictControlKnobMaxUpperGap {
		gap, ok := c[v1alpha1.ControlKnobName(name)]
		if !ok {
			gap = v1alpha1.RestrictConstraints{}
		}
		gap.MaxUpperGap = &maxUpperGap
		c[v1alpha1.ControlKnobName(name)] = gap
	}

	restrictControlKnobMaxLowerGap, err := stringMapToControlKnobMap(o.RestrictControlKnobMaxLowerGap)
	if err != nil {
		return err
	}
	for name, maxLowerGap := range restrictControlKnobMaxLowerGap {
		gap, ok := c[v1alpha1.ControlKnobName(name)]
		if !ok {
			gap = v1alpha1.RestrictConstraints{}
		}
		gap.MaxLowerGap = &maxLowerGap
		c[v1alpha1.ControlKnobName(name)] = gap
	}

	restrictControlKnobMaxUpperGapRatio, err := stringMapToControlKnobMap(o.RestrictControlKnobMaxUpperGapRatio)
	if err != nil {
		return err
	}
	for name, maxUpperGapRatio := range restrictControlKnobMaxUpperGapRatio {
		gap, ok := c[v1alpha1.ControlKnobName(name)]
		if !ok {
			gap = v1alpha1.RestrictConstraints{}
		}
		gap.MaxUpperGapRatio = &maxUpperGapRatio
		c[v1alpha1.ControlKnobName(name)] = gap
	}

	restrictControlKnobMaxLowerGapRatio, err := stringMapToControlKnobMap(o.RestrictControlKnobMaxLowerGapRatio)
	if err != nil {
		return err
	}
	for name, maxLowerGapRatio := range restrictControlKnobMaxLowerGapRatio {
		gap, ok := c[v1alpha1.ControlKnobName(name)]
		if !ok {
			gap = v1alpha1.RestrictConstraints{}
		}
		gap.MaxLowerGapRatio = &maxLowerGapRatio
		c[v1alpha1.ControlKnobName(name)] = gap
	}

	return nil
}

type CPUProvisionOptions struct {
	AllowSharedCoresOverlapReclaimedCores bool
	RegionIndicatorTargetOptions          map[string]string
	ControlKnobConstrains                 *ControlKnobConstrains
}

func NewCPUProvisionOptions() *CPUProvisionOptions {
	return &CPUProvisionOptions{
		AllowSharedCoresOverlapReclaimedCores: false,
		RegionIndicatorTargetOptions:          map[string]string{},
		ControlKnobConstrains:                 NewControlKnobConstrains(),
	}
}

// ApplyTo fills up config with options
func (o *CPUProvisionOptions) ApplyTo(c *advisor.CPUProvisionConfiguration) error {
	var errList []error
	c.AllowSharedCoresOverlapReclaimedCores = o.AllowSharedCoresOverlapReclaimedCores

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
			regionIndicatorTarget = append(regionIndicatorTarget, v1alpha1.IndicatorTargetConfiguration{Name: workloadapi.ServiceSystemIndicatorName(tmp[0]), Target: target})
		}
		c.RegionIndicatorTargetConfiguration[v1alpha1.QoSRegionType(regionType)] = regionIndicatorTarget
	}
	errList = append(errList, o.ControlKnobConstrains.ApplyTo(c.RestrictConstraints))

	return errors.NewAggregate(errList)
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUProvisionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("cpu-provision")
	fs.BoolVar(&o.AllowSharedCoresOverlapReclaimedCores, "allow-shared-cores-overlap-reclaimed-cores", o.AllowSharedCoresOverlapReclaimedCores,
		"set true to allow shared_cores overlap reclaimed_cores")
	fs.StringToStringVar(&o.RegionIndicatorTargetOptions, "region-indicator-targets", o.RegionIndicatorTargetOptions,
		"indicators targets for each region, in format like cpu_sched_wait=400/cpu_iowait_ratio=0.8")
	o.ControlKnobConstrains.AddFlags(fs)
}
