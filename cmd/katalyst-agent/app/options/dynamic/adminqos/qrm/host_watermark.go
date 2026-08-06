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

	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

type HostWatermarkOptions struct {
	EnableHostWatermark       bool
	VMWatermarkScaleFactor    int
	VMWatermarkBoostFactor    int
	VMExtFragThreshold        int
	ReservedKswapdWatermarkGB uint64
}

func NewHostWatermarkOptions() *HostWatermarkOptions {
	return &HostWatermarkOptions{
		EnableHostWatermark:       false,
		VMWatermarkScaleFactor:    500,
		VMWatermarkBoostFactor:    15000,
		VMExtFragThreshold:        500,
		ReservedKswapdWatermarkGB: 10,
	}
}

func (o *HostWatermarkOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory_resource_plugin")
	fs.BoolVar(&o.EnableHostWatermark, "enable-setting-host-watermark",
		o.EnableHostWatermark, "if set true, we will tune host vm.* watermark sysctls")
	fs.IntVar(&o.VMWatermarkScaleFactor, "qrm-memory-vm-watermark-scale-factor",
		o.VMWatermarkScaleFactor, "set /proc/sys/vm/watermark_scale_factor (per 10000, 0 means do not change)")
	fs.IntVar(&o.VMWatermarkBoostFactor, "qrm-memory-vm-watermark-boost-factor",
		o.VMWatermarkBoostFactor, "set /proc/sys/vm/watermark_boost_factor")
	fs.IntVar(&o.VMExtFragThreshold, "qrm-memory-vm-extfrag-threshold",
		o.VMExtFragThreshold, "set /proc/sys/vm/extfrag_threshold")
	fs.Uint64Var(&o.ReservedKswapdWatermarkGB, "qrm-memory-kswapd-watermark-reserved-gb",
		o.ReservedKswapdWatermarkGB, "auto-calculate vm.watermark_scale_factor by reserving this many GB on a single NUMA (only when qrm-memory-vm-watermark-scale-factor=0)")
}

func (o *HostWatermarkOptions) ApplyTo(c *dynamicqrm.HostWatermarkConfiguration) error {
	c.EnableHostWatermark = o.EnableHostWatermark
	c.VMWatermarkScaleFactor = o.VMWatermarkScaleFactor
	c.VMWatermarkBoostFactor = o.VMWatermarkBoostFactor
	c.VMExtFragThreshold = o.VMExtFragThreshold
	c.ReservedKswapdWatermarkGB = o.ReservedKswapdWatermarkGB
	return nil
}
