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

package realtime

import "time"

type RealtimeOvercommitConfiguration struct {
	SyncPeriod time.Duration

	SyncPodTimeout time.Duration

	TargetCPULoad    float64
	TargetMemoryLoad float64

	EstimatedPodCPULoad    float64
	EstimatedPodMemoryLoad float64

	CPUMetricsToGather    []string
	MemoryMetricsToGather []string
}

func NewRealtimeOvercommitConfiguration() *RealtimeOvercommitConfiguration {
	return &RealtimeOvercommitConfiguration{
		SyncPeriod:     10 * time.Second,
		SyncPodTimeout: 2 * time.Second,

		TargetCPULoad:          0.6,
		TargetMemoryLoad:       0.8,
		EstimatedPodCPULoad:    0.4,
		EstimatedPodMemoryLoad: 0.8,

		CPUMetricsToGather:    []string{},
		MemoryMetricsToGather: []string{},
	}
}
