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

import "time"

type VPARecommendationConfig struct{}

type ResourceRecommendConfig struct {
	// time interval of resync VPA
	VPAReSyncPeriod time.Duration
}

type VPAConfig struct {
	// VPAWorkloadGVResources define those VPA concerned GVRs
	VPAWorkloadGVResources []string
	// SPDPodLabelIndexerKeys are used
	VPAPodLabelIndexerKeys []string
	// number of workers to sync VPA and VPARec
	VPASyncWorkers    int
	VPARecSyncWorkers int

	*VPARecommendationConfig
	*ResourceRecommendConfig
}

func NewVPAConfig() *VPAConfig {
	return &VPAConfig{
		VPARecommendationConfig: &VPARecommendationConfig{},
		ResourceRecommendConfig: &ResourceRecommendConfig{VPAReSyncPeriod: 0},
	}
}
