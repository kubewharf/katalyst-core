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

package cpu

import (
	"k8s.io/apimachinery/pkg/util/sets"
)

// CPUIsolationConfiguration stores configurations of cpu isolation
type CPUIsolationConfiguration struct {
	// IsolationCPURatio and IsolationCPUSize defines the threshold to trigger isolation
	IsolationCPURatio float32
	IsolationCPUSize  int32

	// IsolatedMaxPoolResourceRatios defines the max resource-ratio for each pool
	// key indicates the pool-name that supports cpu-isolation
	// val indicates the max ratio for this cpu-isolation,
	IsolatedMaxResourceRatio      float32
	IsolatedMaxPoolResourceRatios map[string]float32

	// IsolatedMaxPoolPodRatios defines the max pod-amount-ratio for each pool
	// key indicates the pool-name that supports cpu-isolation
	// val indicates the max ratio for this cpu-isolation,
	IsolatedMaxPodRatio      float32
	IsolatedMaxPoolPodRatios map[string]float32

	// IsolationLockInThreshold and IsolationLockOutPeriodSecs defines the lasting periods
	// before state switches between lock-in and lock-out
	IsolationLockInThreshold   int
	IsolationLockOutPeriodSecs int

	IsolationDisabled          bool
	IsolationDisabledPools     sets.String
	IsolationForceEnablePools  sets.String
	IsolationNonExclusivePools sets.String
}

// NewCPUIsolationConfiguration creates new resource advisor configurations
func NewCPUIsolationConfiguration() *CPUIsolationConfiguration {
	return &CPUIsolationConfiguration{}
}
