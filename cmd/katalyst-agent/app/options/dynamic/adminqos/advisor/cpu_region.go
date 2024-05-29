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
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
)

type CPURegionOptions struct {
	AllowSharedCoresOverlapReclaimedCores bool
}

func NewCPURegionOptions() *CPURegionOptions {
	return &CPURegionOptions{
		AllowSharedCoresOverlapReclaimedCores: false,
	}
}

// AddFlags parses the flags to CPURegionOptions
func (o *CPURegionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("cpu-region")
	//
	fs.BoolVar(&o.AllowSharedCoresOverlapReclaimedCores, "cpu-region-allow-shared-cores-overlap-reclaimed-cores", o.AllowSharedCoresOverlapReclaimedCores,
		"set true to allow shared_cores overlap reclaimed_cores")
}

func (o *CPURegionOptions) ApplyTo(c *advisor.CPURegionConfiguration) error {
	c.AllowSharedCoresOverlapReclaimedCores = o.AllowSharedCoresOverlapReclaimedCores
	return nil
}
