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
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	evictionapi "k8s.io/kubernetes/pkg/kubelet/eviction/api"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type RootfsPressureEvictionConfiguration struct {
	EnableRootfsPressureEviction               bool
	MinimumFreeThreshold                       *evictionapi.ThresholdValue
	MinimumInodesFreeThreshold                 *evictionapi.ThresholdValue
	PodMinimumUsedThreshold                    *evictionapi.ThresholdValue
	PodMinimumInodesUsedThreshold              *evictionapi.ThresholdValue
	ReclaimedQoSPodUsedPriorityThreshold       *evictionapi.ThresholdValue
	ReclaimedQoSPodInodesUsedPriorityThreshold *evictionapi.ThresholdValue
	GracePeriod                                int64
}

func NewRootfsPressureEvictionPluginConfiguration() *RootfsPressureEvictionConfiguration {
	return &RootfsPressureEvictionConfiguration{}
}

func (c *RootfsPressureEvictionConfiguration) ApplyTo(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.RootfsPressureEvictionConfig != nil {
		config := aqc.Spec.Config.EvictionConfig.RootfsPressureEvictionConfig
		if config.EnableRootfsPressureEviction != nil {
			c.EnableRootfsPressureEviction = *config.EnableRootfsPressureEviction
		}
		if config.MinimumFreeThreshold != "" {
			thresholdValue, err := parseThresholdValue(config.MinimumFreeThreshold)
			if err == nil {
				c.MinimumFreeThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse minimumFreeThreshold, ignore this configuration: %q", err)
			}
		}
		if config.MinimumInodesFreeThreshold != "" {
			thresholdValue, err := parseThresholdValue(config.MinimumInodesFreeThreshold)
			if err == nil {
				c.MinimumInodesFreeThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse minimumInodesFreeThreshold, ignore this configuration: %q", err)
			}
		}
		if config.PodMinimumUsedThreshold != "" {
			thresholdValue, err := parseThresholdValue(config.PodMinimumUsedThreshold)
			if err == nil {
				c.PodMinimumUsedThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse podMinimumUsedThreshold, ignore this configuration: %q", err)
			}
		}
		if config.PodMinimumInodesUsedThreshold != "" {
			thresholdValue, err := parseThresholdValue(config.PodMinimumInodesUsedThreshold)
			if err == nil {
				c.PodMinimumInodesUsedThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse podMinimumInodesUsedThreshold, ignore this configuration: %q", err)
			}
		}
		if config.ReclaimedQoSPodUsedPriorityThreshold != "" {
			thresholdValue, err := parseThresholdValue(config.ReclaimedQoSPodUsedPriorityThreshold)
			if err == nil {
				c.ReclaimedQoSPodUsedPriorityThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse reclaimedQoSPodUsedPriorityThreshold, ignore this configuration: %q", err)
			}
		}
		if config.ReclaimedQoSPodInodesUsedPriorityThreshold != "" {
			thresholdValue, err := parseThresholdValue(config.ReclaimedQoSPodInodesUsedPriorityThreshold)
			if err == nil {
				c.ReclaimedQoSPodInodesUsedPriorityThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse podMinimumInodesUsedThreshold, ignore this configuration: %q", err)
			}
		}
		if config.GracePeriod != nil {
			c.GracePeriod = *config.GracePeriod
		}
	}
}

func parseThresholdValue(val string) (*evictionapi.ThresholdValue, error) {
	if strings.HasSuffix(val, "%") {
		// ignore 0% and 100%
		if val == "0%" || val == "100%" {
			return nil, nil
		}
		value, err := strconv.ParseFloat(strings.TrimRight(val, "%"), 32)
		if err != nil {
			return nil, err
		}
		percentage := float32(value) / 100
		if percentage < 0 {
			return nil, fmt.Errorf("eviction percentage threshold must be >= 0%%: %s", val)
		}
		if percentage > 100 {
			return nil, fmt.Errorf("eviction percentage threshold must be <= 100%%: %s", val)
		}
		return &evictionapi.ThresholdValue{
			Percentage: percentage,
		}, nil
	}
	quantity, err := resource.ParseQuantity(val)
	if err != nil {
		return nil, err
	}
	if quantity.Sign() < 0 || quantity.IsZero() {
		return nil, fmt.Errorf("eviction threshold must be positive: %s", &quantity)
	}
	return &evictionapi.ThresholdValue{
		Quantity: &quantity,
	}, nil
}
