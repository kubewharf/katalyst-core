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

package metacache

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metacache"
)

const (
	defaultMetaCacheSyncPeriod = 5
)

// MetaCachePluginOptions holds the configurations for metacache plugin.
type MetaCachePluginOptions struct {
	SyncPeriod time.Duration
}

// NewMetaCachePluginOptions creates a new Options with a default config.
func NewMetaCachePluginOptions() *MetaCachePluginOptions {
	return &MetaCachePluginOptions{
		SyncPeriod: defaultMetaCacheSyncPeriod * time.Second,
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *MetaCachePluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("meta_cache_plugin")

	fs.DurationVar(&o.SyncPeriod, "metacache-sync-period", o.SyncPeriod, "Period for metacache plugin to sync")
}

// ApplyTo fills up config with options
func (o *MetaCachePluginOptions) ApplyTo(c *metacache.MetaCachePluginConfiguration) error {
	c.SyncPeriod = o.SyncPeriod
	return nil
}
