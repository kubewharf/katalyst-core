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
	"math"

	cliflag "k8s.io/component-base/cli/flag"

	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

type SriovOptions struct {
	PolicyName               string
	SkipSriovStateCorruption bool
	SriovDryRun              bool

	SriovAllocationOptions
	SriovStaticPolicyOptions
	SriovDynamicPolicyOptions
}

type SriovAllocationOptions struct {
	PCIAnnotationKey   string
	NetNsAnnotationKey string
	ExtraAnnotations   map[string]string
}

type SriovStaticPolicyOptions struct {
	MinBondingVFQueueCount int
	MaxBondingVFQueueCount int
}

type SriovDynamicPolicyOptions struct {
	LargeSizeVFQueueCount       int
	LargeSizeVFCPUThreshold     int
	LargeSizeVFFailOnExhaustion bool
	SmallSizeVFQueueCount       int
	SmallSizeVFCPUThreshold     int
	SmallSizeVFFailOnExhaustion bool
}

func NewSriovOptions() *SriovOptions {
	return &SriovOptions{
		PolicyName:               "static",
		SkipSriovStateCorruption: true,
		SriovDryRun:              false,
		SriovAllocationOptions: SriovAllocationOptions{
			PCIAnnotationKey:   "pci-devices",
			NetNsAnnotationKey: "netns",
			ExtraAnnotations:   map[string]string{},
		},
		SriovStaticPolicyOptions: SriovStaticPolicyOptions{
			MinBondingVFQueueCount: 32,
			MaxBondingVFQueueCount: math.MaxInt32,
		},
		SriovDynamicPolicyOptions: SriovDynamicPolicyOptions{
			LargeSizeVFQueueCount:       32,
			LargeSizeVFCPUThreshold:     24,
			LargeSizeVFFailOnExhaustion: true,
			SmallSizeVFQueueCount:       8,
			SmallSizeVFCPUThreshold:     8,
			SmallSizeVFFailOnExhaustion: false,
		},
	}
}

func (o *SriovOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("sriov")

	fs.StringVar(&o.PolicyName, "sriov-policy-name", o.PolicyName, "Policy name for sriov qrm plugin")
	fs.BoolVar(&o.SkipSriovStateCorruption, "skip-sriov-state-corruption", o.SkipSriovStateCorruption, "Skip sriov state corruption")
	fs.BoolVar(&o.SriovDryRun, "sriov-plugin-dry-run", o.SriovDryRun, "Dry run the sriov plugin")
	fs.StringVar(&o.PCIAnnotationKey, "sriov-vf-pci-annotation-key", o.PCIAnnotationKey, "PCI annotation key for sriov vf passing to container")
	fs.StringVar(&o.NetNsAnnotationKey, "sriov-vf-netns-annotation-key", "", "Netns annotation key for sriov vf passing to container")
	fs.StringToStringVar(&o.ExtraAnnotations, "sriov-vf-extra-annotations", o.ExtraAnnotations, "Extra annotations for sriov vf")
	fs.IntVar(&o.MinBondingVFQueueCount, "static-min-bonding-vf-queue-count", o.MinBondingVFQueueCount, "Min queue count of bonding VF can be allocated in static policy")
	fs.IntVar(&o.MaxBondingVFQueueCount, "static-max-bonding-vf-queue-count", o.MaxBondingVFQueueCount, "Max queue count of bonding VF can be allocated in static policy")
	fs.IntVar(&o.LargeSizeVFQueueCount, "dynamic-large-size-vf-queue-count", o.LargeSizeVFQueueCount, "Queue count for VF to be identified as large size VF in dynamic policy")
	fs.IntVar(&o.LargeSizeVFCPUThreshold, "dynamic-large-size-vf-cpu-threshold", o.LargeSizeVFCPUThreshold, "Threshold of cpu quantity to allocate large size VF in dynamic policy")
	fs.BoolVar(&o.LargeSizeVFFailOnExhaustion, "dynamic-large-size-vf-fail-on-exhaustion", o.LargeSizeVFFailOnExhaustion, "Should fail or not when large size VF is exhausted in dynamic policy")
	fs.IntVar(&o.SmallSizeVFQueueCount, "dynamic-small-size-vf-queue-count", o.SmallSizeVFQueueCount, "Queue count for VF to be identified as small size VF in dynamic policy")
	fs.IntVar(&o.SmallSizeVFCPUThreshold, "dynamic-small-size-vf-cpu-threshold", o.SmallSizeVFCPUThreshold, "Threshold of cpu quantity to allocate small size VF in dynamic policy")
	fs.BoolVar(&o.SmallSizeVFFailOnExhaustion, "dynamic-small-size-vf-fail-on-exhaustion", o.SmallSizeVFFailOnExhaustion, "Should fail or not when small size VF is exhausted in dynamic policy")
}

func (s *SriovOptions) ApplyTo(config *qrmconfig.SriovQRMPluginConfig) error {
	config.PolicyName = s.PolicyName
	config.SkipSriovStateCorruption = s.SkipSriovStateCorruption
	config.SriovDryRun = s.SriovDryRun
	config.SriovAllocationConfig = qrmconfig.SriovAllocationConfig{
		PCIAnnotationKey:   s.PCIAnnotationKey,
		NetNsAnnotationKey: s.NetNsAnnotationKey,
		ExtraAnnotations:   s.ExtraAnnotations,
	}
	config.MinBondingVFQueueCount = s.MinBondingVFQueueCount
	config.MaxBondingVFQueueCount = s.MaxBondingVFQueueCount
	config.LargeSizeVFQueueCount = s.LargeSizeVFQueueCount
	config.LargeSizeVFCPUThreshold = s.LargeSizeVFCPUThreshold
	config.LargeSizeVFFailOnExhaustion = s.LargeSizeVFFailOnExhaustion
	config.SmallSizeVFQueueCount = s.SmallSizeVFQueueCount
	config.SmallSizeVFCPUThreshold = s.SmallSizeVFCPUThreshold
	config.SmallSizeVFFailOnExhaustion = s.SmallSizeVFFailOnExhaustion

	return nil
}
