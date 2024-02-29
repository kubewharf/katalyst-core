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
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	provisionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/provision"
)

type CPUProvisionPolicyOptions struct {
	PolicyRama                   *PolicyRamaOptions
	RegionIndicatorTargetOptions map[string]string
}

func NewCPUProvisionPolicyOptions() *CPUProvisionPolicyOptions {
	return &CPUProvisionPolicyOptions{
		PolicyRama:                   NewPolicyRamaOptions(),
		RegionIndicatorTargetOptions: map[string]string{},
	}
}

// ApplyTo fills up config with options
func (o *CPUProvisionPolicyOptions) ApplyTo(c *provisionconfig.CPUProvisionPolicyConfiguration) error {
	var errList []error
	errList = append(errList, o.PolicyRama.ApplyTo(c.PolicyRama))

	for regionType, targets := range o.RegionIndicatorTargetOptions {
		regionIndicatorTarget := make([]types.IndicatorTargetConfiguration, 0)
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
			regionIndicatorTarget = append(regionIndicatorTarget, types.IndicatorTargetConfiguration{Name: tmp[0], Target: target})
		}
		c.RegionIndicatorTargetConfiguration[types.QoSRegionType(regionType)] = regionIndicatorTarget
	}

	return errors.NewAggregate(errList)
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUProvisionPolicyOptions) AddFlags(fs *pflag.FlagSet) {
	o.PolicyRama.AddFlags(fs)
	fs.StringToStringVar(&o.RegionIndicatorTargetOptions, "region-indicator-targets", o.RegionIndicatorTargetOptions,
		"indicators targets for each region, in format like cpu_sched_wait=400/cpu_iowait_ratio=0.8")
}
