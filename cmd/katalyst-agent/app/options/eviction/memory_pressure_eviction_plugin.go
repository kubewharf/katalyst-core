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

// MemoryPressureEvictionPluginOptions is the options of MemoryPressureEvictionPlugin
type MemoryPressureEvictionPluginOptions struct {
	RssOveruseEvictionFilter string
}

// NewMemoryPressureEvictionPluginOptions returns a new MemoryPressureEvictionPluginOptions
func NewMemoryPressureEvictionPluginOptions() *MemoryPressureEvictionPluginOptions {
	return &MemoryPressureEvictionPluginOptions{}
}

// AddFlags parses the flags to MemoryPressureEvictionPluginOptions
func (o *MemoryPressureEvictionPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory-pressure-eviction")

	fs.StringVar(&o.RssOveruseEvictionFilter, "rss-overuse-evict-filter", o.RssOveruseEvictionFilter,
		"the labels which used to filter pods which can be evict by rss overuse eviction plugin")
}

// ApplyTo applies MemoryPressureEvictionPluginOptions to MemoryPressureEvictionPluginConfiguration
func (o *MemoryPressureEvictionPluginOptions) ApplyTo(c *eviction.MemoryPressureEvictionPluginConfiguration) error {
	if o.RssOveruseEvictionFilter != "" {
		labelFilter, err := labels.ConvertSelectorToLabelsMap(o.RssOveruseEvictionFilter)
		if err != nil {
			return fmt.Errorf("parse \"rss-overuse-evict-filter\" flag failed, value:%v, err: %v", o.RssOveruseEvictionFilter, err)
		}
		c.RssOveruseEvictionFilter = labelFilter
	}
	return nil
}
