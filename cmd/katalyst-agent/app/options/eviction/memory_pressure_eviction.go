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

	"k8s.io/apimachinery/pkg/labels"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
)

// MemoryPressureEvictionOptions is the options of MemoryPressureEviction
type MemoryPressureEvictionOptions struct {
	RSSOveruseEvictionFilter string
}

// NewMemoryPressureEvictionOptions returns a new MemoryPressureEvictionOptions
func NewMemoryPressureEvictionOptions() *MemoryPressureEvictionOptions {
	return &MemoryPressureEvictionOptions{}
}

// AddFlags parses the flags to MemoryPressureEvictionOptions
func (o *MemoryPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory-pressure-eviction")

	fs.StringVar(&o.RSSOveruseEvictionFilter, "eviction-rss-overuse-filter", o.RSSOveruseEvictionFilter,
		"the labels which used to filter pods which can be evict by rss overuse eviction")
}

// ApplyTo applies MemoryPressureEvictionOptions to MemoryPressureEvictionConfiguration
func (o *MemoryPressureEvictionOptions) ApplyTo(c *eviction.MemoryPressureEvictionConfiguration) error {
	if o.RSSOveruseEvictionFilter != "" {
		labelFilter, err := labels.ConvertSelectorToLabelsMap(o.RSSOveruseEvictionFilter)
		if err != nil {
			return fmt.Errorf("parse \"eviction-rss-overuse-filter\" flag failed, value:%v, err: %v", o.RSSOveruseEvictionFilter, err)
		}
		c.RSSOveruseEvictionFilter = labelFilter
	}
	return nil
}
