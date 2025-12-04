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

package rules

import (
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
)

type CandidatePod struct {
	Pod                   *v1.Pod
	Scores                map[string]int
	TotalScore            int
	WorkloadsEvictionInfo *WorkloadEvictionInfo
	UsageRatio            float64
}

type WorkloadEvictionInfo struct {
	StatsByWindow    map[float64]*EvictionStats
	Replicas         int32
	LastEvictionTime int64
	Limit            int32
}

type EvictionStats struct {
	EvictionCount int64
	EvictionRatio float64
}

type NumaOverStat struct {
	NumaID         int
	OverloadRatio  float64
	AvgUsageRatio  float64
	MetricsHistory *util.NumaMetricHistory
	Gap            float64
}

type NumaSysOverStat struct {
	NumaID             int
	NumaCPUUsageAvg    float64
	NumaSysCPUUsageAvg float64

	IsNumaCPUUsageSoftOver    bool
	IsNumaCPUUsageHardOver    bool
	IsNumaSysCPUUsageSoftOver bool
	IsNumaSysCPUUsageHardOver bool
}

type EvictOptions struct {
	NumaPressureConfig *NumaPressureConfig
	State              State
	Extras             map[string]interface{}
}

type State struct {
	NumaOverStats []NumaOverStat
}

type NumaPressureConfig struct {
	MetricRingSize                 int
	ThresholdMetPercentage         float64
	GracePeriod                    int64
	ExpandFactor                   float64
	CandidateCount                 int
	WorkloadMetricsLabelKeys       []string
	SkippedPodKinds                []string
	EnabledFilters                 []string
	EnabledScorers                 []string
	WorkloadEvictionFrequencyLimit []float64
}
