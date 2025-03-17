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

package eviction

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

type EvictionOptions struct {
	DryRun []string

	*CPUPressureEvictionOptions
	*MemoryPressureEvictionOptions
	*ReclaimedResourcesEvictionOptions
	*SystemLoadPressureEvictionOptions
	*RootfsPressureEvictionOptions
	*NetworkEvictionOptions
}

func NewEvictionOptions() *EvictionOptions {
	return &EvictionOptions{
		CPUPressureEvictionOptions:        NewCPUPressureEvictionOptions(),
		MemoryPressureEvictionOptions:     NewMemoryPressureEvictionOptions(),
		ReclaimedResourcesEvictionOptions: NewReclaimedResourcesEvictionOptions(),
		SystemLoadPressureEvictionOptions: NewSystemLoadPressureEvictionOptions(),
		RootfsPressureEvictionOptions:     NewRootfsPressureEvictionOptions(),
		NetworkEvictionOptions:            NewNetworkEvictionOptions(),
	}
}

func (o *EvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction")
	fs.StringSliceVar(&o.DryRun, "eviction-dry-run-plugins", o.DryRun, fmt.Sprintf(" A list of "+
		"eviction plugins to dry run. If a plugin in this list, it will enter dry run mode"))

	o.CPUPressureEvictionOptions.AddFlags(fss)
	o.MemoryPressureEvictionOptions.AddFlags(fss)
	o.ReclaimedResourcesEvictionOptions.AddFlags(fss)
	o.SystemLoadPressureEvictionOptions.AddFlags(fss)
	o.RootfsPressureEvictionOptions.AddFlags(fss)
	o.NetworkEvictionOptions.AddFlags(fss)
}

func (o *EvictionOptions) ApplyTo(c *eviction.EvictionConfiguration) error {
	var errList []error
	c.DryRun = o.DryRun
	errList = append(errList, o.CPUPressureEvictionOptions.ApplyTo(c.CPUPressureEvictionConfiguration))
	errList = append(errList, o.MemoryPressureEvictionOptions.ApplyTo(c.MemoryPressureEvictionConfiguration))
	errList = append(errList, o.ReclaimedResourcesEvictionOptions.ApplyTo(c.ReclaimedResourcesEvictionConfiguration))
	errList = append(errList, o.SystemLoadPressureEvictionOptions.ApplyTo(c.SystemLoadEvictionPluginConfiguration))
	errList = append(errList, o.RootfsPressureEvictionOptions.ApplyTo(c.RootfsPressureEvictionConfiguration))
	errList = append(errList, o.NetworkEvictionOptions.ApplyTo(c.NetworkEvictionConfiguration))
	return errors.NewAggregate(errList)
}
