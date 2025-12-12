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

	SriovAllocationOptions
	SriovStaticPolicyOptions
}

type SriovAllocationOptions struct {
	PCIAnnotationKey string
	ExtraAnnotations map[string]string
}

type SriovStaticPolicyOptions struct {
	MinBondingVFQueueCount int
	MaxBondingVFQueueCount int
}

func NewSriovOptions() *SriovOptions {
	return &SriovOptions{
		PolicyName:               "static",
		SkipSriovStateCorruption: true,
		SriovAllocationOptions: SriovAllocationOptions{
			PCIAnnotationKey: "pci-devices",
			ExtraAnnotations: map[string]string{},
		},
		SriovStaticPolicyOptions: SriovStaticPolicyOptions{
			MinBondingVFQueueCount: 32,
			MaxBondingVFQueueCount: 32,
		},
	}
}

func (o *SriovOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("sriov")

	fs.StringVar(&o.PolicyName, "sriov-policy-name", o.PolicyName, "Policy name for sriov qrm plugin")
	fs.BoolVar(&o.SkipSriovStateCorruption, "skip-sriov-state-corruption", o.SkipSriovStateCorruption, "Skip sriov state corruption")
	fs.StringVar(&o.PCIAnnotationKey, "pci-annotation-key", o.PCIAnnotationKey, "Pci annotation key for sriov qrm plugin")
	fs.StringToStringVar(&o.ExtraAnnotations, "sriov-vf-extra-annotations", o.ExtraAnnotations, "Extra annotations for sriov vf")
	fs.IntVar(&o.MinBondingVFQueueCount, "min-bonding-vf-queue-count", o.MinBondingVFQueueCount, "Min bonding vf queue count")
	fs.IntVar(&o.MaxBondingVFQueueCount, "max-bonding-vf-queue-count", o.MaxBondingVFQueueCount, "Max bonding vf queue count")
}

func (s *SriovOptions) ApplyTo(config *qrmconfig.SriovQRMPluginConfig) error {
	config.PolicyName = s.PolicyName
	config.SkipSriovStateCorruption = s.SkipSriovStateCorruption
	config.SriovAllocationConfig = qrmconfig.SriovAllocationConfig{
		PCIAnnotation:    s.PCIAnnotationKey,
		ExtraAnnotations: s.ExtraAnnotations,
	}
	config.MinBondingVFQueueCount = s.MinBondingVFQueueCount
	config.MaxBondingVFQueueCount = s.MaxBondingVFQueueCount

	return nil
}
