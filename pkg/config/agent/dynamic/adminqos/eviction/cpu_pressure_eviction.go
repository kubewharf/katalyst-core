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

type CPUPressureEvictionConfiguration struct {
	EnableLoadEviction                      bool
	LoadUpperBoundRatio                     float64
	LoadLowerBoundRatio                     float64
	LoadThresholdMetPercentage              float64
	LoadMetricRingSize                      int
	LoadEvictionCoolDownTime                time.Duration
	EnableSuppressionEviction               bool
	MaxSuppressionToleranceRate             float64
	MinSuppressionToleranceDuration         time.Duration
	GracePeriod                             int64
	NumaCPUPressureEvictionConfiguration    NumaCPUPressureEvictionConfiguration
	NumaSysCPUPressureEvictionConfiguration NumaSysCPUPressureEvictionConfiguration
}

func NewCPUPressureEvictionConfiguration() *CPUPressureEvictionConfiguration {
	return &CPUPressureEvictionConfiguration{
		NumaCPUPressureEvictionConfiguration:    NewNumaCPUPressureEvictionConfiguration(),
		NumaSysCPUPressureEvictionConfiguration: NewNumaSysCPUPressureEvictionConfiguration(),
	}
}

func (c *CPUPressureEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.EvictionConfig != nil && aqc.Spec.Config.EvictionConfig.CPUPressureEvictionConfig != nil {
		config := aqc.Spec.Config.EvictionConfig.CPUPressureEvictionConfig
		if config.EnableLoadEviction != nil {
			c.EnableLoadEviction = *config.EnableLoadEviction
		}

		if config.LoadUpperBoundRatio != nil {
			c.LoadUpperBoundRatio = *config.LoadUpperBoundRatio
		}

		if config.LoadLowerBoundRatio != nil {
			c.LoadLowerBoundRatio = *config.LoadLowerBoundRatio
		}

		if config.LoadThresholdMetPercentage != nil {
			c.LoadThresholdMetPercentage = *config.LoadThresholdMetPercentage
		}

		if config.LoadMetricRingSize != nil {
			c.LoadMetricRingSize = *config.LoadMetricRingSize
		}

		if config.LoadEvictionCoolDownTime != nil {
			c.LoadEvictionCoolDownTime = config.LoadEvictionCoolDownTime.Duration
		}

		if config.EnableSuppressionEviction != nil {
			c.EnableSuppressionEviction = *config.EnableSuppressionEviction
		}

		if config.MaxSuppressionToleranceRate != nil {
			c.MaxSuppressionToleranceRate = *config.MaxSuppressionToleranceRate
		}

		if config.MinSuppressionToleranceDuration != nil {
			c.MinSuppressionToleranceDuration = config.MinSuppressionToleranceDuration.Duration
		}

		if config.GracePeriod != nil {
			c.GracePeriod = *config.GracePeriod
		}
	}

	c.NumaCPUPressureEvictionConfiguration.ApplyConfiguration(conf)
	c.NumaSysCPUPressureEvictionConfiguration.ApplyConfiguration(conf)
}
