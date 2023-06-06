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

type ReclaimedResourcesEvictionPluginConfiguration struct {
	EvictionThreshold native.ResourceThreshold
}

func NewReclaimedResourcesEvictionPluginConfiguration() *ReclaimedResourcesEvictionPluginConfiguration {
	return &ReclaimedResourcesEvictionPluginConfiguration{
		EvictionThreshold: native.ResourceThreshold{},
	}
}

func (c *ReclaimedResourcesEvictionPluginConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if ec := conf.EvictionConfiguration; ec != nil {
		for resourceName, value := range ec.Spec.Config.EvictionPluginsConfig.ReclaimedResourcesEvictionPluginConfig.EvictionThreshold {
			c.EvictionThreshold[resourceName] = value
		}
	}
}
