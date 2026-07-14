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
	DisableSharedCoresRampUp       bool
	SystemExclusivePool            map[string]int
	SystemExclusivePoolShrinkRatio *float64
	SystemExclusivePoolShrinkMin   *int64
	SystemExclusivePoolShrinkMax   *int64
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
	EnableBulkheadWorkqueue      bool
	EnableBulkheadSystemService  bool
}

func NewCPUPluginConfiguration() *CPUPluginConfiguration {
	return &CPUPluginConfiguration{}
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
			if config.BulkheadConfig.EnableBulkheadWorkqueue != nil {
				c.BulkheadConfig.EnableBulkheadWorkqueue = *config.BulkheadConfig.EnableBulkheadWorkqueue
			}
			if config.BulkheadConfig.EnableBulkheadSystemService != nil {
				c.BulkheadConfig.EnableBulkheadSystemService = *config.BulkheadConfig.EnableBulkheadSystemService
			}
		}
		if config.DisableSharedCoresRampUp != nil {
			c.DisableSharedCoresRampUp = *config.DisableSharedCoresRampUp
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
