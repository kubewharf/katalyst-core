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

import qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"

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
)

type MBPolicyConfig struct {
	MBConfig    qrmconfig.MBQRMPluginConfig
	CCDMBMax    int
	DomainMBMax int
	DomainMBMin int

	RemoteLimit int
	ZombieCCDMB int
}

// global mb policy configuration
var PolicyConfig = MBPolicyConfig{
	MBConfig: qrmconfig.MBQRMPluginConfig{
		MinMBPerCCD: 4_000,
	},
	CCDMBMax:    ccdMBMax,
	DomainMBMax: domainMBMax,
	DomainMBMin: domainMBMin,
	RemoteLimit: defaultRemoteLimit,
	ZombieCCDMB: defaultZombieMB,
}
