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

package reclaimedresource

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/adminqos/reclaimedresource/cpuheadroom"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/adminqos/reclaimedresource/memoryheadroom"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type ReclaimedResourceOptions struct {
	EnableReclaim                     bool
	ReservedResourceForReport         general.ResourceList
	MinReclaimedResourceForReport     general.ResourceList
	ReservedResourceForAllocate       general.ResourceList
	ReservedResourceForReclaimedCores general.ResourceList
	MaxNodeUtilizationPercent         map[string]int64

	*cpuheadroom.CPUHeadroomOptions
	*memoryheadroom.MemoryHeadroomOptions
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
		ReservedResourceForReclaimedCores: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("4"),
			v1.ResourceMemory: resource.MustParse("0"),
		},
		MaxNodeUtilizationPercent: map[string]int64{},
		CPUHeadroomOptions:        cpuheadroom.NewCPUHeadroomOptions(),
		MemoryHeadroomOptions:     memoryheadroom.NewMemoryHeadroomOptions(),
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
	fs.Var(&o.ReservedResourceForReclaimedCores, "reserved-resource-for-reclaimed-cores",
		"reserved resources for reclaimed_cores pods")
	fs.StringToInt64Var(&o.MaxNodeUtilizationPercent, "max-node-utilization-percent", o.MaxNodeUtilizationPercent,
		"node utilization resource limit for reclaimed pool")

	o.CPUHeadroomOptions.AddFlags(fss)
	o.MemoryHeadroomOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *ReclaimedResourceOptions) ApplyTo(c *reclaimedresource.ReclaimedResourceConfiguration) error {
	var errList []error
	c.EnableReclaim = o.EnableReclaim
	c.ReservedResourceForReport = v1.ResourceList(o.ReservedResourceForReport)
	c.MinReclaimedResourceForReport = v1.ResourceList(o.MinReclaimedResourceForReport)
	c.ReservedResourceForAllocate = v1.ResourceList(o.ReservedResourceForAllocate)
	c.MinReclaimedResourceForAllocate = v1.ResourceList(o.ReservedResourceForReclaimedCores)

	maxNodeUtilizationPercent := make(map[v1.ResourceName]int64)
	for resourceName, value := range o.MaxNodeUtilizationPercent {
		maxNodeUtilizationPercent[v1.ResourceName(resourceName)] = value
	}
	c.MaxNodeUtilizationPercent = maxNodeUtilizationPercent

	errList = append(errList, o.CPUHeadroomOptions.ApplyTo(c.CPUHeadroomConfiguration))
	errList = append(errList, o.MemoryHeadroomOptions.ApplyTo(c.MemoryHeadroomConfiguration))
	return errors.NewAggregate(errList)
}
