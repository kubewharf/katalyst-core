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

package global

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type ReclaimedResourceOptions struct {
	EnableReclaim                   bool
	ReservedResourceForReport       general.ResourceList
	MinReclaimedResourceForReport   general.ResourceList
	ReservedResourceForAllocate     general.ResourceList
	MinReclaimedResourceForAllocate general.ResourceList
}

func NewReclaimedResourceOptions() *ReclaimedResourceOptions {
	return &ReclaimedResourceOptions{
		EnableReclaim: false,
		ReservedResourceForReport: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("0"),
			v1.ResourceMemory: resource.MustParse("0"),
		},
		MinReclaimedResourceForReport: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("4"),
			v1.ResourceMemory: resource.MustParse("5Gi"),
		},
		ReservedResourceForAllocate: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("4"),
			v1.ResourceMemory: resource.MustParse("5Gi"),
		},
		MinReclaimedResourceForAllocate: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("4"),
			v1.ResourceMemory: resource.MustParse("5Gi"),
		},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *ReclaimedResourceOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("reclaimed-resource")

	fs.BoolVar(&o.EnableReclaim, "enable-reclaim", o.EnableReclaim,
		"show whether enable reclaim resource from shared and agent resource")
	fs.Var(&o.ReservedResourceForReport, "reserved-resource-for-report",
		"reserved reclaimed resource report to cnr")
	fs.Var(&o.MinReclaimedResourceForReport, "min-reclaimed-resource-for-report",
		"min reclaimed resource report to cnr")
	fs.Var(&o.ReservedResourceForAllocate, "reserved-resource-for-allocate",
		"reserved reclaimed resource actually not allocate to reclaimed resource")
	fs.Var(&o.MinReclaimedResourceForAllocate, "min-reclaimed-resource-for-allocate",
		"min reclaimed resource actually allocate to reclaimed resource")
}

// ApplyTo fills up config with options
func (o *ReclaimedResourceOptions) ApplyTo(c *global.ReclaimedResourceConfiguration) error {
	c.EnableReclaim = o.EnableReclaim
	c.ReservedResourceForReport = v1.ResourceList(o.ReservedResourceForReport)
	c.ReservedResourceForAllocate = v1.ResourceList(o.ReservedResourceForAllocate)
	c.MinReclaimedResourceForReport = v1.ResourceList(o.MinReclaimedResourceForReport)
	c.MinReclaimedResourceForAllocate = v1.ResourceList(o.MinReclaimedResourceForAllocate)
	return nil
}
