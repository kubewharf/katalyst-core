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

import (
	cliflag "k8s.io/component-base/cli/flag"

	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

type SriovOptions struct {
	PolicyName               string
	SkipSriovStateCorruption bool

	SriovStaticPolicyOptions
}

type SriovStaticPolicyOptions struct {
	MinBondingVfQueueCount int
	MaxBondingVfQueueCount int
}

func NewSriovOptions() *SriovOptions {
	return &SriovOptions{
		PolicyName:               "static",
		SkipSriovStateCorruption: true,
		SriovStaticPolicyOptions: SriovStaticPolicyOptions{
			MinBondingVfQueueCount: 32,
			MaxBondingVfQueueCount: 32,
		},
	}
}

func (o *SriovOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("sriov")

	fs.StringVar(&o.PolicyName, "sriov-policy-name", o.PolicyName, "Policy name for sriov qrm plugin")
	fs.BoolVar(&o.SkipSriovStateCorruption, "skip-sriov-state-corruption", o.SkipSriovStateCorruption, "Skip sriov state corruption")
	fs.IntVar(&o.MinBondingVfQueueCount, "min-bonding-vf-queue-count", o.MinBondingVfQueueCount, "Min bonding vf queue count")
	fs.IntVar(&o.MaxBondingVfQueueCount, "max-bonding-vf-queue-count", o.MaxBondingVfQueueCount, "Max bonding vf queue count")
}

func (s *SriovOptions) ApplyTo(config *qrmconfig.SriovQRMPluginConfig) error {
	config.PolicyName = s.PolicyName
	config.SkipSriovStateCorruption = s.SkipSriovStateCorruption
	config.MinBondingVfQueueCount = s.MinBondingVfQueueCount
	config.MaxBondingVfQueueCount = s.MaxBondingVfQueueCount

	return nil
}
