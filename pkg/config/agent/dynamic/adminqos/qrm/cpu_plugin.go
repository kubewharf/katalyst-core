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

	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type CPUPluginConfiguration struct {
	PreferUseExistNUMAHintResult bool
	// EnableBypassCPUSetAdjustment bypasses cpuset backfill in QRM CPU plugin
	// responses for shared_cores, reclaimed_cores and system_cores pods.
	// Dedicated pools are unaffected.
	EnableBypassCPUSetAdjustment bool
	BulkheadConfig               DynamicBulkheadConfiguration
	// DisableSharedCoresRampUp disables initial full-pool cpuset binding for newly
	// scheduled shared_cores pods.
	DisableSharedCoresRampUp bool
	// EnableRampUpReclaimHardPartition enables hard reclaim partitioning while a
	// workload is in ramp-up.
	EnableRampUpReclaimHardPartition bool
	// InitialRampUpReclaimCPUSetRatio controls the node-level reclaim floor used
	// by every shared and dedicated ramp-up path. Zero means reserveReclaimed
	// only; a positive value may expand that floor on every reclaim NUMA.
	InitialRampUpReclaimCPUSetRatio float64
	SystemExclusivePool             map[string]int
	SystemExclusivePoolShrinkRatio  *float64
	SystemExclusivePoolShrinkMin    *int64
	SystemExclusivePoolShrinkMax    *int64
	// BindIRQToReclaimedPool, when true, forces GetIRQForbiddenCores to return
	// "machine cpuset - reclaimed pool cpuset (still unioned with reservedCPUs
	// and other unconditional forbidden sources)" so that network IRQs are
	// effectively pinned into the reclaimed pool. Requires the reclaimed pool
	// to exist and be non-empty; otherwise the plugin falls back to the
	// previous behavior.
	BindIRQToReclaimedPool bool
}

type DynamicBulkheadConfiguration struct {
	Enable                       bool
	EnableBulkheadCpusetTopology bool
	EnableBulkheadCpusetMems     bool
	EnableBulkheadWorkqueue      bool
	EnableBulkheadSystemService  bool
	NonReclaimPoolMinSize        int64
	BulkheadRDTConfig            DynamicBulkheadRDTConfiguration
}

type DynamicBulkheadRDTConfiguration struct {
	EnableCPUList  bool
	EnableCAT      bool
	DefaultCATWays CATWaysExpression
	ClosCATWays    map[string]CATWaysExpression
	CATPolicy      CATPolicy
	CATConfigError string
}

func NewCPUPluginConfiguration() *CPUPluginConfiguration {
	return &CPUPluginConfiguration{
		BulkheadConfig: DynamicBulkheadConfiguration{
			EnableBulkheadCpusetMems: true,
		},
	}
}

