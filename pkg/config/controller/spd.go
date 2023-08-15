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

type SPDConfig struct {
	// ReSyncPeriod controls the resync period to generate spd
	ReSyncPeriod time.Duration

	// SPDWorkloadGVResources define those SPD concerned GVRs
	SPDWorkloadGVResources []string

	// SPDPodLabelIndexerKeys are used
	SPDPodLabelIndexerKeys []string

	IndicatorPlugins []string
}

func NewSPDConfig() *SPDConfig {
	return &SPDConfig{}
}
