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
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/qrm/hintoptimizer"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/qrm/irqtuner"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

type CPUOptions struct {
	PolicyName             string
	ReservedCPUCores       int
	ReservedCPUList        string
	SkipCPUStateCorruption bool

	CPUDynamicPolicyOptions
	CPUNativePolicyOptions
}

type CPUDynamicPolicyOptions struct {
	EnableCPUAdvisor                          bool
	AdvisorGetAdviceInterval                  time.Duration
	EnableCPUPressureEviction                 bool
	LoadPressureEvictionSkipPools             []string
	EnableSyncingCPUIdle                      bool
	EnableCPUIdle                             bool
	CPUNUMAHintPreferPolicy                   string
	CPUNUMAHintPreferLowThreshold             float64
	SharedCoresNUMABindingResultAnnotationKey string
	EnableReserveCPUReversely                 bool
	*irqtuner.IRQTunerOptions
	*hintoptimizer.HintOptimizerOptions
}

type CPUNativePolicyOptions struct {
	EnableFullPhysicalCPUsOnly bool
	CPUAllocationOption        string
}

func NewCPUOptions() *CPUOptions {
	return &CPUOptions{
		PolicyName:             "dynamic",
		ReservedCPUCores:       0,
		SkipCPUStateCorruption: false,
		CPUDynamicPolicyOptions: CPUDynamicPolicyOptions{
			EnableCPUAdvisor:          false,
			AdvisorGetAdviceInterval:  5 * time.Second,
			EnableCPUPressureEviction: false,
			EnableSyncingCPUIdle:      false,
			EnableCPUIdle:             false,
			LoadPressureEvictionSkipPools: []string{
				commonstate.PoolNameReclaim,
				commonstate.PoolNameDedicated,
				commonstate.PoolNameFallback,
				commonstate.PoolNameReserve,
			},
			SharedCoresNUMABindingResultAnnotationKey: consts.PodAnnotationNUMABindResultKey,
			HintOptimizerOptions:                      hintoptimizer.NewHintOptimizerOptions(),
			IRQTunerOptions:                           irqtuner.NewIRQTunerOptions(),
		},
		CPUNativePolicyOptions: CPUNativePolicyOptions{
			EnableFullPhysicalCPUsOnly: false,
			CPUAllocationOption:        "packed",
		},
	}
}

func (o *CPUOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("cpu_resource_plugin")

	fs.StringVar(&o.PolicyName, "cpu-resource-plugin-policy",
		o.PolicyName, "The policy cpu resource plugin should use")
	fs.BoolVar(&o.EnableCPUAdvisor, "cpu-resource-plugin-advisor",
		o.EnableCPUAdvisor, "Whether cpu resource plugin should enable sys-advisor")
	fs.DurationVar(&o.AdvisorGetAdviceInterval, "cpu-resource-plugin-advisor-interval",
		o.AdvisorGetAdviceInterval, "If cpu advisor is enabled, this is the interval at which we get advice from sys-advisor")
	fs.IntVar(&o.ReservedCPUCores, "cpu-resource-plugin-reserved",
		o.ReservedCPUCores, "The total cores cpu resource plugin should reserve")
	fs.StringVar(&o.ReservedCPUList, "cpu-resource-plugin-reserved-list",
		o.ReservedCPUList, "The cpu resource plugin should reserve cpu list")
	fs.BoolVar(&o.SkipCPUStateCorruption, "skip-cpu-state-corruption",
		o.SkipCPUStateCorruption, "if set true, we will skip cpu state corruption")
	fs.BoolVar(&o.EnableCPUPressureEviction, "enable-cpu-pressure-eviction", o.EnableCPUPressureEviction,
		"if set true, it can enable cpu-related eviction, such as cpu pressure eviction and cpu suppression eviction")
	fs.StringSliceVar(&o.LoadPressureEvictionSkipPools, "load-pressure-eviction-skip-pools", o.LoadPressureEvictionSkipPools,
		"the pool in this list will be ignored when check load pressure")
	fs.BoolVar(&o.EnableSyncingCPUIdle, "enable-syncing-cpu-idle",
		o.EnableSyncingCPUIdle, "if set true, we will sync specific cgroup paths with value specified by --enable-cpu-idle option")
	fs.BoolVar(&o.EnableCPUIdle, "enable-cpu-idle", o.EnableCPUIdle,
		"if set true, we will enable cpu idle for "+
			"specific cgroup paths and it requires --enable-syncing-cpu-idle=true to make effect")
	fs.StringVar(&o.CPUAllocationOption, "cpu-allocation-option",
		o.CPUAllocationOption, "The allocation option of cpu (packed/distributed). The default value is packed."+
			"in cases where more than one NUMA node is required to satisfy the allocation.")
	fs.BoolVar(&o.EnableFullPhysicalCPUsOnly, "enable-full-physical-cpus-only",
		o.EnableFullPhysicalCPUsOnly, "if set true, we will enable extra allocation restrictions to "+
			"avoid different containers to possibly end up on the same core.")
	fs.StringVar(&o.SharedCoresNUMABindingResultAnnotationKey, "shared-cores-numa-binding-result-annotation-key",
		o.SharedCoresNUMABindingResultAnnotationKey, "the key of shared cores numa binding result annotation, "+
			"default is katalyst.kubewharf.io/numa_bind_result")
	fs.BoolVar(&o.EnableReserveCPUReversely, "enable-reserve-cpu-reversely",
		o.EnableReserveCPUReversely, "by default, the reservation of cpu starts from the cpu with lower id,"+
			"if set to true, it starts from the cpu with higher id")
	o.HintOptimizerOptions.AddFlags(fss)
	o.IRQTunerOptions.AddFlags(fss)
}

func (o *CPUOptions) ApplyTo(conf *qrmconfig.CPUQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.EnableCPUAdvisor = o.EnableCPUAdvisor
	conf.GetAdviceInterval = o.AdvisorGetAdviceInterval
	conf.ReservedCPUCores = o.ReservedCPUCores
	conf.ReservedCPUList = o.ReservedCPUList
	conf.SkipCPUStateCorruption = o.SkipCPUStateCorruption
	conf.EnableCPUPressureEviction = o.EnableCPUPressureEviction
	conf.LoadPressureEvictionSkipPools = o.LoadPressureEvictionSkipPools
	conf.EnableSyncingCPUIdle = o.EnableSyncingCPUIdle
	conf.EnableCPUIdle = o.EnableCPUIdle
	conf.EnableFullPhysicalCPUsOnly = o.EnableFullPhysicalCPUsOnly
	conf.CPUAllocationOption = o.CPUAllocationOption
	conf.SharedCoresNUMABindingResultAnnotationKey = o.SharedCoresNUMABindingResultAnnotationKey
	conf.EnableReserveCPUReversely = o.EnableReserveCPUReversely
	if err := o.HintOptimizerOptions.ApplyTo(conf.HintOptimizerConfiguration); err != nil {
		return err
	}
	if err := o.IRQTunerOptions.ApplyTo(conf.IRQTunerConfiguration); err != nil {
		return err
	}
	return nil
}
