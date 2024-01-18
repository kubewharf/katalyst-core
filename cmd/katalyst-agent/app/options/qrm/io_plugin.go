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

type IOOptions struct {
	PolicyName string

	WritebackThrottlingOption // option for writeback throttling, it determin the recycling speed of dirty memory.
	// TO-DO
	//DirtyThrottlingOption // option for dirty throttling, it determin the global watermark of dirty memory.
}

type WritebackThrottlingOption struct {
	EnableSettingWBT bool
	WBTValueHDD      int
	WBTValueSSD      int
}

func NewIOOptions() *IOOptions {
	return &IOOptions{
		PolicyName: "static",
		WritebackThrottlingOption: WritebackThrottlingOption{
			EnableSettingWBT: false,
			WBTValueHDD:      75000,
			WBTValueSSD:      2000,
		},
	}
}

func (o *IOOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("io_resource_plugin")

	fs.StringVar(&o.PolicyName, "io-resource-plugin-policy",
		o.PolicyName, "The policy io resource plugin should use")
	fs.BoolVar(&o.EnableSettingWBT, "enable-disk-wbt",
		o.EnableSettingWBT, "if set it to true, disk wbt related control operations will be executed")
	fs.IntVar(&o.WBTValueHDD, "disk-wbt-hdd",
		o.WBTValueHDD, "writeback throttling value for HDD")
	fs.IntVar(&o.WBTValueSSD, "disk-wbt-ssd",
		o.WBTValueSSD, "writeback throttling value for SSD")
}

func (o *IOOptions) ApplyTo(conf *qrmconfig.IOQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.EnableSettingWBT = o.EnableSettingWBT
	conf.WBTValueHDD = o.WBTValueHDD
	conf.WBTValueSSD = o.WBTValueSSD
	return nil
}
