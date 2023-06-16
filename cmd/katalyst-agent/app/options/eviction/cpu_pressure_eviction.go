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
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
)

const (
	defaultLoadEvictionSyncPeriod = 30 * time.Second
)

// CPUPressureEvictionOptions is the options of CPUPressureEviction
type CPUPressureEvictionOptions struct {
	LoadEvictionSyncPeriod time.Duration
}

// NewCPUPressureEvictionOptions returns a new CPUPressureEvictionOptions
func NewCPUPressureEvictionOptions() *CPUPressureEvictionOptions {
	return &CPUPressureEvictionOptions{
		LoadEvictionSyncPeriod: defaultLoadEvictionSyncPeriod,
	}
}

// AddFlags parses the flags to CPUPressureEvictionOptions
func (o *CPUPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-cpu-pressure")

	fs.DurationVar(&o.LoadEvictionSyncPeriod, "eviction-load-sync-period",
		o.LoadEvictionSyncPeriod, "cpu load eviction syncing period")
}

// ApplyTo applies CPUPressureEvictionOptions to CPUPressureEvictionConfiguration
func (o *CPUPressureEvictionOptions) ApplyTo(c *evictionconfig.CPUPressureEvictionConfiguration) error {
	c.LoadEvictionSyncPeriod = o.LoadEvictionSyncPeriod
	return nil
}
