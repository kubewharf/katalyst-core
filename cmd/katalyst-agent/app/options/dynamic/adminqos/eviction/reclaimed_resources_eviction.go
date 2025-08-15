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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type ReclaimedResourcesEvictionOptions struct {
	EvictionThreshold             native.ResourceThreshold
	SoftEvictionThreshold         native.ResourceThreshold
	GracePeriod                   int64
	ThresholdMetToleranceDuration int64
}

func NewReclaimedResourcesEvictionOptions() *ReclaimedResourcesEvictionOptions {
	return &ReclaimedResourcesEvictionOptions{
		EvictionThreshold: native.ResourceThreshold{
			consts.ReclaimedResourceMilliCPU: 5.0,
			consts.ReclaimedResourceMemory:   5.0,
		},
		SoftEvictionThreshold: native.ResourceThreshold{
			consts.ReclaimedResourceMilliCPU: 1.5,
			consts.ReclaimedResourceMemory:   1.5,
		},
		GracePeriod:                   60,
		ThresholdMetToleranceDuration: 0,
	}
}

func (o *ReclaimedResourcesEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-reclaimed-resources")

	fs.Var(&o.EvictionThreshold, "eviction-reclaimed-resources-threshold",
		"the threshold rate for best effort resources")
	fs.Int64Var(&o.GracePeriod, "eviction-reclaimed-resources-grace-period", o.GracePeriod,
		"the graceful eviction period (in seconds) for reclaimed pods")
	fs.Int64Var(&o.ThresholdMetToleranceDuration, "eviction-reclaimed-resources-threshold-met-tolerance-duration",
		o.ThresholdMetToleranceDuration, "the tolerance duration before eviction.")
}

func (o *ReclaimedResourcesEvictionOptions) ApplyTo(c *eviction.ReclaimedResourcesEvictionConfiguration) error {
	c.EvictionThreshold = o.EvictionThreshold
	c.SoftEvictionThreshold = o.SoftEvictionThreshold
	c.DeletionGracePeriod = o.GracePeriod
	c.ThresholdMetToleranceDuration = o.ThresholdMetToleranceDuration
	return nil
}
