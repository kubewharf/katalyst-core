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

	"github.com/kubewharf/katalyst-api/pkg/consts"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
)

type ReclaimedResourcesEvictionPluginOptions struct {
	EvictionReclaimedResourcesPodGracePeriod int64
	EvictionThreshold                        evictionconfig.ResourceEvictionThreshold
	SkipZeroQuantityResourceNames            []string
}

func NewReclaimedResourcesEvictionPluginOptions() *ReclaimedResourcesEvictionPluginOptions {
	return &ReclaimedResourcesEvictionPluginOptions{
		EvictionReclaimedResourcesPodGracePeriod: 60,
		EvictionThreshold: evictionconfig.ResourceEvictionThreshold{
			consts.ReclaimedResourceMilliCPU: 5.0,
			consts.ReclaimedResourceMemory:   5.0,
		},
	}
}

func (o *ReclaimedResourcesEvictionPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-reclaimed-resources")

	fs.Int64Var(&o.EvictionReclaimedResourcesPodGracePeriod, "eviction-reclaimed-resources-pod-grace-period",
		o.EvictionReclaimedResourcesPodGracePeriod, "The graceful eviction period (in seconds) for reclaimed pods")
	fs.Var(&o.EvictionThreshold, "eviction-reclaimed-resources-threshold", "The threshold rate for best effort resources")
	fs.StringSliceVar(&o.SkipZeroQuantityResourceNames, "eviction-skip-zero-quantity-resource-name", o.SkipZeroQuantityResourceNames,
		"skip to evict when some resource with zero quantity to avoid abnormal eviction")
}

func (o *ReclaimedResourcesEvictionPluginOptions) ApplyTo(c *evictionconfig.ReclaimedResourcesEvictionPluginConfiguration) error {
	c.EvictionReclaimedPodGracefulPeriod = o.EvictionReclaimedResourcesPodGracePeriod
	for _, name := range o.SkipZeroQuantityResourceNames {
		c.SkipZeroQuantityResourceNames.Insert(name)
	}

	c.DynamicConf.SetEvictionThreshold(o.EvictionThreshold)
	return nil
}
