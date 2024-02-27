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

// MemoryProvisionPolicyName defines policy names for memory advisor resource provision
type MemoryProvisionPolicyName string

const (
	MemoryPressureNoRisk    MemoryPressureState = 0
	MemoryPressureTuneMemCg MemoryPressureState = 1
	MemoryPressureDropCache MemoryPressureState = 2

	MemoryHeadroomPolicyNone      MemoryHeadroomPolicyName = "none"
	MemoryHeadroomPolicyCanonical MemoryHeadroomPolicyName = "canonical"
	MemoryHeadroomPolicyNUMAAware MemoryHeadroomPolicyName = "numa-aware"

	MemoryProvisionPolicyNone      MemoryProvisionPolicyName = "none"
	MemoryProvisionPolicyCanonical MemoryProvisionPolicyName = "canonical"
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

type NumaMemoryBalanceContainerInfo struct {
	PodUID        string `json:"podUID"`
	ContainerName string `json:"containerName"`
	DestNumaList  []int  `json:"destNumaList"`
}

type NumaMemoryBalanceAdvice struct {
	DestNumaList      []int                            `json:"destNumaList"`
	SourceNuma        int                              `json:"sourceNuma"`
	MigrateContainers []NumaMemoryBalanceContainerInfo `json:"migrateContainers"`
	TotalRSS          float64                          `json:"totalRSS"`
	// if the successful migrated memory ratio is over this threshold, this turn can be considered successful.
	Threshold float64 `json:"threshold"`
}
