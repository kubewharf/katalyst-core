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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

type NetworkEvictionOptions struct {
	EnableNICHealthEviction            bool
	NICUnhealthyToleranceDuration      time.Duration
	GracePeriod                        int64
	EnableNICBandwidthEviction         bool
	NICBandwidthUtilizationThreshold   float64
	NICBandwidthContinuousMetThreshold int
	NICBandwidthRingSize               int
	NICBandwidthRingMetThreshold       int
	NICBandwidthGracePeriod            int64
}

func NewNetworkEvictionOptions() *NetworkEvictionOptions {
	return &NetworkEvictionOptions{
		NICUnhealthyToleranceDuration:      5 * time.Minute,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}
}

func (o *NetworkEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-network")

	fs.BoolVar(&o.EnableNICHealthEviction, "eviction-network-nic-health-enable", o.EnableNICHealthEviction, "enable nic health eviction")
	fs.DurationVar(&o.NICUnhealthyToleranceDuration, "eviction-network-nic-unhealthy-tolerance-duration", o.NICUnhealthyToleranceDuration, "nic unhealthy tolerance duration")
	fs.Int64Var(&o.GracePeriod, "eviction-network-grace-period", o.GracePeriod, "the grace period of nic health eviction")
	fs.BoolVar(&o.EnableNICBandwidthEviction, "eviction-network-nic-bandwidth-enable", o.EnableNICBandwidthEviction, "enable nic bandwidth eviction")
	fs.Float64Var(&o.NICBandwidthUtilizationThreshold, "eviction-network-nic-bandwidth-utilization-threshold", o.NICBandwidthUtilizationThreshold, "nic bandwidth utilization threshold")
	fs.IntVar(&o.NICBandwidthContinuousMetThreshold, "eviction-network-nic-bandwidth-continuous-met-threshold", o.NICBandwidthContinuousMetThreshold, "continuous met threshold for nic bandwidth eviction")
	fs.IntVar(&o.NICBandwidthRingSize, "eviction-network-nic-bandwidth-ring-size", o.NICBandwidthRingSize, "ring size for nic bandwidth eviction")
	fs.IntVar(&o.NICBandwidthRingMetThreshold, "eviction-network-nic-bandwidth-ring-met-threshold", o.NICBandwidthRingMetThreshold, "ring met threshold for nic bandwidth eviction")
	fs.Int64Var(&o.NICBandwidthGracePeriod, "eviction-network-nic-bandwidth-grace-period", o.NICBandwidthGracePeriod, "the grace period of nic bandwidth eviction")
}

func (o *NetworkEvictionOptions) ApplyTo(c *eviction.NetworkEvictionConfiguration) error {
	c.EnableNICHealthEviction = o.EnableNICHealthEviction
	c.NICUnhealthyToleranceDuration = o.NICUnhealthyToleranceDuration
	c.GracePeriod = o.GracePeriod
	c.EnableNICBandwidthEviction = o.EnableNICBandwidthEviction
	c.NICBandwidthUtilizationThreshold = o.NICBandwidthUtilizationThreshold
	c.NICBandwidthContinuousMetThreshold = o.NICBandwidthContinuousMetThreshold
	c.NICBandwidthRingSize = o.NICBandwidthRingSize
	c.NICBandwidthRingMetThreshold = o.NICBandwidthRingMetThreshold
	c.NICBandwidthGracePeriod = o.NICBandwidthGracePeriod

	return nil
}
