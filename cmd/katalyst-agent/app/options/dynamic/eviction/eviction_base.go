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
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/eviction"
)

type EvictionOptions struct {
	*EvictionPluginsOptions
}

func NewEvictionOptions() *EvictionOptions {
	return &EvictionOptions{
		EvictionPluginsOptions: NewEvictionPluginsOptions(),
	}
}

func (o *EvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.EvictionPluginsOptions.AddFlags(fss)
}

func (o *EvictionOptions) ApplyTo(c *eviction.EvictionConfiguration) error {
	var errList []error
	errList = append(errList, o.EvictionPluginsOptions.ApplyTo(c.EvictionPluginsConfiguration))
	return errors.NewAggregate(errList)
}

type EvictionPluginsOptions struct {
	*MemoryPressureEvictionPluginOptions
	*ReclaimedResourcesEvictionPluginOptions
}

func NewEvictionPluginsOptions() *EvictionPluginsOptions {
	return &EvictionPluginsOptions{
		MemoryPressureEvictionPluginOptions:     NewMemoryPressureEvictionPluginOptions(),
		ReclaimedResourcesEvictionPluginOptions: NewReclaimedResourcesEvictionPluginOptions(),
	}
}

func (o *EvictionPluginsOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.MemoryPressureEvictionPluginOptions.AddFlags(fss)
	o.ReclaimedResourcesEvictionPluginOptions.AddFlags(fss)
}

func (o *EvictionPluginsOptions) ApplyTo(c *eviction.EvictionPluginsConfiguration) error {
	var errList []error
	errList = append(errList, o.MemoryPressureEvictionPluginOptions.ApplyTo(c.MemoryPressureEvictionPluginConfiguration))
	errList = append(errList, o.ReclaimedResourcesEvictionPluginOptions.ApplyTo(c.ReclaimedResourcesEvictionPluginConfiguration))
	return errors.NewAggregate(errList)
}
