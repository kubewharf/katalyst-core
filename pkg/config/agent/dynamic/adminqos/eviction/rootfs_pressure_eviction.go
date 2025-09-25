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
	MinimumImageFsFreeThreshold                *evictionapi.ThresholdValue
	MinimumImageFsInodesFreeThreshold          *evictionapi.ThresholdValue
	PodMinimumUsedThreshold                    *evictionapi.ThresholdValue
	PodMinimumInodesUsedThreshold              *evictionapi.ThresholdValue
	ReclaimedQoSPodUsedPriorityThreshold       *evictionapi.ThresholdValue
	ReclaimedQoSPodInodesUsedPriorityThreshold *evictionapi.ThresholdValue
	MinimumImageFsDiskCapacityThreshold        *resource.Quantity
	IgnorePrjquotaLocalStoragePath             string
	EnableIgnorePrjquotaLocalStorage           bool

	EnableRootfsOveruseEviction             bool
	RootfsOveruseEvictionSupportedQoSLevels []string
	SharedQoSRootfsOveruseThreshold         *evictionapi.ThresholdValue
	ReclaimedQoSRootfsOveruseThreshold      *evictionapi.ThresholdValue
	RootfsOveruseEvictionCount              int
	SharedQoSNamespaceFilter                []string

	GracePeriod int64
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
		if config.MinimumImageFsFreeThreshold != nil && *config.MinimumImageFsFreeThreshold != "" {
			thresholdValue, err := ParseThresholdValue(*config.MinimumImageFsFreeThreshold)
			if err == nil {
				c.MinimumImageFsFreeThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse minimumImageFsFreeThreshold, ignore this configuration: %q", err)
			}
		}
		if config.MinimumImageFsInodesFreeThreshold != nil && *config.MinimumImageFsInodesFreeThreshold != "" {
			thresholdValue, err := ParseThresholdValue(*config.MinimumImageFsInodesFreeThreshold)
			if err == nil {
				c.MinimumImageFsInodesFreeThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse minimumImageFsInodesFreeThreshold, ignore this configuration: %q", err)
			}
		}
		if config.PodMinimumUsedThreshold != nil && *config.PodMinimumUsedThreshold != "" {
			thresholdValue, err := ParseThresholdValue(*config.PodMinimumUsedThreshold)
			if err == nil {
				c.PodMinimumUsedThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse podMinimumUsedThreshold, ignore this configuration: %q", err)
			}
		}
		if config.PodMinimumInodesUsedThreshold != nil && *config.PodMinimumInodesUsedThreshold != "" {
			thresholdValue, err := ParseThresholdValue(*config.PodMinimumInodesUsedThreshold)
			if err == nil {
				c.PodMinimumInodesUsedThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse podMinimumInodesUsedThreshold, ignore this configuration: %q", err)
			}
		}
		if config.ReclaimedQoSPodUsedPriorityThreshold != nil && *config.ReclaimedQoSPodUsedPriorityThreshold != "" {
			thresholdValue, err := ParseThresholdValue(*config.ReclaimedQoSPodUsedPriorityThreshold)
			if err == nil {
				c.ReclaimedQoSPodUsedPriorityThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse reclaimedQoSPodUsedPriorityThreshold, ignore this configuration: %q", err)
			}
		}
		if config.ReclaimedQoSPodInodesUsedPriorityThreshold != nil && *config.ReclaimedQoSPodInodesUsedPriorityThreshold != "" {
			thresholdValue, err := ParseThresholdValue(*config.ReclaimedQoSPodInodesUsedPriorityThreshold)
			if err == nil {
				c.ReclaimedQoSPodInodesUsedPriorityThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse podMinimumInodesUsedThreshold, ignore this configuration: %q", err)
			}
		}
		if config.MinimumImageFsDiskCapacityThreshold != nil {
			c.MinimumImageFsDiskCapacityThreshold = config.MinimumImageFsDiskCapacityThreshold
		}
		if config.GracePeriod != nil {
			c.GracePeriod = *config.GracePeriod
		}
		if config.EnableRootfsOveruseEviction != nil {
			c.EnableRootfsOveruseEviction = *config.EnableRootfsOveruseEviction
		}
		if len(config.RootfsOveruseEvictionSupportedQoSLevels) > 0 {
			c.RootfsOveruseEvictionSupportedQoSLevels = config.RootfsOveruseEvictionSupportedQoSLevels
		}
		if config.SharedQoSRootfsOveruseThreshold != nil {
			thresholdValue, err := ParseThresholdValue(*config.SharedQoSRootfsOveruseThreshold)
			if err == nil {
				c.SharedQoSRootfsOveruseThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse sharedQoSRootfsOveruseThreshold, ignore this configuration: %q", err)
			}
		}
		if config.ReclaimedQoSRootfsOveruseThreshold != nil {
			thresholdValue, err := ParseThresholdValue(*config.ReclaimedQoSRootfsOveruseThreshold)
			if err == nil {
				c.ReclaimedQoSRootfsOveruseThreshold = thresholdValue
			} else {
				general.Warningf("failed to parse reclaimedQoSRootfsOveruseThreshold, ignore this configuration: %q", err)
			}
		}
	}
}

func ParseThresholdValue(val string) (*evictionapi.ThresholdValue, error) {
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
