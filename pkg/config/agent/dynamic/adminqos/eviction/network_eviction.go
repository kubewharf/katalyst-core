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
}

func NewNetworkEvictionConfiguration() *NetworkEvictionConfiguration {
	return &NetworkEvictionConfiguration{}
}

func (n *NetworkEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.NetworkEvictionConfig != nil {
		config := aqc.Spec.Config.EvictionConfig.NetworkEvictionConfig
		if config.NICUnhealthyToleranceDuration != nil {
			n.EnableNICHealthEviction = *config.EnableNICHealthEviction
		}

		if config.NICUnhealthyToleranceDuration != nil {
			n.NICUnhealthyToleranceDuration = config.NICUnhealthyToleranceDuration.Duration
		}

		if config.GracePeriod != nil {
			n.GracePeriod = *config.GracePeriod
		}
	}
}
