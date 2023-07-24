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

type MemoryOptions struct {
	PolicyName                 string
	ReservedMemoryGB           uint64
	SkipMemoryStateCorruption  bool
	EnableSettingMemoryMigrate bool
	EnableMemoryAdvisor        bool
	ExtraControlKnobConfigFile string
}

func NewMemoryOptions() *MemoryOptions {
	return &MemoryOptions{
		PolicyName:                 "dynamic",
		ReservedMemoryGB:           0,
		SkipMemoryStateCorruption:  false,
		EnableSettingMemoryMigrate: false,
		EnableMemoryAdvisor:        false,
	}
}

func (o *MemoryOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory_resource_plugin")

	fs.StringVar(&o.PolicyName, "memory-resource-plugin-policy",
		o.PolicyName, "The policy memory resource plugin should use")
	fs.Uint64Var(&o.ReservedMemoryGB, "memory-resource-plugin-reserved",
		o.ReservedMemoryGB, "reserved memory(GB) for system agents")
	fs.BoolVar(&o.SkipMemoryStateCorruption, "skip-memory-state-corruption",
		o.SkipMemoryStateCorruption, "if set true, we will skip memory state corruption")
	fs.BoolVar(&o.EnableSettingMemoryMigrate, "enable-setting-memory-migrate",
		o.EnableSettingMemoryMigrate, "if set true, we will enable cpuset.memory_migrate for containers not numa_binding")
	fs.BoolVar(&o.EnableMemoryAdvisor, "memory-resource-plugin-advisor",
		o.EnableMemoryAdvisor, "Whether memory resource plugin should enable sys-advisor")
	fs.StringVar(&o.ExtraControlKnobConfigFile, "memory-extra-control-knob-config-file",
		o.ExtraControlKnobConfigFile, "the absolute path of extra control knob config file")
}
func (o *MemoryOptions) ApplyTo(conf *qrmconfig.MemoryQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.ReservedMemoryGB = o.ReservedMemoryGB
	conf.SkipMemoryStateCorruption = o.SkipMemoryStateCorruption
	conf.EnableSettingMemoryMigrate = o.EnableSettingMemoryMigrate
	conf.EnableMemoryAdvisor = o.EnableMemoryAdvisor
	conf.ExtraControlKnobConfigFile = o.ExtraControlKnobConfigFile
	return nil
}
