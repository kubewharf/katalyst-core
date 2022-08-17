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

type CPUOptions struct {
	PolicyName             string
	EnableSysAdvisor       bool
	ReservedCPUCores       int
	SkipCPUStateCorruption bool
}

func NewCPUOptions() *CPUOptions {
	return &CPUOptions{
		PolicyName:             "dynamic",
		EnableSysAdvisor:       false,
		ReservedCPUCores:       0,
		SkipCPUStateCorruption: false,
	}
}

func (o *CPUOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("cpu_resource_plugin")

	fs.StringVar(&o.PolicyName, "cpu-resource-plugin-policy",
		o.PolicyName, "The policy cpu resource plugin should use")
	fs.BoolVar(&o.EnableSysAdvisor, "cpu-resource-plugin-advisor",
		o.EnableSysAdvisor, "Whether cpu resource plugin should enable sys-advisor")
	fs.IntVar(&o.ReservedCPUCores, "cpu-resource-plugin-reserved",
		o.ReservedCPUCores, "The total cores cpu resource plugin should reserve")
	fs.BoolVar(&o.SkipCPUStateCorruption, "skip-cpu-state-corruption",
		o.SkipCPUStateCorruption, "if set true, we will skip cpu state corruption")
}

func (o *CPUOptions) ApplyTo(conf *qrmconfig.CPUQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.EnableSysAdvisor = o.EnableSysAdvisor
	conf.ReservedCPUCores = o.ReservedCPUCores
	conf.SkipCPUStateCorruption = o.SkipCPUStateCorruption
	return nil
}
