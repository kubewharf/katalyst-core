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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

type MemorySetEvictionOptions struct {
	EnableMemorySetEviction bool
}

func NewMemorySetEvictionOptions() *MemorySetEvictionOptions {
	return &MemorySetEvictionOptions{}
}

func (o *MemorySetEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-memory-set")
	fs.BoolVar(&o.EnableMemorySetEviction, "eviction-memory-set-enable", false,
		"set true to enable memory set eviction")
}

func (o *MemorySetEvictionOptions) ApplyTo(c *eviction.MemorySetEvictionConfiguration) error {
	c.EnableMemorySetEviction = o.EnableMemorySetEviction
	return nil
}
