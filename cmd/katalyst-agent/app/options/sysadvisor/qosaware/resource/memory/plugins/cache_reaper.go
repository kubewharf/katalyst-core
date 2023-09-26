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

package plugins

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory/plugins"
)

type CacheReaperOptions struct {
	MinCacheUtilizationThreshold float64
}

func NewCacheReaperOptions() *CacheReaperOptions {
	return &CacheReaperOptions{
		MinCacheUtilizationThreshold: 0.005,
	}
}

func (o *CacheReaperOptions) AddFlags(fs *pflag.FlagSet) {
	fs.Float64Var(&o.MinCacheUtilizationThreshold, "memory-advisor-min-cache-utilization-threshold", o.MinCacheUtilizationThreshold,
		"the pod minimum cache usage on a NUMA node, if a pod uses less memory on a NUMA node than this threshold,"+
			" it's cache won't be dropped by cache-reaper.")
}

func (o *CacheReaperOptions) ApplyTo(c *plugins.CacheReaperConfiguration) error {
	c.MinCacheUtilizationThreshold = o.MinCacheUtilizationThreshold
	return nil
}
