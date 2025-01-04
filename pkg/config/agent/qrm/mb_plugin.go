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

package qrm

import "time"

type MBQRMPluginConfig struct {
	// shared meta group related which has several subgroups like shared-50, shared-30
	// this is to notify kubelet which subgroup a pod should be in
	// based on pod spec (qos level + relevant cpuset_pool annotation)
	CPUSetPoolToSharedSubgroup map[string]int

	// mb resource allocation and policy related
	MinMBPerCCD      int
	DomainMBCapacity int
	MBRemoteLimit    int

	// socket (top qos) mb reservation related
	IncubationInterval time.Duration

	// leaf (lowest qos) mb planner related
	LeafThrottleType string
	LeafEaseType     string

	// domain mb usage policy
	MBPressureThreshold int
	MBEaseThreshold     int

	// ccd mb planner
	CCDMBPlannerType string

	// incoming (recipient view) to outgoing (sender view) mapping related
	SourcerType string
}

func NewMBQRMPluginConfig() *MBQRMPluginConfig {
	return &MBQRMPluginConfig{}
}
