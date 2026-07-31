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
	"fmt"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

type CPUPluginOptions struct {
	PreferUseExistNUMAHintResult     bool
	EnableBypassCPUSetAdjustment     bool
	DisableSharedCoresRampUp         bool
	EnableRampUpReclaimHardPartition bool
	InitialRampUpReclaimCPUSetRatio  float64
	EnableBulkhead                   bool
	EnableBulkheadCpusetTopology     bool
	EnableBulkheadCpusetMems         bool
	EnableBulkheadWorkqueue          bool
	EnableBulkheadSystemService      bool
	BulkheadNonReclaimPoolMinSize    int64
	BindIRQToReclaimedPool           bool
}

func NewCPUPluginOptions() *CPUPluginOptions {
	return &CPUPluginOptions{
		BulkheadNonReclaimPoolMinSize: 16,
		EnableBulkheadCpusetMems:      true,
	}
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
	fs.BoolVar(&o.EnableRampUpReclaimHardPartition, "enable-ramp-up-reclaim-hard-partition", o.EnableRampUpReclaimHardPartition,
		"if true, enable hard reclaim partitioning while a workload is in ramp-up.")
	fs.Float64Var(&o.InitialRampUpReclaimCPUSetRatio, "initial-ramp-up-reclaim-cpuset-ratio", o.InitialRampUpReclaimCPUSetRatio,
		"ratio target used by ramp-up hard reclaim partitioning; 0 uses reserve floors only.")
	fs.BoolVar(&o.EnableBulkhead, "enable-bulkhead", o.EnableBulkhead,
		"if true, enable bulkhead.")
	fs.BoolVar(&o.EnableBulkheadCpusetTopology, "enable-bulkhead-cpuset-topology", o.EnableBulkheadCpusetTopology,
		"if true, enable bulkhead cpuset topology plugin.")
	fs.BoolVar(&o.EnableBulkheadCpusetMems, "enable-bulkhead-cpuset-mems", o.EnableBulkheadCpusetMems,
		"if true, enable bulkhead cpuset_mems plugin.")
	fs.BoolVar(&o.EnableBulkheadWorkqueue, "enable-bulkhead-workqueue", o.EnableBulkheadWorkqueue,
		"if true, enable bulkhead workqueue plugin.")
	fs.BoolVar(&o.EnableBulkheadSystemService, "enable-bulkhead-system-service", o.EnableBulkheadSystemService,
		"if true, enable bulkhead system_service plugin.")
	fs.Int64Var(&o.BulkheadNonReclaimPoolMinSize, "bulkhead-non-reclaim-pool-min-size", o.BulkheadNonReclaimPoolMinSize,
		"minimum CPU count kept in the non-reclaim pool for bulkhead cpuset topology.")
	fs.BoolVar(&o.BindIRQToReclaimedPool, "bind-irq-to-reclaimed-pool", o.BindIRQToReclaimedPool,
		"if true and the reclaimed pool is present and non-empty, GetIRQForbiddenCores expands its result to "+
			"(machine cpuset - reclaimed pool cpuset), effectively pinning network IRQs into the reclaimed pool.")
}

func (o *CPUPluginOptions) ApplyTo(c *qrm.CPUPluginConfiguration) error {
	if o.InitialRampUpReclaimCPUSetRatio < 0 || o.InitialRampUpReclaimCPUSetRatio > 1 {
		return fmt.Errorf("initial-ramp-up-reclaim-cpuset-ratio must be in [0,1], got %f", o.InitialRampUpReclaimCPUSetRatio)
	}

	c.PreferUseExistNUMAHintResult = o.PreferUseExistNUMAHintResult
	c.EnableBypassCPUSetAdjustment = o.EnableBypassCPUSetAdjustment
	c.DisableSharedCoresRampUp = o.DisableSharedCoresRampUp
	c.EnableRampUpReclaimHardPartition = o.EnableRampUpReclaimHardPartition
	c.InitialRampUpReclaimCPUSetRatio = o.InitialRampUpReclaimCPUSetRatio
	c.BulkheadConfig.Enable = o.EnableBulkhead
	c.BulkheadConfig.EnableBulkheadCpusetTopology = o.EnableBulkheadCpusetTopology
	c.BulkheadConfig.EnableBulkheadCpusetMems = o.EnableBulkheadCpusetMems
	c.BulkheadConfig.EnableBulkheadWorkqueue = o.EnableBulkheadWorkqueue
	c.BulkheadConfig.EnableBulkheadSystemService = o.EnableBulkheadSystemService
	c.BulkheadConfig.NonReclaimPoolMinSize = o.BulkheadNonReclaimPoolMinSize
	c.BindIRQToReclaimedPool = o.BindIRQToReclaimedPool

	return nil
}
