/*
Copyright 2026 The Katalyst Authors.

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
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"
	cliflag "k8s.io/component-base/cli/flag"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	qrmconfigresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	resctrlutil "github.com/kubewharf/katalyst-core/pkg/util/resctrl"
)

type ResctrlOptions struct {
	EnableResctrlHint                     bool
	DisableRDT                            bool
	EnableResctrlGroupLifecycleManagement bool
	CPUSetPoolToSharedSubgroup            map[string]int
	EnabledQoS                            []string
	MonGroupEnabledClosIDs                []string
	MonGroupMaxCountRatio                 float64
	DefaultClosIDs                        []string
	SkipCleanupClosIDs                    []string
}

func NewResctrlOptions() *ResctrlOptions {
	return &ResctrlOptions{
		CPUSetPoolToSharedSubgroup: make(map[string]int),
		EnabledQoS:                 []string{apiconsts.PodAnnotationQoSLevelSharedCores},
		MonGroupEnabledClosIDs:     []string{},
		SkipCleanupClosIDs:         []string{},
	}
}

func (o *ResctrlOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("resctrl")

	fs.BoolVar(&o.EnableResctrlHint, "pod-admit-resctrl-layout-hint",
		o.EnableResctrlHint, "if set true, we will enable resctrl hint on pod admission")
	fs.BoolVar(&o.DisableRDT, "disable-rdt",
		o.DisableRDT, "if set true, we will disable RDT CLOS injection and lifecycle")
	fs.BoolVar(&o.EnableResctrlGroupLifecycleManagement, "enable-resctrl-group-lifecycle-management",
		o.EnableResctrlGroupLifecycleManagement, "deprecated no-op kept for compatibility; resctrl CLOS lifecycle is controlled by dynamic RDTConfig.DisableRDT")
	fs.StringToIntVar(&o.CPUSetPoolToSharedSubgroup, "resctrl-cpuset-pool-to-shared-subgroup",
		o.CPUSetPoolToSharedSubgroup, "customize share-xx subgroup if present")
	fs.StringSliceVar(&o.EnabledQoS, "resctrl-enabled-qos",
		o.EnabledQoS, "enabled qos levels to create resctrl closID")
	fs.StringSliceVar(&o.MonGroupEnabledClosIDs, "resctrl-mon-groups-enabled-closids",
		o.MonGroupEnabledClosIDs, "enabled-closid mon-groups")
	fs.Float64Var(&o.MonGroupMaxCountRatio, "resctrl-mon-groups-max-count-ratio",
		o.MonGroupMaxCountRatio, "ratio of mon_groups max count")
	fs.StringSliceVar(&o.DefaultClosIDs, "resctrl-default-closids",
		o.DefaultClosIDs, "a list of resctrl closID directories to create by default")
	fs.StringSliceVar(&o.SkipCleanupClosIDs, "resctrl-skip-cleanup-closids",
		o.SkipCleanupClosIDs, "a list of resctrl closID directories to skip cleaning")
}

func (o *ResctrlOptions) ApplyTo(conf *qrmconfigresctrl.ResctrlConfig) error {
	for poolName, subgroup := range o.CPUSetPoolToSharedSubgroup {
		if poolName == "" {
			return fmt.Errorf("shared subgroup pool name must not be empty")
		}
		// Rejecting an explicit mapping for the default share pool keeps admission,
		// CPUList, and CAT resolution on the same fixed physical CLOS.
		if poolName == consts.ResctrlGroupShare {
			return fmt.Errorf("default share pool must not be configured in resctrl cpuset pool mappings")
		}
		if resctrlutil.IsReservedPhysicalClosID(poolName) {
			return fmt.Errorf("shared subgroup pool %q conflicts with a reserved clos id", poolName)
		}
		if subgroup < 0 {
			return fmt.Errorf("shared subgroup for pool %q must be non-negative", poolName)
		}
	}
	conf.EnableResctrlHint = o.EnableResctrlHint
	conf.DisableRDT = o.DisableRDT
	conf.EnableResctrlGroupLifecycleManagement = o.EnableResctrlGroupLifecycleManagement
	conf.CPUSetPoolToSharedSubgroup = o.CPUSetPoolToSharedSubgroup
	conf.EnabledQoS = o.EnabledQoS
	conf.MonGroupEnabledClosIDs = o.MonGroupEnabledClosIDs
	conf.MonGroupMaxCountRatio = o.MonGroupMaxCountRatio
	conf.DefaultClosIDs = o.DefaultClosIDs
	conf.SkipCleanupClosIDs = sets.NewString(o.SkipCleanupClosIDs...)
	return nil
}
