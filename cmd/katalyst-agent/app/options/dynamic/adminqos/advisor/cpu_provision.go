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

	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapi "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type CPUProvisionOptions struct {
	AllowSharedCoresOverlapReclaimedCores       bool
	RegionIndicatorTargetOptions                map[string]string
	IndicatorTargetGetters                      map[string]string
	IndicatorTargetDefaultGetter                string
	IndicatorTargetMetricThresholdExpandFactors map[string]string
}

func NewCPUProvisionOptions() *CPUProvisionOptions {
	return &CPUProvisionOptions{
		AllowSharedCoresOverlapReclaimedCores:       false,
		RegionIndicatorTargetOptions:                map[string]string{},
		IndicatorTargetGetters:                      map[string]string{},
		IndicatorTargetDefaultGetter:                string(consts.IndicatorTargetGetterSPDMin),
		IndicatorTargetMetricThresholdExpandFactors: map[string]string{},
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
	if o.IndicatorTargetGetters != nil && len(o.IndicatorTargetGetters) > 0 {
		c.IndicatorTargetGetters = o.IndicatorTargetGetters
	}
	if o.IndicatorTargetDefaultGetter != "" {
		c.IndicatorTargetDefaultGetter = o.IndicatorTargetDefaultGetter
	}

	if len(o.IndicatorTargetMetricThresholdExpandFactors) > 0 {
		c.IndicatorTargetMetricThresholdExpandFactors = make(map[string]float64)
		for indicatorName, expandFactor := range o.IndicatorTargetMetricThresholdExpandFactors {
			factor, err := strconv.ParseFloat(expandFactor, 64)
			if err != nil {
				errList = append(errList, err)
				continue
			}
			c.IndicatorTargetMetricThresholdExpandFactors[indicatorName] = factor
		}
	}

	return errors.NewAggregate(errList)
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUProvisionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("cpu-provision")
	fs.BoolVar(&o.AllowSharedCoresOverlapReclaimedCores, "allow-shared-cores-overlap-reclaimed-cores", o.AllowSharedCoresOverlapReclaimedCores,
		"set true to allow shared_cores overlap reclaimed_cores")
	fs.StringToStringVar(&o.RegionIndicatorTargetOptions, "region-indicator-targets", o.RegionIndicatorTargetOptions,
		"indicators targets for each region, in format like cpu_sched_wait=400/cpu_iowait_ratio=0.8")
	fs.StringToStringVar(&o.IndicatorTargetGetters, "indicator-target-getters", o.IndicatorTargetGetters,
		"indicators target getters funcs keyed by indicator name, in format like cpu_usage_ratio=spd-avg/cpu_sched_wait=spd-min")
	fs.StringVar(&o.IndicatorTargetDefaultGetter, "indicator-target-default-getter", o.IndicatorTargetDefaultGetter,
		"default indicator target getter func like spd-avg")
	fs.StringToStringVar(&o.IndicatorTargetMetricThresholdExpandFactors, "indicator-target-metric-threshold-expand-factors", o.IndicatorTargetMetricThresholdExpandFactors,
		"indicator target expand factor, in format like cpu_usage_ratio=1.1,cpi=1.2")
}
