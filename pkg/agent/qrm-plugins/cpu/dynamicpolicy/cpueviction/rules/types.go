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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/history"
	v1 "k8s.io/api/core/v1"
)

type CandidatePod struct {
	Pod                   *v1.Pod
	Scores                map[string]int
	TotalScore            int
	WorkloadsEvictionInfo map[string]*WorkloadEvictionInfo
	UsageRatio            float64
}

type WorkloadEvictionInfo struct {
	WorkloadName     string
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
	MetricsHistory *history.NumaMetricHistory
	Gap            float64
}
