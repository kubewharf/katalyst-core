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
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
)

type AdvisorOptions struct {
	*MemoryGuardOptions
	*CPURegionOptions
}

func NewAdvisorOptions() *AdvisorOptions {
	return &AdvisorOptions{
		MemoryGuardOptions: NewMemoryGuardOptions(),
		CPURegionOptions:   NewCPURegionOptions(),
	}
}

func (o *AdvisorOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.MemoryGuardOptions.AddFlags(fss)
	o.CPURegionOptions.AddFlags(fss)
}

func (o *AdvisorOptions) ApplyTo(c *advisor.AdvisorConfiguration) error {
	var errList []error
	errList = append(errList, o.MemoryGuardOptions.ApplyTo(c.MemoryGuardConfiguration))
	errList = append(errList, o.CPURegionOptions.ApplyTo(c.CPURegionConfiguration))
	return errors.NewAggregate(errList)
}
