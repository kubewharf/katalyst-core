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

package tmodefault

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	tmodynamicconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/tmo"
)

type DefaultOptions struct {
	DefaultEnableTMO                                   bool
	DefaultEnableSwap                                  bool
	DefaultTMOInterval                                 time.Duration
	DefaultTMOPolicyName                               string
	DefaultTMOMaxProbe                                 float64
	DefaultTMOPSIPolicyPSIAvg60Threshold               float64
	DefaultTMORefaultPolicyReclaimAccuracyTarget       float64
	DefaultTMORefaultPolicyReclaimScanEfficiencyTarget float64
}

func NewDefaultOptions() *DefaultOptions {
	return &DefaultOptions{
		DefaultEnableTMO:                                   tmodynamicconf.DefaultEnableTMO,
		DefaultEnableSwap:                                  tmodynamicconf.DefaultEnableSwap,
		DefaultTMOInterval:                                 tmodynamicconf.DefaultTMOInterval,
		DefaultTMOPolicyName:                               string(tmodynamicconf.DefaultTMOPolicyName),
		DefaultTMOMaxProbe:                                 tmodynamicconf.DefaultTMOMaxProbe,
		DefaultTMOPSIPolicyPSIAvg60Threshold:               tmodynamicconf.DefaultTMOPSIPolicyPSIAvg60Threshold,
		DefaultTMORefaultPolicyReclaimAccuracyTarget:       tmodynamicconf.DefaultTMORefaultPolicyReclaimAccuracyTarget,
		DefaultTMORefaultPolicyReclaimScanEfficiencyTarget: tmodynamicconf.DefaultTMORefaultPolicyReclaimScanEfficiencyTarget,
	}
}

func (o *DefaultOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("tmo-default-config")

	fs.BoolVar(&o.DefaultEnableTMO, "default-enable-tmo", o.DefaultEnableTMO,
		"whether to enable transparent memory offloading by default")
	fs.BoolVar(&o.DefaultEnableSwap, "default-enable-swap", o.DefaultEnableSwap,
		"whether to enable swap (offload rss) in TMO by default")
	fs.DurationVar(&o.DefaultTMOInterval, "default-tmo-min-interval", o.DefaultTMOInterval,
		"default minimum interval to trigger TMO on each container or cgroup")
	fs.StringVar(&o.DefaultTMOPolicyName, "default-tmo-policy-name", o.DefaultTMOPolicyName,
		"default policy used to calculate memory offloading size")
	fs.Float64Var(&o.DefaultTMOMaxProbe, "default-max-probe", o.DefaultTMOMaxProbe,
		"default maximum ratio of memory usage could be offloaded in one cycle")
	fs.Float64Var(&o.DefaultTMOPSIPolicyPSIAvg60Threshold, "default-psi-policy-psi-avg60-threshold", o.DefaultTMOPSIPolicyPSIAvg60Threshold,
		"indicates the default threshold of memory pressure. If observed pressure exceeds this threshold, memory offloading will be paused.")
	fs.Float64Var(&o.DefaultTMORefaultPolicyReclaimAccuracyTarget, "default-refault-policy-reclaim-accuracy-target", o.DefaultTMORefaultPolicyReclaimAccuracyTarget,
		"indicates the default desired level of precision or accuracy in offloaded pages")
	fs.Float64Var(&o.DefaultTMORefaultPolicyReclaimScanEfficiencyTarget, "default-refault-policy-reclaim-scan-efficiency-target", o.DefaultTMORefaultPolicyReclaimScanEfficiencyTarget,
		"indicates the default desired level of efficiency in scanning and identifying memory pages that can be offloaded.")
}

func (o *DefaultOptions) ApplyTo(c *tmodynamicconf.TMODefaultConfigurations) error {
	c.DefaultEnableTMO = o.DefaultEnableTMO
	c.DefaultEnableSwap = o.DefaultEnableSwap
	c.DefaultTMOInterval = o.DefaultTMOInterval
	c.DefaultTMOPolicyName = v1alpha1.TMOPolicyName(o.DefaultTMOPolicyName)
	c.DefaultTMOMaxProbe = o.DefaultTMOMaxProbe
	c.DefaultTMOPSIPolicyPSIAvg60Threshold = o.DefaultTMOPSIPolicyPSIAvg60Threshold
	c.DefaultTMORefaultPolicyReclaimAccuracyTarget = o.DefaultTMORefaultPolicyReclaimAccuracyTarget
	c.DefaultTMORefaultPolicyReclaimScanEfficiencyTarget = o.DefaultTMORefaultPolicyReclaimScanEfficiencyTarget
	return nil
}
