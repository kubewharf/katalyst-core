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
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
)

type ReclaimedResourcesEvictionOptions struct {
	SkipZeroQuantityResourceNames []string
}

func NewReclaimedResourcesEvictionOptions() *ReclaimedResourcesEvictionOptions {
	return &ReclaimedResourcesEvictionOptions{}
}

func (o *ReclaimedResourcesEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-reclaimed-resources")

	fs.StringSliceVar(&o.SkipZeroQuantityResourceNames, "eviction-skip-zero-quantity-resource-name", o.SkipZeroQuantityResourceNames,
		"skip to evict when some resource with zero quantity to avoid abnormal eviction")
}

func (o *ReclaimedResourcesEvictionOptions) ApplyTo(c *eviction.ReclaimedResourcesEvictionConfiguration) error {
	for _, name := range o.SkipZeroQuantityResourceNames {
		c.SkipZeroQuantityResourceNames.Insert(name)
	}
	return nil
}
