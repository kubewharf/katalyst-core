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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type NetworkEvictionConfiguration struct {
	// EnableNICHealthEviction indicates whether to enable NIC health eviction
	EnableNICHealthEviction bool
	// NICUnhealthyToleranceDuration is the tolerance duration for NIC unhealthy
	// if the NIC is unhealthy for this duration, the pod will be evicted
	NICUnhealthyToleranceDuration time.Duration
	// GracePeriod is the grace period for NIC health eviction
	GracePeriod int64
	// EnableNICBandwidthEviction indicates whether to enable NIC bandwidth eviction.
	EnableNICBandwidthEviction bool
	// NICBandwidthUtilizationThreshold is the threshold for NIC bandwidth utilization.
	NICBandwidthUtilizationThreshold float64
	// NICBandwidthContinuousMetThreshold is the continuous hit threshold for NIC bandwidth pressure.
	NICBandwidthContinuousMetThreshold int
	// NICBandwidthRingSize is the ring size for NIC bandwidth pressure observations.
	NICBandwidthRingSize int
	// NICBandwidthRingMetThreshold is the ring hit threshold for NIC bandwidth pressure.
	NICBandwidthRingMetThreshold int
	// NICBandwidthGracePeriod is the grace period for NIC bandwidth eviction.
	NICBandwidthGracePeriod int64
}

func NewNetworkEvictionConfiguration() *NetworkEvictionConfiguration {
	return &NetworkEvictionConfiguration{
		NICUnhealthyToleranceDuration:      5 * time.Minute,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}
}

func (n *NetworkEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil {
		if config := aqc.Spec.Config.EvictionConfig.NetworkEvictionConfig; config != nil {
			if config.EnableNICHealthEviction != nil {
				n.EnableNICHealthEviction = *config.EnableNICHealthEviction
			}

			if config.NICUnhealthyToleranceDuration != nil {
				n.NICUnhealthyToleranceDuration = config.NICUnhealthyToleranceDuration.Duration
			}

			if config.GracePeriod != nil {
				n.GracePeriod = *config.GracePeriod
			}

			if config.EnableNICBandwidthEviction != nil {
				n.EnableNICBandwidthEviction = *config.EnableNICBandwidthEviction
			}

			if config.NICBandwidthUtilizationThreshold != nil {
				n.NICBandwidthUtilizationThreshold = *config.NICBandwidthUtilizationThreshold
			}

			if config.NICBandwidthContinuousMetThreshold != nil {
				n.NICBandwidthContinuousMetThreshold = int(*config.NICBandwidthContinuousMetThreshold)
			}

			if config.NICBandwidthRingSize != nil {
				n.NICBandwidthRingSize = int(*config.NICBandwidthRingSize)
			}

			if config.NICBandwidthRingMetThreshold != nil {
				n.NICBandwidthRingMetThreshold = int(*config.NICBandwidthRingMetThreshold)
			}

			if config.NICBandwidthGracePeriod != nil {
				n.NICBandwidthGracePeriod = *config.NICBandwidthGracePeriod
			}
		}
	}
}
