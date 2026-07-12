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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

type CPUPluginOptions struct {
	PreferUseExistNUMAHintResult bool
	EnableBypassCPUSetAdjustment bool
	DisableSharedCoresRampUp     bool
	EnableBulkheadCpusetTopology bool
	EnableBulkheadWorkqueue      bool
}

func NewCPUPluginOptions() *CPUPluginOptions {
	return &CPUPluginOptions{}
}

func (o *CPUPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("qrm-cpu-plugin")
	fs.BoolVar(&o.PreferUseExistNUMAHintResult, "prefer-use-exist-numa-hint-result", o.PreferUseExistNUMAHintResult,
		"prefer to use existing numa hint results")
	fs.BoolVar(&o.EnableBypassCPUSetAdjustment, "enable-bypass-cpuset-adjustment", o.EnableBypassCPUSetAdjustment,
		"if true, GetResourcesAllocation clears CPU AllocationResult for all QoS classes; "+
			"allocation responses returned by Allocate/AllocateForPod keep their cpuset unchanged.")
	fs.BoolVar(&o.DisableSharedCoresRampUp, "disable-shared-cores-ramp-up", o.DisableSharedCoresRampUp,
		"if true, shared_cores pods skip initial RampUp full-pool cpuset binding and are allocated from their target pool directly.")
	fs.BoolVar(&o.EnableBulkheadCpusetTopology, "enable-bulkhead-cpuset-topology", o.EnableBulkheadCpusetTopology,
		"if true, enable bulkhead cpuset topology plugin.")
	fs.BoolVar(&o.EnableBulkheadWorkqueue, "enable-bulkhead-workqueue", o.EnableBulkheadWorkqueue,
		"if true, enable bulkhead workqueue plugin.")
}

func (o *CPUPluginOptions) ApplyTo(c *qrm.CPUPluginConfiguration) error {
	c.PreferUseExistNUMAHintResult = o.PreferUseExistNUMAHintResult
	c.EnableBypassCPUSetAdjustment = o.EnableBypassCPUSetAdjustment
	c.DisableSharedCoresRampUp = o.DisableSharedCoresRampUp
	c.BulkheadConfig.EnableBulkheadCpusetTopology = o.EnableBulkheadCpusetTopology
	c.BulkheadConfig.EnableBulkheadWorkqueue = o.EnableBulkheadWorkqueue

	return nil
}
