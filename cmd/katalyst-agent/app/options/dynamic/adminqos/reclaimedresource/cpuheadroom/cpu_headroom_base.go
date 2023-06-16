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

package cpuheadroom

import (
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/cpuheadroom"
)

type CPUHeadroomOptions struct {
	*CPUHeadroomUtilBasedOptions
}

func NewCPUHeadroomOptions() *CPUHeadroomOptions {
	return &CPUHeadroomOptions{
		CPUHeadroomUtilBasedOptions: NewCPUHeadroomUtilBasedOptions(),
	}
}

func (o *CPUHeadroomOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("cpu-headroom")

	o.CPUHeadroomUtilBasedOptions.AddFlags(fs)
}

func (o *CPUHeadroomOptions) ApplyTo(c *cpuheadroom.CPUHeadroomConfiguration) error {
	var errList []error
	errList = append(errList, o.CPUHeadroomUtilBasedOptions.ApplyTo(c.CPUUtilBasedConfiguration))
	return errors.NewAggregate(errList)
}