func (c *CPUPluginConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.QRMPluginConfig != nil && aqc.Spec.Config.QRMPluginConfig.CPUPluginConfig != nil {
		config := aqc.Spec.Config.QRMPluginConfig.CPUPluginConfig
		if config.PreferUseExistNUMAHintResult != nil {
			c.PreferUseExistNUMAHintResult = *config.PreferUseExistNUMAHintResult
		}
		if config.EnableBypassCPUSetAdjustment != nil {
			c.EnableBypassCPUSetAdjustment = *config.EnableBypassCPUSetAdjustment
		}
		if config.BulkheadConfig != nil {
			if config.BulkheadConfig.Enable != nil {
				c.BulkheadConfig.Enable = *config.BulkheadConfig.Enable
			}
			if config.BulkheadConfig.EnableBulkheadCpusetTopology != nil {
				c.BulkheadConfig.EnableBulkheadCpusetTopology = *config.BulkheadConfig.EnableBulkheadCpusetTopology
			}
			if config.BulkheadConfig.EnableBulkheadCpusetMems != nil {
				c.BulkheadConfig.EnableBulkheadCpusetMems = *config.BulkheadConfig.EnableBulkheadCpusetMems
			}
			if config.BulkheadConfig.EnableBulkheadWorkqueue != nil {
				c.BulkheadConfig.EnableBulkheadWorkqueue = *config.BulkheadConfig.EnableBulkheadWorkqueue
			}
			if config.BulkheadConfig.EnableBulkheadSystemService != nil {
				c.BulkheadConfig.EnableBulkheadSystemService = *config.BulkheadConfig.EnableBulkheadSystemService
			}
			if config.BulkheadConfig.NonReclaimPoolMinSize != nil {
				c.BulkheadConfig.NonReclaimPoolMinSize = *config.BulkheadConfig.NonReclaimPoolMinSize
			}
			if config.BulkheadConfig.BulkheadRDTConfig != nil {
				bulkheadRDTConfig := config.BulkheadConfig.BulkheadRDTConfig
				c.BulkheadConfig.BulkheadRDTConfig.CATConfigError = ""
				if bulkheadRDTConfig.EnableCPUList != nil {
					c.BulkheadConfig.BulkheadRDTConfig.EnableCPUList = *bulkheadRDTConfig.EnableCPUList
				}
				if bulkheadRDTConfig.EnableCAT != nil {
					c.BulkheadConfig.BulkheadRDTConfig.EnableCAT = *bulkheadRDTConfig.EnableCAT
				}
				if bulkheadRDTConfig.DefaultCATWays != nil {
					if err := applyCATWaysExpression(&c.BulkheadConfig.BulkheadRDTConfig.DefaultCATWays, *bulkheadRDTConfig.DefaultCATWays); err != nil {
						c.BulkheadConfig.BulkheadRDTConfig.CATConfigError = err.Error()
					}
				}
				if bulkheadRDTConfig.ClosCATWays != nil {
					closCATWays, err := parseCATWaysExpressionMap(bulkheadRDTConfig.ClosCATWays)
					if err != nil {
						c.BulkheadConfig.BulkheadRDTConfig.CATConfigError = err.Error()
					} else {
						c.BulkheadConfig.BulkheadRDTConfig.ClosCATWays = closCATWays
					}
				}
				if bulkheadRDTConfig.CATPolicy != nil {
					c.BulkheadConfig.BulkheadRDTConfig.CATPolicy = mergeCATPolicyFromAPI(
						c.BulkheadConfig.BulkheadRDTConfig.CATPolicy,
						bulkheadRDTConfig.CATPolicy,
					)
				}
			}
		}
		if config.DisableSharedCoresRampUp != nil {
			c.DisableSharedCoresRampUp = *config.DisableSharedCoresRampUp
		}
		if config.EnableRampUpReclaimHardPartition != nil {
			c.EnableRampUpReclaimHardPartition = *config.EnableRampUpReclaimHardPartition
		}
		if config.InitialRampUpReclaimCPUSetRatio != nil {
			c.InitialRampUpReclaimCPUSetRatio = *config.InitialRampUpReclaimCPUSetRatio
		}
		c.SystemExclusivePool = config.SystemExclusivePool
		c.SystemExclusivePoolShrinkRatio = config.SystemExclusivePoolShrinkRatio
		c.SystemExclusivePoolShrinkMin = config.SystemExclusivePoolShrinkMin
		c.SystemExclusivePoolShrinkMax = config.SystemExclusivePoolShrinkMax
		if config.BindIRQToReclaimedPool != nil {
			c.BindIRQToReclaimedPool = *config.BindIRQToReclaimedPool
		}
	}
}

func applyCATWaysExpression(dst *CATWaysExpression, value intstr.IntOrString) error {
	expr, err := ParseCATWaysExpressionFromIntOrString(value)
	if err != nil {
		return fmt.Errorf("invalid default cat ways: %w", err)
	}
	*dst = expr
	return nil
}

func parseCATWaysExpressionMap(values map[string]intstr.IntOrString) (map[string]CATWaysExpression, error) {
	if values == nil {
		return nil, nil
	}

	result := make(map[string]CATWaysExpression, len(values))
	for key, value := range values {
		expr, err := ParseCATWaysExpressionFromIntOrString(value)
		if err != nil {
			return nil, fmt.Errorf("invalid cat ways for clos %q: %w", key, err)
		}
		result[key] = expr
	}
	return result, nil
}
