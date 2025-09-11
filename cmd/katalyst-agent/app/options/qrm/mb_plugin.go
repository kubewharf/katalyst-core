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

package qrm

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

const (
	defaultMinCCDMB = 4_000  // 4GB
	defaultMaxCCDMB = 40_000 // 40GB

	// defaultMBCapLimitPercent to limit quota no more than the target value as the value set in resctrl FS schemata
	// would generally result in slight more mb traffic then the set value and not the other way around
	defaultMBCapLimitPercent = 100

	// defaultMinActiveMB is the threshold above which is considered as there exists active traffic of the specified group
	// default value is 1000 MB, i.e. 1GB
	defaultMinActiveMB = 1_000
)

type MBOptions struct {
	PolicyName               string
	MinCCDMB                 int
	MaxCCDMB                 int
	MaxIncomingRemoteMB      int
	MBCapLimitPercent        int
	ActiveTrafficMBThreshold int
	DomainGroupAwareCapacity map[string]int
	NoThrottleGroups         []string
	CrossDomainGroups        []string
	ResetResctrlOnly         bool
}

func NewMBOptions() *MBOptions {
	return &MBOptions{
		PolicyName:               "generic", // only generic policy is supported right now
		MinCCDMB:                 defaultMinCCDMB,
		MaxCCDMB:                 defaultMaxCCDMB,
		MBCapLimitPercent:        defaultMBCapLimitPercent,
		ActiveTrafficMBThreshold: defaultMinActiveMB,
	}
}

func (o *MBOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("mb_resource_plugin")
	fs.StringVar(&o.PolicyName, "mb-resource-plugin-policy",
		o.PolicyName, "the policy mb resource plugin should use")
	fs.IntVar(&o.MinCCDMB, "mb-ccd-min",
		o.MinCCDMB, "min mb per ccd")
	fs.IntVar(&o.MaxCCDMB, "mb-ccd-max",
		o.MaxCCDMB, "max mb per ccd")
	fs.IntVar(&o.MaxIncomingRemoteMB, "mb-remote-limit",
		o.MaxIncomingRemoteMB, "max mb allowed from remote domains")
	fs.IntVar(&o.MBCapLimitPercent, "mb-cap-limit-percent",
		o.MBCapLimitPercent, "mb cap limit coefficient")
	fs.IntVar(&o.ActiveTrafficMBThreshold, "mb-active-mb-threshold",
		o.ActiveTrafficMBThreshold, "threshold of active traffic MB")
	fs.StringToIntVar(&o.DomainGroupAwareCapacity, "mb-group-aware-capacity",
		o.DomainGroupAwareCapacity, "customized mb capacities required by groups")
	fs.StringSliceVar(&o.NoThrottleGroups, "mb-no-throttle-groups",
		o.NoThrottleGroups, "groups not allowed to throttle mb resource")
	fs.StringSliceVar(&o.CrossDomainGroups, "mb-cross-domain-groups",
		o.CrossDomainGroups, "groups sharing mb resource across domains")
	fs.BoolVar(&o.ResetResctrlOnly, "mb-reset-resctrl-only",
		o.ResetResctrlOnly, "not to run mb plugin really, and only reset to ensure resctrl FS in default status")
}

func (o *MBOptions) ApplyTo(conf *qrm.MBQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.MinCCDMB = o.MinCCDMB
	conf.MaxCCDMB = o.MaxCCDMB
	conf.MaxIncomingRemoteMB = o.MaxIncomingRemoteMB
	conf.MBCapLimitPercent = o.MBCapLimitPercent
	conf.ActiveTrafficMBThreshold = o.ActiveTrafficMBThreshold
	conf.CrossDomainGroups = o.CrossDomainGroups
	conf.NoThrottleGroups = o.NoThrottleGroups
	conf.DomainGroupAwareCapacity = o.DomainGroupAwareCapacity
	conf.ResetResctrlOnly = o.ResetResctrlOnly
	return nil
}
