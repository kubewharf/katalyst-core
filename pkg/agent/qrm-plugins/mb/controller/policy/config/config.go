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

package config

import (
	"fmt"
	"strings"

	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

const (
	// obsolete by arg domain-mb-capacity for flexibility knob of testing/comparison
	// the effective bandwidth 120 GB, with the surplus of part of pressure-check sentinal (which mainly serves as collision detection)
	// DomainTotalMB   = 120_000 + 2_000     //116 GBps in one mb sharing domain, effectively

	ReservedPerNuma = 35_000              // 35 GBps reserved per node for dedicated pod
	ReservedPerCCD  = ReservedPerNuma / 2 // hardcoded divisor 2 may not be applicable all the places

	// set max of CCD mb even higher, hopefully the initial spike efffect would be obliviated
	// todo: to fix test cases (which expect original 30GB) after the 35 is finalized
	ccdMBMax    = 35_000 // per CCD 35 GB (hopefully effective 25GB)
	domainMBMax = 8 * ccdMBMax
	domainMBMin = 4_000

	defaultRemoteLimit = 20_000 //20GB
	defaultZombieMB    = 100    // 100 MB (0.1 GB)

	defaultPressureThreshold = 6_000
	defaultEaseThreshold     = 1.5 * defaultPressureThreshold
)

type MBPolicyConfig struct {
	qrmconfig.MBQRMPluginConfig

	CCDMBMax    int
	DomainMBMax int
	DomainMBMin int

	ZombieCCDMB int
}

// global mb policy configuration
var PolicyConfig = MBPolicyConfig{
	MBQRMPluginConfig: qrmconfig.MBQRMPluginConfig{
		MinMBPerCCD:         4_000,
		MBRemoteLimit:       defaultRemoteLimit,
		MBPressureThreshold: defaultPressureThreshold,
		MBEaseThreshold:     defaultEaseThreshold,
	},
	CCDMBMax:    ccdMBMax,
	DomainMBMax: domainMBMax,
	DomainMBMin: domainMBMin,
	ZombieCCDMB: defaultZombieMB,
}

func (m MBPolicyConfig) String() string {
	var sb strings.Builder

	sb.WriteString("mb policy config {")

	fmt.Fprintf(&sb, "CCDMBMax:%d,DomainMBMax:%d,DomainMBMin:%d,ZombieCCDMB:%d,", m.CCDMBMax, m.DomainMBMax, m.DomainMBMin, m.ZombieCCDMB)
	fmt.Fprintf(&sb, "MinMBPerCCD:%d,MBRemoteLimit:%d,", m.MinMBPerCCD, m.MBRemoteLimit)
	fmt.Fprintf(&sb, "MBPressureThreshold:%d,MBEaseThreshold:%d,", m.MBPressureThreshold, m.MBEaseThreshold)
	fmt.Fprintf(&sb, "DomainMBCapacity:%d,IncubationInterval:%v,", m.DomainMBCapacity, m.IncubationInterval)
	fmt.Fprintf(&sb, "LeafThrottleType:%s,LeafEaseType:%s,", m.LeafThrottleType, m.LeafEaseType)
	fmt.Fprintf(&sb, "CCDMBDistributorType:%s,SourcerType:%s,", m.CCDMBDistributorType, m.SourcerType)

	fmt.Fprintf(&sb, "CPUSetPoolToSharedSubgroup:%v", m.CPUSetPoolToSharedSubgroup)

	sb.WriteString("}")
	return sb.String()
}
