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

package types

import (
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

type MemoryAdvisorPluginName string
type MemoryPressureState int

// MemoryHeadroomPolicyName defines policy names for memory advisor headroom estimation
type MemoryHeadroomPolicyName string

const (
	MemoryPressureNoRisk    MemoryPressureState = 0
	MemoryPressureTuneMemCg MemoryPressureState = 1
	MemoryPressureDropCache MemoryPressureState = 2

	MemoryHeadroomPolicyNone      MemoryHeadroomPolicyName = "none"
	MemoryHeadroomPolicyCanonical MemoryHeadroomPolicyName = "canonical"
	MemoryHeadroomPolicyNUMAAware MemoryHeadroomPolicyName = "numa-aware"
	MemoryHeadroomPolicyNormal    MemoryHeadroomPolicyName = "normal"
)

type MemoryPressureCondition struct {
	TargetReclaimed *resource.Quantity
	State           MemoryPressureState
}

type MemoryPressureStatus struct {
	NodeCondition  *MemoryPressureCondition
	NUMAConditions map[int]*MemoryPressureCondition
}

type ContainerMemoryAdvices struct {
	PodUID        string
	ContainerName string
	Values        map[string]string
}

type ExtraMemoryAdvices struct {
	CgroupPath string
	Values     map[string]string
}

type InternalMemoryCalculationResult struct {
	ContainerEntries []ContainerMemoryAdvices
	ExtraEntries     []ExtraMemoryAdvices
	TimeStamp        time.Time
}
