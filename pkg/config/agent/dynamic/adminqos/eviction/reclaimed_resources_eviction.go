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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type ReclaimedResourcesEvictionConfiguration struct {
	EvictionThreshold             native.ResourceThreshold
	SoftEvictionThreshold         native.ResourceThreshold
	DeletionGracePeriod           int64
	ThresholdMetToleranceDuration int64
}

func NewReclaimedResourcesEvictionConfiguration() *ReclaimedResourcesEvictionConfiguration {
	return &ReclaimedResourcesEvictionConfiguration{
		EvictionThreshold:     native.ResourceThreshold{},
		SoftEvictionThreshold: native.ResourceThreshold{},
	}
}

func (c *ReclaimedResourcesEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.ReclaimedResourcesEvictionConfig != nil {
		config := aqc.Spec.Config.EvictionConfig.ReclaimedResourcesEvictionConfig
		for resourceName, value := range config.EvictionThreshold {
			c.EvictionThreshold[resourceName] = value
		}

		for resourceName, value := range config.SoftEvictionThreshold {
			c.SoftEvictionThreshold[resourceName] = value
		}

		if config.GracePeriod != nil {
			c.DeletionGracePeriod = *config.GracePeriod
		}

		if config.ThresholdMetToleranceDuration != nil {
			c.ThresholdMetToleranceDuration = *config.ThresholdMetToleranceDuration
		}
	}
}
