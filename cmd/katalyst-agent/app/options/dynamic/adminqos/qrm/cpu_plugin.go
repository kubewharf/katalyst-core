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
	"strings"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
	utilflag "github.com/kubewharf/katalyst-core/pkg/util/flags"
)

type CPUPluginOptions struct {
	PreferUseExistNUMAHintResult       bool
	EnableBypassCPUSetAdjustment       bool
	DisableSharedCoresRampUp           bool
	EnableRampUpReclaimHardPartition   bool
	InitialRampUpReclaimCPUSetRatio    float64
	EnableBulkhead                     bool
	EnableBulkheadCpusetTopology       bool
	EnableBulkheadCpusetMems           bool
	EnableBulkheadWorkqueue            bool
	EnableBulkheadSystemService        bool
	BulkheadNonReclaimPoolMinSize      int64
	BulkheadDefaultCATWays             utilflag.ExplicitValue[string]
	BulkheadClosCATWays                map[string]string
	BulkheadCATDefaultAllowedBitUsages string
	BindIRQToReclaimedPool             bool
}

func NewCPUPluginOptions() *CPUPluginOptions {
	return &CPUPluginOptions{
		BulkheadNonReclaimPoolMinSize:      16,
		EnableBulkheadCpusetMems:           true,
		BulkheadCATDefaultAllowedBitUsages: string(qrm.CATBitUsageAll),
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
		"node-level reclaim floor ratio used by all shared and dedicated ramp-up paths; 0 uses reserveReclaimed on every reclaim NUMA.")
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
	fs.StringVar(&o.BulkheadDefaultCATWays.Value, "bulkhead-default-cat-ways", o.BulkheadDefaultCATWays.Value,
		"default CAT way count expression for non-root bulkhead CLOS groups.")
	o.BulkheadDefaultCATWays.TrackFlag(fs, "bulkhead-default-cat-ways")
	fs.StringToStringVar(&o.BulkheadClosCATWays, "bulkhead-clos-cat-ways", o.BulkheadClosCATWays,
		"per-CLOS CAT way count expressions in clos=expression format.")
	fs.StringVar(&o.BulkheadCATDefaultAllowedBitUsages, "bulkhead-cat-default-allowed-bit-usages", o.BulkheadCATDefaultAllowedBitUsages,
		"default allowed resctrl L3 bit_usage classes for CAT placement, comma-separated; use * to allow all ways.")
	fs.BoolVar(&o.BindIRQToReclaimedPool, "bind-irq-to-reclaimed-pool", o.BindIRQToReclaimedPool,
		"if true and the reclaimed pool is present and non-empty, GetIRQForbiddenCores expands its result to "+
			"(machine cpuset - reclaimed pool cpuset), effectively pinning network IRQs into the reclaimed pool.")
}

func (o *CPUPluginOptions) ApplyTo(c *qrm.CPUPluginConfiguration) error {
	if o.InitialRampUpReclaimCPUSetRatio < 0 || o.InitialRampUpReclaimCPUSetRatio > 1 {
		return fmt.Errorf("initial-ramp-up-reclaim-cpuset-ratio must be in [0,1], got %f", o.InitialRampUpReclaimCPUSetRatio)
	}
	var defaultCATWays qrm.CATWaysExpression
	if o.BulkheadDefaultCATWays.Value != "" {
		expr, err := qrm.ParseCATWaysExpression(o.BulkheadDefaultCATWays.Value)
		if err != nil {
			return fmt.Errorf("invalid bulkhead-default-cat-ways: %w", err)
		}
		defaultCATWays = expr
	}

	var closCATWays map[string]qrm.CATWaysExpression
	if o.BulkheadClosCATWays != nil {
		closCATWays = make(map[string]qrm.CATWaysExpression, len(o.BulkheadClosCATWays))
	}
	for clos, raw := range o.BulkheadClosCATWays {
		if clos == "" {
			return fmt.Errorf("bulkhead-clos-cat-ways contains an empty clos")
		}
		expr, err := qrm.ParseCATWaysExpression(raw)
		if err != nil {
			return fmt.Errorf("invalid bulkhead-clos-cat-ways for clos %q: %w", clos, err)
		}
		closCATWays[clos] = expr
	}
	defaultAllowedBitUsages, err := parseCATAllowedBitUsages(o.BulkheadCATDefaultAllowedBitUsages)
	if err != nil {
		return fmt.Errorf("invalid bulkhead-cat-default-allowed-bit-usages: %w", err)
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
	c.BulkheadConfig.BulkheadRDTConfig.DefaultCATWays = defaultCATWays
	c.BulkheadConfig.BulkheadRDTConfig.ClosCATWays = closCATWays
	c.BulkheadConfig.BulkheadRDTConfig.CATPolicy.DefaultPlacement = &qrm.CATPlacementPolicy{
		AllowedBitUsages: defaultAllowedBitUsages,
		Direction:        qrm.CATAllocationDirectionLow,
	}
	c.BindIRQToReclaimedPool = o.BindIRQToReclaimedPool

	return nil
}

func parseCATAllowedBitUsages(raw string) ([]qrm.CATBitUsage, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	usages := make([]qrm.CATBitUsage, 0, len(parts))
	seen := make(map[qrm.CATBitUsage]struct{}, len(parts))
	for _, part := range parts {
		usage := qrm.CATBitUsage(strings.TrimSpace(part))
		switch usage {
		case qrm.CATBitUsageAll, qrm.CATBitUsageSoftware, qrm.CATBitUsageHardware, qrm.CATBitUsageExclusive:
		default:
			return nil, fmt.Errorf("unsupported cat bit usage %q", strings.ToLower(string(usage)))
		}
		if usage == qrm.CATBitUsageAll && len(parts) > 1 {
			return nil, fmt.Errorf("cat bit usage %q must not be combined with specific usages", usage)
		}
		if _, ok := seen[usage]; ok {
			return nil, fmt.Errorf("duplicate cat bit usage %q", strings.ToLower(string(usage)))
		}
		seen[usage] = struct{}{}
		usages = append(usages, usage)
	}
	return usages, nil
}
