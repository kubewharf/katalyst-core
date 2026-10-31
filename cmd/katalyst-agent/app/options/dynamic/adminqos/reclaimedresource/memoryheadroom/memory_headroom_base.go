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

package memoryheadroom

import (
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/memoryheadroom"
)

const (
	defaultReclaimedMemoryMaxRatio = 0
)

type MemoryHeadroomOptions struct {
	*UtilBasedOptions
	ReclaimedMemoryMaxRatio float64
}

func NewMemoryHeadroomOptions() *MemoryHeadroomOptions {
	return &MemoryHeadroomOptions{
		UtilBasedOptions:        NewUtilBasedOptions(),
		ReclaimedMemoryMaxRatio: defaultReclaimedMemoryMaxRatio,
	}
}

func (o *MemoryHeadroomOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory-headroom")

	o.UtilBasedOptions.AddFlags(fs)
	fs.Float64Var(&o.ReclaimedMemoryMaxRatio, "memory-headroom-reclaimed-memory-max-ratio", o.ReclaimedMemoryMaxRatio,
		"the maximum ratio of per-NUMA memory that can be assigned to reclaimed_cores, 0 means no limit")
}

func (o *MemoryHeadroomOptions) ApplyTo(c *memoryheadroom.MemoryHeadroomConfiguration) error {
	var errList []error
	errList = append(errList, o.UtilBasedOptions.ApplyTo(c.MemoryUtilBasedConfiguration))
	c.ReclaimedMemoryMaxRatio = o.ReclaimedMemoryMaxRatio
	return errors.NewAggregate(errList)
}
