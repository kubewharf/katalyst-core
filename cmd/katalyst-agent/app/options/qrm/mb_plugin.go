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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/policy/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

const (
	defaultMinCCDMB = 4_000  // 4GB
	defaultMaxCCDMB = 40_000 // 40GB

	defaultCCDCapKp = 0.1

	// defaultMaxIncomingRemoteMB is that each mb domain is allowed to have traffic from other domains by default;
	// 15GB is the heuristic value based on prior experiences
	defaultMaxIncomingRemoteMB = 15_000 // 15GB

	// defaultMBCapLimitPercent to limit quota no more than the target value as the value set in resctrl FS schemata
	// would generally result in slight more mb traffic then the set value and not the other way around
	defaultMBCapLimitPercent = 100

	// defaultMinActiveMB is the threshold above which is considered as there exists active traffic of the specified group
	// default value is 1000 MB, i.e. 1GB
	defaultMinActiveMB = 1_000
)

type MBOptions struct {
	PolicyName                     string
	MinCCDMB                       int
	MaxCCDMB                       int
	MaxIncomingRemoteMB            int
	MBCapLimitPercent              int
	ActiveTrafficMBThreshold       int
	CCDCapKp                       float64
	CCDCapGroups                   map[string]int
	DomainGroupAwareCapacityPCT    map[string]int
	NoThrottleGroups               []string
	CrossDomainGroups              []string
	ResetResctrlOnly               bool
	LocalIsVictimAndTotalIsAllRead bool
	ExtraGroupPriorities           map[string]int
}

func NewMBOptions() *MBOptions {
	return &MBOptions{
		PolicyName:               consts.MBPluginPolicyNameGeneric, // only generic policy is supported right now
		MinCCDMB:                 defaultMinCCDMB,
		MaxCCDMB:                 defaultMaxCCDMB,
		CCDCapKp:                 defaultCCDCapKp,
		MBCapLimitPercent:        defaultMBCapLimitPercent,
		ActiveTrafficMBThreshold: defaultMinActiveMB,
		MaxIncomingRemoteMB:      defaultMaxIncomingRemoteMB,
	}
}

func (o *MBOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("mb_resource_plugin")
	fs.StringVar(&o.PolicyName, "mb-resource-plugin-policy",
		o.PolicyName, "the policy mb resource plugin should use")
	fs.IntVar(&o.MinCCDMB, "mb-ccd-min",
		o.MinCCDMB, "min mb per ccd")
	fs.IntVar(&o.MaxCCDMB, "mb-ccd-max",
		o.MaxCCDMB, "max mb per ccd; saturation point of mb resource control")
	fs.Float64Var(&o.CCDCapKp, "mb-ccd-cap-kp",
		o.CCDCapKp, "proportional gain for ccd cap governance")
	fs.StringToIntVar(&o.CCDCapGroups, "mb-ccd-cap-groups",
		o.CCDCapGroups, "per-group target actual mb per ccd (e.g. dedicated=20000,shared-50=24000)")
	fs.IntVar(&o.MaxIncomingRemoteMB, "mb-remote-limit",
		o.MaxIncomingRemoteMB, "max mb allowed from remote domains")
	fs.IntVar(&o.MBCapLimitPercent, "mb-cap-limit-percent",
		o.MBCapLimitPercent, "mb cap limit coefficient")
	fs.IntVar(&o.ActiveTrafficMBThreshold, "mb-active-mb-threshold",
		o.ActiveTrafficMBThreshold, "threshold of active traffic MB")
	fs.StringToIntVar(&o.DomainGroupAwareCapacityPCT, "mb-group-aware-capacity-percentage",
		o.DomainGroupAwareCapacityPCT, "customized mb capacities required by groups")
	fs.StringSliceVar(&o.NoThrottleGroups, "mb-no-throttle-groups",
		o.NoThrottleGroups, "groups not allowed to throttle mb resource")
	fs.StringSliceVar(&o.CrossDomainGroups, "mb-cross-domain-groups",
		o.CrossDomainGroups, "groups sharing mb resource across domains")
	fs.BoolVar(&o.ResetResctrlOnly, "mb-reset-resctrl-only",
		o.ResetResctrlOnly, "not to run mb plugin really, and only reset to ensure resctrl FS in default status")
	fs.BoolVar(&o.LocalIsVictimAndTotalIsAllRead, "mb-local-is-victim",
		o.LocalIsVictimAndTotalIsAllRead, "turn resctrl local as victim")
	fs.StringToIntVar(&o.ExtraGroupPriorities, "mb-extra-group-priorities",
		o.ExtraGroupPriorities, "extra resctrl groups with priorities")
}

func (o *MBOptions) ApplyTo(conf *qrm.MBQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.MinCCDMB = o.MinCCDMB
	conf.MaxCCDMB = o.MaxCCDMB
	conf.CCDCapKp = o.CCDCapKp
	conf.CCDCapGroups = o.CCDCapGroups
	conf.MaxIncomingRemoteMB = o.MaxIncomingRemoteMB
	conf.MBCapLimitPercent = o.MBCapLimitPercent
	conf.ActiveTrafficMBThreshold = o.ActiveTrafficMBThreshold
	conf.CrossDomainGroups = o.CrossDomainGroups
	conf.NoThrottleGroups = o.NoThrottleGroups
	conf.DomainGroupAwareCapacityPCT = o.DomainGroupAwareCapacityPCT
	conf.ResetResctrlOnly = o.ResetResctrlOnly
	conf.LocalIsVictimAndTotalIsAllRead = o.LocalIsVictimAndTotalIsAllRead
	conf.ExtraGroupPriorities = o.ExtraGroupPriorities
	return nil
}
