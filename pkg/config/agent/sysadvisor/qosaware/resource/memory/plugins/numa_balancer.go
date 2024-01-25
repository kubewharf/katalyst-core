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

package plugins

type NumaBalancerConfiguration struct {
	ReadLatencyMetric string

	GraceBalanceReadLatencyThreshold float64
	ForceBalanceReadLatencyThreshold float64

	GraceBalanceReadLatencyGapThreshold float64
	GraceBalanceNumaDistanceMax         int64

	GraceBalanceGapRatio float64
	ForceBalanceGapRatio float64

	BalancedPodSourceNumaRSSMax int64
	BalancedPodSourceNumaRSSMin int64

	BalancedReclaimedPodSourceNumaRSSMax int64
	BalancedReclaimedPodSourceNumaRSSMin int64

	BalancedPodsSourceNumaTotalRSSMax          int64
	BalancedReclaimedPodsSourceNumaTotalRSSMax int64

	BalancedReclaimedPodsSingleRoundTotalRssThreshold int64

	SupportedPools []string
}

func NewNumaBalancerConfiguration() *NumaBalancerConfiguration {
	return &NumaBalancerConfiguration{}
}
