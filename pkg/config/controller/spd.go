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

package controller

import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

type SPDConfig struct {
	// ReSyncPeriod controls the resync period to generate spd
	ReSyncPeriod time.Duration

	// SPDWorkloadGVResources define those SPD concerned GVRs
	SPDWorkloadGVResources []string
	// SPDPodLabelIndexerKeys are used
	SPDPodLabelIndexerKeys []string

	// EnableCNCCache is to sync spd cnc target config
	EnableCNCCache bool

	IndicatorPlugins []string

	BaselinePercent map[string]int64

	*ResourcePortraitIndicatorPluginConfig
}

// ResourcePortraitIndicatorPluginConfig holds the configurations for resource portrait indicator plugin data.
type ResourcePortraitIndicatorPluginConfig struct {
	// available datasource: prom
	DataSource string
	// DataSourcePromConfig is the prometheus datasource config
	DataSourcePromConfig prometheus.PromConfig
	// ReSyncPeriod controls the resync period to refresh spd
	ReSyncPeriod time.Duration
	// AlgorithmServingAddress is the algorithm serving address
	AlgorithmServingAddress string
	// AlgorithmConfigMapName is the configmap name used by resource portrait plugin
	AlgorithmConfigMapName string
	// AlgorithmConfigMapNamespace is the configmap namespace used by resource portrait plugin
	AlgorithmConfigMapNamespace string
	// EnableAutomaticResyncGlobalConfiguration is used to enable automatic synchronization of global configuration.
	// If this switch is enabled, the plug-in will refresh itself configuration in SPD with the configuration in the
	// global ConfigMap. If this switch is disable, users can customize the configuration in SPD.
	EnableAutomaticResyncGlobalConfiguration bool
}

func NewSPDConfig() *SPDConfig {
	return &SPDConfig{
		BaselinePercent:                       map[string]int64{},
		ResourcePortraitIndicatorPluginConfig: &ResourcePortraitIndicatorPluginConfig{},
	}
}
